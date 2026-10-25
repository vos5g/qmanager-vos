package api

// vosclean: Ad Blocking and Encrypted DNS (Custom DNS page).
//
// Both run in pkg/dnsproxy on 127.0.0.1:5335. While either is on, vosclean
// redirects dnsmasq's upstream queries (every LAN client's DNS) to the proxy;
// the proxy blocks listed names and forwards the rest over DoH/DoT, or to the
// custom/carrier resolvers in plain DNS.
//
// The redirect only stays while QManager is alive: the proxy touches a
// heartbeat file every 20 s, and `vosclean watch` (every minute) drops back to
// the plain custom DNS (or the carrier's) if it goes stale. QManager also runs
// `vosclean dns` when it stops, so a normal stop or restart switches straight
// back. Blocklists are downloaded weekly and kept in /data.
//
//	GET  /cgi-bin/quecmanager/network/dns_proxy.sh
//	POST {action:"save_resolver", mode:"carrier|custom", provider, protocol:"plain|doh|dot",
//	      servers:[ipv4...], custom_url, fallback}   the DNS Servers card
//	POST {action:"save", encrypted:{...}, blocking:{...}}
//	POST {action:"update_lists"}     download the blocklists now
//	POST {action:"check", domain}    is this name blocked?

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"qmanager-backend/pkg/dnsproxy"
)

const dnsProxyAddr = "127.0.0.1:5335"

