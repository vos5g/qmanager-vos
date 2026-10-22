package api

// vosclean: live bandwidth from the modem's own counters.
//
// On the VOS 5G stick the IPA hardware fast path forwards USB<->mobile traffic
// without Linux, so /proc/net/dev barely moves (a 40 MB download added under
// 1 KB to rmnet_data0). QCMAP keeps per-call counters that include offloaded
// traffic; `cricli wwan_stats` reads them in about 10 ms:
//
//	IPV4 (TX/RX) > bytes: ( 353983215 / 3044417262 ), packets: ( ... )
//
// Upstream ran an unauthenticated WebSocket on its own port (8838) fed from
// /proc/net/dev, and the page used ws:// (blocked on the https UI). This
// serves the same message shape on the main server instead, behind the login
// and a same-origin check:
//
//	GET /cgi-bin/quecmanager/monitoring/bandwidth_ws  (WebSocket)
//	{"type":"traffic_update","channel":"network-monitor",
//	 "data":{"timestamp","upload","download"},
//	 "interfaces":[{"name":"mobile","state":"up","tx":{"bps"},"rx":{"bps"}}, ...]}

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/websocket"
)

const bandwidthStatusFile = "/tmp/qmanager_bandwidth_status.json"

// StartBandwidthWebSocket only marks the monitor as available; the socket
// itself is served by the main HTTP server (no extra port).
func (s *Server) StartBandwidthWebSocket(int) {
	writeJSONFile(bandwidthStatusFile, map[string]any{"websocat_running": true, "monitor_running": true})
}

var wwanStatsRe = regexp.MustCompile(`IPV([46]) \(TX/RX\) > bytes: \( *(\d+) */ *(\d+) *\)`)

// parseWWANStats sums the IPv4 and IPv6 byte counters from cricli output.
func parseWWANStats(out string) (rx, tx uint64, ok bool) {
	for _, m := range wwanStatsRe.FindAllStringSubmatch(out, -1) {
		t, _ := strconv.ParseUint(m[2], 10, 64)
		r, _ := strconv.ParseUint(m[3], 10, 64)
		tx += t
		rx += r
		ok = true
	}
	return rx, tx, ok
}

func wwanCounters() (rx, tx uint64, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "cricli", "wwan_stats").Output()
	if err != nil && len(out) == 0 {
		return 0, 0, false
	}
	return parseWWANStats(string(out))
}

// netDevCounters reads one interface from /proc/net/dev (for tailscale0,
// whose traffic does go through Linux).
func netDevCounters(name string) (rx, tx uint64, ok bool) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		i := strings.Index(line, ":")
		if i < 0 || strings.TrimSpace(line[:i]) != name {
			continue
		}
		f := strings.Fields(line[i+1:])
		if len(f) < 9 {
			return 0, 0, false
		}
		rx, _ = strconv.ParseUint(f[0], 10, 64)
		tx, _ = strconv.ParseUint(f[8], 10, 64)
		return rx, tx, true
	}
	return 0, 0, false
}

type bwSample struct {
	rx, tx uint64
	at     time.Time
	ok     bool
}

func bwRate(prev, cur bwSample) (rxBps, txBps float64) {
	if !prev.ok || !cur.ok {
		return 0, 0
	}
	dt := cur.at.Sub(prev.at).Seconds()
	if dt <= 0 || cur.rx < prev.rx || cur.tx < prev.tx { // counters reset (new data call)
		return 0, 0
	}
	return float64(cur.rx-prev.rx) * 8 / dt, float64(cur.tx-prev.tx) * 8 / dt
}

func bwRefresh() time.Duration {
	ms := qmCfgInt(qmReadConfig()[qmBridgeMonitorSection], "refresh_rate_ms", 1000)
	if ms < 500 {
		ms = 500
	}
	if ms > 10000 {
		ms = 10000
	}
	return time.Duration(ms) * time.Millisecond
}

// HandleBandwidthWS upgrades to the live-traffic socket. Wrapped in RequireAuth.
func (s *Server) HandleBandwidthWS(w http.ResponseWriter, r *http.Request) {
	if !consoleOriginOK(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler:   func(ws *websocket.Conn) { bandwidthSession(ws, r) },
	}.ServeHTTP(w, r)
}

func bandwidthSession(ws *websocket.Conn, r *http.Request) {
	defer ws.Close()
	gone := make(chan struct{})
	go func() { // notice the browser going away
		buf := make([]byte, 256)
		for {
			if _, err := ws.Read(buf); err != nil {
				close(gone)
				return
			}
		}
	}()

	sample := func() (m, t bwSample) {
		now := time.Now()
		m.rx, m.tx, m.ok = wwanCounters()
		t.rx, t.tx, t.ok = netDevCounters("tailscale0")
		m.at, t.at = now, now
		return
	}
	prevM, prevT := sample()
	every := bwRefresh()
	tick := time.NewTicker(every)
	defer tick.Stop()
	checks := 0
	for {
		select {
		case <-gone:
			return
		case <-tick.C:
		}
		if checks++; checks%30 == 0 && !isAuthenticated(r) {
			return // logged out
		}
		curM, curT := sample()
		mrx, mtx := bwRate(prevM, curM)
		trx, ttx := bwRate(prevT, curT)
		prevM, prevT = curM, curT

		state := "down"
		if curM.ok {
			state = "up"
		}
		ifaces := []map[string]any{{"name": "mobile", "state": state, "rx": map[string]any{"bps": mrx}, "tx": map[string]any{"bps": mtx}}}
		if curT.ok { // reported for reference; it also travels over "mobile", so the page leaves it out of the total
			ifaces = append(ifaces, map[string]any{"name": "tailscale0", "state": "up", "rx": map[string]any{"bps": trx}, "tx": map[string]any{"bps": ttx}})
		}
		msg, _ := json.Marshal(map[string]any{
			"type": "traffic_update", "channel": "network-monitor",
			"data":       map[string]any{"timestamp": time.Now().Format(time.RFC3339), "upload": mtx, "download": mrx},
			"interfaces": ifaces,
		})
		if _, err := ws.Write(msg); err != nil {
			return
		}
	}
}
