package api

// vosclean: real speed test against Ookla (speedtest.net) servers, in pure Go.
// Upstream returned a fixed "Jakarta Cloud" server and timed downloads from
// Cloudflare. This uses showwin/speedtest-go (the same engine QManager-GO
// uses) and reports progress in the Ookla CLI JSON shape the UI already reads
// (types/speedtest.ts), so the dashboard dialog works unchanged.
//
// The test runs on the modem itself, so the traffic goes through the modem's
// CPU rather than the IPA hardware path that devices behind the stick use.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/showwin/speedtest-go/speedtest"
)

const (
	stPhaseTime   = 8 * time.Second // per direction; about 15 s of a cellular link's data each way
	stListMaxAge  = 10 * time.Minute
	stMaxRunTime  = 2 * time.Minute
	stLoadedProbe = 400 * time.Millisecond
)

type stState struct {
	mu       sync.Mutex
	running  bool
	cancel   context.CancelFunc
	status   string // idle, running, complete, error
	phase    string // initializing, ping, download, upload
	progress map[string]any
	result   map[string]any
	err      string

	list     speedtest.Servers
	user     *speedtest.User
	listTime time.Time
}

var st = &stState{status: "idle", phase: "idle"}

func stNow() string { return time.Now().UTC().Format(time.RFC3339) }
func stMs(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}

func stHostPort(host string) (string, int) {
	h, p, err := net.SplitHostPort(host)
	if err != nil {
		return host, 8080
	}
	n, _ := strconv.Atoi(p)
	return h, n
}

// stServers returns the nearby server list, sorted by distance, cached for a while.
func stServers(ctx context.Context, client *speedtest.Speedtest) (speedtest.Servers, *speedtest.User, error) {
	st.mu.Lock()
	if st.list != nil && time.Since(st.listTime) < stListMaxAge {
		l, u := st.list, st.user
		st.mu.Unlock()
		return l, u, nil
	}
	st.mu.Unlock()
	user, _ := client.FetchUserInfoContext(ctx)
	list, err := client.FetchServerListContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Distance < list[j].Distance })
	st.mu.Lock()
	st.list, st.user, st.listTime = list, user, time.Now()
	st.mu.Unlock()
	return list, user, nil
}

func stServerJSON(s *speedtest.Server) map[string]any {
	host, port := stHostPort(s.Host)
	id, _ := strconv.Atoi(s.ID)
	return map[string]any{
		"id": id, "host": host, "port": port, "ip": host,
		"name": s.Sponsor, "location": s.Name, "country": s.Country,
	}
}

// stLoadedLatency TCP-connects to the server every few hundred ms while a
// transfer runs: the "latency under load" the result shows.
func stLoadedLatency(ctx context.Context, host string) func() map[string]any {
	var mu sync.Mutex
	var samples []float64
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(stLoadedProbe)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				t0 := time.Now()
				c, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", host)
				if err == nil {
					c.Close()
					mu.Lock()
					samples = append(samples, stMs(time.Since(t0)))
					mu.Unlock()
				}
			}
		}
	}()
	return func() map[string]any {
		<-done
		mu.Lock()
		defer mu.Unlock()
		return stLatencyStats(samples)
	}
}

// stLatencyStats: interquartile mean, low, high and jitter (mean successive difference).
func stLatencyStats(samples []float64) map[string]any {
	if len(samples) == 0 {
		return map[string]any{"iqm": 0.0, "low": 0.0, "high": 0.0, "jitter": 0.0}
	}
	var jit float64
	for i := 1; i < len(samples); i++ {
		d := samples[i] - samples[i-1]
		if d < 0 {
			d = -d
		}
		jit += d
	}
	if len(samples) > 1 {
		jit /= float64(len(samples) - 1)
	}
	s := append([]float64(nil), samples...)
	sort.Float64s(s)
	lo, hi := len(s)/4, len(s)-len(s)/4
	if hi <= lo {
		lo, hi = 0, len(s)
	}
	var sum float64
	for _, v := range s[lo:hi] {
		sum += v
	}
	r := func(v float64) float64 { return float64(int(v*10+0.5)) / 10 }
	return map[string]any{"iqm": r(sum / float64(hi-lo)), "low": r(s[0]), "high": r(s[len(s)-1]), "jitter": r(jit)}
}

func stSet(phase string, progress map[string]any) {
	st.mu.Lock()
	st.phase, st.progress = phase, progress
	st.mu.Unlock()
}

