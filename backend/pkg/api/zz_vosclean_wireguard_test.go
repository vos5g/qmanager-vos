package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"qmanager-backend/pkg/wg"
)

// Throwaway keys generated for this test only.
const (
	wgtPriv = "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk="
	wgtPub  = "xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg="
	wgtPSK  = "FpCyhws9cxwWoV4xELtfJvjJN+zQVRPISllRWgeopVE="
)

func wgCall(t *testing.T, method, query string, body any) (map[string]any, string) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, "/cgi-bin/quecmanager/vpn/wireguard.sh"+query, rd)
	rec := httptest.NewRecorder()
	(&Server{}).HandleWireGuard(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s: HTTP %d", method, query, rec.Code)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	return m, rec.Body.String()
}

func TestWireGuardAPISecrets(t *testing.T) {
	d := t.TempDir()
	wg.Dir, wg.ConfigDir, wg.ActiveFile = d, filepath.Join(d, "configs"), filepath.Join(d, "ACTIVE")
	wg.StateFile = filepath.Join(d, "state.json")

	text := "[Interface]\nPrivateKey = " + wgtPriv + "\nAddress = 10.2.0.2/32\nDNS = 10.2.0.1\nPostUp = touch /tmp/pwned\n\n" +
		"[Peer]\nPublicKey = " + wgtPub + "\nPresharedKey = " + wgtPSK + "\nAllowedIPs = 0.0.0.0/0\nEndpoint = 203.0.113.5:51820\n"
	m, raw := wgCall(t, "POST", "", map[string]any{"action": "import", "name": "Proton UK", "text": text,
		"engine": "userspace", "route": "lan", "kill_switch": true})
	if m["success"] != true {
		t.Fatalf("import: %s", raw)
	}
	if ig, _ := m["ignored"].([]any); len(ig) != 1 {
		t.Fatalf("PostUp not reported: %s", raw)
	}
	if strings.Contains(raw, wgtPriv) || strings.Contains(raw, wgtPSK) {
		t.Fatal("secret in list response")
	}

	if c, _ := wg.Load("Proton UK"); c == nil || !c.KillSwitch || c.Route != "lan" {
		t.Fatal("import options not applied")
	}
	_, raw = wgCall(t, "GET", "?name=Proton+UK", nil)
	if strings.Contains(raw, wgtPriv) || strings.Contains(raw, wgtPSK) || !strings.Contains(raw, `"has_private_key":true`) ||
		!strings.Contains(raw, `"has_preshared_key":true`) {
		t.Fatalf("edit view: %s", raw)
	}

	// Rename + edit with blank keys: both secrets kept.
	m, raw = wgCall(t, "POST", "", map[string]any{
		"action": "save", "original_name": "Proton UK", "name": "Proton London",
		"addresses": []string{"10.2.0.2/32"}, "dns": []string{"10.2.0.1"}, "engine": "kernel", "route": "all", "kill_switch": false,
		"peers": []map[string]any{{"public_key": wgtPub, "endpoint": "203.0.113.5:51820", "allowed_ips": []string{"0.0.0.0/0"}, "keepalive": 25}},
	})
	if m["success"] != true {
		t.Fatalf("save: %s", raw)
	}
	c, err := wg.Load("Proton London")
	if err != nil || c.PrivateKey != wgtPriv || c.Peers[0].PresharedKey != wgtPSK || c.Engine != "kernel" || c.Route != "all" || c.KillSwitch {
		t.Fatalf("after save: %+v %v", c, err)
	}
	if wg.Exists("Proton UK") {
		t.Fatal("old name still there after rename")
	}

	// New config without a key is refused; with generate_key it gets one.
	newCfg := map[string]any{"action": "save", "name": "Home", "addresses": []string{"10.9.0.2/24"},
		"peers": []map[string]any{{"public_key": wgtPub, "endpoint": "vpn.example.org:51820", "allowed_ips": []string{"10.9.0.0/24"}}}}
	if m, _ = wgCall(t, "POST", "", newCfg); m["success"] == true {
		t.Fatal("saved without a private key")
	}
	newCfg["generate_key"] = true
	if m, raw = wgCall(t, "POST", "", newCfg); m["success"] != true || m["public_key"] == "" {
		t.Fatalf("generate: %s", raw)
	}
	// Duplicate name (any case) refused.
	if m, _ = wgCall(t, "POST", "", newCfg); m["error"] != "name_taken" {
		t.Fatal("duplicate accepted")
	}
	newCfg["name"] = "home"
	if m, _ = wgCall(t, "POST", "", newCfg); m["error"] != "name_taken" {
		t.Fatal("duplicate (case) accepted")
	}

	// Clearing the preshared key works.
	m, raw = wgCall(t, "POST", "", map[string]any{
		"action": "save", "original_name": "Proton London", "name": "Proton London",
		"addresses": []string{"10.2.0.2/32"},
		"peers":     []map[string]any{{"public_key": wgtPub, "clear_preshared_key": true, "endpoint": "203.0.113.5:51820", "allowed_ips": []string{"0.0.0.0/0"}}},
	})
	if c, _ = wg.Load("Proton London"); m["success"] != true || c.Peers[0].PresharedKey != "" {
		t.Fatalf("clear psk: %s", raw)
	}

	if m, _ = wgCall(t, "POST", "", map[string]any{"action": "delete", "name": "Home"}); m["success"] != true || wg.Exists("Home") {
		t.Fatal("delete")
	}
	if m, _ = wgCall(t, "POST", "", map[string]any{"action": "import", "name": "bad", "text": "hello"}); m["error"] != "invalid_config" {
		t.Fatal("garbage import accepted")
	}
}