var (
	dnsProxyConfFile  = "/data/vosclean/qm/etc/dns_proxy.json"
	dnsBlocklistFile  = "/data/vosclean/qm/etc/blocklist.txt"
	dnsHeartbeatFile  = "/tmp/qmanager_dnsproxy.alive"
	dnsCustomConfFile = "/data/vosclean/qm/etc/custom_dns.json"
	dnsResolvConf     = "/etc/resolv.conf"
	dnsVoscleanApply  = func() error {
		out, err := exec.Command("/data/vosclean/bin/vosclean", "dns").CombinedOutput()
		if err != nil {
			return fmt.Errorf("vosclean dns: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
)

const (
	blocklistRefresh   = 7 * 24 * time.Hour
	blocklistRetry     = 6 * time.Hour
	blocklistMaxBytes  = 40 << 20
	blocklistMaxDomain = 600000
)

// BlocklistPreset is a well-known list offered in the UI.
type BlocklistPreset struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	URL   string `json:"url"`
}

var blocklistPresets = []BlocklistPreset{
	{"hagezi_light", "HaGeZi Light", "https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/light-onlydomains.txt"},
	{"hagezi_normal", "HaGeZi Normal", "https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/multi-onlydomains.txt"},
	{"hagezi_pro", "HaGeZi Pro", "https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/pro-onlydomains.txt"},
	{"oisd_small", "OISD Small", "https://small.oisd.nl/domainswild2"},
	{"stevenblack", "StevenBlack Unified", "https://raw.githubusercontent.com/StevenBlack/hosts/master/hosts"},
}

type encryptedConf struct {
	Enabled   bool   `json:"enabled"`
	Provider  string `json:"provider"`
	Protocol  string `json:"protocol"` // doh | dot
	CustomURL string `json:"custom_url"`
	Fallback  bool   `json:"fallback"`
}

type blockingConf struct {
	Enabled    bool     `json:"enabled"`
	Lists      []string `json:"lists"`
	CustomURLs []string `json:"custom_urls"`
	Allowlist  []string `json:"allowlist"`
	Denylist   []string `json:"denylist"`
}

type dnsProxyConf struct {
	Encrypted encryptedConf `json:"encrypted"`
	Blocking  blockingConf  `json:"blocking"`

	// Blocklist download state.
	UpdatedAt   int64          `json:"updated_at"`
	UpdateError string         `json:"update_error"`
	ListCounts  map[string]int `json:"list_counts"`
	ListsKey    string         `json:"lists_key"` // lists the cached file was built from
}

func defaultDNSProxyConf() dnsProxyConf {
	return dnsProxyConf{
		Encrypted: encryptedConf{Provider: "cloudflare", Protocol: "doh", Fallback: true},
		Blocking:  blockingConf{Lists: []string{"hagezi_normal"}},
	}
}

func (c *dnsProxyConf) active() bool { return c.Encrypted.Enabled || c.Blocking.Enabled }

func (b blockingConf) sources() []string {
	var urls []string
	for _, id := range b.Lists {
		for _, p := range blocklistPresets {
			if p.ID == id {
				urls = append(urls, p.URL)
			}
		}
	}
	return append(urls, b.CustomURLs...)
}

func (b blockingConf) key() string {
	s := b.sources()
	sort.Strings(s)
	return strings.Join(s, "\n")
}

type dnsProxyState struct {
	mu        sync.Mutex
	conf      dnsProxyConf
	proxy     *dnsproxy.Proxy
	listDoms  []string // domains from the downloaded lists, sorted
	updating  bool
	lastTry   time.Time
	stopBeat  chan struct{}
	applyErr  string
	started   bool
	listsDone chan struct{} // closed after each download (tests)
}

var dnsp = &dnsProxyState{proxy: dnsproxy.New(dnsProxyAddr)}

func loadDNSProxyConf() dnsProxyConf {
	c := defaultDNSProxyConf()
	if b, err := os.ReadFile(dnsProxyConfFile); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	if c.Encrypted.Protocol != "dot" {
		c.Encrypted.Protocol = "doh"
	}
	if c.Encrypted.Provider == "" {
		c.Encrypted.Provider = "cloudflare"
	}
	return c
}

func saveDNSProxyConf(c dnsProxyConf) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return vosWriteFileDurable(dnsProxyConfFile, b, 0644)
}

// dnsProxyWanted tells applyCustomDNS whether to keep the redirect to the proxy.
func dnsProxyWanted() bool {
	dnsp.mu.Lock()
	defer dnsp.mu.Unlock()
	return dnsp.started && dnsp.conf.active() && dnsp.proxy.Running()
}

// customPlainServers returns the Custom DNS IPv4 servers when that is on.
func customPlainServers() []string {
	cfg := qmReadJSONFile(dnsCustomConfFile)
	if qmStr(cfg["mode"]) != "enabled" {
		return nil
	}
	var out []string
	for _, k := range []string{"dns1", "dns2", "dns3"} {
		if ip := net.ParseIP(strings.TrimSpace(qmStr(cfg[k]))); ip != nil && ip.To4() != nil {
			out = append(out, ip.To4().String())
		}
	}
	return out
}

// carrierServers reads the operator resolvers dnsmasq would use.
func carrierServers() []string {
	f, err := os.Open(dnsResolvConf)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) >= 2 && fs[0] == "nameserver" {
			ip := net.ParseIP(fs[1])
			if ip != nil && ip.To4() != nil && !ip.IsLoopback() {
				out = append(out, ip.String())
			}
		}
	}
	return out
}

func newPlainUpstream() dnsproxy.Upstream {
	var mu sync.Mutex
	var cached []string
	var at time.Time
	return &dnsproxy.PlainUpstream{
		Label: "plain",
		Servers: func() []string {
			// Re-read at most every 10 s: follows a Custom DNS change and the
			// carrier handing out new resolvers without a file read per query.
			mu.Lock()
			defer mu.Unlock()
			if time.Since(at) < 10*time.Second && len(cached) > 0 {
				return cached
			}
			if s := customPlainServers(); len(s) > 0 {
				cached = s
			} else {
				cached = carrierServers()
			}
			at = time.Now()
			return cached
		},
	}
}

func plainLabel() string {
	if len(customPlainServers()) > 0 {
		return "Custom DNS"
	}
	return "Carrier DNS"
}