func stFail(ctx context.Context, msg string) {
	if ctx.Err() == context.Canceled {
		return
	}
	st.mu.Lock()
	st.status, st.err = "error", msg
	st.mu.Unlock()
}

func stRun(ctx context.Context, serverID int) {
	defer func() {
		if r := recover(); r != nil {
			stFail(ctx, fmt.Sprintf("speed test crashed: %v", r))
		}
		st.mu.Lock()
		st.running, st.cancel, st.progress = false, nil, nil
		if st.status == "running" {
			st.status = "idle"
		}
		st.mu.Unlock()
	}()

	client := speedtest.New(speedtest.WithUserConfig(&speedtest.UserConfig{PingMode: speedtest.TCP}))
	list, user, err := stServers(ctx, client)
	if err != nil {
		stFail(ctx, "Couldn't reach speedtest.net for the server list: "+err.Error())
		return
	}
	var srv *speedtest.Server
	if serverID > 0 {
		for _, s := range list {
			if s.ID == strconv.Itoa(serverID) {
				srv = s
				break
			}
		}
	}
	if srv == nil {
		// nearest few by distance; pick the one with the lowest latency
		cand := list
		if len(cand) > 5 {
			cand = cand[:5]
		}
		best := time.Duration(1<<62 - 1)
		for _, s := range cand {
			s.Context = client
			if lat, e := stQuickPing(ctx, s.Host); e == nil && lat < best {
				best, srv = lat, s
			}
		}
		if srv == nil && len(list) > 0 {
			srv = list[0]
		}
	}
	if srv == nil {
		stFail(ctx, "No speedtest.net servers available")
		return
	}
	srv.Context = client

	isp, extIP := "", ""
	if user != nil {
		isp, extIP = user.Isp, user.IP
	}
	iface := map[string]any{"name": "rmnet_data0", "internalIp": "", "macAddr": "", "isVpn": false, "externalIp": extIP}
	if v4, _ := vosIfaceAddrs("rmnet_data0"); len(v4) > 0 {
		iface["internalIp"] = v4[0].IP.String()
	}
	server := stServerJSON(srv)
	stSet("initializing", map[string]any{"type": "testStart", "timestamp": stNow(), "isp": isp, "interface": iface, "server": server})

	// ping
	n := 0
	stSet("ping", nil)
	if err := srv.PingTestContext(ctx, func(l time.Duration) {
		n++
		stSet("ping", map[string]any{"type": "ping", "timestamp": stNow(),
			"ping": map[string]any{"latency": stMs(l), "jitter": stMs(srv.Jitter), "progress": minf(float64(n)/10, 1)}})
	}); err != nil {
		stFail(ctx, "Ping to the test server failed: "+err.Error())
		return
	}
	ping := map[string]any{"latency": stMs(srv.Latency), "jitter": stMs(srv.Jitter), "low": stMs(srv.MinLatency), "high": stMs(srv.MaxLatency)}

	transfer := func(c *speedtest.Speedtest, dir string) (map[string]any, bool) {
		srv.Context = c
		pctx, stop := context.WithCancel(ctx)
		loaded := stLoadedLatency(pctx, srv.Host)
		start := time.Now()
		cb := func(rate speedtest.ByteRate) {
			el := time.Since(start)
			moved := c.GetTotalDownload()
			if dir == "upload" {
				moved = c.GetTotalUpload()
			}
			stSet(dir, map[string]any{"type": dir, "timestamp": stNow(), dir: map[string]any{
				"bandwidth": math.Max(float64(rate), 0), "bytes": moved, "elapsed": el.Milliseconds(),
				"progress": minf(float64(el)/float64(stPhaseTime), 1)}})
		}
		c.Reset()
		c.SetCaptureTime(stPhaseTime)
		var err error
		if dir == "download" {
			c.SetCallbackDownload(cb)
			err = srv.DownloadTestContext(ctx)
		} else {
			c.SetCallbackUpload(cb)
			err = srv.UploadTestContext(ctx)
		}
		el := time.Since(start)
		stop()
		lat := loaded()
		if err == nil {
			err = ctx.Err()
		}
		if err != nil {
			stFail(ctx, strings.ToUpper(dir[:1])+dir[1:]+" test failed: "+err.Error())
			return nil, false
		}
		bw, moved := float64(srv.DLSpeed), c.GetTotalDownload()
		if dir == "upload" {
			bw, moved = float64(srv.ULSpeed), c.GetTotalUpload()
		}
		return map[string]any{"bandwidth": math.Max(bw, 0), "bytes": moved, "elapsed": el.Milliseconds(), "latency": lat}, true
	}
	dl, ok := transfer(client, "download")
	if !ok {
		return
	}
	ul, ok := transfer(client, "upload")
	if !ok {
		return
	}
	// Some servers (O2's own among them) refuse HTTP uploads; the library then
	// reports nothing. Measure upload again over Ookla's raw TCP protocol.
	if ul["bandwidth"].(float64) <= 0 {
		tcp := speedtest.New(speedtest.WithUserConfig(&speedtest.UserConfig{PingMode: speedtest.TCP, TestMode: speedtest.TCPTest}))
		if ul, ok = transfer(tcp, "upload"); !ok {
			return
		}
	}
	if dl["bandwidth"].(float64) <= 0 || ul["bandwidth"].(float64) <= 0 {
		stFail(ctx, "The test server didn't accept the transfer; pick another server and try again")
		return
	}

	st.mu.Lock()
	st.status, st.phase, st.progress = "complete", "complete", nil
	st.result = map[string]any{
		"type": "result", "timestamp": stNow(),
		"ping": ping, "download": dl, "upload": ul,
		"isp": isp, "interface": iface, "server": server,
		"result": map[string]any{"id": fmt.Sprintf("qmanager-%d", time.Now().Unix()), "url": "", "persisted": false},
	}
	st.mu.Unlock()
}

