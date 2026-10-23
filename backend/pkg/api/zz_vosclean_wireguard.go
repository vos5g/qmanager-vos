package api

// vosclean: WireGuard VPN client (VPN -> WireGuard).
//
// Saved configs and the tunnel service live in pkg/wg. vosclean owns the
// service's lifecycle (`vosclean wg start|stop`, and at boot when
// /data/vosclean/wireguard/ACTIVE names a config), like it does for modules.
//
//	GET  vpn/wireguard.sh            -> configs + status
//	GET  vpn/wireguard.sh?name=X     -> one config for the edit form (no secrets)
//	POST vpn/wireguard.sh {"action": "save"|"import"|"delete"|"connect"|"disconnect", ...}
//
// Private and preshared keys never leave the stick: the edit form gets
// has_private_key / has_preshared_key, and a blank key on save keeps the old one.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"qmanager-backend/pkg/wg"
)

const wgUnit = "vos-wg"

var (
	wgMu      sync.Mutex
	wgVC      = kmVC // vosclean
	wgTimeout = 30 * time.Second
)

func wgPublicKey(priv string) string {
	k, err := wgtypes.ParseKey(priv)
	if err != nil {
		return ""
	}
	return k.PublicKey().String()
}

func wgUnitActive() bool {
	return exec.Command("systemctl", "is-active", "--quiet", wgUnit).Run() == nil
}

