package daemon

// vosclean: the connection watchdog's recovery ladder for the VOS stick.
//
// Upstream's recovery steps (AT+COPS re-registration, SIM failover, reboot -f)
// either do nothing through qcmd or don't apply to a one-slot dongle, so they
// were switched off. This ladder uses actions tested on the stick:
//
//	step 1  restart the data connection  cricli backhaul_disconnect/connect 1  (~6 s offline)
//	step 2  restart the radio            AT+CFUN=0, AT+CFUN=1 via qcmd         (~15 s offline)
//	step 4  restart the stick            systemctl reboot                      (~80 s offline)
//	        at most once per outage, and at most max_reboots_per_hour
//
// (Step 3, switching to a backup SIM, needs a second SIM slot this board
// doesn't wire, so it is never run.)
//
// The settings come from the Watchdog page (qmanager.conf section watchcat):
// enabled, fail_threshold, cooldown, tier1/2/4_enabled and
// max_reboots_per_hour; the probe interval from its Detection tab
// (ping_profile.json: a named profile or a custom interval). A step runs once the probe has failed fail_threshold
// times in a row; the next step waits `cooldown` seconds after the previous
// one. The ladder starts over when the connection comes back.
//
// It holds off for the first 3 minutes after boot (the modem is still
// attaching) and for 2 minutes after someone uses Reconnect or Restart in the
// UI (/tmp/qmanager_radio_hold). Between its own steps, the cooldown applies.

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"qmanager-backend/pkg/at"
)

// WatchConfig is the subset of the Watchdog page settings the ladder uses.
type WatchConfig struct {
	Enabled           bool
	FailThreshold     int
	CheckInterval     time.Duration
	Cooldown          time.Duration
	Tier1, Tier2      bool
	Tier4             bool
	MaxRebootsPerHour int
}

func (c WatchConfig) normalise() WatchConfig {
	if c.FailThreshold < 1 {
		c.FailThreshold = 5
	}
	if c.CheckInterval < time.Second {
		c.CheckInterval = 5 * time.Second
	}
	if c.Cooldown < 10*time.Second {
		c.Cooldown = 60 * time.Second
	}
	if c.MaxRebootsPerHour < 1 {
		c.MaxRebootsPerHour = 3
	}
	return c
}

const (
	bootHold    = 3 * time.Minute
	radioHold   = 2 * time.Minute
	ladderRetry = 30 * time.Minute // after the last step, start over this long later
)

var (
	RadioHoldFile     = "/tmp/qmanager_radio_hold"
	watchcatStateFile = "/tmp/qmanager_watchcat.json"
	rebootLogFile     = "/data/vosclean/qm/etc/watchdog_reboots"
	lastRebootFile    = "/data/vosclean/qm/etc/watchdog_last_reboot.json"
	eventsFile        = "/tmp/qmanager_events.json"
	// rebootedFile: set when the watchdog restarts the stick, cleared once the
	// connection works again. While it exists the restart step is skipped, so
	// a long outage (no coverage) gets one restart, not one every few minutes.
	rebootedFile = "/data/vosclean/qm/etc/watchdog_rebooted_outage"
)

// Recovery runs the ladder. Its hooks are swappable for tests.
type Recovery struct {
	at     at.Executor
	config func() WatchConfig

	mu       sync.Mutex
	running  bool
	step     int // highest step run in this outage (0 = none)
	lastStep time.Time
	lastTier int
	lastAt   time.Time
	lastWhy  string
	total    int
	state    string
	fails    int

	runCmd func(name string, args ...string) error
	reboot func()
	uptime func() time.Duration
	sleep  func(time.Duration)
}

func NewRecovery(atc at.Executor, config func() WatchConfig) *Recovery {
	r := &Recovery{at: atc, config: config, state: "monitor"}
	r.runCmd = func(name string, args ...string) error {
		return exec.Command(name, args...).Run()
	}
	r.reboot = func() {
		_ = exec.Command("/bin/sh", "-c", "(sleep 2; sync; systemctl reboot || reboot) >/dev/null 2>&1 &").Start()
	}
	r.uptime = func() time.Duration {
		b, err := os.ReadFile("/proc/uptime")
		if err != nil {
			return time.Hour
		}
		f, _ := strconv.ParseFloat(strings.Fields(string(b))[0], 64)
		return time.Duration(f * float64(time.Second))
	}
	r.sleep = time.Sleep
	r.announceLastReboot()
	return r
}

// HoldRadio tells the watchdog someone is deliberately cycling the
// connection, so it doesn't treat the gap as an outage.
func HoldRadio() { _ = os.WriteFile(RadioHoldFile, []byte(time.Now().Format(time.RFC3339)), 0644) }

