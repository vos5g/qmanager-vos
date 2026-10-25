package dnsproxy

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func init() { markSockets = false }

func TestParseListFormats(t *testing.T) {
	in := `# comment
! adblock comment
[Adblock Plus]
0.0.0.0 ads.example.com
127.0.0.1 Tracker.Example.NET # trailing
0.0.0.0 0.0.0.0
127.0.0.1 localhost
::1 localhost
||doubleclick.net^
||withopts.com^$third-party
||path.com/ads
@@||allowed.com^
*.wild.org
plain.io
not a domain line here
192.168.1.1 router.lan
`
	var got []string
	n, err := ParseList(strings.NewReader(in), func(d string) { got = append(got, d) })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ads.example.com", "tracker.example.net", "doubleclick.net", "wild.org", "plain.io"}
	if n != len(want) || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %d %v, want %v", n, got, want)
	}
}

func TestBlocklistMatching(t *testing.T) {
	b := NewBlocklist([]string{"ads.example.com", "doubleclick.net", "DoubleClick.net", "tracker.io"}, []string{"ok.doubleclick.net"})
	if b.Len() != 3 {
		t.Fatalf("len %d", b.Len())
	}
	cases := map[string]bool{
		"ads.example.com.":        true,
		"x.y.ads.example.com":     true,
		"example.com":             false,
		"doubleclick.net":         true,
		"static.doubleclick.net.": true,
		"ok.doubleclick.net":      false,
		"a.ok.doubleclick.net":    false,
		"notdoubleclick.net":      false,
		"tracker.io.evil":         false,
		"":                        false,
	}
	for name, want := range cases {
		if got := b.Blocked(name); got != want {
			t.Errorf("Blocked(%q)=%v want %v", name, got, want)
		}
	}
	var nilList *Blocklist
	if nilList.Blocked("ads.example.com") {
		t.Fatal("nil list blocked")
	}
}

// fakeUpstream answers A queries with 192.0.2.1 and counts calls.
type fakeUpstream struct {
	name  string
	calls atomic.Int32
	fail  bool
	ttl   uint32
}

func (f *fakeUpstream) Name() string { return f.name }
func (f *fakeUpstream) Close()       {}
func (f *fakeUpstream) Exchange(_ context.Context, q []byte) ([]byte, error) {
	f.calls.Add(1)
	if f.fail {
		return nil, errors.New("boom")
	}
	var p dnsmessage.Parser
	h, err := p.Start(q)
	if err != nil {
		return nil, err
	}
	qq, _ := p.Question()
	ttl := f.ttl
	if ttl == 0 {
		ttl = 300
	}
	m := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: h.ID, Response: true, RecursionAvailable: true},
		Questions: []dnsmessage.Question{qq},
		Answers: []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{Name: qq.Name, Class: qq.Class, TTL: ttl},
			Body:   &dnsmessage.AResource{A: [4]byte{192, 0, 2, 1}},
		}},
	}
	return m.Pack()
}

func query(t *testing.T, name string, typ dnsmessage.Type, id uint16) []byte {
	t.Helper()
	q, err := buildQuery(name, typ)
	if err != nil {
		t.Fatal(err)
	}
	q[0], q[1] = byte(id>>8), byte(id)
	return q
}

func answers(t *testing.T, resp []byte) (dnsmessage.Message, []string) {
	t.Helper()
	var m dnsmessage.Message
	if err := m.Unpack(resp); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, a := range m.Answers {
		switch r := a.Body.(type) {
		case *dnsmessage.AResource:
			out = append(out, net.IP(r.A[:]).String())
		case *dnsmessage.AAAAResource:
			out = append(out, net.IP(r.AAAA[:]).String())
		}
	}
	return m, out
}

