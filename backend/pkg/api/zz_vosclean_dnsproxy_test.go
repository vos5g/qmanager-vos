package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"qmanager-backend/pkg/dnsproxy"
)

func setupDNSProxyTest(t *testing.T) (dir string, applied *int) {
	t.Helper()
	dir = t.TempDir()
	oldConf, oldList, oldBeat, oldCustom, oldResolv, oldVos, oldApply :=
		dnsProxyConfFile, dnsBlocklistFile, dnsHeartbeatFile, dnsCustomConfFile, dnsResolvConf, vosDNSConfFile, dnsVoscleanApply
	oldState := dnsp
	dnsProxyConfFile = filepath.Join(dir, "dns_proxy.json")
	dnsBlocklistFile = filepath.Join(dir, "blocklist.txt")
	dnsHeartbeatFile = filepath.Join(dir, "alive")
	dnsCustomConfFile = filepath.Join(dir, "custom_dns.json")
	dnsResolvConf = filepath.Join(dir, "resolv.conf")
	vosDNSConfFile = filepath.Join(dir, "dns.conf")
	if err := os.WriteFile(dnsResolvConf, []byte("nameserver 192.0.2.53\nnameserver ::1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	n := 0
	applied = &n
	dnsVoscleanApply = func() error { n++; return nil }
	dnsp = &dnsProxyState{proxy: dnsproxy.New("127.0.0.1:0"), listsDone: make(chan struct{}, 1)}
	t.Cleanup(func() {
		dnsp.mu.Lock()
		dnsp.stopHeartbeatLocked()
		dnsp.proxy.Stop()
		dnsp.mu.Unlock()
		dnsProxyConfFile, dnsBlocklistFile, dnsHeartbeatFile, dnsCustomConfFile, dnsResolvConf, vosDNSConfFile, dnsVoscleanApply =
			oldConf, oldList, oldBeat, oldCustom, oldResolv, oldVos, oldApply
		dnsp = oldState
	})
	return dir, applied
}

func postDNSProxy(t *testing.T, body any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	rr := httptest.NewRecorder()
	(&Server{}).HandleDNSProxy(rr, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b)))
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad json %q", rr.Body.String())
	}
	return out
}

