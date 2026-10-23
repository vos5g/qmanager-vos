package api

// vosclean: real Tailscale management for the Compal RXM-G1 / VOS 5G.
//
// Upstream QManager-GO only stubs this endpoint. Here the official static
// ARM build from pkgs.tailscale.com is downloaded by this Go process (the
// modem's busybox wget cannot do modern TLS), verified against the published
// SHA-256, and unpacked into /data/vosclean/tailscale/bin. tailscaled runs as a
// transient systemd unit in kernel-TUN mode with netfilter management off
// (QCMAP owns iptables on this firmware) and with log upload disabled.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"qmanager-backend/pkg/wg"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	tsDir        = "/data/vosclean/tailscale"
	tsBinDir     = tsDir + "/bin"
	tsStateDir   = tsDir + "/state"
	tsCLI        = tsBinDir + "/tailscale"
	tsDaemon     = tsBinDir + "/tailscaled"
	tsBootFlag   = tsDir + "/ENABLED_ON_BOOT"
	tsUnit       = "tailscaled"
	tsPkgIndex   = "https://pkgs.tailscale.com/stable/?mode=json"
	tsPkgBase    = "https://pkgs.tailscale.com/stable/"
	tsNeedFreeKB = 60 * 1024 // unpacked size is about 70 MB; UBIFS compresses it
	// Tiny: a single combined binary (tailscaled with the CLI built in) compiled
	// from Tailscale's own source with unneeded features left out
	// (tools/build-tailscale-tiny.sh). install.sh --tailscale-tiny uploads it here.
	tsTinyUpload = "/data/vosclean/qm/tailscale-tiny"
)