// blocklistLocked builds the active blocklist from the lists and user entries.
func (s *dnsProxyState) blocklistLocked() *dnsproxy.Blocklist {
	if !s.conf.Blocking.Enabled {
		return nil
	}
	doms := s.listDoms
	if len(s.conf.Blocking.Denylist) > 0 {
		doms = append(append([]string(nil), s.listDoms...), s.conf.Blocking.Denylist...)
	}
	return dnsproxy.NewBlocklist(doms, s.conf.Blocking.Allowlist)
}

// applyLocked starts/stops the proxy and the vosclean redirect for s.conf.
func (s *dnsProxyState) applyLocked() error {
	c := s.conf
	if !c.active() {
		s.stopHeartbeatLocked()
		s.started = false
		err := writeVosDNSConf(false)
		if err == nil {
			err = dnsVoscleanApply()
		}
		s.proxy.Stop()
		os.Remove(dnsHeartbeatFile)
		return err
	}
	plain := newPlainUpstream()
	var enc dnsproxy.Upstream
	if c.Encrypted.Enabled {
		var err error
		enc, err = dnsproxy.NewEncrypted(c.Encrypted.Provider, c.Encrypted.Protocol, c.Encrypted.CustomURL, plain)
		if err != nil {
			return err
		}
	}
	s.proxy.Configure(enc, plain, c.Encrypted.Fallback, c.Blocking.Enabled, s.blocklistLocked(),
		func() bool { return time.Now().Year() >= 2025 })
	if err := s.proxy.Start(); err != nil {
		return fmt.Errorf("DNS proxy: %w", err)
	}
	s.started = true
	touchHeartbeat()
	s.startHeartbeatLocked()
	if err := writeVosDNSConf(true); err != nil {
		return err
	}
	return dnsVoscleanApply()
}

func touchHeartbeat() {
	now := time.Now()
	if err := os.WriteFile(dnsHeartbeatFile, []byte(dnsProxyAddr+"\n"), 0644); err == nil {
		os.Chtimes(dnsHeartbeatFile, now, now)
	}
}

func (s *dnsProxyState) startHeartbeatLocked() {
	if s.stopBeat != nil {
		return
	}
	stop := make(chan struct{})
	s.stopBeat = stop
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				touchHeartbeat()
			}
		}
	}()
}

func (s *dnsProxyState) stopHeartbeatLocked() {
	if s.stopBeat != nil {
		close(s.stopBeat)
		s.stopBeat = nil
	}
}

// writeVosDNSConf rewrites /data/vosclean/etc/dns.conf from the Custom DNS
// setting plus whether the proxy redirect should be on.
func writeVosDNSConf(proxy bool) error {
	servers := customPlainServers()
	return writeDNSConfFile(len(servers) > 0, servers, proxy)
}

// StartDNSProxy loads the saved settings and blocklist and starts serving.
func StartDNSProxy() {
	dnsp.mu.Lock()
	dnsp.conf = loadDNSProxyConf()
	dnsp.listDoms = readBlocklistFile()
	if dnsp.conf.active() {
		if err := dnsp.applyLocked(); err != nil {
			dnsp.applyErr = err.Error()
			log.Printf("dnsproxy: %v", err)
		}
	}
	dnsp.mu.Unlock()
	go dnsp.refreshLoop()
}

// StopDNSProxy hands DNS back to vosclean's plain mode before QManager exits.
func StopDNSProxy() {
	dnsp.mu.Lock()
	defer dnsp.mu.Unlock()
	if !dnsp.started {
		return
	}
	dnsp.stopHeartbeatLocked()
	dnsp.started = false
	os.Remove(dnsHeartbeatFile)
	_ = writeVosDNSConf(false)
	_ = dnsVoscleanApply()
	dnsp.proxy.Stop()
}

func readBlocklistFile() []string {
	f, err := os.Open(dnsBlocklistFile)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if d := sc.Text(); d != "" {
			out = append(out, d)
		}
	}
	return out
}