func stQuickPing(ctx context.Context, host string) (time.Duration, error) {
	var best time.Duration
	for i := 0; i < 2; i++ {
		t0 := time.Now()
		c, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", host)
		if err != nil {
			return 0, err
		}
		c.Close()
		if d := time.Since(t0); best == 0 || d < best {
			best = d
		}
	}
	return best, nil
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func stJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// HandleSpeedtestCheck: the engine is built in, so it's always available.
func (s *Server) HandleSpeedtestCheck(w http.ResponseWriter, r *http.Request) {
	stJSON(w, 200, map[string]any{"success": true, "available": true, "installed": true, "binary": "embedded"})
}

// HandleSpeedtestServers lists the nearest speedtest.net servers.
func (s *Server) HandleSpeedtestServers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	list, _, err := stServers(ctx, speedtest.New())
	if err != nil {
		stJSON(w, 200, map[string]any{"success": false, "error": "servers_unavailable", "detail": err.Error()})
		return
	}
	out := []map[string]any{}
	for i, sv := range list {
		if i == 20 {
			break
		}
		out = append(out, stServerJSON(sv))
	}
	stJSON(w, 200, map[string]any{"success": true, "servers": out})
}

// HandleSpeedtestStart starts a run in the background. Body: {"server_id": N} (optional).
func (s *Server) HandleSpeedtestStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		stJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "error": "method_not_allowed"})
		return
	}
	var body struct {
		ServerID json.Number `json:"server_id"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
	sid, _ := strconv.Atoi(body.ServerID.String())

	st.mu.Lock()
	if st.running {
		st.mu.Unlock()
		stJSON(w, 200, map[string]any{"success": false, "error": "already_running"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), stMaxRunTime)
	st.running, st.cancel = true, cancel
	st.status, st.phase, st.progress, st.result, st.err = "running", "initializing", nil, nil, ""
	st.mu.Unlock()

	go func() { defer cancel(); stRun(ctx, sid) }()
	stJSON(w, 200, map[string]any{"success": true, "status": "running", "pid": 1})
}

// HandleSpeedtestStatus reports progress or the last result.
func (s *Server) HandleSpeedtestStatus(w http.ResponseWriter, r *http.Request) {
	st.mu.Lock()
	defer st.mu.Unlock()
	switch {
	case st.running:
		resp := map[string]any{"status": "running", "phase": st.phase}
		if st.progress != nil {
			resp["progress"] = st.progress
		}
		stJSON(w, 200, resp)
	case st.status == "error":
		stJSON(w, 200, map[string]any{"status": "error", "error": "test_failed", "detail": st.err})
	case st.status == "complete" && st.result != nil:
		stJSON(w, 200, map[string]any{"status": "complete", "result": st.result})
	default:
		stJSON(w, 200, map[string]any{"status": "idle"})
	}
}

// HandleSpeedtestStop cancels a running test.
func (s *Server) HandleSpeedtestStop(w http.ResponseWriter, r *http.Request) {
	st.mu.Lock()
	if st.cancel != nil {
		st.cancel()
	}
	st.status = "idle"
	st.mu.Unlock()
	stJSON(w, 200, map[string]any{"success": true})
}