func TestDNSProxyBlockingLifecycle(t *testing.T) {
	_, applied := setupDNSProxyTest(t)
	lists := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "# test list\n0.0.0.0 ads.example.com\n||tracker.example.net^\nmetrics.example.org\n")
	}))
	defer lists.Close()

	// Validation.
	out := postDNSProxy(t, map[string]any{"action": "save", "blocking": map[string]any{"enabled": true, "lists": []string{"nope"}}})
	if out["success"] != false {
		t.Fatal("unknown list accepted")
	}
	out = postDNSProxy(t, map[string]any{"action": "save", "blocking": map[string]any{"enabled": true, "allowlist": []string{"not a domain"}, "custom_urls": []string{lists.URL}}})
	if out["success"] != false {
		t.Fatal("bad allowlist accepted")
	}

	out = postDNSProxy(t, map[string]any{"action": "save", "blocking": map[string]any{
		"enabled": true, "custom_urls": []string{lists.URL},
		"allowlist": []string{"ok.ads.example.com"}, "denylist": []string{"mine.example"},
	}})
	if out["success"] != true || out["running"] != true {
		t.Fatalf("save: %v", out)
	}
	select {
	case <-dnsp.listsDone:
	case <-time.After(5 * time.Second):
		t.Fatal("list download did not finish")
	}
	conf, _ := os.ReadFile(vosDNSConfFile)
	if !strings.Contains(string(conf), "DNS_PROXY=yes") || !strings.Contains(string(conf), "DNS_ENABLE=no") {
		t.Fatalf("dns.conf: %s", conf)
	}
	if *applied == 0 {
		t.Fatal("vosclean dns not run")
	}
	if _, err := os.Stat(dnsHeartbeatFile); err != nil {
		t.Fatal("no heartbeat")
	}
	saved, _ := os.ReadFile(dnsBlocklistFile)
	if string(saved) != "ads.example.com\nmetrics.example.org\ntracker.example.net\n" {
		t.Fatalf("blocklist file %q", saved)
	}

	for name, want := range map[string]bool{"x.ads.example.com": true, "ok.ads.example.com": false, "mine.example": true, "example.com": false} {
		out = postDNSProxy(t, map[string]any{"action": "check", "domain": name})
		if out["blocked"] != want {
			t.Errorf("check %s = %v want %v", name, out["blocked"], want)
		}
	}

	// Restart: the saved list is loaded without downloading.
	dnsp.mu.Lock()
	dnsp.stopHeartbeatLocked()
	dnsp.proxy.Stop()
	dnsp.mu.Unlock()
	dnsp = &dnsProxyState{proxy: dnsproxy.New("127.0.0.1:0")}
	dnsp.mu.Lock()
	dnsp.conf = loadDNSProxyConf()
	dnsp.listDoms = readBlocklistFile()
	err := dnsp.applyLocked()
	dnsp.mu.Unlock()
	if err != nil || len(dnsp.listDoms) != 3 || dnsp.conf.ListsKey != dnsp.conf.Blocking.key() {
		t.Fatalf("reload: err=%v doms=%d", err, len(dnsp.listDoms))
	}

	// Turning everything off removes the redirect and the heartbeat.
	out = postDNSProxy(t, map[string]any{"action": "save", "blocking": map[string]any{"enabled": false, "custom_urls": []string{lists.URL}}})
	if out["success"] != true || out["running"] != false {
		t.Fatalf("off: %v", out)
	}
	conf, _ = os.ReadFile(vosDNSConfFile)
	if !strings.Contains(string(conf), "DNS_PROXY=no") {
		t.Fatalf("dns.conf after off: %s", conf)
	}
	if _, err := os.Stat(dnsHeartbeatFile); err == nil {
		t.Fatal("heartbeat left behind")
	}
}

func TestDNSProxyEncryptedValidationAndCustomDNS(t *testing.T) {
	_, _ = setupDNSProxyTest(t)
	out := postDNSProxy(t, map[string]any{"action": "save", "encrypted": map[string]any{"enabled": true, "provider": "custom", "custom_url": "http://x/dns-query"}})
	if out["success"] != false {
		t.Fatal("http DoH accepted")
	}
	if err := os.WriteFile(dnsCustomConfFile, []byte(`{"mode":"enabled","dns1":"9.9.9.9","dns2":"bogus","dns3":""}`), 0644); err != nil {
		t.Fatal(err)
	}
	out = postDNSProxy(t, map[string]any{"action": "save", "encrypted": map[string]any{"enabled": true, "provider": "quad9", "protocol": "dot", "fallback": true}})
	if out["success"] != true {
		t.Fatalf("save: %v", out)
	}
	conf, _ := os.ReadFile(vosDNSConfFile)
	if !strings.Contains(string(conf), `DNS_ENABLE=yes`) || !strings.Contains(string(conf), `DNS_SERVERS="9.9.9.9"`) || !strings.Contains(string(conf), "DNS_PROXY=yes") {
		t.Fatalf("dns.conf: %s", conf)
	}
	if out["plain_upstream"] != "Custom DNS" {
		t.Fatalf("plain label %v", out["plain_upstream"])
	}
	// The Custom DNS card keeps the proxy redirect when it saves.
	if err := applyCustomDNS(true, "1.1.1.1"); err != nil {
		t.Fatal(err)
	}
	conf, _ = os.ReadFile(vosDNSConfFile)
	if !strings.Contains(string(conf), "DNS_PROXY=yes") {
		t.Fatalf("custom DNS save dropped the proxy: %s", conf)
	}
	if got := carrierServers(); len(got) != 1 || got[0] != "192.0.2.53" {
		t.Fatalf("carrier %v", got)
	}
}

