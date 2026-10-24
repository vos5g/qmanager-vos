package api

// vosclean: the Watchdog page's settings, for the in-process watchdog and its
// recovery ladder (pkg/daemon/zz_vosclean_recovery.go). Defaults match what
// the page shows before anything is saved.

import (
	"time"

	"qmanager-backend/pkg/daemon"
)

var pingProfileFile = "/data/vosclean/qm/etc/ping_profile.json"

// probeProfileSec: the Detection tab's named probe intervals (seconds).
var probeProfileSec = map[string]int{"sensitive": 1, "regular": 2, "relaxed": 5, "quiet": 10}

// probeInterval is the custom interval if set, else the named profile's
// (default "relaxed", 5 s).
func probeInterval() time.Duration {
	pp := qmReadJSONFile(pingProfileFile)
	if n := qmCfgInt(pp, "interval_override_sec", 0); n >= 1 && n <= 60 {
		return time.Duration(n) * time.Second
	}
	if n, ok := probeProfileSec[qmStr(pp["profile"])]; ok {
		return time.Duration(n) * time.Second
	}
	return 5 * time.Second
}

// WatchdogConfig reads the saved Watchdog settings.
func WatchdogConfig() daemon.WatchConfig {
	cfg := qmReadConfig()["watchcat"]
	return daemon.WatchConfig{
		Enabled:           qmCfgBool(cfg, "enabled", true),
		FailThreshold:     qmCfgInt(cfg, "fail_threshold", 5),
		CheckInterval:     probeInterval(),
		Cooldown:          time.Duration(qmCfgInt(cfg, "cooldown", 60)) * time.Second,
		Tier1:             qmCfgBool(cfg, "tier1_enabled", true),
		Tier2:             qmCfgBool(cfg, "tier2_enabled", true),
		Tier4:             qmCfgBool(cfg, "tier4_enabled", true),
		MaxRebootsPerHour: qmCfgInt(cfg, "max_reboots_per_hour", 3),
	}
}
