package api

// vosclean: System Health Check for the VOS 5G / SG500M2-X.
//
// Contract (src/hooks/use-system-health-check.ts):
//   POST health-check/run.sh       -> {success, job_id, started_at}
//   GET  health-check/status.sh    -> job | {status:"none"}
//   GET  health-check/status.sh?test_id=ID -> {success, test_id, output, truncated}
//   POST health-check/clear.sh     -> {success}
//   GET  health-check/download.sh?job_id=ID -> redacted support bundle (.tar.gz)
//
// Everything runs in this process and lives in RAM (/tmp); nothing is written
// to flash. Categories reuse the page's eight groups with VOS meanings
// (see public/locales/en/system-health-check.json "category").

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type hcTest struct {
	ID         string `json:"id"`
	Category   string `json:"category"`
	Label      string `json:"label"`
	Status     string `json:"status"`
	DurationMS int    `json:"duration_ms"`
	Detail     string `json:"detail"`
}

type hcSummary struct {
	Pass       int `json:"pass"`
	Fail       int `json:"fail"`
	Warn       int `json:"warn"`
	Skip       int `json:"skip"`
	Total      int `json:"total"`
	DurationMS int `json:"duration_ms,omitempty"`
}

type hcJob struct {
	JobID       string    `json:"job_id"`
	Status      string    `json:"status"`
	StartedAt   int64     `json:"started_at"`
	FinishedAt  *int64    `json:"finished_at"`
	PID         int       `json:"pid"`
	Summary     hcSummary `json:"summary"`
	Tests       []hcTest  `json:"tests"`
	TarballPath *string   `json:"tarball_path"`
	TarballSize *int64    `json:"tarball_size"`
	Error       *string   `json:"error"`
}

// hcCheck is one probe: it returns status, a one-line detail and raw output.
type hcCheck struct {
	id, category, label string
	run                 func(s *Server) (status, detail, output string)
}

var (
	hcMu      sync.Mutex
	hcCurrent *hcJob
	hcOutputs = map[string]string{}
)

const hcBundleGlob = "/tmp/qmanager-vos-health-*.tar.gz"

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func hcSh(timeout time.Duration, script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/bin/sh", "-c", script).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func hcAT(s *Server, cmd string) string {
	if s.atClient == nil {
		return ""
	}
	out, err := s.atClient.Exec(cmd)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func hcFreeMB(path string) (free, total float64, ok bool) {
	var st syscallStatfs
	if !statfs(path, &st) {
		return 0, 0, false
	}
	return float64(st.avail) / 1048576, float64(st.total) / 1048576, true
}

func hcMeminfo(key string) int {
	b, _ := os.ReadFile("/proc/meminfo")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, key+":") {
			n, _ := strconv.Atoi(strings.Fields(l)[1])
			return n
		}
	}
	return -1
}

