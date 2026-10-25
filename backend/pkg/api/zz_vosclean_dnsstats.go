package api

// vosclean: Ad Blocking history. The DNS proxy's counters start from zero with
// every QManager start; this keeps per-day totals and the most-blocked names
// across restarts in /data (written every 10 minutes and on shutdown, like
// data usage, to spare the flash).

import (
	"encoding/json"
	"log"
	"os"
	"sort"
	"sync"
	"time"

	"qmanager-backend/pkg/dnsproxy"
)

var dnsStatsFile = "/data/vosclean/qm/etc/dns_stats.json"

const (
	dnsStatsDays   = 30
	dnsStatsTopMax = 3000 // names kept between trims
	dnsStatsTopKep = 1000 // names kept after a trim
)

type dnsDay struct {
	Queries uint64 `json:"queries"`
	Blocked uint64 `json:"blocked"`
}

type dnsStatsData struct {
	Days  map[string]dnsDay `json:"days"`
	Top   map[string]uint64 `json:"top"`
	Since int64             `json:"since"`
}

type dnsStatsState struct {
	mu        sync.Mutex
	d         dnsStatsData
	lastQ     uint64
	lastB     uint64
	dirty     bool
	proxy     func() *dnsproxy.Proxy
	startOnce sync.Once
}

var dstats = &dnsStatsState{proxy: func() *dnsproxy.Proxy { return dnsp.proxy }}

func (s *dnsStatsState) load() {
	s.d = dnsStatsData{}
	if b, err := os.ReadFile(dnsStatsFile); err == nil {
		_ = json.Unmarshal(b, &s.d)
	}
	if s.d.Days == nil {
		s.d.Days = map[string]dnsDay{}
	}
	if s.d.Top == nil {
		s.d.Top = map[string]uint64{}
	}
	if s.d.Since == 0 {
		s.d.Since = time.Now().Unix()
	}
}

// sample books the proxy's counters since the last sample under today.
func (s *dnsStatsState) sample(now time.Time) {
	p := s.proxy()
	st := p.Stats()
	drained := p.DrainBlocked()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.Days == nil || s.d.Top == nil {
		s.load() // not started yet (tests, or a request before StartDNSStats)
	}
	dq, db := st.Queries-s.lastQ, st.Blocked-s.lastB
	if st.Queries < s.lastQ || st.Blocked < s.lastB { // counters restarted
		dq, db = st.Queries, st.Blocked
	}
	s.lastQ, s.lastB = st.Queries, st.Blocked
	if dq > 0 || db > 0 {
		day := now.Format("2006-01-02")
		d := s.d.Days[day]
		d.Queries += dq
		d.Blocked += db
		s.d.Days[day] = d
		s.dirty = true
	}
	for n, c := range drained {
		s.d.Top[n] += c
		s.dirty = true
	}
	if len(s.d.Top) > dnsStatsTopMax {
		s.d.Top = topN(s.d.Top, dnsStatsTopKep)
	}
	cutoff := now.AddDate(0, 0, -dnsStatsDays).Format("2006-01-02")
	for day := range s.d.Days {
		if day < cutoff {
			delete(s.d.Days, day)
		}
	}
}

func topN(m map[string]uint64, n int) map[string]uint64 {
	type kv struct {
		k string
		v uint64
	}
	all := make([]kv, 0, len(m))
	for k, v := range m {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v > all[j].v
		}
		return all[i].k < all[j].k
	})
	if len(all) > n {
		all = all[:n]
	}
	out := make(map[string]uint64, len(all))
	for _, e := range all {
		out[e.k] = e.v
	}
	return out
}

func (s *dnsStatsState) save() {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return
	}
	b, err := json.Marshal(s.d)
	s.dirty = false
	s.mu.Unlock()
	if err == nil {
		err = vosWriteFileDurable(dnsStatsFile, b, 0644)
	}
	if err != nil {
		log.Printf("dnsstats: save: %v", err)
	}
}

func (s *dnsStatsState) reset() {
	s.sample(time.Now()) // move pending counts out of the proxy first
	s.mu.Lock()
	s.d = dnsStatsData{Days: map[string]dnsDay{}, Top: map[string]uint64{}, Since: time.Now().Unix()}
	s.dirty = true
	s.mu.Unlock()
	s.save()
}

// snapshot: the last 14 days (oldest first, gaps filled), today, the top names.
func (s *dnsStatsState) snapshot(now time.Time) map[string]any {
	s.sample(now)
	s.mu.Lock()
	defer s.mu.Unlock()
	var days []map[string]any
	for i := 13; i >= 0; i-- {
		day := now.AddDate(0, 0, -i).Format("2006-01-02")
		d := s.d.Days[day]
		days = append(days, map[string]any{"date": day, "queries": d.Queries, "blocked": d.Blocked})
	}
	var total dnsDay
	for _, d := range s.d.Days {
		total.Queries += d.Queries
		total.Blocked += d.Blocked
	}
	top := []map[string]any{}
	for n, c := range topN(s.d.Top, 10) {
		top = append(top, map[string]any{"name": n, "count": c})
	}
	sort.Slice(top, func(i, j int) bool {
		ci, cj := top[i]["count"].(uint64), top[j]["count"].(uint64)
		if ci != cj {
			return ci > cj
		}
		return top[i]["name"].(string) < top[j]["name"].(string)
	})
	today := s.d.Days[now.Format("2006-01-02")]
	return map[string]any{
		"days":  days,
		"today": map[string]any{"queries": today.Queries, "blocked": today.Blocked},
		"total": map[string]any{"queries": total.Queries, "blocked": total.Blocked},
		"top":   top,
		"since": s.d.Since,
	}
}

// StartDNSStats loads the history and keeps it up to date.
func StartDNSStats() {
	dstats.startOnce.Do(func() {
		dstats.mu.Lock()
		dstats.load()
		dstats.mu.Unlock()
		go func() {
			sampleT := time.NewTicker(30 * time.Second)
			saveT := time.NewTicker(10 * time.Minute)
			for {
				select {
				case <-sampleT.C:
					dstats.sample(time.Now())
				case <-saveT.C:
					dstats.save()
				}
			}
		}()
	})
}

// SaveDNSStats books the last counts and writes the history (on shutdown).
func SaveDNSStats() {
	dstats.sample(time.Now())
	dstats.save()
}