func TestSaveResolverModes(t *testing.T) {
	_, applied := setupDNSProxyTest(t)
	readConf := func() string { b, _ := os.ReadFile(vosDNSConfFile); return string(b) }
	resolver := func(out map[string]any) map[string]any { return out["resolver"].(map[string]any) }

	// Plain preset.
	out := postDNSProxy(t, map[string]any{"action": "save_resolver", "mode": "custom", "provider": "quad9", "protocol": "plain"})
	if out["success"] != true || out["running"] != false {
		t.Fatalf("plain: %v", out)
	}
	if c := readConf(); !strings.Contains(c, `DNS_ENABLE=yes`) || !strings.Contains(c, `DNS_SERVERS="9.9.9.9 149.112.112.112"`) || !strings.Contains(c, "DNS_PROXY=no") {
		t.Fatalf("plain conf: %s", c)
	}
	if r := resolver(out); r["provider"] != "quad9" || r["protocol"] != "plain" || r["mode"] != "custom" {
		t.Fatalf("plain state %v", r)
	}

	// Encrypted preset keeps the provider's plain servers as fallback.
	out = postDNSProxy(t, map[string]any{"action": "save_resolver", "mode": "custom", "provider": "cloudflare", "protocol": "dot", "fallback": true})
	if out["success"] != true || out["running"] != true {
		t.Fatalf("dot: %v", out)
	}
	if c := readConf(); !strings.Contains(c, `DNS_SERVERS="1.1.1.1 1.0.0.1"`) || !strings.Contains(c, "DNS_PROXY=yes") {
		t.Fatalf("dot conf: %s", c)
	}
	if r := resolver(out); r["provider"] != "cloudflare" || r["protocol"] != "dot" {
		t.Fatalf("dot state %v", r)
	}

	// Encrypted-only provider can't be plain.
	if out = postDNSProxy(t, map[string]any{"action": "save_resolver", "mode": "custom", "provider": "mullvad", "protocol": "plain"}); out["success"] != false {
		t.Fatal("mullvad plain accepted")
	}
	// Mullvad encrypted: no plain fallback servers, carrier is the fallback.
	out = postDNSProxy(t, map[string]any{"action": "save_resolver", "mode": "custom", "provider": "mullvad", "protocol": "doh", "fallback": true})
	if out["success"] != true || !strings.Contains(readConf(), "DNS_ENABLE=no") {
		t.Fatalf("mullvad: %v %s", out, readConf())
	}

	// Custom URL without scheme gets one from the protocol.
	out = postDNSProxy(t, map[string]any{"action": "save_resolver", "mode": "custom", "provider": "custom", "protocol": "dot", "custom_url": "dns.example.net", "servers": []string{"192.0.2.9"}})
	if r := resolver(out); out["success"] != true || r["custom_url"] != "tls://dns.example.net" || r["protocol"] != "dot" {
		t.Fatalf("custom dot: %v", out)
	}
	// Custom plain needs servers; bad ones are rejected and nothing changes.
	before := readConf()
	if out = postDNSProxy(t, map[string]any{"action": "save_resolver", "mode": "custom", "provider": "custom", "protocol": "plain", "servers": []string{"nope"}}); out["success"] != false {
		t.Fatal("bad server accepted")
	}
	if readConf() != before {
		t.Fatal("rejected save changed dns.conf")
	}

	// Carrier turns everything off.
	n := *applied
	out = postDNSProxy(t, map[string]any{"action": "save_resolver", "mode": "carrier"})
	if out["success"] != true || out["running"] != false || resolver(out)["mode"] != "carrier" || *applied == n {
		t.Fatalf("carrier: %v", out)
	}
	if c := readConf(); !strings.Contains(c, "DNS_ENABLE=no") || !strings.Contains(c, "DNS_PROXY=no") {
		t.Fatalf("carrier conf: %s", c)
	}
}