func (s *dnsProxyState) refreshLoop() {
	time.Sleep(2 * time.Minute) // let the data call and clock settle after boot
	for {
		s.mu.Lock()
		c := s.conf
		due := c.Blocking.Enabled && !s.updating &&
			(c.ListsKey != c.Blocking.key() || time.Since(time.Unix(c.UpdatedAt, 0)) > blocklistRefresh) &&
			time.Since(s.lastTry) > blocklistRetry
		s.mu.Unlock()
		if due && time.Now().Year() >= 2025 {
			s.startUpdate()
		}
		time.Sleep(time.Hour)
	}
}

// startUpdate runs updateLists in the background unless one is running.
func (s *dnsProxyState) startUpdate() bool {
	s.mu.Lock()
	if s.updating {
		s.mu.Unlock()
		return false
	}
	s.updating = true // so the reply to this request already says "updating"
	s.mu.Unlock()
	go s.updateLists()
	return true
}

var blocklistHTTP = &http.Client{Timeout: 2 * time.Minute}

func (s *dnsProxyState) updateLists() {
	s.mu.Lock()
	s.updating = true
	s.lastTry = time.Now()
	bc := s.conf.Blocking
	s.mu.Unlock()

	counts := map[string]int{}
	var all []string
	var errs []string
	for _, src := range bc.sources() {
		n, err := fetchList(src, func(d string) {
			if len(all) < blocklistMaxDomain {
				all = append(all, d)
			}
		})
		counts[src] = n
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", listLabel(src), err))
		}
	}
	bl := dnsproxy.NewBlocklist(all, nil)
	all = nil
	doms := bl.Domains()

	s.mu.Lock()
	defer func() {
		s.updating = false
		if s.listsDone != nil {
			select {
			case s.listsDone <- struct{}{}:
			default:
			}
		}
		s.mu.Unlock()
	}()
	if len(doms) == 0 && len(errs) > 0 {
		s.conf.UpdateError = strings.Join(errs, "; ")
		_ = saveDNSProxyConf(s.conf)
		return
	}
	if err := writeBlocklistFile(doms); err != nil {
		errs = append(errs, err.Error())
	}
	s.listDoms = doms
	s.conf.UpdatedAt = time.Now().Unix()
	s.conf.UpdateError = strings.Join(errs, "; ")
	s.conf.ListCounts = counts
	s.conf.ListsKey = bc.key()
	_ = saveDNSProxyConf(s.conf)
	if s.started {
		s.proxy.SetBlocklist(s.blocklistLocked())
	}
	log.Printf("dnsproxy: blocklist updated, %d domains", len(doms))
}

func listLabel(src string) string {
	for _, p := range blocklistPresets {
		if p.URL == src {
			return p.Label
		}
	}
	if u, err := url.Parse(src); err == nil && u.Host != "" {
		return u.Host
	}
	return src
}

func fetchList(src string, add func(string)) (int, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, src, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "QManager-VOS")
	resp, err := blocklistHTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return dnsproxy.ParseList(io.LimitReader(resp.Body, blocklistMaxBytes), add)
}

func writeBlocklistFile(doms []string) error {
	var b strings.Builder
	for _, d := range doms {
		b.WriteString(d)
		b.WriteByte('\n')
	}
	return vosWriteFileDurable(dnsBlocklistFile, []byte(b.String()), 0644)
}

// redirectActive reports whether vosclean currently sends DNS to the proxy.
func redirectActive() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "iptables", "-t", "nat", "-S", "VOS_DNS").Output()
	return err == nil && strings.Contains(string(out), "REDIRECT")
}

func cleanDomains(in []string, field string) ([]string, error) {
	var out []string
	for _, d := range in {
		if strings.TrimSpace(d) == "" {
			continue
		}
		n := dnsproxy.NormalizeDomain(d)
		if n == "" {
			return nil, fmt.Errorf("%s: %q is not a domain", field, strings.TrimSpace(d))
		}
		out = append(out, n)
	}
	if len(out) > 2000 {
		return nil, fmt.Errorf("%s: at most 2000 entries", field)
	}
	return out, nil
}

