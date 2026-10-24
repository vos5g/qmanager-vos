package api

import (
	"path/filepath"
	"testing"
	"time"
)

func TestMonthStart(t *testing.T) {
	loc := time.UTC
	for _, c := range []struct {
		now  string
		day  int
		want string
	}{{"2026-10-24", 1, "2026-10-01"}, {"2026-10-24", 25, "2026-09-25"}, {"2026-10-25", 25, "2026-10-25"}, {"2026-01-03", 15, "2025-12-15"}} {
		n, _ := time.ParseInLocation("2006-01-02", c.now, loc)
		if got := monthStart(n, c.day).Format("2006-01-02"); got != c.want {
			t.Errorf("%s day %d: %s, want %s", c.now, c.day, got, c.want)
		}
	}
}

func TestUsageCounting(t *testing.T) {
	dataUsageFile = filepath.Join(t.TempDir(), "usage.json")
	var rx, tx uint64
	u := &dataUsage{counters: func() (uint64, uint64, bool) { return rx, tx, true }}
	u.load()
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

	rx, tx = 1000, 100 // first sample: baseline only
	u.sample(now)
	rx, tx = 5000, 600
	u.sample(now.Add(30 * time.Second))
	rx, tx = 200, 50 // new data call: counters restarted
	u.sample(now.Add(60 * time.Second))
	m := u.st.Months["2026-10-01"]
	if m.RX != 4000+200 || m.TX != 500+50 {
		t.Fatalf("month %+v", m)
	}

	// cap warnings: 80% then 100%, once each
	u.st.CapGB = 0.00001  // 10 KB
	rx, tx = 200+3700, 50 // 8.45 KB of 10 KB: the 80% warning
	u.sample(now.Add(90 * time.Second))
	if u.st.Warned["2026-10-01"] != 80 {
		t.Fatalf("warned %v", u.st.Warned)
	}
	rx = 200 + 3700 + 2000 // over 10 KB: the 100% warning
	u.sample(now.Add(100 * time.Second))
	if u.st.Warned["2026-10-01"] != 100 {
		t.Fatalf("warned %v", u.st.Warned)
	}
	m = u.st.Months["2026-10-01"]

	// persists, and a reload continues from the saved sample
	u.saveIfDue(now, true)
	u2 := &dataUsage{counters: func() (uint64, uint64, bool) { return rx + 1000, tx, true }}
	u2.load()
	u2.sample(now.Add(120 * time.Second))
	if got := u2.st.Months["2026-10-01"].RX; got != m.RX+1000 {
		t.Fatalf("after reload rx=%d", got)
	}

	// changing the reset day re-buckets the days
	u2.st.ResetDay = 25
	u2.rebucket()
	if _, ok := u2.st.Months["2026-09-25"]; !ok {
		t.Fatalf("rebucket %v", u2.st.Months)
	}
}

func TestUsageWaitsForClock(t *testing.T) {
	dataUsageFile = filepath.Join(t.TempDir(), "usage.json")
	n := uint64(0)
	u := &dataUsage{counters: func() (uint64, uint64, bool) { n += 1000; return n, n, true }}
	u.load()
	u.sample(time.Unix(11000, 0)) // 1970: stick clock not set
	u.sample(time.Unix(11030, 0))
	if len(u.st.Months) != 0 {
		t.Fatalf("booked to 1970: %v", u.st.Months)
	}
}