func hcModemSubsys() string {
	m, _ := filepath.Glob("/sys/bus/msm_subsys/devices/*")
	for _, d := range m {
		if vosReadTrim(d+"/name") == "modem" {
			return d
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// the checks
// ---------------------------------------------------------------------------

func hcChecks() []hcCheck {
	return []hcCheck{
		// --- binaries: Software & versions ---------------------------------
		{"qmanager_version", "binaries", "QManager build", func(s *Server) (string, string, string) {
			exe, _ := os.Executable()
			return "pass", fmt.Sprintf("%s, %s %s/%s", filepath.Base(exe), runtime.Version(), runtime.GOOS, runtime.GOARCH), exe
		}},
		{"qcmd", "binaries", "AT translator (qcmd)", func(s *Server) (string, string, string) {
			p := "/data/vosclean/qm/bin/qcmd"
			fi, err := os.Stat(p)
			if err != nil || fi.Mode()&0111 == 0 {
				return "fail", "qcmd is missing or not executable: QManager can't reach the modem", p
			}
			out := hcAT(s, "AT")
			if !strings.Contains(out, "OK") {
				return "fail", "qcmd is installed but didn't answer AT", out
			}
			return "pass", "Installed and answering", out
		}},
		{"firmware", "binaries", "Modem firmware version", func(s *Server) (string, string, string) {
			v := vosReadTrim("/oem/cei_module_ver_vos_id")
			if v == "" {
				return "warn", "Couldn't read the firmware version", ""
			}
			if !strings.HasPrefix(v, "RXMG1.20.00.326.73") {
				return "warn", v + ": this port was tested on RXMG1.20.00.326.73 (v73)", v
			}
			return "pass", v, v
		}},

		// --- permissions: Storage & flash ------------------------------------
		{"data_free", "permissions", "Free space on /data", func(s *Server) (string, string, string) {
			free, total, ok := hcFreeMB("/data")
			if !ok {
				return "fail", "Can't read /data", ""
			}
			d := fmt.Sprintf("%.0f MB free of %.0f MB", free, total)
			switch {
			case free < 5:
				return "fail", d, d
			case free < 20:
				return "warn", d, d
			}
			return "pass", d, d
		}},
		{"rootfs", "permissions", "System partition", func(s *Server) (string, string, string) {
			free, total, _ := hcFreeMB("/")
			ro := strings.Contains(readMountOpts("/"), ",ro,")
			d := fmt.Sprintf("%.0f%% used, read-only=%v", 100-100*free/maxf(total, 1), ro)
			if !ro {
				return "warn", d + ": expected read-only", d
			}
			return "pass", d, d
		}},
		{"tmp_free", "permissions", "RAM disk (/tmp)", func(s *Server) (string, string, string) {
			free, total, ok := hcFreeMB("/tmp")
			if !ok {
				return "fail", "Can't read /tmp", ""
			}
			d := fmt.Sprintf("%.0f MB free of %.0f MB", free, total)
			if free < 10 {
				return "warn", d, d
			}
			return "pass", d, d
		}},
		{"flash_wear", "permissions", "Flash wear (UBI erase counts)", func(s *Server) (string, string, string) {
			devs, _ := filepath.Glob("/sys/class/ubi/ubi[0-9]")
			sort.Strings(devs)
			worst, bad := 0, 0
			var parts []string
			for _, d := range devs {
				ec, _ := strconv.Atoi(vosReadTrim(d + "/max_ec"))
				b, _ := strconv.Atoi(vosReadTrim(d + "/bad_peb_count"))
				if ec > worst {
					worst = ec
				}
				bad += b
				parts = append(parts, fmt.Sprintf("%s: max erase count %d, bad blocks %d", filepath.Base(d), ec, b))
			}
			out := strings.Join(parts, "\n")
			if len(devs) == 0 {
				return "skip", "No UBI devices found", ""
			}
			d := fmt.Sprintf("Most-worn block erased %d times, %d bad blocks", worst, bad)
			switch {
			case worst > 20000:
				return "fail", d, out
			case worst > 5000 || bad > 0:
				return "warn", d, out
			}
			return "pass", d, out
		}},
		{"flash_writes", "permissions", "Recent flash writes", func(s *Server) (string, string, string) {
			out, _ := hcSh(15*time.Second, "find /data /oem /systemrw -xdev -type f -mmin -10 2>/dev/null | grep -v '/data/vosclean/log/heartbeat.log' | head -50")
			n := 0
			if out != "" {
				n = len(strings.Split(out, "\n"))
			}
			if _, err := os.Stat("/data/vosclean/DIAG"); err == nil {
				return "warn", "Diagnostics mode is on: the kernel log is synced to flash every 3 s. Delete /data/vosclean/DIAG when done", out
			}
			d := fmt.Sprintf("%d files changed on flash in the last 10 minutes", n)
			if n > 20 {
				return "warn", d, out
			}
			return "pass", d, out
		}},

		// --- at_transport: Modem & radio ---------------------------------------
		{"modem_identity", "at_transport", "Modem identity", func(s *Server) (string, string, string) {
			out := hcAT(s, "ATI")
			if out == "" || strings.Contains(out, "ERROR") {
				return "fail", "No answer to ATI", out
			}
			lines := strings.Split(out, "\n")
			return "pass", strings.TrimSpace(strings.Join(lines[:min(2, len(lines))], " ")), out
		}},
		{"sim", "at_transport", "SIM card", func(s *Server) (string, string, string) {
			out := hcAT(s, "AT+CPIN?")
			switch {
			case strings.Contains(out, "READY"):
				return "pass", "Ready", out
			case strings.Contains(out, "PIN"):
				return "fail", "SIM needs a PIN/PUK", out
			}
			return "fail", "No SIM detected", out
		}},
		{"registration", "at_transport", "Network registration", func(s *Server) (string, string, string) {
			reg := hcAT(s, "AT+CREG?")
			cops := hcAT(s, "AT+COPS?")
			op := ""
			if m := regexp.MustCompile(`"([^"]+)"`).FindStringSubmatch(cops); m != nil {
				op = m[1]
			}
			out := reg + "\n" + cops
			switch {
			case strings.Contains(reg, ",1"):
				return "pass", "Registered on " + op, out
			case strings.Contains(reg, ",5"):
				return "pass", "Registered (roaming) on " + op, out
			}
			return "fail", "Not registered", out
		}},
		{"signal", "at_transport", "Signal on the serving cell", func(s *Server) (string, string, string) {
			out := hcAT(s, `AT+QENG="servingcell"`)
			f := strings.Split(strings.SplitN(out, "\n", 2)[0], ",")
			// NR5G-SA: ...,band,NR_DL_bandwidth,RSRP,RSRQ,SINR,...  LTE: ...,RSRP,RSRQ,RSSI,SINR
			rat := ""
			if len(f) > 2 {
				rat = strings.Trim(f[2], `"`)
			}
			rsrp := 0
			switch {
			case rat == "NR5G-SA" && len(f) > 12:
				rsrp, _ = strconv.Atoi(strings.TrimSpace(f[12]))
			case rat == "LTE" && len(f) > 13:
				rsrp, _ = strconv.Atoi(strings.TrimSpace(f[13]))
			}
			if rsrp == 0 {
				return "skip", "No serving-cell reading", out
			}
			d := fmt.Sprintf("%s, RSRP %d dBm", rat, rsrp)
			switch {
			case rsrp < -115:
				return "warn", d + " (very weak)", out
			case rsrp < -105:
				return "warn", d + " (weak)", out
			}
			return "pass", d, out
		}},
		{"modem_crashes", "at_transport", "Modem crash handling", func(s *Server) (string, string, string) {
			d := hcModemSubsys()
			if d == "" {
				return "skip", "Modem subsystem not found", ""
			}
			lvl := vosReadTrim(d + "/restart_level")
			crashes, _ := strconv.Atoi(vosReadTrim(d + "/crash_count"))
			log, _ := hcSh(5*time.Second, "tail -n 20 /data/vosclean/log/modem-crashes.log 2>/dev/null")
			out := fmt.Sprintf("restart_level=%s crash_count=%d\n%s", lvl, crashes, log)
			if lvl != "RELATED" {
				return "warn", "restart_level is " + lvl + ": a baseband crash resets the whole device", out
			}
			if crashes > 0 {
				return "warn", fmt.Sprintf("%d modem crash(es) this boot; the radio was restarted on its own", crashes), out
			}
			return "pass", "No crashes this boot; a crash restarts only the radio", out
		}},

		// --- sms ----------------------------------------------------------------
		{"sms_storage", "sms", "SMS storage", func(s *Server) (string, string, string) {
			out := hcAT(s, "AT+CPMS?")
			m := regexp.MustCompile(`\+CPMS: *(\d+),(\d+)`).FindStringSubmatch(out)
			if m == nil {
				return "warn", "Couldn't read SMS storage", out
			}
			used, _ := strconv.Atoi(m[1])
			total, _ := strconv.Atoi(m[2])
			d := fmt.Sprintf("%d of %d slots used", used, total)
			if total > 0 && used*10 >= total*9 {
				return "warn", d + ": nearly full, new texts may be refused", out
			}
			return "pass", d, out
		}},
		{"sms_path", "sms", "SMS sending path", func(s *Server) (string, string, string) {
			out := hcAT(s, `AT+QENG="servingcell"`)
			if strings.Contains(out, "NR5G-SA") {
				return "pass", "On 5G SA: sending switches to LTE briefly (O2 doesn't carry SMS on SA for this module); receiving may be delayed", out
			}
			return "pass", "On LTE/NSA: SMS works directly", out
		}},

		// --- sudoers: Security ------------------------------------------------
		{"ssh_passwords", "sudoers", "SSH password login", func(s *Server) (string, string, string) {
			out, _ := hcSh(5*time.Second, "grep -E '^(PasswordAuthentication|PermitRootLogin)' /etc/ssh/sshd_config_readonly 2>/dev/null")
			if strings.Contains(out, "PasswordAuthentication no") {
				return "pass", "Off (key-only root login)", out
			}
			return "warn", "Password login is on: the stock root password is public. Add an SSH key, then set SSH_PASSWORD_AUTH=no", out
		}},
		{"wwan_firewall", "sudoers", "Firewall on the cellular side", func(s *Server) (string, string, string) {
			v4, e4 := hcSh(5*time.Second, "iptables -C INPUT -j VOSCLEAN_IN && echo on")
			v6, e6 := hcSh(5*time.Second, "ip6tables -C INPUT -j VOSCLEAN_IN && echo on")
			out := "ipv4: " + v4 + "\nipv6: " + v6
			if e4 != nil || e6 != nil {
				return "fail", "Inbound connections from the mobile network aren't blocked", out
			}
			return "pass", "Inbound connections from the mobile network are dropped (IPv4 and IPv6)", out
		}},
		{"fota_blocked", "sudoers", "Vendor update client", func(s *Server) (string, string, string) {
			out, _ := hcSh(5*time.Second, "grep ' /sbin/fotaclient ' /proc/mounts")
			if out != "" {
				return "pass", "Blocked (unauthenticated update and log-upload path)", out
			}
			return "warn", "fotaclient is not blocked", out
		}},
		{"rpcbind", "sudoers", "RPC / NFS services", func(s *Server) (string, string, string) {
			out, _ := hcSh(5*time.Second, "systemctl is-active rpcbind.socket rpcbind.service 2>&1")
			if strings.Contains(out, "active") && !strings.Contains(out, "inactive") {
				return "warn", "rpcbind is running", out
			}
			return "pass", "Stopped", out
		}},
		{"qm_password", "sudoers", "QManager password", func(s *Server) (string, string, string) {
			if _, err := os.Stat("/data/vosclean/qm/etc/auth.json"); err == nil {
				return "pass", "Set", ""
			}
			return "fail", "Not set: the default password admin works. Run vosclean qmpass", ""
		}},

		// --- services -------------------------------------------------------------
		{"vosclean", "services", "vosclean", func(s *Server) (string, string, string) {
			out, _ := hcSh(15*time.Second, "/data/vosclean/bin/vosclean status 2>&1")
			act, _ := hcSh(5*time.Second, "systemctl is-active vosclean-boot")
			if act != "active" {
				return "fail", "The vosclean watch loop isn't running", out
			}
			return "pass", "Running (watch loop active)", out
		}},
		{"qmanager_memory", "services", "QManager memory", func(s *Server) (string, string, string) {
			b, _ := os.ReadFile("/proc/self/status")
			rss := 0
			for _, l := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(l, "VmRSS:") {
					rss, _ = strconv.Atoi(strings.Fields(l)[1])
				}
			}
			d := fmt.Sprintf("%d MB resident (limit 80 MB)", rss/1024)
			if rss > 60*1024 {
				return "warn", d, d
			}
			return "pass", d, d
		}},
		{"memory", "services", "Available memory", func(s *Server) (string, string, string) {
			avail := hcMeminfo("MemAvailable")
			total := hcMeminfo("MemTotal")
			d := fmt.Sprintf("%d MB available of %d MB", avail/1024, total/1024)
			if avail >= 0 && avail < 20*1024 {
				return "warn", d, d
			}
			return "pass", d, d
		}},
		{"temperature", "services", "Temperature", func(s *Server) (string, string, string) {
			zones, _ := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
			hot, hotName := -1000, ""
			var lines []string
			for _, z := range zones {
				t, err := strconv.Atoi(vosReadTrim(z))
				if err != nil {
					continue
				}
				if t > 1000 {
					t /= 1000
				}
				name := vosReadTrim(filepath.Dir(z) + "/type")
				lines = append(lines, fmt.Sprintf("%s: %d C", name, t))
				if t > hot && t < 150 {
					hot, hotName = t, name
				}
			}
			if hot == -1000 {
				return "skip", "No thermal sensors", ""
			}
			d := fmt.Sprintf("Hottest sensor %d °C (%s)", hot, hotName)
			if hot >= 80 {
				return "warn", d, strings.Join(lines, "\n")
			}
			return "pass", d, strings.Join(lines, "\n")
		}},
		{"tailscale", "services", "Tailscale", func(s *Server) (string, string, string) {
			if !tsInstalled() {
				return "skip", "Not installed", ""
			}
			if !tsDaemonRunning() {
				return "warn", "Installed but tailscaled isn't running", ""
			}
			st := tsStatus()
			state, _ := st["backend_state"].(string)
			out, _ := json.MarshalIndent(map[string]any{"backend_state": state, "version": st["version"], "health": st["health"]}, "", "  ")
			if state != "Running" {
				return "warn", "tailscaled is up, state " + state, string(out)
			}
			return "pass", fmt.Sprintf("Connected (%v)", st["version"]), string(out)
		}},

		// --- network -------------------------------------------------------------
		{"wan_ip", "network", "Mobile data connection", func(s *Server) (string, string, string) {
			out, _ := hcSh(10*time.Second, "timeout -t 8 cricli wwan_status 2>&1")
			ip := ""
			ifaces, _ := net.Interfaces()
			for _, ifc := range ifaces {
				if v4, _ := vosIfaceAddrs(ifc.Name); strings.HasPrefix(ifc.Name, "rmnet_data") && len(v4) > 0 {
					ip = v4[0].IP.String()
					break
				}
			}
			if strings.Contains(out, "IPV4: CONNECTED") && ip != "" {
				return "pass", "Connected, WAN IPv4 " + ip, out
			}
			return "fail", "No IPv4 data connection", out
		}},
		{"internet", "network", "Internet reachability", func(s *Server) (string, string, string) {
			out, err := hcSh(12*time.Second, "ping -c 3 -W 3 1.1.1.1 2>&1 | tail -2")
			if err != nil {
				return "fail", "No reply from 1.1.1.1", out
			}
			if m := regexp.MustCompile(`= [\d.]+/([\d.]+)/`).FindStringSubmatch(out); m != nil {
				return "pass", "1.1.1.1 replies, average " + m[1] + " ms", out
			}
			return "pass", "1.1.1.1 replies", out
		}},
		{"dns", "network", "DNS for connected devices", func(s *Server) (string, string, string) {
			r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 4 * time.Second}).DialContext(ctx, "udp", "192.168.225.1:53")
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			ips, err := r.LookupHost(ctx, "example.com")
			if err != nil {
				return "fail", "The stick's DNS server (192.168.225.1) didn't resolve example.com", err.Error()
			}
			return "pass", "Resolves through 192.168.225.1", strings.Join(ips, "\n")
		}},
		{"custom_dns", "network", "Custom DNS rules", func(s *Server) (string, string, string) {
			conf := vosReadTrim("/data/vosclean/etc/dns.conf")
			rules, err := hcSh(5*time.Second, "iptables -t nat -S VOS_DNS 2>/dev/null; iptables -t nat -C OUTPUT -j VOS_DNS && echo hooked")
			out := conf + "\n---\n" + rules
			if !strings.Contains(conf, "DNS_ENABLE=yes") {
				return "pass", "Off: devices use the operator's DNS", out
			}
			if err != nil || !strings.Contains(rules, "hooked") {
				return "fail", "Custom DNS is on, but its redirect rules are missing", out
			}
			m := regexp.MustCompile(`DNS_SERVERS="([^"]*)"`).FindStringSubmatch(conf)
			srv := ""
			if m != nil {
				srv = m[1]
			}
			return "pass", "On: " + srv, out
		}},
		{"ipa", "network", "Hardware fast path (IPA)", func(s *Server) (string, string, string) {
			out, _ := hcSh(5*time.Second, "echo \"ipacm pid: $(pidof ipacm || echo none)\"; ls -l /dev/ipa 2>&1")
			if !strings.Contains(out, "pid: none") && !strings.Contains(out, "No such file") {
				return "pass", "Active: USB traffic is forwarded in hardware", out
			}
			return "warn", "ipacm isn't running: traffic goes through the CPU (slower, hotter)", out
		}},

		// --- configuration -------------------------------------------------------
		{"boot_hook", "configuration", "Boot hook", func(s *Server) (string, string, string) {
			out, _ := hcSh(5*time.Second, "ls -l /oem/script/oem_init_post.sh; grep -n vosclean /oem/script/oem_init_post.sh; ls /data/vosclean/ENABLED")
			if !strings.Contains(out, "boot.sh") || !strings.Contains(out, "-rwx") {
				return "fail", "vosclean won't start after a reboot (hook missing or script not executable)", out
			}
			if !strings.Contains(out, "/data/vosclean/ENABLED") || strings.Contains(out, "No such file") {
				return "warn", "Hook present but vosclean is disabled (no ENABLED file)", out
			}
			return "pass", "Installed and enabled", out
		}},
		{"hostkeys", "configuration", "SSH host keys", func(s *Server) (string, string, string) {
			if _, err := os.Stat("/data/vosclean/hostkeys/ssh_host_ed25519_key"); err == nil {
				return "pass", "Saved: the host key stays the same across reboots", ""
			}
			return "warn", "Not saved: SSH will warn about a changed host key after each reboot", ""
		}},
		{"clock", "configuration", "System clock", func(s *Server) (string, string, string) {
			now := time.Now().UTC()
			if now.Year() < 2025 {
				return "warn", "Clock not set (" + now.Format("2006-01-02") + "): TLS and Tailscale may fail", ""
			}
			return "pass", now.Format("2006-01-02 15:04 UTC"), ""
		}},
		{"resets", "configuration", "Unexpected resets (24 h)", func(s *Server) (string, string, string) {
			b, err := os.ReadFile("/data/vosclean/log/heartbeat.log")
			if err != nil {
				return "skip", "No heartbeat log", ""
			}
			lines := strings.Split(string(b), "\n")
			cut := time.Now().UTC().Add(-24 * time.Hour)
			var resets []string
			for i, l := range lines {
				if !strings.HasPrefix(l, "--- boot") || i == 0 {
					continue
				}
				prev := lines[i-1]
				if len(prev) < 19 {
					continue
				}
				t, err := time.Parse("2006-01-02 15:04:05", prev[:19])
				if err == nil && t.After(cut) {
					resets = append(resets, prev)
				}
			}
			out := "Last heartbeat before each boot in the last 24 h (planned reboots and power cuts included):\n" + strings.Join(resets, "\n")
			d := fmt.Sprintf("%d boots in the last 24 h", len(resets))
			if len(resets) > 3 {
				return "warn", d + ": check power (a weak USB port causes resets) and the modem crash log", out
			}
			return "pass", d, out
		}},
	}
}