func TestHandleBlockCacheFallback(t *testing.T) {
	enc := &fakeUpstream{name: "enc"}
	plain := &fakeUpstream{name: "plain"}
	p := New("127.0.0.1:0")
	p.Configure(enc, plain, true, true, NewBlocklist([]string{"ads.example.com"}, nil), nil)
	ctx := context.Background()

	// Blocked A and AAAA.
	m, a := answers(t, p.Handle(ctx, query(t, "x.ads.example.com", dnsmessage.TypeA, 7)))
	if m.ID != 7 || len(a) != 1 || a[0] != "0.0.0.0" {
		t.Fatalf("blocked A: id=%d %v", m.ID, a)
	}
	_, a = answers(t, p.Handle(ctx, query(t, "ads.example.com", dnsmessage.TypeAAAA, 8)))
	if len(a) != 1 || a[0] != "::" {
		t.Fatalf("blocked AAAA: %v", a)
	}
	if enc.calls.Load() != 0 {
		t.Fatal("blocked name reached upstream")
	}

	// Forwarded, then served from cache with the new ID.
	_, a = answers(t, p.Handle(ctx, query(t, "good.example.org", dnsmessage.TypeA, 9)))
	if len(a) != 1 || a[0] != "192.0.2.1" || enc.calls.Load() != 1 {
		t.Fatalf("forward: %v calls=%d", a, enc.calls.Load())
	}
	m, _ = answers(t, p.Handle(ctx, query(t, "GOOD.example.org", dnsmessage.TypeA, 10)))
	if m.ID != 10 || enc.calls.Load() != 1 {
		t.Fatalf("cache: id=%d calls=%d", m.ID, enc.calls.Load())
	}

	// Encrypted fails -> plain fallback.
	enc.fail = true
	_, a = answers(t, p.Handle(ctx, query(t, "other.example.org", dnsmessage.TypeA, 11)))
	if len(a) != 1 || plain.calls.Load() != 1 {
		t.Fatalf("fallback: %v plain=%d", a, plain.calls.Load())
	}

	// No fallback -> SERVFAIL.
	p.Configure(enc, plain, false, false, nil, nil)
	m, _ = answers(t, p.Handle(ctx, query(t, "third.example.org", dnsmessage.TypeA, 12)))
	if m.RCode != dnsmessage.RCodeServerFailure {
		t.Fatalf("rcode %v", m.RCode)
	}
	// Blocking off -> blocked name is forwarded (to the failing upstream).
	m, _ = answers(t, p.Handle(ctx, query(t, "ads.example.com", dnsmessage.TypeA, 13)))
	if m.RCode != dnsmessage.RCodeServerFailure {
		t.Fatal("blocking off should forward")
	}

	// Clock not set -> plain is used even with encryption on.
	enc.fail = false
	before := enc.calls.Load()
	p.Configure(enc, plain, false, false, nil, func() bool { return false })
	_, a = answers(t, p.Handle(ctx, query(t, "clock.example.org", dnsmessage.TypeA, 14)))
	if len(a) != 1 || enc.calls.Load() != before {
		t.Fatal("clock gate did not use plain")
	}

	s := p.Stats()
	if s.Blocked != 2 || s.CacheHits != 1 || len(s.TopBlocked) != 2 {
		t.Fatalf("stats %+v", s)
	}
}

func TestCacheAgesTTL(t *testing.T) {
	c := newCache(10)
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	up := &fakeUpstream{ttl: 100}
	resp, _ := up.Exchange(context.Background(), query(t, "a.example", dnsmessage.TypeA, 1))
	c.put("k", resp)
	now = now.Add(40 * time.Second)
	m, _ := answers(t, c.get("k", 5))
	if m.Answers[0].Header.TTL != 60 || m.ID != 5 {
		t.Fatalf("ttl %d id %d", m.Answers[0].Header.TTL, m.ID)
	}
	now = now.Add(61 * time.Second)
	if c.get("k", 5) != nil {
		t.Fatal("expired entry returned")
	}
}

func TestServeUDPAndTCP(t *testing.T) {
	p := New("127.0.0.1:0")
	up := &fakeUpstream{name: "f"}
	p.Configure(nil, up, false, true, NewBlocklist([]string{"blocked.test"}, nil), nil)
	// Bind to a free port.
	ln, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	addr := ln.LocalAddr().String()
	ln.Close()
	p.addr = addr
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer p.Stop()

	resp, err := exchangeUDP(context.Background(), addr, query(t, "www.example.org", dnsmessage.TypeA, 77))
	if err != nil {
		t.Fatal(err)
	}
	if _, a := answers(t, resp); len(a) != 1 || a[0] != "192.0.2.1" {
		t.Fatalf("udp %v", a)
	}
	resp, err = exchangeTCP(context.Background(), addr, query(t, "ad.blocked.test", dnsmessage.TypeA, 78))
	if err != nil {
		t.Fatal(err)
	}
	if _, a := answers(t, resp); len(a) != 1 || a[0] != "0.0.0.0" {
		t.Fatalf("tcp %v", a)
	}
}

func TestNewEncryptedValidation(t *testing.T) {
	if _, err := NewEncrypted("custom", "", "http://plain.example/dns-query", nil); err == nil {
		t.Fatal("http accepted")
	}
	if _, err := NewEncrypted("nope", "doh", "", nil); err == nil {
		t.Fatal("unknown provider accepted")
	}
	u, err := NewEncrypted("quad9", "dot", "", nil)
	if err != nil || u.Name() != "Quad9 (DoT)" {
		t.Fatalf("%v %v", u, err)
	}
	u, err = NewEncrypted("custom", "", "tls://dns.example:8853", nil)
	if err != nil || u.(*DoTUpstream).port != "8853" {
		t.Fatalf("custom dot: %v", err)
	}
}
