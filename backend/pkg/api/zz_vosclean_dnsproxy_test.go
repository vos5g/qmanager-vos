package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