// ---------------------------------------------------------------------------
// runner, bundle and handlers
// ---------------------------------------------------------------------------

func hcRun(s *Server, jobID string) {
	start := time.Now()
	checks := hcChecks()
	for _, c := range checks {
		hcMu.Lock()
		if hcCurrent == nil || hcCurrent.JobID != jobID {
			hcMu.Unlock()
			return // cleared mid-run
		}
		hcCurrent.Tests = append(hcCurrent.Tests, hcTest{ID: c.id, Category: c.category, Label: c.label, Status: "running"})
		idx := len(hcCurrent.Tests) - 1
		hcMu.Unlock()

		t0 := time.Now()
		status, detail, output := func() (st, d, o string) {
			defer func() {
				if r := recover(); r != nil {
					st, d, o = "fail", fmt.Sprintf("check crashed: %v", r), ""
				}
			}()
			return c.run(s)
		}()

		hcMu.Lock()
		if hcCurrent == nil || hcCurrent.JobID != jobID {
			hcMu.Unlock()
			return
		}
		t := &hcCurrent.Tests[idx]
		t.Status, t.Detail, t.DurationMS = status, detail, int(time.Since(t0).Milliseconds())
		hcOutputs[c.id] = output
		switch status {
		case "pass":
			hcCurrent.Summary.Pass++
		case "fail":
			hcCurrent.Summary.Fail++
		case "warn":
			hcCurrent.Summary.Warn++
		case "skip":
			hcCurrent.Summary.Skip++
		}
		hcMu.Unlock()
	}

	hcMu.Lock()
	defer hcMu.Unlock()
	if hcCurrent == nil || hcCurrent.JobID != jobID {
		return
	}
	now := time.Now().Unix()
	hcCurrent.FinishedAt = &now
	hcCurrent.Summary.DurationMS = int(time.Since(start).Milliseconds())
	if path, size, err := hcBuildBundle(hcCurrent, hcOutputs); err == nil {
		hcCurrent.TarballPath, hcCurrent.TarballSize = &path, &size
		hcCurrent.Status = "complete"
	} else {
		msg := "support bundle: " + err.Error()
		hcCurrent.Error = &msg
		hcCurrent.Status = "complete_no_bundle"
	}
}

