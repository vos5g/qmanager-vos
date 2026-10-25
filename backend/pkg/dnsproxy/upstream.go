package dnsproxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// BypassMark is set on every socket the proxy opens so vosclean's DNS redirect
// leaves the proxy's own upstream traffic alone (iptables "-m mark --mark").
const BypassMark = 0xca6d

// Upstream sends one DNS query (wire format) and returns the response.
type Upstream interface {
	Exchange(ctx context.Context, q []byte) ([]byte, error)
	Name() string
	Close()
}

// markControl sets SO_MARK on a socket before it connects.
func markControl(_, _ string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, BypassMark)
	})
	if err != nil {
		return err
	}
	return serr
}

// markedDialer is a net.Dialer whose sockets carry BypassMark. Tests turn the
// mark off (setting it needs CAP_NET_ADMIN).
var markSockets = true

func newDialer(timeout time.Duration) *net.Dialer {
	d := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	if markSockets {
		d.Control = markControl
	}
	return d
}

// ---------------------------------------------------------------------------
// Plain DNS (UDP, TCP when truncated)
// ---------------------------------------------------------------------------

// PlainUpstream asks classic resolvers on port 53. Servers is called for every
// query so it can follow /etc/resolv.conf or the custom DNS setting.
type PlainUpstream struct {
	Label   string
	Servers func() []string
}

func (p *PlainUpstream) Name() string { return p.Label }
func (p *PlainUpstream) Close()       {}

func (p *PlainUpstream) Exchange(ctx context.Context, q []byte) ([]byte, error) {
	servers := p.Servers()
	if len(servers) == 0 {
		return nil, errors.New("no plain DNS servers")
	}
	var lastErr error
	start := rand.Intn(len(servers))
	for i := range servers {
		s := servers[(start+i)%len(servers)]
		addr := net.JoinHostPort(s, "53")
		resp, err := exchangeUDP(ctx, addr, q)
		if err == nil && len(resp) > 2 && resp[2]&0x02 != 0 { // TC bit
			resp, err = exchangeTCP(ctx, addr, q)
		}
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, lastErr
}

func exchangeUDP(ctx context.Context, addr string, q []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	c, err := newDialer(2*time.Second).DialContext(ctx, "udp", addr)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	}
	if _, err := c.Write(q); err != nil {
		return nil, err
	}
	buf := make([]byte, 4096)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return nil, err
		}
		if n >= 2 && buf[0] == q[0] && buf[1] == q[1] {
			return append([]byte(nil), buf[:n]...), nil
		}
	}
}

func exchangeTCP(ctx context.Context, addr string, q []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	c, err := newDialer(3*time.Second).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	}
	return streamExchange(c, q)
}

