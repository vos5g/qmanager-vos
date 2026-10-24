package api

// vosclean: mobile data usage per billing month.
//
// The modem's per-call counters (`cricli wwan_stats`, the same ones Live
// Traffic uses) include traffic the IPA hardware forwards without Linux, but
// they start from zero with every new data call (a reconnect, a radio
// restart, a reboot). The collector samples them every 30 s and adds up the
// increases; a counter that went backwards means a new call, so its whole
// value is new traffic.
//
// Totals are kept per day and per billing month (the month starts on a
// chosen day). They're written to flash every 10 minutes and on shutdown, so
// pulling the plug loses at most 10 minutes of counting while sparing the
// NAND. An optional monthly allowance adds warnings at a chosen percentage
// and at 100% to Recent Activity.
//
//	GET  /cgi-bin/quecmanager/monitoring/data_usage.sh
//	POST {action:"save", reset_day:1-28, cap_gb:0-100000, warn_pct:50-100}
//	POST {action:"reset"}   start the current month's count from zero

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"qmanager-backend/pkg/daemon"
)

var dataUsageFile = "/data/vosclean/qm/etc/data_usage.json"

const (
	usageSample = 30 * time.Second
	usageSave   = 10 * time.Minute
	maxMonths   = 13
	maxDays     = 62
)

type usageTotals struct {
	RX uint64 `json:"rx"`
	TX uint64 `json:"tx"`
}

type usageState struct {
	ResetDay   int                    `json:"reset_day"` // 1-28
	CapGB      float64                `json:"cap_gb"`    // 0 = no allowance
	WarnPct    int                    `json:"warn_pct"`
	Months     map[string]usageTotals `json:"months"` // key: start date of the billing month, "2026-10-01"
	Days       map[string]usageTotals `json:"days"`   // key: "2026-10-24"
	Warned     map[string]int         `json:"warned"` // month -> highest warning sent (pct)
	ResetAt    int64                  `json:"reset_at,omitempty"`
	LastSample *struct {
		RX, TX uint64
	} `json:"last_sample,omitempty"`
}

type dataUsage struct {
	mu       sync.Mutex
	st       usageState
	dirty    bool
	lastSave time.Time
	counters func() (rx, tx uint64, ok bool)
}

var usage = &dataUsage{counters: wwanCounters}

func (u *dataUsage) load() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.st = usageState{ResetDay: 1, WarnPct: 80}
	if b, err := os.ReadFile(dataUsageFile); err == nil {
		_ = json.Unmarshal(b, &u.st)
	}
	if u.st.ResetDay < 1 || u.st.ResetDay > 28 {
		u.st.ResetDay = 1
	}
	if u.st.WarnPct < 50 || u.st.WarnPct > 100 {
		u.st.WarnPct = 80
	}
	if u.st.Months == nil {
		u.st.Months = map[string]usageTotals{}
	}
	if u.st.Days == nil {
		u.st.Days = map[string]usageTotals{}
	}
	if u.st.Warned == nil {
		u.st.Warned = map[string]int{}
	}
}

// monthStart is the first day of the billing month that contains t.
func monthStart(t time.Time, resetDay int) time.Time {
	y, m, d := t.Date()
	if d < resetDay {
		m--
	}
	return time.Date(y, m, resetDay, 0, 0, 0, 0, t.Location())
}