// hcRedactor replaces identifiers (IMEI, ICCID, IMSI, phone number, public IPs)
// with placeholders before anything goes into the support bundle.
func hcRedactor() func(string) string {
	var pairs []string
	add := func(val, repl string) {
		val = strings.TrimSpace(val)
		if len(val) >= 6 {
			pairs = append(pairs, val, repl)
		}
	}
	for _, q := range []struct{ cmd, re, repl string }{
		{"imei", `IMEI: (\d+)`, "<IMEI>"},
		{"iccid", `ICCID: (\w+)`, "<ICCID>"},
		{"imsi", `IMSI: (\d+)`, "<IMSI>"},
		{"msisdn", `Number: \+?(\d+)`, "<PHONE>"},
	} {
		out, _ := hcSh(10*time.Second, "timeout -t 8 cricli "+q.cmd+" 2>/dev/null")
		if m := regexp.MustCompile(q.re).FindStringSubmatch(out); m != nil {
			add(m[1], q.repl)
			if q.cmd == "msisdn" && strings.HasPrefix(m[1], "44") {
				add("0"+m[1][2:], q.repl) // UK national format
			}
		}
	}
	v4, v6 := vosPublicIPs()
	add(v4, "<PUBLIC-IPv4>")
	add(v6, "<PUBLIC-IPv6>")
	r := strings.NewReplacer(pairs...)
	email := regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	return func(s string) string { return email.ReplaceAllString(r.Replace(s), "<EMAIL>") }
}

