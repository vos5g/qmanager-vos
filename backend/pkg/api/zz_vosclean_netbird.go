package api

// vosclean: NetBird on the VOS 5G stick.
//
// Upstream only looked for a netbird binary in system paths (read-only here)
// and had no "install" action, so the page's Install button did nothing.
// This installs NetBird the way the Tailscale page does: the official static
// ARM build from GitHub (linux_armv6 runs on the Cortex-A7), checked against
// the release's published SHA-256 list, kept under /data/vosclean/netbird.
//
// The daemon runs as a transient systemd unit "netbird" with its state on
// /data (NB_STATE_DIR; the default /var/lib/netbird is read-only), its log in
// RAM, and NetBird's DNS management off: devices behind the stick resolve
// through the stick's dnsmasq, and NetBird rewriting the stick's resolver
// would break that. vosclean starts it at boot when ENABLED_ON_BOOT exists.

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"qmanager-backend/pkg/wg"
	"regexp"
	"strings"
	"time"
)

const (
	nbDir        = "/data/vosclean/netbird"
	nbBin        = nbDir + "/bin/netbird"
	nbStateDir   = nbDir + "/state"
	nbConfig     = nbStateDir + "/config.json"
	nbBootFlag   = nbDir + "/ENABLED_ON_BOOT"
	nbUnit       = "netbird"
	nbLog        = "/tmp/vosclean/log/netbird.log"
	nbReleases   = "https://github.com/netbirdio/netbird/releases"
	nbNeedFreeKB = 48 * 1024 // binary is about 40 MB
)

var nbInstall = &tsInstallState{Status: "idle"}

func nbInstalled() bool {
	st, err := os.Stat(nbBin)
	return err == nil && st.Mode()&0111 != 0
}

func nbDaemonRunning() bool {
	return exec.Command("systemctl", "is-active", "--quiet", nbUnit).Run() == nil
}

func nbRun(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, nbBin, args...)
	cmd.Env = append(os.Environ(), "NB_STATE_DIR="+nbStateDir)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(nbStripGRPCNoise(string(out))), err
}

// The CLI prints gRPC reconnect warnings while the daemon socket comes up.
func nbStripGRPCNoise(s string) string {
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, "WARNING: [core]") || strings.Contains(l, "caller_not_available") {
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(keep, "\n")
}