func wgVosclean(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), wgTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, wgVC, append([]string{"wg"}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func wgSummary(name string) map[string]any {
	c, err := wg.Load(name)
	if err != nil {
		return map[string]any{"name": name, "error": err.Error()}
	}
	endpoint := ""
	var allowed []string
	full := false
	for _, p := range c.Peers {
		if endpoint == "" {
			endpoint = p.Endpoint
		}
		for _, a := range p.AllowedIPs {
			allowed = append(allowed, a)
			full = full || a == "0.0.0.0/0"
		}
	}
	return map[string]any{
		"name": name, "addresses": c.Addresses, "dns": c.DNS, "endpoint": endpoint,
		"peer_count": len(c.Peers), "allowed_ips": allowed, "full_tunnel": full,
		"engine": c.Engine, "route": c.Route, "kill_switch": c.KillSwitch,
		"mtu": c.EffectiveMTU(), "public_key": wgPublicKey(c.PrivateKey),
	}
}

// wgLive reads the interface's peers (kernel or userspace) via wgctrl.
func wgLive() map[string]any {
	cl, err := wgctrl.New()
	if err != nil {
		return nil
	}
	defer cl.Close()
	d, err := cl.Device(wg.Iface)
	if err != nil {
		return nil
	}
	var rx, tx int64
	var last time.Time
	endpoint := ""
	for _, p := range d.Peers {
		rx += p.ReceiveBytes
		tx += p.TransmitBytes
		if p.LastHandshakeTime.After(last) {
			last = p.LastHandshakeTime
		}
		if endpoint == "" && p.Endpoint != nil {
			endpoint = p.Endpoint.String()
		}
	}
	live := map[string]any{"rx_bytes": rx, "tx_bytes": tx, "endpoint": endpoint, "listen_port": d.ListenPort}
	if !last.IsZero() {
		live["last_handshake"] = last.Unix()
		live["handshake_age_s"] = int64(time.Since(last).Seconds())
	}
	return live
}

func wgStatus() map[string]any {
	active := wg.Active()
	st := wg.ReadState()
	running := wgUnitActive()
	status := map[string]any{"active": nil, "phase": "disconnected", "service_running": running}
	if active != "" {
		status["active"] = active
		phase := "starting"
		if st != nil && st.Name == active {
			phase = st.Phase
			status["error"] = st.Error
			status["since"] = st.Since
			status["engine"], status["route"], status["kill_switch"] = st.Engine, st.Route, st.KillSwitch
		}
		if !running && phase != "error" {
			phase = "error"
			status["error"] = "the tunnel service isn't running; it retries every 15 seconds"
		}
		status["phase"] = phase
		if live := wgLive(); live != nil {
			status["live"] = live
		}
		if ks, _ := status["kill_switch"].(bool); ks && phase == "error" {
			status["blocking"] = true
		}
	}
	return status
}

func wgList() map[string]any {
	names := wg.List()
	configs := make([]map[string]any, 0, len(names))
	for _, n := range names {
		configs = append(configs, wgSummary(n))
	}
	return map[string]any{
		"success":         true,
		"configs":         configs,
		"status":          wgStatus(),
		"kernel_loaded":   kmLoaded()["wireguard"],
		"kernel_present":  fileExists(kmRoot + "/" + vosReadTrim("/proc/sys/kernel/osrelease") + "/wireguard.ko"),
		"netbird_running": nbDaemonRunning(),
	}
}

// wgEditView is one config for the form, without secrets.
func wgEditView(c *wg.Config) map[string]any {
	peers := make([]map[string]any, 0, len(c.Peers))
	for _, p := range c.Peers {
		peers = append(peers, map[string]any{
			"public_key": p.PublicKey, "endpoint": p.Endpoint, "allowed_ips": p.AllowedIPs,
			"keepalive": p.Keepalive, "has_preshared_key": p.PresharedKey != "",
		})
	}
	return map[string]any{
		"name": c.Name, "addresses": c.Addresses, "dns": c.DNS, "mtu": c.MTU, "listen_port": c.ListenPort,
		"engine": c.Engine, "route": c.Route, "kill_switch": c.KillSwitch, "peers": peers,
		"has_private_key": c.PrivateKey != "", "public_key": wgPublicKey(c.PrivateKey),
	}
}

type wgFormPeer struct {
	PublicKey         string   `json:"public_key"`
	PresharedKey      string   `json:"preshared_key"`
	ClearPresharedKey bool     `json:"clear_preshared_key"`
	Endpoint          string   `json:"endpoint"`
	AllowedIPs        []string `json:"allowed_ips"`
	Keepalive         int      `json:"keepalive"`
}

type wgRequest struct {
	Action       string       `json:"action"`
	Name         string       `json:"name"`
	OriginalName string       `json:"original_name"`
	Text         string       `json:"text"`
	PrivateKey   string       `json:"private_key"`
	GenerateKey  bool         `json:"generate_key"`
	Addresses    []string     `json:"addresses"`
	DNS          []string     `json:"dns"`
	MTU          int          `json:"mtu"`
	ListenPort   int          `json:"listen_port"`
	Engine       string       `json:"engine"`
	Route        string       `json:"route"`
	KillSwitch   bool         `json:"kill_switch"`
	Peers        []wgFormPeer `json:"peers"`
}

func trimAll(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// wgFromForm builds a config from the form, keeping secrets left blank.
func wgFromForm(r *wgRequest, old *wg.Config) (*wg.Config, error) {
	c := &wg.Config{
		Name: strings.TrimSpace(r.Name), Addresses: trimAll(r.Addresses), DNS: trimAll(r.DNS),
		MTU: r.MTU, ListenPort: r.ListenPort, Engine: r.Engine, Route: r.Route, KillSwitch: r.KillSwitch,
	}
	switch {
	case r.GenerateKey:
		k, err := wgtypes.GeneratePrivateKey()
		if err != nil {
			return nil, err
		}
		c.PrivateKey = k.String()
	case strings.TrimSpace(r.PrivateKey) != "":
		c.PrivateKey = strings.TrimSpace(r.PrivateKey)
	case old != nil:
		c.PrivateKey = old.PrivateKey
	default:
		return nil, errors.New("private key: paste the one your VPN provider gave you, or generate a new one")
	}
	oldPSK := map[string]string{}
	if old != nil {
		for _, p := range old.Peers {
			oldPSK[p.PublicKey] = p.PresharedKey
		}
	}
	for _, fp := range r.Peers {
		p := wg.Peer{
			PublicKey: strings.TrimSpace(fp.PublicKey), Endpoint: strings.TrimSpace(fp.Endpoint),
			AllowedIPs: trimAll(fp.AllowedIPs), Keepalive: fp.Keepalive,
		}
		switch {
		case strings.TrimSpace(fp.PresharedKey) != "":
			p.PresharedKey = strings.TrimSpace(fp.PresharedKey)
		case !fp.ClearPresharedKey:
			p.PresharedKey = oldPSK[p.PublicKey]
		}
		c.Peers = append(c.Peers, p)
	}
	return c, nil
}

// wgConflicts are checks that depend on what else runs on the stick.
func wgConflicts(c *wg.Config) error {
	if nbDaemonRunning() {
		return errors.New("NetBird is running. Stop it on the NetBird page first: both change the stick's routing")
	}
	return c.CGNATConflict()
}

// wgEnsureKernelModule switches the WireGuard module group on (via vosclean,
// with its crash guard) if a kernel-engine config needs it.
func wgEnsureKernelModule() error {
	if kmLoaded()["wireguard"] {
		return nil
	}
	kmMu.Lock()
	defer kmMu.Unlock()
	order := kmGroups()
	en := kmEnabled()
	if !en["wireguard"] {
		en["wireguard"] = true
		if err := kmSetEnabled(en, order); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), kmTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, kmVC, "modules").CombinedOutput()
	if !kmLoaded()["wireguard"] {
		msg := "the kernel WireGuard module didn't load (see Kernel Modules); use the userspace engine instead"
		if err != nil {
			msg += ": " + strings.TrimSpace(string(out))
		}
		return errors.New(msg)
	}
	return nil
}

// wgStart (re)starts the service and waits for it to report.
func wgStart() error {
	before := wg.ReadState()
	if out, err := wgVosclean("start"); err != nil {
		return errors.New(strings.TrimSpace("couldn't start the tunnel service: " + out))
	}
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		st := wg.ReadState()
		if st != nil && st.Phase != "starting" && (before == nil || st.PID != before.PID || st.Since != before.Since) {
			if st.Phase == "error" {
				return errors.New(st.Error)
			}
			return nil
		}
	}
	return nil // still starting: the page keeps polling
}