func hcBuildBundle(job *hcJob, outputs map[string]string) (string, int64, error) {
	redact := hcRedactor()
	files := map[string]string{}
	j, _ := json.MarshalIndent(job, "", "  ")
	files["health-check.json"] = string(j)
	var tests strings.Builder
	for _, t := range job.Tests {
		fmt.Fprintf(&tests, "===== [%s] %s (%s) — %s\n%s\n%s\n\n", t.Category, t.Label, t.ID, t.Status, t.Detail, outputs[t.ID])
	}
	files["test-outputs.txt"] = tests.String()
	cmds := map[string]string{
		"vosclean-status.txt":    "/data/vosclean/bin/vosclean status",
		"versions.txt":           "cat /oem/cei_module_ver_vos_id /etc/version /proc/version; grep PRETTY /etc/os-release",
		"storage.txt":            "df -h; echo; ubinfo -a 2>/dev/null | grep -E 'ubi[0-9]|Name|Size|bad|erase counter'",
		"memory-processes.txt":   "free; echo; ps -o pid,rss,args 2>/dev/null || ps",
		"network.txt":            "ip addr; echo; ip route; echo; ip rule; echo; iptables -S; echo; iptables -t nat -S; echo; ip6tables -S",
		"modem.txt":              "timeout -t 10 cricli rat; timeout -t 10 cricli signal; timeout -t 10 cricli band; timeout -t 10 cricli wwan_status; for d in /sys/bus/msm_subsys/devices/*; do echo \"$(cat $d/name) restart_level=$(cat $d/restart_level) crash_count=$(cat $d/crash_count)\"; done",
		"dmesg-tail.txt":         "dmesg | tail -n 300",
		"syslog-tail.txt":        "tail -n 300 /var/log/messages 2>/dev/null",
		"logs/vosclean.log":      "tail -n 400 /data/vosclean/log/vosclean.log",
		"logs/heartbeat.log":     "tail -n 400 /data/vosclean/log/heartbeat.log",
		"logs/modem-crashes.log": "tail -n 400 /data/vosclean/log/modem-crashes.log 2>/dev/null",
		"logs/qm-requests.log":   "tail -n 200 /data/vosclean/log/qm-requests.log 2>/dev/null",
		"logs/qcmd-writes.log":   "tail -n 200 /data/vosclean/log/qcmd-writes.log 2>/dev/null",
		"logs/qmanager.log":      "tail -n 400 /tmp/vosclean/log/qmanager.log 2>/dev/null",
		"logs/last-boot.txt":     "tail -n 200 /data/vosclean/log/last-boot/*.log 2>/dev/null",
	}
	for name, c := range cmds {
		out, _ := hcSh(20*time.Second, c+" 2>&1")
		files[name] = out + "\n"
	}
	if b, err := os.ReadFile("/tmp/qmanager_status.json"); err == nil {
		files["qmanager-status.json"] = string(b)
	}
	files["README.txt"] = "QManager (VOS 5G) support bundle, job " + job.JobID + ", created " + time.Now().UTC().Format(time.RFC3339) +
		".\nIMEI, ICCID, IMSI, phone number, public IP addresses and e-mail addresses are replaced with placeholders.\n" +
		"Cell IDs and the WAN (carrier-NAT) address are kept because they help with radio problems; remove them before posting publicly if you prefer.\n"

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	root := "qmanager-vos-health-" + time.Now().UTC().Format("20060102-150405")
	for _, n := range names {
		data := []byte(redact(files[n]))
		if err := tw.WriteHeader(&tar.Header{Name: root + "/" + n, Mode: 0644, Size: int64(len(data)), ModTime: time.Now()}); err != nil {
			return "", 0, err
		}
		if _, err := tw.Write(data); err != nil {
			return "", 0, err
		}
	}
	if err := tw.Close(); err != nil {
		return "", 0, err
	}
	if err := gz.Close(); err != nil {
		return "", 0, err
	}
	old, _ := filepath.Glob(hcBundleGlob)
	for _, f := range old {
		_ = os.Remove(f)
	}
	path := "/tmp/" + root + ".tar.gz"
	if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
		return "", 0, err
	}
	return path, int64(buf.Len()), nil
}

func hcJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// HandleHealthCheckRun starts a run (POST).
func (s *Server) HandleHealthCheckRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	hcMu.Lock()
	if hcCurrent != nil && hcCurrent.Status == "running" {
		id := hcCurrent.JobID
		hcMu.Unlock()
		hcJSON(w, map[string]any{"success": false, "error": "already_running", "detail": "A diagnostic is already running (" + id + ")"})
		return
	}
	now := time.Now()
	id := fmt.Sprintf("hc-%d", now.UnixNano())
	hcCurrent = &hcJob{JobID: id, Status: "running", StartedAt: now.Unix(), PID: os.Getpid(),
		Tests: []hcTest{}, Summary: hcSummary{Total: len(hcChecks())}}
	hcOutputs = map[string]string{}
	hcMu.Unlock()
	go hcRun(s, id)
	hcJSON(w, map[string]any{"success": true, "job_id": id, "started_at": now.Unix()})
}

// HandleHealthCheckStatus returns the current job, or one test's raw output.
func (s *Server) HandleHealthCheckStatus(w http.ResponseWriter, r *http.Request) {
	hcMu.Lock()
	defer hcMu.Unlock()
	if id := r.URL.Query().Get("test_id"); id != "" {
		out, ok := hcOutputs[id]
		if hcCurrent == nil || !ok {
			hcJSON(w, map[string]any{"success": false, "error": "not_found"})
			return
		}
		truncated := false
		if len(out) > 4096 {
			out, truncated = out[len(out)-4096:], true
		}
		hcJSON(w, map[string]any{"success": true, "test_id": id, "output": out, "truncated": truncated})
		return
	}
	if hcCurrent == nil {
		hcJSON(w, map[string]any{"status": "none"})
		return
	}
	hcJSON(w, hcCurrent)
}

