package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type recAT struct {
	mu   sync.Mutex
	cmds []string
}

func (r *recAT) Exec(c string) (string, error) {
	r.mu.Lock()
	r.cmds = append(r.cmds, c)
	r.mu.Unlock()
	return "OK", nil
}

func newTestRecovery(t *testing.T, cfg *WatchConfig) (*Recovery, *recAT, *[]string, *int) {
	d := t.TempDir()
	RadioHoldFile = filepath.Join(d, "hold")
	watchcatStateFile = filepath.Join(d, "watchcat.json")
	rebootLogFile = filepath.Join(d, "reboots")
	lastRebootFile = filepath.Join(d, "last_reboot.json")
	eventsFile = filepath.Join(d, "events.json")
	PlannedRebootFile = filepath.Join(d, "planned_reboot")
	rebootedFile = filepath.Join(d, "rebooted_outage")
	syncFS = func() {}
	at := &recAT{}
	r := NewRecovery(at, func() WatchConfig { return *cfg })
	var cmds []string
	reboots := 0
	r.runCmd = func(name string, args ...string) error {
		cmds = append(cmds, name+" "+strings.Join(args, " "))
		return nil
	}
	r.reboot = func() { reboots++ }
	r.uptime = func() time.Duration { return time.Hour }
	r.sleep = func(time.Duration) {}
	return r, at, &cmds, &reboots
}

// wait for the step's goroutine, then clear the hold it set (time passes).
func settle(r *Recovery) {
	for i := 0; i < 2000; i++ { // up to 10 s: CI machines can be slow
		r.mu.Lock()
		running := r.running
		r.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = os.Remove(RadioHoldFile)
}

func TestRecoveryLadder(t *testing.T) {
	cfg := &WatchConfig{Enabled: true, FailThreshold: 3, CheckInterval: 10 * time.Second, Cooldown: 60 * time.Second, Tier1: true, Tier2: true, Tier4: true, MaxRebootsPerHour: 1}
	r, at, cmds, reboots := newTestRecovery(t, cfg)
	t0 := time.Now()

	r.OnCheck(false, 2, t0) // below the threshold
	settle(r)
	if len(*cmds) != 0 || r.state != "suspect" {
		t.Fatalf("acted early: %v %s", *cmds, r.state)
	}
	r.OnCheck(false, 3, t0) // step 1: data connection
	settle(r)
	if len(*cmds) != 2 || (*cmds)[0] != "cricli backhaul_disconnect 1" || (*cmds)[1] != "cricli backhaul_connect 1" {
		t.Fatalf("step 1: %v", *cmds)
	}
	r.OnCheck(false, 4, t0.Add(30*time.Second)) // within cooldown
	settle(r)
	if r.state != "cooldown" || len(at.cmds) != 0 {
		t.Fatalf("cooldown: %s %v", r.state, at.cmds)
	}
	r.OnCheck(false, 9, t0.Add(61*time.Second)) // step 2: radio
	settle(r)
	if len(at.cmds) != 2 || at.cmds[0] != "AT+CFUN=0" || at.cmds[1] != "AT+CFUN=1" {
		t.Fatalf("step 2: %v", at.cmds)
	}
	r.OnCheck(false, 15, t0.Add(122*time.Second)) // step 4: restart
	settle(r)
	if *reboots != 1 || rebootsSince(t0.Add(-time.Hour)) != 1 {
		t.Fatalf("step 4: reboots=%d logged=%d", *reboots, rebootsSince(t0.Add(-time.Hour)))
	}
	// After the restart (simulated), still no connection: steps 1 and 2 run
	// again, but no second restart for the same outage.
	r2, _, _, reboots2 := newTestRecoveryKeepFiles(t, cfg)
	r2.OnCheck(false, 3, t0.Add(10*time.Minute))
	settle(r2)
	r2.OnCheck(false, 9, t0.Add(12*time.Minute))
	settle(r2)
	r2.OnCheck(false, 15, t0.Add(14*time.Minute))
	settle(r2)
	if *reboots2 != 0 || r2.state != "locked" {
		t.Fatalf("once per outage: reboots=%d state=%s", *reboots2, r2.state)
	}
	r2.OnCheck(true, 0, t0.Add(15*time.Minute))
	if r2.step != 0 || r2.state != "monitor" {
		t.Fatalf("reset: %d %s", r2.step, r2.state)
	}
	if _, err := os.Stat(rebootedFile); err == nil {
		t.Fatal("outage flag not cleared when the connection came back")
	}
	// A new outage within the hour: now the per-hour cap (1) stops the restart.
	r2.OnCheck(false, 3, t0.Add(20*time.Minute))
	settle(r2)
	r2.OnCheck(false, 9, t0.Add(22*time.Minute))
	settle(r2)
	r2.OnCheck(false, 15, t0.Add(24*time.Minute))
	settle(r2)
	if *reboots2 != 0 || r2.state != "locked" {
		t.Fatalf("cap: reboots=%d state=%s", *reboots2, r2.state)
	}
	ev, _ := os.ReadFile(eventsFile)
	for _, want := range []string{"restarted the data connection", "restarted the radio", "restarting the stick", "Watchdog restarted the stick", "already did 1 times", "Connection back"} {
		if !strings.Contains(string(ev), want) {
			t.Errorf("missing event %q in\n%s", want, ev)
		}
	}
}

// newTestRecoveryKeepFiles makes a second Recovery (as after a restart)
// sharing the first one's files.
func newTestRecoveryKeepFiles(t *testing.T, cfg *WatchConfig) (*Recovery, *recAT, *[]string, *int) {
	at := &recAT{}
	r := NewRecovery(at, func() WatchConfig { return *cfg })
	var cmds []string
	reboots := 0
	r.runCmd = func(name string, args ...string) error { cmds = append(cmds, name); return nil }
	r.reboot = func() { reboots++ }
	r.uptime = func() time.Duration { return time.Hour }
	r.sleep = func(time.Duration) {}
	return r, at, &cmds, &reboots
}

func TestRecoveryHolds(t *testing.T) {
	cfg := &WatchConfig{Enabled: true, FailThreshold: 1, Tier1: true}
	r, _, cmds, _ := newTestRecovery(t, cfg)
	now := time.Now()
	r.uptime = func() time.Duration { return time.Minute } // just booted
	r.OnCheck(false, 5, now)
	if r.state != "ssr_hold" || len(*cmds) != 0 {
		t.Fatalf("boot hold: %s %v", r.state, *cmds)
	}
	r.uptime = func() time.Duration { return time.Hour }
	HoldRadio() // someone pressed Reconnect
	r.OnCheck(false, 5, now)
	if r.state != "ssr_hold" || len(*cmds) != 0 {
		t.Fatalf("manual hold: %s %v", r.state, *cmds)
	}
	_ = os.Remove(RadioHoldFile)
	cfg.Enabled = false
	r.OnCheck(false, 5, now)
	if r.state != "disabled" || len(*cmds) != 0 {
		t.Fatalf("disabled: %s %v", r.state, *cmds)
	}
}