func nbStartDaemon() error {
	if nbDaemonRunning() {
		return nil
	}
	_ = os.MkdirAll(nbStateDir, 0700)
	_ = os.MkdirAll(path.Dir(nbLog), 0755)
	_ = exec.Command("systemctl", "reset-failed", nbUnit).Run()
	args := []string{"--unit=" + nbUnit, "-p", "Restart=on-failure", "-p", "RestartSec=10", "-p", "MemoryMax=90M",
		"--setenv=NB_STATE_DIR=" + nbStateDir, "--setenv=GOMEMLIMIT=60MiB", "--setenv=HOME=" + nbDir,
		"--setenv=NB_LOG_MAX_SIZE_MB=1",
		nbBin, "service", "run", "--config", nbConfig, "--log-file", nbLog, "--log-level", "info"}
	if out, err := exec.Command("systemd-run", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	for i := 0; i < 30; i++ {
		if _, err := os.Stat("/var/run/netbird.sock"); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("the NetBird daemon did not start (log: %s)", nbLog)
}

func nbStopDaemon() {
	_ = exec.Command("systemctl", "stop", nbUnit).Run()
	_ = exec.Command("systemctl", "reset-failed", nbUnit).Run()
}

// --- install ------------------------------------------------------------------

var nbVersionRe = regexp.MustCompile(`/tag/v(\d+\.\d+\.\d+)$`)

func nbLatestVersion(ctx context.Context) (string, error) {
	resp, err := tsHTTPGet(ctx, nbReleases+"/latest") // redirects to .../tag/vX.Y.Z
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	m := nbVersionRe.FindStringSubmatch(resp.Request.URL.Path)
	if m == nil {
		return "", fmt.Errorf("couldn't read the latest version from %s", resp.Request.URL)
	}
	return m[1], nil
}

func nbDoInstall() {
	fail := func(msg string, err error) {
		d := ""
		if err != nil {
			d = err.Error()
		}
		nbInstall.set("error", msg, d)
	}
	if free := tsFreeKB("/data"); free >= 0 && free < nbNeedFreeKB && !nbInstalled() {
		fail(fmt.Sprintf("Not enough space on /data: %d MB free, about %d MB needed", free/1024, nbNeedFreeKB/1024), nil)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	nbInstall.set("running", "Finding the latest NetBird release…", "")
	ver, err := nbLatestVersion(ctx)
	if err != nil {
		fail("Couldn't reach GitHub", err)
		return
	}
	name := fmt.Sprintf("netbird_%s_linux_armv6.tar.gz", ver)
	base := fmt.Sprintf("%s/download/v%s/", nbReleases, ver)

	resp, err := tsHTTPGet(ctx, base+fmt.Sprintf("netbird_%s_checksums.txt", ver))
	if err != nil {
		fail("Couldn't download the checksum list", err)
		return
	}
	want := ""
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 1<<20))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && f[1] == name {
			want = f[0]
		}
	}
	resp.Body.Close()
	if len(want) != 64 {
		fail("The release has no checksum for "+name, nil)
		return
	}

	nbInstall.set("running", fmt.Sprintf("Downloading NetBird %s (about 15 MB)…", ver), "")
	tmp := "/tmp/netbird-download.tar.gz"
	defer os.Remove(tmp)
	resp, err = tsHTTPGet(ctx, base+name)
	if err != nil {
		fail("Download failed", err)
		return
	}
	f, err := os.Create(tmp)
	if err != nil {
		resp.Body.Close()
		fail("Couldn't write to /tmp", err)
		return
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 100<<20))
	resp.Body.Close()
	f.Close()
	if err != nil {
		fail("Download failed", err)
		return
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		fail("Checksum mismatch: the download was not the published file", fmt.Errorf("got %s, want %s", got, want))
		return
	}

	nbInstall.set("running", "Unpacking…", "")
	if err := os.MkdirAll(path.Dir(nbBin), 0755); err != nil {
		fail("Couldn't create "+nbDir, err)
		return
	}
	if err := nbExtract(tmp, nbBin+".new"); err != nil {
		os.Remove(nbBin + ".new")
		fail("Couldn't unpack NetBird", err)
		return
	}
	nbStopDaemon()
	if err := os.Rename(nbBin+".new", nbBin); err != nil {
		fail("Couldn't install the binary", err)
		return
	}
	if out, err := nbRun(15*time.Second, "version"); err != nil {
		fail("The installed binary doesn't run on this modem", fmt.Errorf("%v: %s", err, out))
		return
	}
	nbInstall.set("running", "Starting the NetBird service…", "")
	if err := nbStartDaemon(); err != nil {
		fail("Installed, but the service didn't start", err)
		return
	}
	nbInstall.set("complete", fmt.Sprintf("NetBird %s installed", ver), "")
}

func nbExtract(tarball, dst string) error {
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("no netbird binary in the archive")
		}
		if err != nil {
			return err
		}
		if path.Base(hdr.Name) != "netbird" || hdr.Typeflag != tar.TypeReg {
			continue
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(tr, 200<<20))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		return err
	}
}

// --- status -------------------------------------------------------------------