type tsInstallState struct {
	sync.Mutex
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

var tsInstall = &tsInstallState{Status: "idle"}

func (t *tsInstallState) set(status, msg, detail string) {
	t.Lock()
	t.Status, t.Message, t.Detail = status, msg, detail
	t.Unlock()
}

func (t *tsInstallState) snapshot() map[string]any {
	t.Lock()
	defer t.Unlock()
	return map[string]any{"success": t.Status != "error", "status": t.Status, "message": t.Message, "detail": t.Detail}
}

func tsInstalled() bool {
	a, err1 := os.Stat(tsCLI)
	b, err2 := os.Stat(tsDaemon)
	return err1 == nil && err2 == nil && a.Mode()&0111 != 0 && b.Mode()&0111 != 0
}

func tsDaemonRunning() bool {
	return exec.Command("systemctl", "is-active", "--quiet", tsUnit).Run() == nil
}

func tsRun(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, tsCLI, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// tsStartDaemon starts tailscaled as a transient unit (same mechanism vosclean uses for QManager).
func tsStartDaemon() error {
	if tsDaemonRunning() {
		return nil
	}
	_ = os.MkdirAll(tsStateDir, 0700)
	_ = os.MkdirAll("/var/run/tailscale", 0755)
	_ = exec.Command("systemctl", "reset-failed", tsUnit).Run()
	args := []string{"--unit=" + tsUnit, "-p", "Restart=on-failure", "-p", "RestartSec=10", "-p", "MemoryMax=90M",
		"--setenv=GOGC=200", "--setenv=GOMEMLIMIT=60MiB", "--setenv=HOME=" + tsDir,
		tsDaemon, "--state=" + tsStateDir + "/tailscaled.state", "--statedir=" + tsStateDir,
		"--socket=/var/run/tailscale/tailscaled.sock", "--port=41641", "--no-logs-no-support"}
	if out, err := exec.Command("systemd-run", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	for i := 0; i < 20; i++ { // wait for the LocalAPI socket
		if _, err := os.Stat("/var/run/tailscale/tailscaled.sock"); err == nil {
			tsForwarding()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("tailscaled did not start (see: journalctl -u %s)", tsUnit)
}

func tsStopDaemon() {
	_ = exec.Command("systemctl", "stop", tsUnit).Run()
	_ = exec.Command("systemctl", "reset-failed", tsUnit).Run()
}

type tsPrefsJSON struct {
	AdvertiseRoutes []string
	RouteAll        bool
	ExitNodeID      string
	ExitNodeIP      string
}

func tsPrefs() (tsPrefsJSON, bool) {
	var p tsPrefsJSON
	out, err := tsRun(8*time.Second, "debug", "prefs")
	if err != nil || json.Unmarshal([]byte(out), &p) != nil {
		return p, false
	}
	return p, true
}

func (p tsPrefsJSON) exitNodeAdvertised() bool {
	for _, r := range p.AdvertiseRoutes {
		if r == "0.0.0.0/0" || r == "::/0" {
			return true
		}
	}
	return false
}

// tsForwarding (re)applies the iptables rules that let devices behind the stick
// use accepted subnet routes: MASQUERADE out of tailscale0 and MSS clamping.
// vosclean's watch loop keeps them in place if QCMAP flushes the tables.
func tsForwarding() {
	_ = exec.Command("/data/vosclean/bin/vosclean", "tsfw").Run()
}

type tsPeerJSON struct {
	HostName       string
	DNSName        string
	TailscaleIPs   []string
	OS             string
	Online         bool
	LastSeen       string
	Relay          string
	ExitNode       bool
	ExitNodeOption bool
	ID             string
	PrimaryRoutes  []string
}

func tsPeerOut(p tsPeerJSON) map[string]any {
	ips := p.TailscaleIPs
	if ips == nil {
		ips = []string{}
	}
	return map[string]any{
		"hostname": p.HostName, "dns_name": strings.TrimSuffix(p.DNSName, "."), "tailscale_ips": ips,
		"os": p.OS, "online": p.Online, "last_seen": p.LastSeen, "relay": p.Relay, "exit_node": p.ExitNode,
		"exit_node_option": p.ExitNodeOption, "routes": append([]string{}, p.PrimaryRoutes...),
	}
}

func tsStatus() map[string]any {
	out := map[string]any{"success": true, "installed": false}
	if !tsInstalled() && nbInstalled() { // one mesh VPN at a time: both use 100.64.0.0/10
		out["other_vpn_installed"], out["other_vpn_name"] = true, "NetBird"
	}
	if !tsInstalled() {
		hint := "Official: downloads Tailscale's static ARM build from pkgs.tailscale.com (about 47 MB on /data)."
		if _, err := os.Stat(tsTinyUpload); err == nil {
			hint += " Tiny: a 15 MB single-binary build uploaded with install.sh is ready to install."
		} else {
			hint += " Tiny: build it with tools/build-tailscale-tiny.sh and upload it with install.sh --tailscale-tiny."
		}
		out["install_hint"] = hint
		return out
	}
	out["installed"] = true
	out["install_variant"] = "official"
	if fi, err := os.Lstat(tsCLI); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		out["install_variant"] = "tiny"
	}
	_, err := os.Stat(tsBootFlag)
	out["enabled_on_boot"] = err == nil
	if v, err := tsRun(5*time.Second, "version"); err == nil {
		out["version"] = strings.SplitN(v, "\n", 2)[0]
	}
	if !tsDaemonRunning() {
		out["daemon_running"] = false
		return out
	}
	out["daemon_running"] = true
	raw, err := tsRun(10*time.Second, "status", "--json")
	var st struct {
		Version        string
		BackendState   string
		AuthURL        string
		Self           *tsPeerJSON
		Health         []string
		CurrentTailnet *struct {
			Name            string
			MagicDNSSuffix  string
			MagicDNSEnabled bool
		}
		Peer map[string]tsPeerJSON
	}
	if jerr := json.Unmarshal([]byte(raw), &st); jerr != nil {
		out["backend_state"] = "Unknown"
		if err != nil {
			out["error_detail"] = raw
		}
		return out
	}
	if st.Version != "" {
		out["version"] = strings.SplitN(st.Version, "-", 2)[0]
	}
	out["backend_state"] = st.BackendState
	if st.AuthURL != "" {
		out["auth_url"] = st.AuthURL
	}
	if st.Self != nil {
		out["self"] = tsPeerOut(*st.Self)
	}
	if st.CurrentTailnet != nil {
		out["tailnet"] = map[string]any{"name": st.CurrentTailnet.Name, "magic_dns_suffix": st.CurrentTailnet.MagicDNSSuffix, "magic_dns_enabled": st.CurrentTailnet.MagicDNSEnabled}
	}
	peers := []map[string]any{}
	for _, p := range st.Peer {
		peers = append(peers, tsPeerOut(p))
	}
	out["peers"] = peers
	if st.Health == nil {
		st.Health = []string{}
	}
	out["health"] = st.Health
	prefs, _ := tsPrefs()
	out["exit_node_advertised"] = prefs.exitNodeAdvertised()
	out["accept_routes"] = prefs.RouteAll
	inUse := prefs.ExitNodeIP
	if inUse == "" && prefs.ExitNodeID != "" {
		for _, p := range st.Peer {
			if p.ID == prefs.ExitNodeID {
				for _, ip := range p.TailscaleIPs {
					if strings.Count(ip, ".") == 3 {
						inUse = ip
						break
					}
				}
			}
		}
	}
	out["exit_node_in_use"] = inUse
	return out
}

func tsFreeKB(path string) int64 {
	var s syscall.Statfs_t
	if syscall.Statfs(path, &s) != nil {
		return -1
	}
	return int64(s.Bavail) * int64(s.Bsize) / 1024
}

func tsHTTPGet(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return resp, nil
}

// tsDoInstall downloads, verifies and unpacks the official ARM tarball.
func tsDoInstall() {
	fail := func(msg string, err error) {
		d := ""
		if err != nil {
			d = err.Error()
		}
		tsInstall.set("error", msg, d)
	}
	if free := tsFreeKB("/data"); free >= 0 && free < tsNeedFreeKB && !tsInstalled() {
		fail(fmt.Sprintf("Not enough space on /data: %d MB free, about %d MB needed", free/1024, tsNeedFreeKB/1024), nil)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	tsInstall.set("running", "Looking up the latest Tailscale release...", "")
	resp, err := tsHTTPGet(ctx, tsPkgIndex)
	if err != nil {
		fail("Could not reach pkgs.tailscale.com", err)
		return
	}
	var idx struct {
		TarballsVersion string
		Tarballs        map[string]string
	}
	err = json.NewDecoder(resp.Body).Decode(&idx)
	resp.Body.Close()
	name := idx.Tarballs["arm"]
	if err != nil || name == "" || strings.ContainsAny(name, "/\\") {
		fail("Unexpected package index from pkgs.tailscale.com", err)
		return
	}

	resp, err = tsHTTPGet(ctx, tsPkgBase+name+".sha256")
	if err != nil {
		fail("Could not fetch the checksum", err)
		return
	}
	sumRaw, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	resp.Body.Close()
	wantSum := strings.Fields(string(sumRaw) + " ")[0]
	if len(wantSum) != 64 {
		fail("Bad checksum file", nil)
		return
	}

	tsInstall.set("running", "Downloading "+name+" (about 37 MB)...", "")
	resp, err = tsHTTPGet(ctx, tsPkgBase+name)
	if err != nil {
		fail("Download failed", err)
		return
	}
	defer resp.Body.Close()
	h := sha256.New()
	gz, err := gzip.NewReader(io.TeeReader(resp.Body, h))
	if err != nil {
		fail("Download is not a valid archive", err)
		return
	}
	tmpDir := tsDir + "/bin.new"
	_ = os.RemoveAll(tmpDir)
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		fail("Cannot write to /data", err)
		return
	}
	got := map[string]bool{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = os.RemoveAll(tmpDir)
			fail("Download interrupted", err)
			return
		}
		base := filepath.Base(hdr.Name)
		if hdr.Typeflag != tar.TypeReg || (base != "tailscale" && base != "tailscaled") || filepath.Base(filepath.Dir(hdr.Name)) == "systemd" {
			continue
		}
		tsInstall.set("running", "Unpacking "+base+"...", "")
		f, err := os.OpenFile(filepath.Join(tmpDir, base), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
		if err != nil {
			_ = os.RemoveAll(tmpDir)
			fail("Cannot write to /data", err)
			return
		}
		_, err = io.Copy(f, tr)
		f.Close()
		if err != nil {
			_ = os.RemoveAll(tmpDir)
			fail("Unpacking failed (disk full?)", err)
			return
		}
		got[base] = true
	}
	_, _ = io.Copy(io.Discard, resp.Body) // hash the whole download
	if hex.EncodeToString(h.Sum(nil)) != wantSum {
		_ = os.RemoveAll(tmpDir)
		fail("Checksum mismatch: download discarded", nil)
		return
	}
	if !got["tailscale"] || !got["tailscaled"] {
		_ = os.RemoveAll(tmpDir)
		fail("Archive did not contain tailscale and tailscaled", nil)
		return
	}

	tsStopDaemon()
	_ = os.RemoveAll(tsBinDir)
	if err := os.Rename(tmpDir, tsBinDir); err != nil {
		fail("Could not install the binaries", err)
		return
	}
	_ = vosWriteFileDurable(tsBootFlag, []byte("1\n"), 0644)
	tsInstall.set("running", "Starting tailscaled...", "")
	if err := tsStartDaemon(); err != nil {
		fail("Installed, but tailscaled did not start", err)
		return
	}

	tsInstall.set("complete", "Tailscale "+idx.TarballsVersion+" installed. Press Connect to log in.", "")
}

// tsUp runs `tailscale up` with this device's fixed flags. It blocks until
// logged in, so it runs in the background; the status poll shows the auth URL.
func tsUp() (string, error) {
	if err := tsStartDaemon(); err != nil {
		return "", err
	}
	host, _ := os.Hostname()
	if host == "" || host == "localhost" || strings.HasPrefix(host, "mdm") || host == "sdxprairie" {
		host = "vos5g"
	}
	args := []string{"up", "--reset", "--netfilter-mode=off", "--accept-dns=false", "--hostname=" + host}
	// "up --reset" must restate every setting we keep, or it would drop them
	if prefs, ok := tsPrefs(); ok {
		if prefs.exitNodeAdvertised() {
			args = append(args, "--advertise-exit-node")
		}
		if prefs.RouteAll {
			args = append(args, "--accept-routes")
		}
		if prefs.ExitNodeIP != "" {
			args = append(args, "--exit-node="+prefs.ExitNodeIP, "--exit-node-allow-lan-access")
		} else if prefs.ExitNodeID != "" {
			args = append(args, "--exit-node="+prefs.ExitNodeID, "--exit-node-allow-lan-access")
		}
	}
	done := make(chan error, 1)
	var out []byte
	go func() {
		var err error
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		out, err = exec.CommandContext(ctx, tsCLI, args...).CombinedOutput()
		done <- err
	}()
	select {
	case err := <-done:
		return strings.TrimSpace(string(out)), err
	case <-time.After(8 * time.Second):
		return "", nil // waiting for browser login; auth_url appears in status
	}
}

func tsEnableForwarding() {
	_ = os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644)
	_ = os.WriteFile("/proc/sys/net/ipv6/conf/all/forwarding", []byte("1"), 0644)
}

// HandleVPNTailscale implements GET status and the POST actions used by use-tailscale.ts.
func (s *Server) HandleVPNTailscale(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	if r.Method == http.MethodGet {
		_ = enc.Encode(tsStatus())
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Action  string `json:"action"`
		Variant string `json:"variant"`
		Enabled bool   `json:"enabled"`
		Node    string `json:"node"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	ok := func(extra map[string]any) {
		m := map[string]any{"success": true}
		for k, v := range extra {
			m[k] = v
		}
		_ = enc.Encode(m)
	}
	bad := func(code string, err error, out string) {
		d := strings.TrimSpace(out)
		if err != nil && d == "" {
			d = err.Error()
		}
		_ = enc.Encode(map[string]any{"success": false, "error": code, "detail": d})
	}

	switch body.Action {
	case "install":
		if nbInstalled() && !tsInstalled() {
			_ = enc.Encode(map[string]any{"success": false, "error": "other_vpn_installed",
				"detail": "NetBird is installed. Both use the 100.64.0.0/10 range, so uninstall NetBird first."})
			return
		}
		if body.Variant == "tiny" {
			tsInstall.set("running", "Installing Tailscale Tiny...", "")
			if err := tsInstallTiny(); err != nil {
				tsInstall.set("error", "Tailscale Tiny could not be installed", err.Error())
			} else {
				tsInstall.set("complete", "Tailscale Tiny installed. Press Connect to log in.", "")
			}
			ok(nil)
			return
		}
		tsInstall.Lock()
		busy := tsInstall.Status == "running"
		if !busy {
			tsInstall.Status, tsInstall.Message, tsInstall.Detail = "running", "Starting installation...", ""
		}
		tsInstall.Unlock()
		if !busy {
			go tsDoInstall()
		}
		ok(nil)
	case "install_status":
		_ = enc.Encode(tsInstall.snapshot())
	}
	if body.Action == "install" || body.Action == "install_status" {
		return
	}
	if !tsInstalled() {
		bad("not_installed", nil, "Tailscale is not installed")
		return
	}
	switch body.Action {
	case "connect":
		if n := wg.ActiveCGNATConfig(); n != "" {
			bad("wireguard_active", nil, "The WireGuard config "+n+" is connected and uses 100.64.0.0/10, the range Tailscale uses. Disconnect it on the WireGuard page first.")
			return
		}
		out, err := tsUp()
		if err != nil {
			bad("connect_failed", err, out)
			return
		}
		st := tsStatus()
		ok(map[string]any{"auth_url": st["auth_url"], "already_authenticated": st["backend_state"] == "Running"})
	case "disconnect":
		if out, err := tsRun(15*time.Second, "down"); err != nil {
			bad("disconnect_failed", err, out)
			return
		}
		ok(nil)
	case "logout":
		if out, err := tsRun(20*time.Second, "logout"); err != nil {
			bad("logout_failed", err, out)
			return
		}
		ok(nil)
	case "start_service":
		if n := wg.ActiveCGNATConfig(); n != "" {
			bad("wireguard_active", nil, "The WireGuard config "+n+" is connected and uses 100.64.0.0/10, the range Tailscale uses. Disconnect it on the WireGuard page first.")
			return
		}
		if err := tsStartDaemon(); err != nil {
			bad("start_failed", err, "")
			return
		}
		ok(nil)
	case "stop_service":
		tsStopDaemon()
		ok(nil)
	case "set_boot_enabled":
		if body.Enabled {
			_ = vosWriteFileDurable(tsBootFlag, []byte("1\n"), 0644)
		} else {
			_ = os.Remove(tsBootFlag)
		}
		ok(nil)
	case "set_accept_routes":
		if out, err := tsRun(15*time.Second, "set", fmt.Sprintf("--accept-routes=%v", body.Enabled)); err != nil {
			bad("accept_routes_failed", err, out)
			return
		}
		if body.Enabled {
			tsEnableForwarding()
		}
		tsForwarding()
		ok(nil)
	case "set_use_exit_node":
		node := strings.TrimSpace(body.Node)
		args := []string{"set", "--exit-node="}
		if node != "" {
			if ip := net.ParseIP(node); ip == nil {
				bad("bad_exit_node", nil, "not an IP address: "+node)
				return
			}
			args = []string{"set", "--exit-node=" + node, "--exit-node-allow-lan-access=true"}
		}
		if out, err := tsRun(20*time.Second, args...); err != nil {
			bad("exit_node_failed", err, out)
			return
		}
		ok(nil)
	case "set_exit_node":
		if body.Enabled {
			tsEnableForwarding()
		}
		if out, err := tsRun(15*time.Second, "set", fmt.Sprintf("--advertise-exit-node=%v", body.Enabled)); err != nil {
			bad("exit_node_failed", err, out)
			return
		}
		ok(nil)
	case "uninstall":
		_, _ = tsRun(15*time.Second, "logout")
		tsStopDaemon()
		if err := os.RemoveAll(tsDir); err != nil {
			bad("uninstall_failed", err, "")
			return
		}
		tsInstall.set("idle", "", "")
		ok(nil)
	default:
		bad("unknown_action", nil, "unknown action: "+body.Action)
	}
}

// tsInstallTiny installs the combined binary uploaded by install.sh: tailscaled
// itself, plus "tailscale" as a symlink (the binary acts as the CLI when called
// by that name). The login state in tsStateDir is kept.
func tsInstallTiny() error {
	fi, err := os.Stat(tsTinyUpload)
	if err != nil {
		return fmt.Errorf("no Tiny build uploaded: build it with tools/build-tailscale-tiny.sh, then run install.sh --tailscale-tiny dist/tailscaled-tiny-armv7")
	}
	if fi.Size() < 1<<20 {
		return fmt.Errorf("%s looks truncated (%d bytes)", tsTinyUpload, fi.Size())
	}
	tmp := tsDir + "/bin.new"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0755); err != nil {
		return err
	}
	in, err := os.Open(tsTinyUpload)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(tmp+"/tailscaled", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil {
		in.Close()
		return err
	}
	_, err = io.Copy(out, in)
	in.Close()
	out.Close()
	if err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("copy failed (disk full?): %v", err)
	}
	if err := os.Symlink("tailscaled", tmp+"/tailscale"); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if v, err := exec.Command(tmp+"/tailscale", "version").CombinedOutput(); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("the uploaded binary does not run here: %v %s", err, strings.TrimSpace(string(v)))
	}
	tsStopDaemon()
	_ = os.RemoveAll(tsBinDir)
	if err := os.Rename(tmp, tsBinDir); err != nil {
		return err
	}
	_ = os.Remove(tsTinyUpload) // the installed copy is the only one kept
	_ = vosWriteFileDurable(tsBootFlag, []byte("1\n"), 0644)
	return tsStartDaemon()
}