func wgStop() error {
	if err := wg.SetActive(""); err != nil {
		return err
	}
	if out, err := wgVosclean("stop"); err != nil {
		return errors.New(strings.TrimSpace("couldn't stop the tunnel service: " + out))
	}
	return nil
}

func wgConnect(name string) error {
	c, err := wg.Load(name)
	if err != nil {
		return err
	}
	if err := c.Validate(wg.LANPrefix()); err != nil {
		return err
	}
	if err := wgConflicts(c); err != nil {
		return err
	}
	if c.Engine == wg.EngineKernel {
		if err := wgEnsureKernelModule(); err != nil {
			return err
		}
	}
	if err := wg.SetActive(name); err != nil {
		return err
	}
	return wgStart()
}

func (s *Server) HandleWireGuard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	fail := func(code, detail string) {
		reply(map[string]any{"success": false, "error": code, "detail": detail})
	}
	if r.Method == http.MethodGet {
		if name := r.URL.Query().Get("name"); name != "" {
			c, err := wg.Load(name)
			if err != nil {
				fail("not_found", err.Error())
				return
			}
			reply(map[string]any{"success": true, "config": wgEditView(c)})
			return
		}
		reply(wgList())
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		fail("method_not_allowed", "")
		return
	}
	var req wgRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		fail("invalid_json", err.Error())
		return
	}
	wgMu.Lock()
	defer wgMu.Unlock()
	lan := wg.LANPrefix()
	ok := func(extra map[string]any) {
		res := wgList()
		for k, v := range extra {
			res[k] = v
		}
		reply(res)
	}

	switch req.Action {
	case "save":
		var old *wg.Config
		if req.OriginalName != "" {
			var err error
			if old, err = wg.Load(req.OriginalName); err != nil {
				fail("not_found", err.Error())
				return
			}
		}
		c, err := wgFromForm(&req, old)
		if err != nil {
			fail("invalid_config", err.Error())
			return
		}
		if err := c.Validate(lan); err != nil {
			fail("invalid_config", err.Error())
			return
		}
		renamed := old != nil && c.Name != old.Name
		if (old == nil || !strings.EqualFold(c.Name, old.Name)) && wg.Exists(c.Name) {
			fail("name_taken", "a config called "+c.Name+" already exists")
			return
		}
		wasActive := old != nil && wg.Active() == old.Name
		if wasActive {
			if err := wgConflicts(c); err != nil {
				fail("conflict", err.Error())
				return
			}
		}
		if err := wg.Save(c); err != nil {
			fail("write_failed", err.Error())
			return
		}
		if renamed {
			_ = wg.Delete(old.Name)
		}
		extra := map[string]any{"saved": c.Name, "public_key": wgPublicKey(c.PrivateKey)}
		if wasActive { // apply the edit to the running tunnel
			if err := wgConnect(c.Name); err != nil {
				extra["warning"] = "saved, but reconnecting failed: " + err.Error()
			}
		}
		ok(extra)

	case "import":
		c, err := wg.Parse(req.Text)
		if err != nil {
			fail("invalid_config", err.Error())
			return
		}
		c.Name = strings.TrimSpace(req.Name)
		if req.Engine != "" { // the import tabs send the stick options too
			c.Engine, c.Route, c.KillSwitch = req.Engine, req.Route, req.KillSwitch
		}
		if err := c.Validate(lan); err != nil {
			fail("invalid_config", err.Error())
			return
		}
		if wg.Exists(c.Name) {
			fail("name_taken", "a config called "+c.Name+" already exists")
			return
		}
		if err := wg.Save(c); err != nil {
			fail("write_failed", err.Error())
			return
		}
		ok(map[string]any{"saved": c.Name, "ignored": c.Ignored, "public_key": wgPublicKey(c.PrivateKey)})

	case "delete":
		if !wg.Exists(req.Name) {
			fail("not_found", "no saved config called "+req.Name)
			return
		}
		if wg.Active() == req.Name {
			if err := wgStop(); err != nil {
				fail("stop_failed", err.Error())
				return
			}
		}
		if err := wg.Delete(req.Name); err != nil {
			fail("delete_failed", err.Error())
			return
		}
		ok(nil)

	case "connect":
		if err := wgConnect(req.Name); err != nil {
			code := "connect_failed"
			if errors.Is(err, wg.ErrNotFound) {
				code = "not_found"
			}
			fail(code, err.Error())
			return
		}
		ok(nil)

	case "disconnect":
		if err := wgStop(); err != nil {
			fail("stop_failed", err.Error())
			return
		}
		ok(nil)

	default:
		fail("unknown_action", req.Action)
	}
}