func held(now time.Time) bool {
	st, err := os.Stat(RadioHoldFile)
	return err == nil && now.Sub(st.ModTime()) < radioHold
}

// OnCheck is called after every probe with its result and the number of
// failures in a row. It decides whether to run the next step.
func (r *Recovery) OnCheck(ok bool, fails int, now time.Time) {
	cfg := r.config().normalise()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fails = fails
	defer r.writeState(cfg, now)

	if ok {
		if _, err := os.Stat(rebootedFile); err == nil {
			_ = os.Remove(rebootedFile)
		}
		if r.step > 0 {
			AppendEvent(now, "watchcat_recovery", "info",
				fmt.Sprintf("Connection back after %s", stepName(r.step)))
		}
		r.step, r.state = 0, "monitor"
		return
	}
	switch {
	case !cfg.Enabled:
		r.state = "disabled"
		return
	case r.running:
		r.state = "recovery"
		return
	case fails < cfg.FailThreshold:
		r.state = "suspect"
		return
	case r.uptime() < bootHold, held(now):
		r.state = "ssr_hold"
		return
	case r.step > 0 && now.Sub(r.lastStep) < cfg.Cooldown:
		r.state = "cooldown"
		return
	}

	next := 0
	for _, s := range []int{1, 2, 4} {
		if s > r.step && map[int]bool{1: cfg.Tier1, 2: cfg.Tier2, 4: cfg.Tier4}[s] {
			next = s
			break
		}
	}
	if next == 0 {
		// Every enabled step has run. Wait, then start over.
		if now.Sub(r.lastStep) >= ladderRetry {
			r.step = 0
		}
		r.state = "locked"
		return
	}
	if next == 4 {
		if _, err := os.Stat(rebootedFile); err == nil {
			// Already restarted once for this outage and it didn't help.
			r.step, r.lastStep, r.state = 4, now, "locked"
			return
		}
		if n := rebootsSince(now.Add(-time.Hour)); n >= cfg.MaxRebootsPerHour {
			if r.state != "locked" {
				AppendEvent(now, "watchcat_recovery", "error",
					fmt.Sprintf("Watchdog would restart the stick, but it already did %d times this hour", n))
			}
			r.step, r.lastStep, r.state = 4, now, "locked"
			return
		}
	}

	r.step, r.lastStep, r.lastTier, r.lastAt, r.state = next, now, next, now, "recovery"
	r.lastWhy = fmt.Sprintf("no connection for %d checks", fails)
	r.total++
	r.running = true
	go r.run(next, fails, now)
}

func (r *Recovery) run(step, fails int, now time.Time) {
	defer func() { r.mu.Lock(); r.running = false; r.mu.Unlock() }()
	why := fmt.Sprintf("no connection for %d checks in a row", fails)
	log.Printf("[Watchdog] recovery step %d (%s): %s", step, stepName(step), why)
	switch step {
	case 1:
		AppendEvent(now, "watchcat_recovery", "warning", "Watchdog restarted the data connection: "+why)
		_ = r.runCmd("cricli", "backhaul_disconnect", "1")
		r.sleep(2 * time.Second)
		_ = r.runCmd("cricli", "backhaul_connect", "1")
	case 2:
		AppendEvent(now, "watchcat_recovery", "warning", "Watchdog restarted the radio: "+why)
		_, _ = r.at.Exec("AT+CFUN=0")
		r.sleep(3 * time.Second)
		for i := 0; i < 3; i++ {
			if out, err := r.at.Exec("AT+CFUN=1"); err == nil && strings.Contains(out, "OK") {
				break
			}
			r.sleep(2 * time.Second)
		}
	case 4:
		AppendEvent(now, "watchcat_recovery", "error", "Watchdog is restarting the stick: "+why)
		logReboot(now, why)
		_ = os.WriteFile(rebootedFile, []byte(strconv.FormatInt(now.Unix(), 10)), 0644)
		MarkPlannedReboot("watchdog")
		r.reboot()
	}
}

func stepName(step int) string {
	switch step {
	case 1:
		return "restarting the data connection"
	case 2:
		return "restarting the radio"
	case 4:
		return "restarting the stick"
	}
	return "recovery"
}