func validateDNSProxyBody(c *dnsProxyConf) error {
	e := &c.Encrypted
	if e.Provider == "" {
		e.Provider = "cloudflare"
	}
	if e.Protocol != "dot" {
		e.Protocol = "doh"
	}
	if e.Provider == "custom" {
		e.CustomURL = strings.TrimSpace(e.CustomURL)
		if e.Enabled || e.CustomURL != "" {
			if _, err := dnsproxy.NewEncrypted("custom", "", e.CustomURL, nil); err != nil {
				return err
			}
		}
	} else if _, err := dnsproxy.NewEncrypted(e.Provider, e.Protocol, "", nil); err != nil {
		return err
	}
	b := &c.Blocking
	var lists []string
	for _, id := range b.Lists {
		ok := false
		for _, p := range blocklistPresets {
			ok = ok || p.ID == id
		}
		if !ok {
			return fmt.Errorf("unknown blocklist %q", id)
		}
		lists = append(lists, id)
	}
	b.Lists = lists
	var urls []string
	for _, u := range b.CustomURLs {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		pu, err := url.Parse(u)
		if err != nil || (pu.Scheme != "https" && pu.Scheme != "http") || pu.Host == "" {
			return fmt.Errorf("blocklist URL %q must start with https://", u)
		}
		urls = append(urls, pu.String())
	}
	if len(urls) > 10 {
		return errors.New("at most 10 custom blocklist URLs")
	}
	b.CustomURLs = urls
	var err error
	if b.Allowlist, err = cleanDomains(b.Allowlist, "allowlist"); err != nil {
		return err
	}
	if b.Denylist, err = cleanDomains(b.Denylist, "blocked domains"); err != nil {
		return err
	}
	if b.Enabled && len(b.Lists)+len(b.CustomURLs)+len(b.Denylist) == 0 {
		return errors.New("choose at least one blocklist")
	}
	return nil
}

func (s *dnsProxyState) snapshot() map[string]any {
	s.mu.Lock()
	c := s.conf
	running := s.started && s.proxy.Running()
	updating := s.updating
	count := len(s.listDoms)
	applyErr := s.applyErr
	s.mu.Unlock()

	var providers []map[string]any
	for _, p := range dnsproxy.Providers {
		providers = append(providers, map[string]any{"id": p.ID, "label": p.Label, "ipv4": p.IPs, "plain": p.Plain,
			"doh": p.DoH, "dot": "tls://" + p.DoTHost})
	}
	var lists []map[string]any
	for _, p := range blocklistPresets {
		lists = append(lists, map[string]any{"id": p.ID, "label": p.Label, "count": c.ListCounts[p.URL]})
	}
	if c.Blocking.Allowlist == nil {
		c.Blocking.Allowlist = []string{}
	}
	if c.Blocking.Denylist == nil {
		c.Blocking.Denylist = []string{}
	}
	if c.Blocking.CustomURLs == nil {
		c.Blocking.CustomURLs = []string{}
	}
	if c.Blocking.Lists == nil {
		c.Blocking.Lists = []string{}
	}
	out := map[string]any{
		"success":   true,
		"encrypted": c.Encrypted,
		"blocking": map[string]any{
			"enabled":      c.Blocking.Enabled,
			"lists":        c.Blocking.Lists,
			"custom_urls":  c.Blocking.CustomURLs,
			"allowlist":    c.Blocking.Allowlist,
			"denylist":     c.Blocking.Denylist,
			"domain_count": count,
			"updated_at":   c.UpdatedAt,
			"update_error": c.UpdateError,
			"updating":     updating,
			"stale":        c.Blocking.Enabled && c.ListsKey != c.Blocking.key(),
		},
		"providers":       providers,
		"resolver":        resolverState(c.Encrypted),
		"blocklists":      lists,
		"running":         running,
		"redirect_active": running && redirectActive(),
		"plain_upstream":  plainLabel(),
		"apply_error":     applyErr,
		"clock_ok":        time.Now().Year() >= 2025,
	}
	if running {
		out["stats"] = s.proxy.Stats()
	}
	return out
}

