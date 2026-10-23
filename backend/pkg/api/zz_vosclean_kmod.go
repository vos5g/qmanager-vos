package api

// vosclean: extra kernel modules (System Settings -> Kernel Modules).
//
// The modules themselves are built for the stick's 4.14.117-perf kernel by the
// vos5g-kmod repo and installed under /data/vosclean/modules/<release>/.
// vosclean owns loading them: MODULES="..." in /data/vosclean/etc/vosclean.conf
// lists the enabled groups, `vosclean modules` loads those and unloads the rest
// (a module still in use stays until a reboot), and boot loads them with a guard
// that skips them after a crash or reset during loading. This page only edits
// that one line and runs `vosclean modules`.
//
//	GET  system/kernel_modules.sh -> state
//	POST system/kernel_modules.sh {"group":"bbr","enabled":true} -> state + result

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	kmRoot    = "/data/vosclean/modules"
	kmVC      = "/data/vosclean/bin/vosclean"
	kmLog     = "/data/vosclean/log/vosclean.log"
	kmTimeout = 60 * time.Second
)

var (
	kmConfPath  = "/data/vosclean/etc/vosclean.conf" // var so tests can point it elsewhere
	kmMu        sync.Mutex
	kmModulesRe = regexp.MustCompile(`^MODULES="([a-z0-9 ]*)"\s*$`)
	kmGroupRe   = regexp.MustCompile(`^[a-z0-9]+$`)
)

// kmFallbackGroups mirrors vosclean's modules.sh, used if the script is older.
var kmFallbackGroups = [][]string{
	{"wireguard", "udp_tunnel", "ip6_udp_tunnel", "wireguard"},
	{"ipv6nat", "nf_nat_ipv6", "nf_nat_masquerade_ipv6", "ip6table_nat", "ip6t_MASQUERADE"},
	{"bbr", "tcp_bbr", "sch_fq"},
	{"fqcodel", "sch_fq_codel"},
	{"xtmatch", "ts_bm", "ts_fsm", "xt_string", "xt_owner", "xt_connbytes"},
}

func kmGroups() [][]string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, kmVC, "modules-list").Output()
	if err != nil {
		return kmFallbackGroups
	}
	var gs [][]string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && kmGroupRe.MatchString(f[0]) {
			gs = append(gs, f)
		}
	}
	if len(gs) == 0 {
		return kmFallbackGroups
	}
	return gs
}

// kmEnabled reads the MODULES line (last one wins, as in the shell).
func kmEnabled() map[string]bool {
	en := map[string]bool{}
	b, err := os.ReadFile(kmConfPath)
	if err != nil {
		return en
	}
	var last string
	for _, l := range strings.Split(string(b), "\n") {
		if m := kmModulesRe.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			last = m[1]
		}
	}
	for _, g := range strings.Fields(last) {
		en[g] = true
	}
	return en
}

// kmSetEnabled rewrites the MODULES line, keeping the rest of the file as is.
func kmSetEnabled(en map[string]bool, order [][]string) error {
	var list []string
	for _, g := range order {
		if en[g[0]] {
			list = append(list, g[0])
		}
	}
	line := fmt.Sprintf(`MODULES="%s"`, strings.Join(list, " "))
	b, err := os.ReadFile(kmConfPath)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	done := false
	for i, l := range lines {
		if kmModulesRe.MatchString(strings.TrimSpace(l)) {
			if !done {
				lines[i], done = line, true
			} else {
				lines[i] = "# (replaced by QManager) " + l
			}
		}
	}
	if !done {
		lines = append(lines, "", "# Extra kernel module groups (set from QManager -> Kernel Modules)", line)
	}
	tmp := kmConfPath + ".qm"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, kmConfPath)
}

func kmLoaded() map[string]bool {
	m := map[string]bool{}
	b, _ := os.ReadFile("/proc/modules")
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) > 0 {
			m[f[0]] = true
		}
	}
	return m
}

func kmRecentLog(n int) []string {
	b, err := os.ReadFile(kmLog)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, " modules") {
			out = append(out, l)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

func kmState() map[string]any {
	rel := vosReadTrim("/proc/sys/kernel/osrelease")
	dir := filepath.Join(kmRoot, rel)
	kos, _ := filepath.Glob(filepath.Join(dir, "*.ko"))
	en, loaded := kmEnabled(), kmLoaded()
	var groups []map[string]any
	for _, g := range kmGroups() {
		var mods []map[string]any
		all, some := true, false
		for _, m := range g[1:] {
			_, err := os.Stat(filepath.Join(dir, m+".ko"))
			mods = append(mods, map[string]any{"name": m, "loaded": loaded[m], "present": err == nil})
			all = all && loaded[m]
			some = some || loaded[m]
		}
		state := "unloaded"
		if all {
			state = "loaded"
		} else if some {
			state = "partial"
		}
		groups = append(groups, map[string]any{"id": g[0], "enabled": en[g[0]], "state": state, "modules": mods})
	}
	return map[string]any{
		"success":        true,
		"kernel":         rel,
		"installed":      len(kos) > 0,
		"module_count":   len(kos),
		"skipped_boot":   fileExists(filepath.Join(kmRoot, "FAILED")),
		"tcp_congestion": vosReadTrim("/proc/sys/net/ipv4/tcp_congestion_control"),
		"groups":         groups,
		"log":            kmRecentLog(6),
	}
}

func (s *Server) HandleKernelModules(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	if r.Method == http.MethodGet {
		reply(kmState())
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		reply(map[string]any{"success": false, "error": "method_not_allowed"})
		return
	}
	var body struct {
		Group   string `json:"group"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil {
		reply(map[string]any{"success": false, "error": "invalid_json"})
		return
	}
	kmMu.Lock()
	defer kmMu.Unlock()
	order := kmGroups()
	known := false
	for _, g := range order {
		known = known || g[0] == body.Group
	}
	if !known {
		reply(map[string]any{"success": false, "error": "unknown_group", "detail": body.Group})
		return
	}
	if !kmState()["installed"].(bool) && body.Enabled {
		reply(map[string]any{"success": false, "error": "not_installed",
			"detail": "No modules are installed for this kernel. Build them with vos5g-kmod and upload with install.sh --modules."})
		return
	}
	en := kmEnabled()
	en[body.Group] = body.Enabled
	if err := kmSetEnabled(en, order); err != nil {
		reply(map[string]any{"success": false, "error": "write_failed", "detail": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), kmTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, kmVC, "modules").CombinedOutput()
	st := kmState()
	st["output"] = strings.TrimSpace(string(out))
	if err != nil {
		st["success"], st["error"], st["detail"] = false, "apply_failed", strings.TrimSpace(string(out)+" "+err.Error())
	}
	// did the requested change take effect?
	for _, g := range st["groups"].([]map[string]any) {
		if g["id"] == body.Group {
			if body.Enabled && g["state"] != "loaded" {
				st["warning"] = "not_loaded"
			} else if !body.Enabled && g["state"] != "unloaded" {
				st["warning"] = "still_in_use"
			}
		}
	}
	reply(st)
}