// streamExchange does one length-prefixed exchange (TCP / DoT framing).
func streamExchange(c io.ReadWriter, q []byte) ([]byte, error) {
	msg := make([]byte, 2+len(q))
	binary.BigEndian.PutUint16(msg, uint16(len(q)))
	copy(msg[2:], q)
	if _, err := c.Write(msg); err != nil {
		return nil, err
	}
	var l [2]byte
	if _, err := io.ReadFull(c, l[:]); err != nil {
		return nil, err
	}
	resp := make([]byte, binary.BigEndian.Uint16(l[:]))
	if _, err := io.ReadFull(c, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// Bootstrap: resolving the encrypted resolver's own host name
// ---------------------------------------------------------------------------

// Resolver turns a host name into IPv4 addresses for dialing an encrypted
// resolver. Known providers use fixed addresses; others are looked up with the
// plain upstream.
type bootstrap struct {
	fixed map[string][]string
	plain Upstream
	mu    sync.Mutex
	cache map[string]bootEntry
}

type bootEntry struct {
	ips []string
	exp time.Time
}

func (b *bootstrap) lookup(ctx context.Context, host string) ([]string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []string{host}, nil
	}
	if ips := b.fixed[host]; len(ips) > 0 {
		return ips, nil
	}
	b.mu.Lock()
	if e, ok := b.cache[host]; ok && time.Now().Before(e.exp) {
		b.mu.Unlock()
		return e.ips, nil
	}
	b.mu.Unlock()
	if b.plain == nil {
		return nil, fmt.Errorf("cannot resolve %s", host)
	}
	q, err := buildQuery(host, dnsmessage.TypeA)
	if err != nil {
		return nil, err
	}
	resp, err := b.plain.Exchange(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("bootstrap %s: %w", host, err)
	}
	var m dnsmessage.Message
	if err := m.Unpack(resp); err != nil {
		return nil, err
	}
	var ips []string
	ttl := uint32(300)
	for _, a := range m.Answers {
		if r, ok := a.Body.(*dnsmessage.AResource); ok {
			ips = append(ips, net.IP(r.A[:]).String())
			if a.Header.TTL < ttl {
				ttl = a.Header.TTL
			}
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("bootstrap %s: no address", host)
	}
	if ttl < 60 {
		ttl = 60
	}
	b.mu.Lock()
	if b.cache == nil {
		b.cache = map[string]bootEntry{}
	}
	b.cache[host] = bootEntry{ips, time.Now().Add(time.Duration(ttl) * time.Second)}
	b.mu.Unlock()
	return ips, nil
}

// dial connects to host:port trying each bootstrap address.
func (b *bootstrap) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := b.lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, ip := range ips {
		c, err := newDialer(5*time.Second).DialContext(ctx, network, net.JoinHostPort(ip, port))
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func buildQuery(host string, t dnsmessage.Type) ([]byte, error) {
	name, err := dnsmessage.NewName(strings.TrimSuffix(host, ".") + ".")
	if err != nil {
		return nil, err
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: uint16(rand.Intn(65536)), RecursionDesired: true})
	b.StartQuestions()
	b.Question(dnsmessage.Question{Name: name, Type: t, Class: dnsmessage.ClassINET})
	return b.Finish()
}

// ---------------------------------------------------------------------------
// DNS over HTTPS (RFC 8484, POST)
// ---------------------------------------------------------------------------

type DoHUpstream struct {
	url    string
	label  string
	client *http.Client
	tr     *http.Transport
}

func newDoH(rawURL, label string, boot *bootstrap) (*DoHUpstream, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("not an https:// URL: %q", rawURL)
	}
	tr := &http.Transport{
		DialContext:         boot.dial,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 5 * time.Second,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConnsPerHost: 4,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		Proxy:               nil,
	}
	return &DoHUpstream{
		url:    u.String(),
		label:  label,
		tr:     tr,
		client: &http.Client{Transport: tr, Timeout: 6 * time.Second},
	}, nil
}

func (d *DoHUpstream) Name() string { return d.label }
func (d *DoHUpstream) Close()       { d.tr.CloseIdleConnections() }

func (d *DoHUpstream) Exchange(ctx context.Context, q []byte) ([]byte, error) {
	// RFC 8484 recommends ID 0 for cache friendliness; restore it afterwards.
	id0, id1 := q[0], q[1]
	body := append([]byte(nil), q...)
	body[0], body[1] = 0, 0
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("DoH HTTP %d", resp.StatusCode)
	}
	out, err := io.ReadAll(io.LimitReader(resp.Body, 65535))
	if err != nil {
		return nil, err
	}
	if len(out) < 12 {
		return nil, errors.New("DoH: short response")
	}
	out[0], out[1] = id0, id1
	return out, nil
}

// ---------------------------------------------------------------------------
// DNS over TLS (RFC 7858), one reused connection
// ---------------------------------------------------------------------------

type DoTUpstream struct {
	host, port, label string
	boot              *bootstrap
	mu                sync.Mutex
	conn              *tls.Conn
	used              time.Time
}

func newDoT(host, port, label string, boot *bootstrap) *DoTUpstream {
	if port == "" {
		port = "853"
	}
	return &DoTUpstream{host: host, port: port, label: label, boot: boot}
}

func (d *DoTUpstream) Name() string { return d.label }

func (d *DoTUpstream) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn != nil {
		d.conn.Close()
		d.conn = nil
	}
}

func (d *DoTUpstream) connect(ctx context.Context) error {
	raw, err := d.boot.dial(ctx, "tcp", net.JoinHostPort(d.host, d.port))
	if err != nil {
		return err
	}
	tc := tls.Client(raw, &tls.Config{ServerName: d.host, MinVersion: tls.VersionTLS12})
	hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := tc.HandshakeContext(hctx); err != nil {
		raw.Close()
		return err
	}
	d.conn = tc
	return nil
}

func (d *DoTUpstream) Exchange(ctx context.Context, q []byte) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		// Servers drop idle connections after a while. A dropped one fails at
		// once (EOF/RST) and is retried below, so only give up on very old ones.
		if d.conn != nil && time.Since(d.used) > 2*time.Minute {
			d.conn.Close()
			d.conn = nil
		}
		fresh := false
		if d.conn == nil {
			if err := d.connect(ctx); err != nil {
				return nil, err
			}
			fresh = true
		}
		// A reused connection gets a short deadline: if the carrier's NAT
		// silently dropped it, retry on a fresh one instead of waiting.
		wait := 4 * time.Second
		if !fresh {
			wait = 1500 * time.Millisecond
		}
		dl := time.Now().Add(wait)
		if cdl, ok := ctx.Deadline(); ok && cdl.Before(dl) {
			dl = cdl
		}
		d.conn.SetDeadline(dl)
		resp, err := streamExchange(d.conn, q)
		if err == nil {
			d.used = time.Now()
			return resp, nil
		}
		d.conn.Close()
		d.conn = nil
		if fresh || ctx.Err() != nil {
			return nil, err
		}
	}
	return nil, errors.New("DoT: exchange failed")
}