// writeState publishes the live state in the shape the Watchdog page reads
// (WatchdogLiveStatus).
func (r *Recovery) writeState(cfg WatchConfig, now time.Time) {
	cool := 0
	if r.step > 0 && r.state == "cooldown" {
		cool = int((cfg.Cooldown - now.Sub(r.lastStep)).Seconds())
	}
	var lastTime, lastTier any
	if !r.lastAt.IsZero() {
		lastTime, lastTier = r.lastAt.Unix(), r.lastTier
	}
	st := map[string]any{
		"timestamp":            now.Unix(),
		"enabled":              cfg.Enabled,
		"state":                r.state,
		"current_tier":         r.step,
		"failure_count":        r.fails,
		"last_recovery_time":   lastTime,
		"last_recovery_tier":   lastTier,
		"total_recoveries":     r.total,
		"cooldown_remaining":   cool,
		"sim_failover_active":  false,
		"original_sim_slot":    nil,
		"current_sim_slot":     nil,
		"reboots_this_hour":    rebootsSince(now.Add(-time.Hour)),
		"quality_breach_count": 0,
		"quality_enabled":      false,
		"last_recovery_reason": r.lastWhy,
		"ssr_hold":             r.state == "ssr_hold",
		"last_ssr_detected":    nil,
	}
	if b, err := json.Marshal(st); err == nil {
		_ = os.WriteFile(watchcatStateFile+".tmp", b, 0644)
		_ = os.Rename(watchcatStateFile+".tmp", watchcatStateFile)
	}
}

// rebootsSince counts watchdog restarts logged on flash after t.
func rebootsSince(t time.Time) int {
	b, err := os.ReadFile(rebootLogFile)
	if err != nil {
		return 0
	}
	n := 0
	for _, l := range strings.Fields(string(b)) {
		if v, err := strconv.ParseInt(l, 10, 64); err == nil && v >= t.Unix() {
			n++
		}
	}
	return n
}

func logReboot(now time.Time, why string) {
	keep := []string{}
	if b, err := os.ReadFile(rebootLogFile); err == nil {
		for _, l := range strings.Fields(string(b)) {
			if v, err := strconv.ParseInt(l, 10, 64); err == nil && v >= now.Add(-24*time.Hour).Unix() {
				keep = append(keep, l)
			}
		}
	}
	keep = append(keep, strconv.FormatInt(now.Unix(), 10))
	_ = os.WriteFile(rebootLogFile, []byte(strings.Join(keep, "\n")+"\n"), 0644)
	if b, err := json.Marshal(map[string]any{"timestamp": now.Unix(), "reason": why}); err == nil {
		_ = os.WriteFile(lastRebootFile, b, 0644)
	}
	syncFS()
}

// announceLastReboot turns a restart the watchdog did before this boot into
// an event (the event log lives in RAM and was lost with the restart).
func (r *Recovery) announceLastReboot() {
	b, err := os.ReadFile(lastRebootFile)
	if err != nil {
		return
	}
	_ = os.Remove(lastRebootFile)
	var m struct {
		Timestamp int64  `json:"timestamp"`
		Reason    string `json:"reason"`
	}
	if json.Unmarshal(b, &m) != nil || m.Timestamp == 0 {
		return
	}
	r.lastAt, r.lastTier, r.lastWhy = time.Unix(m.Timestamp, 0), 4, m.Reason
	AppendEvent(time.Unix(m.Timestamp, 0), "watchcat_recovery", "error", "Watchdog restarted the stick: "+m.Reason)
}

// syncFS flushes the flash so restart notes survive a power cut (a var so
// tests can skip the real, slow sync).
var syncFS = func() { _ = exec.Command("sync").Run() }

var PlannedRebootFile = "/data/vosclean/qm/etc/planned_reboot"

// MarkPlannedReboot leaves a note on flash that the next restart was planned
// ("user" or "watchdog"), so the restart can be told apart from a crash or
// power loss after boot.
func MarkPlannedReboot(cause string) {
	_ = os.WriteFile(PlannedRebootFile, []byte(cause), 0644)
	syncFS()
}

// TakePlannedReboot returns and clears the planned-restart note:
// "user", "watchdog" or "unplanned".
func TakePlannedReboot() string {
	b, err := os.ReadFile(PlannedRebootFile)
	_ = os.Remove(PlannedRebootFile)
	if c := strings.TrimSpace(string(b)); err == nil && (c == "user" || c == "watchdog") {
		return c
	}
	return "unplanned"
}

// AppendEvent adds one entry to the Recent Activity / Network Events log.
func AppendEvent(ts time.Time, typ, severity, message string) {
	line, err := json.Marshal(map[string]any{"timestamp": ts.Unix(), "type": typ, "severity": severity, "message": message})
	if err != nil {
		return
	}
	var keep []string
	if data, err := os.ReadFile(eventsFile); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				keep = append(keep, l)
			}
		}
	}
	keep = append(keep, string(line))
	if len(keep) > 200 {
		keep = keep[len(keep)-200:]
	}
	_ = os.WriteFile(eventsFile, []byte(strings.Join(keep, "\n")+"\n"), 0644)
}
