package dnsproxy

import (
	"context"
	"errors"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Provider is a well-known public resolver.
type Provider struct {
	ID      string
	Label   string
	DoH     string   // https URL
	DoTHost string   // TLS server name for port 853
	IPs     []string // IPv4 addresses (bootstrap, and plain DNS when Plain)
	Plain   bool     // also answers unencrypted DNS on port 53
}

// Providers lists the resolvers offered in the UI.
var Providers = []Provider{
	{"cloudflare", "Cloudflare", "https://cloudflare-dns.com/dns-query", "one.one.one.one", []string{"1.1.1.1", "1.0.0.1"}, true},
	{"quad9", "Quad9", "https://dns.quad9.net/dns-query", "dns.quad9.net", []string{"9.9.9.9", "149.112.112.112"}, true},
	{"google", "Google", "https://dns.google/dns-query", "dns.google", []string{"8.8.8.8", "8.8.4.4"}, true},
	{"adguard", "AdGuard", "https://dns.adguard-dns.com/dns-query", "dns.adguard-dns.com", []string{"94.140.14.14", "94.140.15.15"}, true},
	{"controld", "Control D", "https://freedns.controld.com/p0", "p0.freedns.controld.com", []string{"76.76.2.0", "76.76.10.0"}, true},
	{"mullvad", "Mullvad", "https://dns.mullvad.net/dns-query", "dns.mullvad.net", []string{"194.242.2.2"}, false},
}

// ProviderByID finds a provider in Providers.
func ProviderByID(id string) (Provider, bool) { return providerByID(id) }

func providerByID(id string) (Provider, bool) {
	for _, p := range Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// NewEncrypted builds a DoH or DoT upstream. provider is a Providers ID or
// "custom" (then customURL is https://... or tls://host[:port]); protocol is
// "doh" or "dot" for presets. plain resolves custom host names.
func NewEncrypted(provider, protocol, customURL string, plain Upstream) (Upstream, error) {
	boot := &bootstrap{fixed: map[string][]string{}, plain: plain}
	for _, p := range Providers {
		h, _ := url.Parse(p.DoH)
		boot.fixed[h.Hostname()] = p.IPs
		boot.fixed[p.DoTHost] = p.IPs
	}
	if provider == "custom" {
		u, err := url.Parse(strings.TrimSpace(customURL))
		if err != nil || u.Host == "" {
			return nil, errors.New("enter a URL like https://dns.example/dns-query or tls://dns.example")
		}
		switch u.Scheme {
		case "https":
			return newDoH(u.String(), u.Hostname(), boot)
		case "tls":
			return newDoT(u.Hostname(), u.Port(), u.Hostname(), boot), nil
		}
		return nil, errors.New("the URL must start with https:// or tls://")
	}
	p, ok := providerByID(provider)
	if !ok {
		return nil, errors.New("unknown provider")
	}
	if protocol == "dot" {
		return newDoT(p.DoTHost, "853", p.Label+" (DoT)", boot), nil
	}
	return newDoH(p.DoH, p.Label+" (DoH)", boot)
}

// Stats is a snapshot of the proxy counters since it started.
type Stats struct {
	Queries      uint64       `json:"queries"`
	Blocked      uint64       `json:"blocked"`
	CacheHits    uint64       `json:"cache_hits"`
	Encrypted    uint64       `json:"encrypted"`
	Fallback     uint64       `json:"fallback"`
	Failures     uint64       `json:"failures"`
	LatencyMs    int          `json:"latency_ms"`
	LastError    string       `json:"last_error"`
	LastErrorAt  int64        `json:"last_error_at"`
	TopBlocked   []NamedCount `json:"top_blocked"`
	Since        int64        `json:"since"`
	UpstreamName string       `json:"upstream"`
}

type NamedCount struct {
	Name  string `json:"name"`
	Count uint64 `json:"count"`
}

// Proxy is the running forwarder.
type Proxy struct {
	addr string

	mu        sync.RWMutex
	encrypted Upstream
	plain     Upstream
	fallback  bool
	blocking  bool
	list      *Blocklist
	clockOK   func() bool

	cache *cache

	queries, blocked, hits, enc, fb, fails atomic.Uint64
	pausedUntil                            atomic.Int64 // unix seconds; 0 = not paused, -1 = until resumed
	latency                                atomic.Int64 // EWMA in µs
	since                                  time.Time

	statMu   sync.Mutex
	lastErr  string
	lastErrT time.Time
	top      map[string]uint64

	udp    net.PacketConn
	tcp    net.Listener
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates a proxy listening on addr (e.g. 127.0.0.1:5335) once started.
func New(addr string) *Proxy {
	return &Proxy{addr: addr, cache: newCache(2000), top: map[string]uint64{}, clockOK: func() bool { return true }}
}

// Configure swaps upstreams and blocking settings; safe while running.
func (p *Proxy) Configure(encrypted, plain Upstream, fallback, blocking bool, list *Blocklist, clockOK func() bool) {
	p.mu.Lock()
	old := p.encrypted
	p.encrypted, p.plain, p.fallback, p.blocking, p.list = encrypted, plain, fallback, blocking, list
	if clockOK != nil {
		p.clockOK = clockOK
	}
	p.mu.Unlock()
	if old != nil && old != encrypted {
		old.Close()
	}
	p.cache.clear()
}

// SetBlocklist replaces the blocklist only.
func (p *Proxy) SetBlocklist(l *Blocklist) {
	p.mu.Lock()
	p.list = l
	p.mu.Unlock()
	p.cache.clear()
}

// Start binds UDP and TCP and serves until Stop.
func (p *Proxy) Start() error {
	if p.udp != nil {
		return nil
	}
	pc, err := net.ListenPacket("udp4", p.addr)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp4", p.addr)
	if err != nil {
		pc.Close()
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.udp, p.tcp, p.cancel = pc, ln, cancel
	p.since = time.Now()
	p.wg.Add(2)
	go p.serveUDP(ctx)
	go p.serveTCP(ctx)
	return nil
}

// Running reports whether the listeners are up.
func (p *Proxy) Running() bool { return p.udp != nil }

// Stop closes the listeners and waits for the serve loops.
func (p *Proxy) Stop() {
	if p.udp == nil {
		return
	}
	p.cancel()
	p.udp.Close()
	p.tcp.Close()
	p.wg.Wait()
	p.udp, p.tcp = nil, nil
	p.mu.Lock()
	if p.encrypted != nil {
		p.encrypted.Close()
	}
	p.mu.Unlock()
}

func (p *Proxy) serveUDP(ctx context.Context) {
	defer p.wg.Done()
	sem := make(chan struct{}, 64)
	for {
		buf := make([]byte, 4096)
		n, from, err := p.udp.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		sem <- struct{}{}
		go func(q []byte, from net.Addr) {
			defer func() { <-sem }()
			if resp := p.Handle(ctx, q); resp != nil {
				if len(resp) > 4096 {
					resp = truncate(q)
				}
				_, _ = p.udp.WriteTo(resp, from) // the client retries if a reply is lost
			}
		}(buf[:n], from)
	}
}

func (p *Proxy) serveTCP(ctx context.Context) {
	defer p.wg.Done()
	for {
		c, err := p.tcp.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		go func(c net.Conn) {
			defer c.Close()
			for {
				if c.SetDeadline(time.Now().Add(10*time.Second)) != nil {
					return
				}
				var l [2]byte
				if _, err := readFull(c, l[:]); err != nil {
					return
				}
				q := make([]byte, int(l[0])<<8|int(l[1]))
				if _, err := readFull(c, q); err != nil {
					return
				}
				resp := p.Handle(ctx, q)
				if resp == nil {
					return
				}
				out := append([]byte{byte(len(resp) >> 8), byte(len(resp))}, resp...)
				if _, err := c.Write(out); err != nil {
					return
				}
			}
		}(c)
	}
}

func readFull(c net.Conn, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := c.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// Handle answers one query (wire format). nil means drop.
func (p *Proxy) Handle(ctx context.Context, q []byte) []byte {
	var parser dnsmessage.Parser
	hdr, err := parser.Start(q)
	if err != nil || hdr.Response {
		return nil
	}
	question, err := parser.Question()
	if err != nil {
		return nil
	}
	p.queries.Add(1)
	name := strings.ToLower(question.Name.String())

	p.mu.RLock()
	enc, plain, fallback, blocking, list, clockOK := p.encrypted, p.plain, p.fallback, p.blocking, p.list, p.clockOK
	p.mu.RUnlock()

	if blocking && !p.Paused() && list.Blocked(name) {
		p.blocked.Add(1)
		p.countBlocked(strings.TrimSuffix(name, "."))
		return blockedReply(hdr, question)
	}

	key := cacheKey(name, question.Type, question.Class)
	if resp := p.cache.get(key, hdr.ID); resp != nil {
		p.hits.Add(1)
		return resp
	}

	ctx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	start := time.Now()
	var resp []byte
	// Certificates can't be checked before the clock is set (the stick boots
	// at 1970 until NITZ/NTP), so use plain DNS until then: NTP itself needs it.
	if enc != nil && clockOK() {
		resp, err = enc.Exchange(ctx, q)
		if err == nil {
			p.enc.Add(1)
		} else {
			p.noteErr(enc.Name() + ": " + err.Error())
			if fallback && plain != nil {
				p.fb.Add(1)
				resp, err = plain.Exchange(ctx, q)
			}
		}
	} else if plain != nil {
		if enc != nil {
			p.fb.Add(1)
		}
		resp, err = plain.Exchange(ctx, q)
	} else {
		err = errors.New("no upstream")
	}
	if err != nil {
		p.fails.Add(1)
		p.noteErr(err.Error())
		return servfail(hdr, question)
	}
	p.observeLatency(time.Since(start))
	p.cache.put(key, resp)
	return resp
}

func (p *Proxy) observeLatency(d time.Duration) {
	us := d.Microseconds()
	old := p.latency.Load()
	if old == 0 {
		p.latency.Store(us)
		return
	}
	p.latency.Store((old*7 + us) / 8)
}

func (p *Proxy) noteErr(s string) {
	p.statMu.Lock()
	p.lastErr, p.lastErrT = s, time.Now()
	p.statMu.Unlock()
}

func (p *Proxy) countBlocked(name string) {
	p.statMu.Lock()
	defer p.statMu.Unlock()
	if _, ok := p.top[name]; ok || len(p.top) < 5000 {
		p.top[name]++
	}
}

// Pause stops blocking until the given time; a zero time pauses until Resume.
func (p *Proxy) Pause(until time.Time) {
	if until.IsZero() {
		p.pausedUntil.Store(-1)
	} else {
		p.pausedUntil.Store(until.Unix())
	}
	p.cache.clear() // cached answers don't include blocked names, but be safe
}

// Resume ends a pause.
func (p *Proxy) Resume() { p.pausedUntil.Store(0) }

// Paused reports whether blocking is paused right now.
func (p *Proxy) Paused() bool {
	u := p.pausedUntil.Load()
	return u == -1 || (u > 0 && time.Now().Unix() < u)
}

// PausedUntil is the pause end (unix), -1 until resumed, 0 when not paused.
func (p *Proxy) PausedUntil() int64 {
	if !p.Paused() {
		return 0
	}
	return p.pausedUntil.Load()
}

// DrainBlocked returns the per-name blocked counts since the last call and
// starts counting from zero.
func (p *Proxy) DrainBlocked() map[string]uint64 {
	p.statMu.Lock()
	defer p.statMu.Unlock()
	out := p.top
	p.top = map[string]uint64{}
	return out
}

// Stats returns a snapshot of the counters.
func (p *Proxy) Stats() Stats {
	s := Stats{
		Queries:   p.queries.Load(),
		Blocked:   p.blocked.Load(),
		CacheHits: p.hits.Load(),
		Encrypted: p.enc.Load(),
		Fallback:  p.fb.Load(),
		Failures:  p.fails.Load(),
		LatencyMs: int((p.latency.Load() + 500) / 1000),
	}
	if !p.since.IsZero() {
		s.Since = p.since.Unix()
	}
	p.mu.RLock()
	if p.encrypted != nil {
		s.UpstreamName = p.encrypted.Name()
	} else if p.plain != nil {
		s.UpstreamName = p.plain.Name()
	}
	p.mu.RUnlock()
	p.statMu.Lock()
	s.LastError = p.lastErr
	if !p.lastErrT.IsZero() {
		s.LastErrorAt = p.lastErrT.Unix()
	}
	for n, c := range p.top {
		s.TopBlocked = append(s.TopBlocked, NamedCount{n, c})
	}
	p.statMu.Unlock()
	sort.Slice(s.TopBlocked, func(i, j int) bool {
		if s.TopBlocked[i].Count != s.TopBlocked[j].Count {
			return s.TopBlocked[i].Count > s.TopBlocked[j].Count
		}
		return s.TopBlocked[i].Name < s.TopBlocked[j].Name
	})
	if len(s.TopBlocked) > 10 {
		s.TopBlocked = s.TopBlocked[:10]
	}
	return s
}

// ---------------------------------------------------------------------------
// Replies built by the proxy
// ---------------------------------------------------------------------------

func replyHeader(h dnsmessage.Header, rcode dnsmessage.RCode) dnsmessage.Header {
	return dnsmessage.Header{
		ID: h.ID, Response: true, OpCode: h.OpCode, RecursionDesired: h.RecursionDesired,
		RecursionAvailable: true, RCode: rcode,
	}
}

// pack builds a reply with one question and the given answers. Message.Pack
// reports a single error instead of one per builder step.
func pack(h dnsmessage.Header, q dnsmessage.Question, answers ...dnsmessage.Resource) []byte {
	m := dnsmessage.Message{Header: h, Questions: []dnsmessage.Question{q}, Answers: answers}
	out, err := m.Pack()
	if err != nil {
		return nil
	}
	return out
}

// blockedReply answers A with 0.0.0.0, AAAA with ::, anything else with no data.
func blockedReply(h dnsmessage.Header, q dnsmessage.Question) []byte {
	rh := dnsmessage.ResourceHeader{Name: q.Name, Class: q.Class, TTL: 60}
	switch q.Type {
	case dnsmessage.TypeA:
		return pack(replyHeader(h, dnsmessage.RCodeSuccess), q, dnsmessage.Resource{Header: rh, Body: &dnsmessage.AResource{}})
	case dnsmessage.TypeAAAA:
		return pack(replyHeader(h, dnsmessage.RCodeSuccess), q, dnsmessage.Resource{Header: rh, Body: &dnsmessage.AAAAResource{}})
	}
	return pack(replyHeader(h, dnsmessage.RCodeSuccess), q)
}

func servfail(h dnsmessage.Header, q dnsmessage.Question) []byte {
	return pack(replyHeader(h, dnsmessage.RCodeServerFailure), q)
}

// truncate makes an empty TC=1 reply so the client retries over TCP.
func truncate(q []byte) []byte {
	var parser dnsmessage.Parser
	h, err := parser.Start(q)
	if err != nil {
		return nil
	}
	question, err := parser.Question()
	if err != nil {
		return nil
	}
	rh := replyHeader(h, dnsmessage.RCodeSuccess)
	rh.Truncated = true
	return pack(rh, question)
}