// nbStatusJSON is the part of `netbird status --json` the page shows.
type nbStatusJSON struct {
	Peers struct {
		Total     int `json:"total"`
		Connected int `json:"connected"`
		Details   []struct {
			FQDN             string `json:"fqdn"`
			NetbirdIP        string `json:"netbirdIp"`
			Status           string `json:"status"`
			ConnType         string `json:"connectionType"`
			LastStatusUpdate string `json:"lastStatusUpdate"`
			TransferReceived int64  `json:"transferReceived"`
			TransferSent     int64  `json:"transferSent"`
		} `json:"details"`
	} `json:"peers"`
	DaemonVersion string `json:"daemonVersion"`
	DaemonStatus  string `json:"daemonStatus"` // NeedsLogin, Idle, Connecting, Connected, LoginFailed, SessionExpired
	Management    struct {
		URL       string `json:"url"`
		Connected bool   `json:"connected"`
	} `json:"management"`
	Signal struct {
		URL       string `json:"url"`
		Connected bool   `json:"connected"`
	} `json:"signal"`
	NetbirdIP    string `json:"netbirdIp"`
	FQDN         string `json:"fqdn"`
	UsesKernelIF bool   `json:"usesKernelInterface"`
}

func nbConnWord(ok bool) string {
	if ok {
		return "Connected"
	}
	return "Disconnected"
}

func nbBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func nbStatus() map[string]any {
	out := map[string]any{"success": true, "installed": nbInstalled(), "daemon_running": false,
		"enabled_on_boot": fileExists(nbBootFlag)}
	if tsInstalled() && !nbInstalled() { // one mesh VPN at a time: both use 100.64.0.0/10
		out["other_vpn_installed"], out["other_vpn_name"] = true, "Tailscale"
	}
	if !nbInstalled() {
		out["install_hint"] = "NetBird isn't installed. Install downloads the official ARM build (about 15 MB download, 40 MB on /data)."
		return out
	}
	if v, err := nbRun(10*time.Second, "version"); err == nil {
		out["version"] = strings.TrimSpace(v)
	}
	if !nbDaemonRunning() {
		return out
	}
	out["daemon_running"] = true
	raw, err := nbRun(15*time.Second, "status", "--json")
	var st nbStatusJSON
	i := strings.Index(raw, "{")
	if err != nil || i < 0 || json.Unmarshal([]byte(raw[i:]), &st) != nil {
		// not logged in yet: the CLI answers with text, not JSON
		out["backend_state"] = "NeedsLogin"
		if txt, _ := nbRun(10*time.Second, "status"); strings.Contains(txt, "Daemon status:") {
			for _, l := range strings.Split(txt, "\n") {
				if v, ok := strings.CutPrefix(strings.TrimSpace(l), "Daemon status:"); ok {
					out["backend_state"] = strings.TrimSpace(v)
				}
			}
		}
		return out
	}
	state := st.DaemonStatus
	if state == "" {
		state = "Disconnected"
		if st.Management.Connected && st.NetbirdIP != "" {
			state = "Connected"
		}
	}
	out["backend_state"] = state
	out["management"] = nbConnWord(st.Management.Connected)
	out["signal"] = nbConnWord(st.Signal.Connected)
	out["fqdn"] = st.FQDN
	out["netbird_ip"] = st.NetbirdIP
	out["interface_type"] = map[bool]string{true: "Kernel", false: "Userspace"}[st.UsesKernelIF]
	out["peers_total"] = st.Peers.Total
	out["peers_connected"] = st.Peers.Connected
	peers := []map[string]any{}
	for _, p := range st.Peers.Details {
		peers = append(peers, map[string]any{
			"hostname": strings.TrimSuffix(strings.SplitN(p.FQDN, ".", 2)[0], "."), "netbird_ip": p.NetbirdIP,
			"status": p.Status, "connection_type": p.ConnType,
			"direct":            map[bool]string{true: "true", false: "false"}[p.ConnType == "P2P"],
			"last_seen":         p.LastStatusUpdate,
			"transfer_received": nbBytes(p.TransferReceived), "transfer_sent": nbBytes(p.TransferSent),
		})
	}
	out["peers"] = peers
	return out
}

// --- handler --------------------------------------------------------------------