func (u *dataUsage) sample(now time.Time) {
	rx, tx, ok := u.counters()
	if !ok {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if now.Year() < 2025 { // clock not set yet: don't book traffic to 1970
		u.st.LastSample = &struct{ RX, TX uint64 }{rx, tx}
		return
	}
	var dRX, dTX uint64
	if p := u.st.LastSample; p != nil {
		if rx >= p.RX {
			dRX = rx - p.RX
		} else {
			dRX = rx // new data call: counters restarted
		}
		if tx >= p.TX {
			dTX = tx - p.TX
		} else {
			dTX = tx
		}
	}
	// The last sample is saved with the totals, so after a QManager restart
	// the traffic since the last save is counted, and after a reboot (new
	// call, counters lower than the saved sample) the new call's traffic is.
	u.st.LastSample = &struct{ RX, TX uint64 }{rx, tx}
	if dRX == 0 && dTX == 0 {
		return
	}
	mKey := monthStart(now, u.st.ResetDay).Format("2006-01-02")
	dKey := now.Format("2006-01-02")
	m := u.st.Months[mKey]
	m.RX, m.TX = m.RX+dRX, m.TX+dTX
	u.st.Months[mKey] = m
	d := u.st.Days[dKey]
	d.RX, d.TX = d.RX+dRX, d.TX+dTX
	u.st.Days[dKey] = d
	u.dirty = true
	u.prune()
	u.checkCap(now, mKey, m)
}

func (u *dataUsage) prune() {
	trim := func(mp map[string]usageTotals, keep int) {
		if len(mp) <= keep {
			return
		}
		keys := make([]string, 0, len(mp))
		for k := range mp {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys[:len(keys)-keep] {
			delete(mp, k)
		}
	}
	trim(u.st.Months, maxMonths)
	trim(u.st.Days, maxDays)
}

func (u *dataUsage) checkCap(now time.Time, mKey string, m usageTotals) {
	if u.st.CapGB <= 0 {
		return
	}
	pct := int(float64(m.RX+m.TX) / (u.st.CapGB * 1e9) * 100)
	for _, level := range []int{100, u.st.WarnPct} {
		if pct >= level && u.st.Warned[mKey] < level {
			u.st.Warned[mKey] = level
			sev := "warning"
			if level >= 100 {
				sev = "error"
			}
			daemon.AppendEvent(now, "data_usage", sev,
				fmt.Sprintf("Used %s this month: %d%% of your %g GB allowance", fmtBytes(m.RX+m.TX), pct, u.st.CapGB))
			return
		}
	}
}

// saveIfDue writes the file when something changed and usageSave has passed
// (force: on shutdown and after a settings change).
func (u *dataUsage) saveIfDue(now time.Time, force bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.dirty && !force {
		return
	}
	if !force && now.Sub(u.lastSave) < usageSave {
		return
	}
	b, err := json.Marshal(u.st)
	if err != nil {
		return
	}
	if err := vosWriteFileDurable(dataUsageFile, b, 0644); err != nil {
		log.Printf("[DataUsage] save failed: %v", err)
		return
	}
	u.dirty, u.lastSave = false, now
}

// StartDataUsage runs the collector; call SaveDataUsage on shutdown.
func StartDataUsage() {
	usage.load()
	usage.lastSave = time.Now()
	go func() {
		t := time.NewTicker(usageSample)
		defer t.Stop()
		for now := range t.C {
			usage.sample(now)
			usage.saveIfDue(now, false)
		}
	}()
}

func SaveDataUsage() { usage.saveIfDue(time.Now(), true) }

func fmtBytes(b uint64) string {
	switch {
	case b >= 1e9:
		return fmt.Sprintf("%.2f GB", float64(b)/1e9)
	case b >= 1e6:
		return fmt.Sprintf("%.1f MB", float64(b)/1e6)
	}
	return fmt.Sprintf("%d KB", b/1000)
}

func (u *dataUsage) snapshot(now time.Time) map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	start := monthStart(now, u.st.ResetDay)
	cur := u.st.Months[start.Format("2006-01-02")]
	months := []map[string]any{}
	for k, v := range u.st.Months {
		months = append(months, map[string]any{"start": k, "rx": v.RX, "tx": v.TX})
	}
	sort.Slice(months, func(i, j int) bool { return months[i]["start"].(string) < months[j]["start"].(string) })
	days := []map[string]any{}
	for i := 29; i >= 0; i-- {
		k := now.AddDate(0, 0, -i).Format("2006-01-02")
		v := u.st.Days[k]
		days = append(days, map[string]any{"date": k, "rx": v.RX, "tx": v.TX})
	}
	return map[string]any{
		"success":   true,
		"clock_ok":  now.Year() >= 2025,
		"reset_day": u.st.ResetDay,
		"cap_gb":    u.st.CapGB,
		"warn_pct":  u.st.WarnPct,
		"period": map[string]any{
			"start": start.Format("2006-01-02"),
			"end":   start.AddDate(0, 1, 0).Format("2006-01-02"),
			"rx":    cur.RX,
			"tx":    cur.TX,
		},
		"months":   months,
		"days":     days,
		"reset_at": u.st.ResetAt,
	}
}

// HandleDataUsage serves the Data Usage card.
func (s *Server) HandleDataUsage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	now := time.Now()
	if r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(usage.snapshot(now))
		return
	}
	if r.Method != http.MethodPost {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "method_not_allowed"})
		return
	}
	var body struct {
		Action   string   `json:"action"`
		ResetDay *int     `json:"reset_day"`
		CapGB    *float64 `json:"cap_gb"`
		WarnPct  *int     `json:"warn_pct"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "invalid_json"})
		return
	}
	switch body.Action {
	case "save":
		if body.ResetDay != nil && (*body.ResetDay < 1 || *body.ResetDay > 28) {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "reject_field", "detail": "reset_day must be 1-28"})
			return
		}
		if body.CapGB != nil && (*body.CapGB < 0 || *body.CapGB > 100000) {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "reject_field", "detail": "cap_gb must be 0-100000"})
			return
		}
		if body.WarnPct != nil && (*body.WarnPct < 50 || *body.WarnPct > 100) {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "reject_field", "detail": "warn_pct must be 50-100"})
			return
		}
		usage.mu.Lock()
		if body.ResetDay != nil && *body.ResetDay != usage.st.ResetDay {
			// Re-bucket this month's days under the new billing month.
			usage.st.ResetDay = *body.ResetDay
			usage.rebucket()
		}
		if body.CapGB != nil {
			usage.st.CapGB = *body.CapGB
			usage.st.Warned = map[string]int{}
		}
		if body.WarnPct != nil {
			usage.st.WarnPct = *body.WarnPct
		}
		usage.dirty = true
		usage.mu.Unlock()
	case "reset":
		usage.mu.Lock()
		k := monthStart(now, usage.st.ResetDay).Format("2006-01-02")
		delete(usage.st.Months, k)
		delete(usage.st.Warned, k)
		usage.st.ResetAt = now.Unix()
		usage.dirty = true
		usage.mu.Unlock()
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "unknown_action"})
		return
	}
	usage.saveIfDue(now, true)
	_ = json.NewEncoder(w).Encode(usage.snapshot(now))
}

// rebucket rebuilds the month totals from the daily ones after the reset day
// changed (months older than the daily history keep their old totals).
func (u *dataUsage) rebucket() {
	if len(u.st.Days) == 0 {
		return
	}
	oldest := ""
	for k := range u.st.Days {
		if oldest == "" || k < oldest {
			oldest = k
		}
	}
	rebuilt := map[string]usageTotals{}
	for k, v := range u.st.Months {
		if k < oldest {
			rebuilt[k] = v
		}
	}
	for k, v := range u.st.Days {
		d, err := time.ParseInLocation("2006-01-02", k, time.Local)
		if err != nil {
			continue
		}
		mk := monthStart(d, u.st.ResetDay).Format("2006-01-02")
		m := rebuilt[mk]
		m.RX, m.TX = m.RX+v.RX, m.TX+v.TX
		rebuilt[mk] = m
	}
	u.st.Months = rebuilt
	u.st.Warned = map[string]int{}
}