// HandleHealthCheckClear forgets the last run and deletes its bundle (POST).
func (s *Server) HandleHealthCheckClear(w http.ResponseWriter, r *http.Request) {
	hcMu.Lock()
	defer hcMu.Unlock()
	old, _ := filepath.Glob(hcBundleGlob)
	for _, f := range old {
		_ = os.Remove(f)
	}
	hcCurrent, hcOutputs = nil, map[string]string{}
	hcJSON(w, map[string]any{"success": true})
}

// HandleHealthCheckDownload streams the redacted support bundle.
func (s *Server) HandleHealthCheckDownload(w http.ResponseWriter, r *http.Request) {
	hcMu.Lock()
	job := hcCurrent
	hcMu.Unlock()
	if job == nil || job.TarballPath == nil {
		http.Error(w, `{"success":false,"error":"no_bundle"}`, http.StatusNotFound)
		return
	}
	if q := r.URL.Query().Get("job_id"); q != "" && q != job.JobID {
		http.Error(w, `{"success":false,"error":"stale_job"}`, http.StatusNotFound)
		return
	}
	data, err := os.ReadFile(*job.TarballPath)
	if err != nil {
		http.Error(w, `{"success":false,"error":"no_bundle"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(*job.TarballPath)))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// --- small portable helpers ---------------------------------------------------

func readMountOpts(mount string) string {
	b, _ := os.ReadFile("/proc/mounts")
	opts := ""
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) >= 4 && f[1] == mount {
			opts = f[3] // last match wins (overlays)
		}
	}
	return "," + opts + ","
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

type syscallStatfs struct{ avail, total uint64 }

func statfs(path string, out *syscallStatfs) bool {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return false
	}
	out.avail = uint64(st.Bavail) * uint64(st.Bsize)
	out.total = uint64(st.Blocks) * uint64(st.Bsize)
	return true
}