func (s *Server) HandleNetBird(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	if r.Method == http.MethodGet {
		reply(nbStatus())
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		reply(map[string]any{"success": false, "error": "method_not_allowed"})
		return
	}
	var body map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		reply(map[string]any{"success": false, "error": "invalid_json"})
		return
	}
	action, _ := body["action"].(string)
	notInstalled := func() bool {
		if !nbInstalled() {
			reply(map[string]any{"success": false, "error": "not_installed"})
			return true
		}
		return false
	}

	switch action {
	case "install":
		if tsInstalled() && !nbInstalled() {
			reply(map[string]any{"success": false, "error": "other_vpn_installed",
				"detail": "Tailscale is installed. Both use the 100.64.0.0/10 range, so uninstall Tailscale first."})
			return
		}
		nbInstall.Lock()
		busy := nbInstall.Status == "running"
		if !busy {
			nbInstall.Status, nbInstall.Message, nbInstall.Detail = "running", "Starting installation…", ""
		}
		nbInstall.Unlock()
		if !busy {
			go nbDoInstall()
		}
		reply(map[string]any{"success": true, "status": "running"})
	case "install_status":
		reply(nbInstall.snapshot())
	case "start_service":
		if notInstalled() || wgBlocksNetBird(reply) {
			return
		}
		if err := nbStartDaemon(); err != nil {
			reply(map[string]any{"success": false, "error": "start_failed", "detail": err.Error()})
			return
		}
		reply(map[string]any{"success": true})
	case "stop_service":
		nbStopDaemon()
		reply(map[string]any{"success": true})
	case "connect":
		if notInstalled() || wgBlocksNetBird(reply) {
			return
		}
		key, _ := body["setup_key"].(string)
		key = strings.TrimSpace(key)
		if key == "" {
			reply(map[string]any{"success": false, "error": "setup_key_required",
				"detail": "Enter a setup key (NetBird dashboard → Setup Keys). Browser login isn't supported from the modem."})
			return
		}
		if err := nbStartDaemon(); err != nil {
			reply(map[string]any{"success": false, "error": "start_failed", "detail": err.Error()})
			return
		}
		args := []string{"up", "--setup-key", key, "--disable-dns", "--hostname", "vos5g"}
		if out, err := nbRun(90*time.Second, args...); err != nil {
			reply(map[string]any{"success": false, "error": "connect_failed", "detail": strings.TrimSpace(out + " " + err.Error())})
			return
		}
		reply(map[string]any{"success": true})
	case "disconnect":
		if notInstalled() {
			return
		}
		if out, err := nbRun(30*time.Second, "down"); err != nil {
			reply(map[string]any{"success": false, "error": "disconnect_failed", "detail": strings.TrimSpace(out + " " + err.Error())})
			return
		}
		reply(map[string]any{"success": true})
	case "set_boot_enabled":
		v, _ := body["enabled"].(bool)
		var err error
		if v {
			err = vosWriteFileDurable(nbBootFlag, []byte("1\n"), 0644)
		} else if err = os.Remove(nbBootFlag); os.IsNotExist(err) {
			err = nil
		}
		if err != nil {
			reply(map[string]any{"success": false, "error": "write_failed", "detail": err.Error()})
			return
		}
		reply(map[string]any{"success": true})
	case "uninstall":
		if nbInstalled() && nbDaemonRunning() {
			_, _ = nbRun(20*time.Second, "down")
		}
		nbStopDaemon()
		if err := os.RemoveAll(nbDir); err != nil {
			reply(map[string]any{"success": false, "error": "uninstall_failed", "detail": err.Error()})
			return
		}
		nbInstall.set("idle", "", "")
		reply(map[string]any{"success": true})
	default:
		reply(map[string]any{"success": false, "error": "unknown_action", "detail": action})
	}
}

// wgBlocksNetBird refuses to start NetBird while a WireGuard config is
// connected: both change the stick's routing.
func wgBlocksNetBird(reply func(any)) bool {
	if a := wg.Active(); a != "" {
		reply(map[string]any{"success": false, "error": "wireguard_active",
			"detail": "The WireGuard config " + a + " is connected. Disconnect it on the WireGuard page first: both change the stick's routing."})
		return true
	}
	return false
}