func TestDNSStatsHistory(t *testing.T) {
	dir := t.TempDir()
	oldFile, oldProxy := dnsStatsFile, dstats.proxy
	defer func() { dnsStatsFile, dstats.proxy = oldFile, oldProxy }()
	dnsStatsFile = filepath.Join(dir, "dns_stats.json")

	p := dnsproxy.New("127.0.0.1:0")
	up := &countingUpstream{}
	p.Configure(nil, up, false, true, dnsproxy.NewBlocklist([]string{"ads.example.com"}, nil), nil)
	st := &dnsStatsState{proxy: func() *dnsproxy.Proxy { return p }}
	st.load()
	ask := func(name string) { p.Handle(context.Background(), dnsQuery(t, name)) }

	ask("ads.example.com")
	ask("ads.example.com")
	ask("good.example.org")
	day1 := time.Date(2026, 10, 24, 12, 0, 0, 0, time.Local)
	st.sample(day1)
	ask("x.ads.example.com")
	day2 := day1.AddDate(0, 0, 1)
	st.sample(day2)
	st.sample(day2) // no new lookups: nothing double-counted

	snap := st.snapshot(day2)
	days := snap["days"].([]map[string]any)
	if len(days) != 14 || days[12]["blocked"] != uint64(2) || days[12]["queries"] != uint64(3) || days[13]["blocked"] != uint64(1) {
		t.Fatalf("days %v", days[11:])
	}
	top := snap["top"].([]map[string]any)
	if len(top) != 2 || top[0]["name"] != "ads.example.com" || top[0]["count"] != uint64(2) {
		t.Fatalf("top %v", top)
	}
	st.save()

	// A restart: history comes back from disk, new counters add on top.
	st2 := &dnsStatsState{proxy: func() *dnsproxy.Proxy { return dnsproxy.New("127.0.0.1:0") }}
	st2.load()
	if st2.d.Days["2026-10-24"].Blocked != 2 || st2.d.Top["ads.example.com"] != 2 {
		t.Fatalf("reload %+v", st2.d)
	}
	st2.reset()
	if len(st2.d.Days) != 0 || len(st2.d.Top) != 0 {
		t.Fatal("reset")
	}
}

type countingUpstream struct{}

func (countingUpstream) Name() string { return "c" }
func (countingUpstream) Close()       {}
func (countingUpstream) Exchange(_ context.Context, q []byte) ([]byte, error) {
	r := append([]byte(nil), q...)
	r[2] |= 0x80 // QR
	return r, nil
}

func dnsQuery(t *testing.T, name string) []byte {
	t.Helper()
	n, err := dnsmessage.NewName(name + ".")
	if err != nil {
		t.Fatal(err)
	}
	m := dnsmessage.Message{Header: dnsmessage.Header{ID: 1, RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: n, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}
	b, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDNSProxyPauseAction(t *testing.T) {
	_, _ = setupDNSProxyTest(t)
	paused := func(out map[string]any) any { return out["blocking"].(map[string]any)["paused_until"] }
	if out := postDNSProxy(t, map[string]any{"action": "pause", "minutes": 5}); out["success"] != true || paused(out).(float64) < float64(time.Now().Unix()+290) {
		t.Fatalf("pause 5: %v", out)
	}
	if loadDNSProxyConf().PausedUntil <= 0 {
		t.Fatal("pause not saved")
	}
	if out := postDNSProxy(t, map[string]any{"action": "pause", "minutes": 0}); paused(out) != float64(-1) {
		t.Fatalf("pause forever: %v", paused(out))
	}
	if out := postDNSProxy(t, map[string]any{"action": "pause", "minutes": -3}); out["success"] != false {
		t.Fatal("negative accepted")
	}
	if out := postDNSProxy(t, map[string]any{"action": "resume"}); paused(out) != float64(0) || loadDNSProxyConf().PausedUntil != 0 {
		t.Fatalf("resume: %v", paused(out))
	}
}