// HandleDNSProxy serves the Encrypted DNS and Ad Blocking cards.
func (s *Server) HandleDNSProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, dnsp.snapshot())
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "error": "method_not_allowed"})
		return
	}
	var body struct {
		Action    string         `json:"action"`
		Encrypted *encryptedConf `json:"encrypted"`
		Blocking  *blockingConf  `json:"blocking"`
		Domain    string         `json:"domain"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err == nil {
		err = json.Unmarshal(raw, &body)
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "invalid_json"})
		return
	}
	switch body.Action {
	case "save":
		dnsp.mu.Lock()
		next := dnsp.conf
		if body.Encrypted != nil {
			next.Encrypted = *body.Encrypted
		}
		if body.Blocking != nil {
			next.Blocking = *body.Blocking
		}
		if err := validateDNSProxyBody(&next); err != nil {
			dnsp.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "reject_field", "detail": err.Error()})
			return
		}
		prev := dnsp.conf
		dnsp.conf = next
		err := dnsp.applyLocked()
		if err != nil {
			dnsp.applyErr = err.Error()
			// Put the previous working setup back.
			dnsp.conf = prev
			if perr := dnsp.applyLocked(); perr == nil {
				dnsp.applyErr = ""
			}
			dnsp.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "dns_apply_failed", "detail": err.Error()})
			return
		}
		dnsp.applyErr = ""
		if serr := saveDNSProxyConf(dnsp.conf); serr != nil {
			log.Printf("dnsproxy: save: %v", serr)
		}
		needLists := next.Blocking.Enabled && next.ListsKey != next.Blocking.key()
		dnsp.mu.Unlock()
		if needLists {
			dnsp.startUpdate()
		}
		writeJSON(w, http.StatusOK, dnsp.snapshot())
	case "save_resolver":
		var rb resolverBody
		if err := json.Unmarshal(raw, &rb); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "invalid_json"})
			return
		}
		if err := saveResolver(rb); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "dns_apply_failed", "detail": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, dnsp.snapshot())
	case "update_lists":
		dnsp.mu.Lock()
		enabled := dnsp.conf.Blocking.Enabled
		dnsp.mu.Unlock()
		if !enabled {
			writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "reject_field", "detail": "ad blocking is off"})
			return
		}
		dnsp.startUpdate()
		writeJSON(w, http.StatusOK, dnsp.snapshot())
	case "check":
		name := dnsproxy.NormalizeDomain(body.Domain)
		if name == "" {
			writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "reject_field", "detail": "not a domain"})
			return
		}
		dnsp.mu.Lock()
		on := dnsp.conf.Blocking.Enabled
		bl := dnsp.blocklistLocked()
		allowed := dnsproxy.NewBlocklist(dnsp.conf.Blocking.Allowlist, nil).Blocked(name)
		dnsp.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true, "domain": name, "blocking_enabled": on,
			"blocked": on && bl.Blocked(name), "allowlisted": allowed,
		})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "unknown_action"})
	}
}

// ---------------------------------------------------------------------------
// DNS Servers card: carrier DNS, or a provider/custom resolver, plain or
// encrypted. Plain servers live in custom_dns.json (vosclean DNATs to them, and
// they are the fallback when encryption is on); encryption is the proxy's
// encrypted upstream.
// ---------------------------------------------------------------------------

type resolverBody struct {
	Mode      string   `json:"mode"`     // carrier | custom
	Provider  string   `json:"provider"` // provider id or "custom"
	Protocol  string   `json:"protocol"` // plain | doh | dot
	Servers   []string `json:"servers"`  // custom plain servers / fallback
	CustomURL string   `json:"custom_url"`
	Fallback  bool     `json:"fallback"`
}

func sameServers(a, b []string) bool {
	if len(a) == 0 || len(a) > len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// resolverState describes the current choice for the DNS Servers card.
func resolverState(enc encryptedConf) map[string]any {
	servers := customPlainServers()
	if servers == nil {
		servers = []string{}
	}
	st := map[string]any{"mode": "carrier", "provider": "cloudflare", "protocol": "doh",
		"servers": servers, "custom_url": "", "fallback": true, "carrier_servers": len(carrierServers())}
	switch {
	case enc.Enabled:
		st["mode"], st["provider"], st["protocol"] = "custom", enc.Provider, enc.Protocol
		st["custom_url"], st["fallback"] = enc.CustomURL, enc.Fallback
	case len(servers) > 0:
		st["mode"], st["provider"], st["protocol"] = "custom", "custom", "plain"
		for _, p := range dnsproxy.Providers {
			if p.Plain && sameServers(servers, p.IPs) {
				st["provider"] = p.ID
			}
		}
	}
	return st
}

func saveResolver(b resolverBody) error {
	var servers []string
	enc := encryptedConf{Provider: "cloudflare", Protocol: "doh", Fallback: b.Fallback}
	switch b.Mode {
	case "carrier":
	case "custom":
		switch b.Protocol {
		case "plain", "doh", "dot":
		default:
			return errors.New("encryption must be plain, doh or dot")
		}
		if b.Provider == "custom" {
			for _, sv := range b.Servers {
				if sv = strings.TrimSpace(sv); sv == "" {
					continue
				}
				ip := net.ParseIP(sv)
				if ip == nil || ip.To4() == nil {
					return fmt.Errorf("%q is not an IPv4 address", sv)
				}
				servers = append(servers, ip.To4().String())
			}
			if len(servers) > 3 {
				return errors.New("at most 3 servers")
			}
			if b.Protocol == "plain" {
				if len(servers) == 0 {
					return errors.New("enter at least one DNS server")
				}
			} else {
				u := strings.TrimSpace(b.CustomURL)
				if !strings.Contains(u, "://") {
					if b.Protocol == "dot" {
						u = "tls://" + u
					} else {
						u = "https://" + u
					}
				}
				if _, err := dnsproxy.NewEncrypted("custom", "", u, nil); err != nil {
					return err
				}
				enc.Enabled, enc.Provider, enc.CustomURL = true, "custom", u
				if strings.HasPrefix(u, "tls://") {
					enc.Protocol = "dot"
				}
			}
		} else {
			p, ok := dnsproxy.ProviderByID(b.Provider)
			if !ok {
				return errors.New("unknown provider")
			}
			if b.Protocol == "plain" && !p.Plain {
				return fmt.Errorf("%s only offers encrypted DNS", p.Label)
			}
			if p.Plain {
				servers = append([]string(nil), p.IPs...) // plain mode, or the fallback
			}
			if b.Protocol != "plain" {
				enc.Enabled, enc.Provider, enc.Protocol = true, p.ID, b.Protocol
			}
		}
	default:
		return errors.New("mode must be carrier or custom")
	}

	dnsp.mu.Lock()
	defer dnsp.mu.Unlock()
	prevCustom, _ := os.ReadFile(dnsCustomConfFile)
	prevEnc := dnsp.conf.Encrypted

	cfg := qmReadJSONFile(dnsCustomConfFile)
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg["mode"] = "disabled"
	if len(servers) > 0 {
		cfg["mode"] = "enabled"
	}
	cfg["preset"] = b.Provider
	for i, k := range []string{"dns1", "dns2", "dns3"} {
		cfg[k] = ""
		if i < len(servers) {
			cfg[k] = servers[i]
		}
	}
	bs, _ := json.MarshalIndent(cfg, "", "  ")
	if err := vosWriteFileDurable(dnsCustomConfFile, bs, 0644); err != nil {
		return err
	}
	dnsp.conf.Encrypted = enc
	if err := dnsp.applyLocked(); err != nil {
		// Put the previous working setup back.
		if prevCustom != nil {
			_ = vosWriteFileDurable(dnsCustomConfFile, prevCustom, 0644)
		} else {
			os.Remove(dnsCustomConfFile)
		}
		dnsp.conf.Encrypted = prevEnc
		_ = dnsp.applyLocked()
		return err
	}
	dnsp.applyErr = ""
	return saveDNSProxyConf(dnsp.conf)
}
