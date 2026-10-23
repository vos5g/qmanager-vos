package wg

// The tunnel service: `qmanager-core wg-run` (systemd unit vos-wg, started by
// `vosclean wg start`). It runs apart from QManager, so restarting QManager
// doesn't drop the tunnel.
//
// Routing (IPv4 only; the mobile side has no IPv6):
//
//	5289  fwmark 0xca6c lookup main            the tunnel's own UDP and our DNS lookups
//	5290  lookup main suppress_prefixlength 0  route "all": LAN and other local routes stay
//	5300  lookup 51820                         route "all": everything else into the tunnel
//	5300  iif bridge0 lookup 51820             route "lan": devices behind the stick
//	5301+ to <DNS> lookup 51820                route "lan": dnsmasq's queries to the tunnel DNS
//
// Table 51820 holds a route into vwg0 for each AllowedIPs range. With the kill
// switch on, each range also gets an unreachable route with a worse metric, so
// when the interface goes the traffic is dropped instead of falling through to
// the mobile network. Tailscale's rules (5210-5270) come first and are left alone.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/ipc"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const (
	Iface      = "vwg0"
	Table      = 51820
	Mark       = 0xca6c // 51820
	LANIface   = "bridge0"
	prioMark   = 5289
	prioSupp   = 5290
	prioMain   = 5300
	prioDNS    = 5301
	prioLast   = 5316
	unreachMet = 10000

	chainFilter = "VOS_WG"
	chainDNS    = "VOS_WG_DNS"
	tsMark      = "0x80000/0xff0000" // Tailscale's own packets

	dnsmasqPID = "/var/run/data/dnsmasq.pid"
)

var (
	StateFile = "/tmp/vosclean/wg-state.json"
	LogFile   = "/tmp/vosclean/log/wireguard.log"
)

// State is what the service reports to QManager.
type State struct {
	Name       string `json:"name"`
	Engine     string `json:"engine"`
	Route      string `json:"route"`
	KillSwitch bool   `json:"kill_switch"`
	Phase      string `json:"phase"` // starting | connecting | up | error
	Error      string `json:"error,omitempty"`
	Since      int64  `json:"since"`
	PID        int    `json:"pid"`
}

// ReadState returns the service's last state, or nil.
func ReadState() *State {
	b, err := os.ReadFile(StateFile)
	if err != nil {
		return nil
	}
	var s State
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	return &s
}

func writeState(s *State) {
	b, _ := json.Marshal(s)
	_ = os.MkdirAll("/tmp/vosclean", 0o755)
	tmp := StateFile + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, StateFile)
	}
}

// ---------------------------------------------------------------- helpers ---

func ipt(args ...string) error {
	out, err := exec.Command("iptables", append([]string{"-w"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func iptS(table string, chain ...string) string {
	args := []string{"-w", "-t", table, "-S"}
	out, _ := exec.Command("iptables", append(args, chain...)...).Output()
	return string(out)
}

// LANPrefix returns the stick's LAN (bridge0's IPv4 network).
func LANPrefix() netip.Prefix {
	l, err := netlink.LinkByName(LANIface)
	if err == nil {
		addrs, _ := netlink.AddrList(l, netlink.FAMILY_V4)
		for _, a := range addrs {
			ones, _ := a.Mask.Size()
			if ip, ok := netip.AddrFromSlice(a.IP.To4()); ok {
				return netip.PrefixFrom(ip, ones).Masked()
			}
		}
	}
	return netip.MustParsePrefix("192.168.225.0/24")
}

func ipnet(p netip.Prefix) *net.IPNet {
	return &net.IPNet{IP: p.Addr().AsSlice(), Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen())}
}

func v4Prefixes(list []string) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range list {
		if p, err := netip.ParsePrefix(s); err == nil && p.Addr().Is4() {
			out = append(out, p.Masked())
		}
	}
	return out
}

func (c *Config) allowedV4() []netip.Prefix {
	var all []netip.Prefix
	for _, p := range c.Peers {
		all = append(all, v4Prefixes(p.AllowedIPs)...)
	}
	return all
}

func (c *Config) dnsV4() []netip.Addr {
	var out []netip.Addr
	for _, d := range c.DNS {
		if a, err := netip.ParseAddr(d); err == nil && a.Is4() {
			out = append(out, a)
		}
	}
	return out
}

// markedResolver looks names up with the tunnel's fwmark, so the lookups go
// out on the mobile network even while the kill switch blocks everything else.
func markedResolver() *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second, Control: func(_, _ string, rc syscall.RawConn) error {
				var serr error
				err := rc.Control(func(fd uintptr) { serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, Mark) })
				if err != nil {
					return err
				}
				return serr
			}}
			return d.DialContext(ctx, network, address)
		},
	}
}

// resolveEndpoint resolves host:port, preferring IPv4.
func resolveEndpoint(ep string) (*net.UDPAddr, error) {
	host, port, err := SplitEndpoint(ep)
	if err != nil {
		return nil, err
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return net.UDPAddrFromAddrPort(netip.AddrPortFrom(a, uint16(port))), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ips, err := markedResolver().LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("looking up %s: %w", host, err)
	}
	var pick netip.Addr
	for _, ip := range ips {
		if ip.Unmap().Is4() {
			pick = ip.Unmap()
			break
		}
	}
	if !pick.IsValid() {
		return nil, fmt.Errorf("%s has no IPv4 address (the mobile network here is IPv4 only)", host)
	}
	return net.UDPAddrFromAddrPort(netip.AddrPortFrom(pick, uint16(port))), nil
}

func hostIsName(ep string) bool {
	h, _, err := SplitEndpoint(ep)
	if err != nil {
		return false
	}
	_, err = netip.ParseAddr(h)
	return err != nil
}

// FlushConntrack drops the connection-tracking entries of devices behind the
// stick (except their connections to the stick itself) and all DNS entries,
// so existing connections, including ones the hardware fast path took over,
// start again on the new route. It also clears dnsmasq's cache.
func FlushConntrack() {
	lan := LANPrefix()
	f := flowFilter{lan: lan}
	n, err := netlink.ConntrackDeleteFilters(netlink.ConntrackTable, unix.AF_INET, f)
	if err != nil {
		log.Printf("conntrack flush: %v", err)
	} else {
		log.Printf("conntrack: dropped %d entries", n)
	}
	if b, err := os.ReadFile(dnsmasqPID); err == nil {
		var pid int
		if _, err := fmt.Sscan(strings.TrimSpace(string(b)), &pid); err == nil && pid > 1 {
			_ = syscall.Kill(pid, syscall.SIGHUP)
		}
	}
}

type flowFilter struct{ lan netip.Prefix }

func (f flowFilter) MatchConntrackFlow(fl *netlink.ConntrackFlow) bool {
	if fl.Forward.Protocol == unix.IPPROTO_UDP && fl.Forward.DstPort == 53 {
		return true
	}
	src, ok1 := netip.AddrFromSlice(fl.Forward.SrcIP.To4())
	dst, ok2 := netip.AddrFromSlice(fl.Forward.DstIP.To4())
	return ok1 && ok2 && f.lan.Contains(src) && !f.lan.Contains(dst)
}

// ------------------------------------------------------------- firewall ---

func applyFirewall(c *Config) error {
	_ = ipt("-N", chainFilter)
	if err := ipt("-F", chainFilter); err != nil {
		return err
	}
	// Nothing from the VPN side may open a connection to the stick or the LAN.
	if err := ipt("-A", chainFilter, "-i", Iface, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "RETURN"); err != nil {
		return err
	}
	if err := ipt("-A", chainFilter, "-i", Iface, "-j", "DROP"); err != nil {
		return err
	}
	for _, ch := range []string{"INPUT", "FORWARD"} {
		if ipt("-C", ch, "-j", chainFilter) != nil {
			if err := ipt("-I", ch, "1", "-j", chainFilter); err != nil {
				return err
			}
		}
	}
	if ipt("-t", "nat", "-C", "POSTROUTING", "-o", Iface, "-j", "MASQUERADE") != nil {
		if err := ipt("-t", "nat", "-I", "POSTROUTING", "1", "-o", Iface, "-j", "MASQUERADE"); err != nil {
			return err
		}
	}
	for _, d := range []string{"-o", "-i"} {
		r := []string{"FORWARD", d, Iface, "-p", "tcp", "--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS", "--clamp-mss-to-pmtu"}
		if ipt(append([]string{"-t", "mangle", "-C"}, r...)...) != nil {
			if err := ipt(append([]string{"-t", "mangle", "-A"}, r...)...); err != nil {
				return err
			}
		}
	}
	return applyDNS(c)
}

// applyDNS sends dnsmasq's upstream queries (which every LAN device uses) to
// the config's DNS server, ahead of vosclean's Custom DNS chain.
func applyDNS(c *Config) error {
	dns := c.dnsV4()
	if len(dns) == 0 {
		return nil
	}
	_ = ipt("-t", "nat", "-N", chainDNS)
	if err := ipt("-t", "nat", "-F", chainDNS); err != nil {
		return err
	}
	rules := [][]string{
		{"-m", "mark", "--mark", fmt.Sprint(Mark), "-j", "RETURN"},
		{"-m", "mark", "--mark", tsMark, "-j", "RETURN"},
	}
	for _, p := range []string{"udp", "tcp"} {
		rules = append(rules, []string{"-o", "rmnet+", "-p", p, "--dport", "53", "-j", "DNAT", "--to-destination", dns[0].String()})
	}
	for _, r := range rules {
		if err := ipt(append([]string{"-t", "nat", "-A", chainDNS}, r...)...); err != nil {
			return err
		}
	}
	return ensureDNSFirst()
}

func ensureDNSFirst() error {
	first := ""
	for _, l := range strings.Split(iptS("nat", "OUTPUT"), "\n") {
		if strings.HasPrefix(l, "-A OUTPUT") {
			first = l
			break
		}
	}
	if first == "-A OUTPUT -j "+chainDNS {
		return nil
	}
	for ipt("-t", "nat", "-D", "OUTPUT", "-j", chainDNS) == nil {
	}
	return ipt("-t", "nat", "-I", "OUTPUT", "1", "-j", chainDNS)
}

func removeFirewall() {
	for _, ch := range []string{"INPUT", "FORWARD"} {
		for ipt("-D", ch, "-j", chainFilter) == nil {
		}
	}
	_ = ipt("-F", chainFilter)
	_ = ipt("-X", chainFilter)
	for ipt("-t", "nat", "-D", "POSTROUTING", "-o", Iface, "-j", "MASQUERADE") == nil {
	}
	for ipt("-t", "nat", "-D", "OUTPUT", "-j", chainDNS) == nil {
	}
	_ = ipt("-t", "nat", "-F", chainDNS)
	_ = ipt("-t", "nat", "-X", chainDNS)
	for _, d := range []string{"-o", "-i"} {
		for ipt("-t", "mangle", "-D", "FORWARD", d, Iface, "-p", "tcp", "--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS", "--clamp-mss-to-pmtu") == nil {
		}
	}
}

// firewallIntact checks the rules QCMAP likes to flush (three iptables calls).
func firewallIntact(c *Config) bool {
	f := iptS("filter")
	if !strings.Contains(f, "-A INPUT -j "+chainFilter) || !strings.Contains(f, "-A FORWARD -j "+chainFilter) ||
		strings.Count(f, "-A "+chainFilter+" ") != 2 {
		return false
	}
	n := iptS("nat")
	if !strings.Contains(n, "-A POSTROUTING -o "+Iface+" -j MASQUERADE") {
		return false
	}
	if len(c.dnsV4()) > 0 {
		if strings.Count(n, "-A "+chainDNS+" ") != 4 {
			return false
		}
		for _, l := range strings.Split(n, "\n") {
			if strings.HasPrefix(l, "-A OUTPUT") {
				if l != "-A OUTPUT -j "+chainDNS {
					return false
				}
				break
			}
		}
	}
	return strings.Count(iptS("mangle", "FORWARD"), Iface+" -p tcp") == 2
}

// -------------------------------------------------------------- routing ---

func ourRules(c *Config, lan netip.Prefix) []*netlink.Rule {
	mk := func(prio, table int) *netlink.Rule {
		r := netlink.NewRule()
		r.Family = netlink.FAMILY_V4
		r.Priority = prio
		r.Table = table
		return r
	}
	mask := uint32(0xffffffff)
	m := mk(prioMark, unix.RT_TABLE_MAIN)
	m.Mark = Mark
	m.Mask = &mask
	rules := []*netlink.Rule{m}
	if c.Route == RouteAll {
		s := mk(prioSupp, unix.RT_TABLE_MAIN)
		s.SuppressPrefixlen = 0
		rules = append(rules, s, mk(prioMain, Table))
	} else {
		r := mk(prioMain, Table)
		r.IifName = LANIface
		rules = append(rules, r)
		for i, d := range c.dnsV4() {
			if i > prioLast-prioDNS {
				break
			}
			r := mk(prioDNS+i, Table)
			r.Dst = ipnet(netip.PrefixFrom(d, 32))
			rules = append(rules, r)
		}
	}
	return rules
}

func applyRules(c *Config, lan netip.Prefix) error {
	for _, r := range ourRules(c, lan) {
		if err := netlink.RuleAdd(r); err != nil && !errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("ip rule %d: %w", r.Priority, err)
		}
	}
	return nil
}

func rulesIntact(c *Config, lan netip.Prefix) bool {
	have := map[int]bool{}
	rs, err := netlink.RuleList(netlink.FAMILY_V4)
	if err != nil {
		return true
	}
	for _, r := range rs {
		have[r.Priority] = true
	}
	for _, r := range ourRules(c, lan) {
		if !have[r.Priority] {
			return false
		}
	}
	return true
}

func removeRules() {
	rs, _ := netlink.RuleList(netlink.FAMILY_V4)
	for i := range rs {
		if rs[i].Priority >= prioMark && rs[i].Priority <= prioLast {
			_ = netlink.RuleDel(&rs[i])
		}
	}
}

// applyKillRoutes adds an unreachable route per AllowedIPs range.
func applyKillRoutes(c *Config) error {
	for _, p := range c.allowedV4() {
		r := &netlink.Route{Table: Table, Dst: ipnet(p), Type: unix.RTN_UNREACHABLE, Priority: unreachMet}
		if err := netlink.RouteReplace(r); err != nil {
			return fmt.Errorf("kill switch route %s: %w", p, err)
		}
	}
	return nil
}

func applyTunnelRoutes(c *Config, link netlink.Link) error {
	for _, p := range c.allowedV4() {
		r := &netlink.Route{Table: Table, Dst: ipnet(p), LinkIndex: link.Attrs().Index, Scope: netlink.SCOPE_LINK}
		if err := netlink.RouteReplace(r); err != nil {
			return fmt.Errorf("route %s: %w", p, err)
		}
	}
	return nil
}

func tunnelRoutesIntact(c *Config, link netlink.Link) bool {
	rs, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{Table: Table}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return true
	}
	n := 0
	for _, r := range rs {
		if r.LinkIndex == link.Attrs().Index {
			n++
		}
	}
	return n >= len(c.allowedV4())
}

func flushTable() {
	rs, _ := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{Table: Table}, netlink.RT_FILTER_TABLE)
	for i := range rs {
		_ = netlink.RouteDel(&rs[i])
	}
}

// --------------------------------------------------------------- tunnel ---

type tunnel struct {
	cfg    *Config
	link   netlink.Link
	dev    *device.Device // userspace engine only
	uapi   net.Listener
	client *wgctrl.Client
}

func (t *tunnel) close() {
	if t.client != nil {
		_ = t.client.Close()
	}
	if t.uapi != nil {
		_ = t.uapi.Close()
	}
	if t.dev != nil {
		t.dev.Close() // removes the TUN interface
	}
	if l, err := netlink.LinkByName(Iface); err == nil {
		_ = netlink.LinkDel(l)
	}
}

func deviceLogger() *device.Logger {
	return &device.Logger{
		Verbosef: func(string, ...any) {},
		Errorf:   func(f string, a ...any) { log.Printf("wireguard-go: "+f, a...) },
	}
}

func peerConfigs(c *Config) ([]wgtypes.PeerConfig, error) {
	var out []wgtypes.PeerConfig
	for _, p := range c.Peers {
		pub, err := wgtypes.ParseKey(p.PublicKey)
		if err != nil {
			return nil, err
		}
		pc := wgtypes.PeerConfig{PublicKey: pub, ReplaceAllowedIPs: true}
		if p.PresharedKey != "" {
			psk, err := wgtypes.ParseKey(p.PresharedKey)
			if err != nil {
				return nil, err
			}
			pc.PresharedKey = &psk
		}
		if p.Endpoint != "" {
			ep, err := resolveEndpoint(p.Endpoint)
			if err != nil {
				return nil, err
			}
			pc.Endpoint = ep
		}
		if p.Keepalive > 0 {
			ka := time.Duration(p.Keepalive) * time.Second
			pc.PersistentKeepaliveInterval = &ka
		}
		for _, a := range p.AllowedIPs {
			if pf, err := netip.ParsePrefix(a); err == nil {
				pc.AllowedIPs = append(pc.AllowedIPs, *ipnet(pf))
			}
		}
		out = append(out, pc)
	}
	return out, nil
}

func startTunnel(c *Config) (*tunnel, error) {
	t := &tunnel{cfg: c}
	mtu := c.EffectiveMTU()
	if c.Engine == EngineKernel {
		la := netlink.NewLinkAttrs()
		la.Name, la.MTU = Iface, mtu
		if err := netlink.LinkAdd(&netlink.GenericLink{LinkAttrs: la, LinkType: "wireguard"}); err != nil {
			if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EINVAL) {
				return nil, errors.New("the kernel WireGuard module isn't loaded: switch on the WireGuard group in Kernel Modules, or use the userspace engine")
			}
			return nil, fmt.Errorf("creating %s: %w", Iface, err)
		}
	} else {
		tdev, err := tun.CreateTUN(Iface, mtu)
		if err != nil {
			return nil, fmt.Errorf("creating %s: %w", Iface, err)
		}
		t.dev = device.NewDevice(tdev, conn.NewDefaultBind(), deviceLogger())
		f, err := ipc.UAPIOpen(Iface)
		if err != nil {
			t.close()
			return nil, fmt.Errorf("control socket: %w", err)
		}
		if t.uapi, err = ipc.UAPIListen(Iface, f); err != nil {
			t.close()
			return nil, fmt.Errorf("control socket: %w", err)
		}
		go func(l net.Listener, d *device.Device) {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				go d.IpcHandle(c)
			}
		}(t.uapi, t.dev)
	}
	var err error
	if t.client, err = wgctrl.New(); err != nil {
		t.close()
		return nil, err
	}
	priv, err := wgtypes.ParseKey(c.PrivateKey)
	if err != nil {
		t.close()
		return nil, err
	}
	peers, err := peerConfigs(c)
	if err != nil {
		t.close()
		return nil, err
	}
	mark := Mark
	wc := wgtypes.Config{PrivateKey: &priv, FirewallMark: &mark, ReplacePeers: true, Peers: peers}
	if c.ListenPort > 0 {
		wc.ListenPort = &c.ListenPort
	}
	if err := t.client.ConfigureDevice(Iface, wc); err != nil {
		t.close()
		return nil, fmt.Errorf("configuring %s: %w", Iface, err)
	}
	if t.link, err = netlink.LinkByName(Iface); err != nil {
		t.close()
		return nil, err
	}
	for _, a := range v4Prefixes(c.Addresses) {
		if err := netlink.AddrReplace(t.link, &netlink.Addr{IPNet: ipnet(a)}); err != nil {
			t.close()
			return nil, fmt.Errorf("address %s: %w", a, err)
		}
	}
	_ = netlink.LinkSetMTU(t.link, mtu)
	if err := netlink.LinkSetUp(t.link); err != nil {
		t.close()
		return nil, err
	}
	if err := applyTunnelRoutes(c, t.link); err != nil {
		t.close()
		return nil, err
	}
	return t, nil
}

// lastHandshake returns the newest handshake across peers.
func (t *tunnel) lastHandshake() time.Time {
	d, err := t.client.Device(Iface)
	if err != nil {
		return time.Time{}
	}
	var last time.Time
	for _, p := range d.Peers {
		if p.LastHandshakeTime.After(last) {
			last = p.LastHandshakeTime
		}
	}
	return last
}

// reresolve updates endpoints given as names (the server's address may move).
func (t *tunnel) reresolve() {
	var upd []wgtypes.PeerConfig
	for _, p := range t.cfg.Peers {
		if p.Endpoint == "" || !hostIsName(p.Endpoint) {
			continue
		}
		ep, err := resolveEndpoint(p.Endpoint)
		if err != nil {
			log.Printf("re-resolve %s: %v", p.Endpoint, err)
			continue
		}
		pub, _ := wgtypes.ParseKey(p.PublicKey)
		upd = append(upd, wgtypes.PeerConfig{PublicKey: pub, UpdateOnly: true, Endpoint: ep})
	}
	if len(upd) > 0 {
		if err := t.client.ConfigureDevice(Iface, wgtypes.Config{Peers: upd}); err != nil {
			log.Printf("re-resolve: %v", err)
		}
	}
}

// --------------------------------------------------------------- service ---

// Cleanup removes everything the service sets up (safe to run any time).
func Cleanup() {
	if l, err := netlink.LinkByName(Iface); err == nil {
		_ = netlink.LinkDel(l)
	}
	removeRules()
	flushTable()
	removeFirewall()
	_ = os.Remove(StateFile)
}

func setupLog() {
	_ = os.MkdirAll("/tmp/vosclean/log", 0o755)
	if fi, err := os.Stat(LogFile); err == nil && fi.Size() > 128<<10 {
		_ = os.Rename(LogFile, LogFile+".1")
	}
	f, err := os.OpenFile(LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		log.SetOutput(f) // RAM log; the unit's own output goes nowhere
	}
	log.SetFlags(log.LstdFlags)
}

// Run is `qmanager-core wg-run`. It returns the process exit code: non-zero
// makes systemd retry (Restart=on-failure).
func Run() int {
	setupLog()
	name := Active()
	if name == "" {
		log.Printf("no active config; cleaning up")
		Cleanup()
		FlushConntrack()
		return 0
	}
	c, err := Load(name)
	st := &State{Name: name, Phase: "starting", Since: time.Now().Unix(), PID: os.Getpid()}
	if err != nil {
		st.Phase, st.Error = "error", "can't read the config: "+err.Error()
		writeState(st)
		log.Printf("%s: %v", name, err)
		return 1
	}
	st.Engine, st.Route, st.KillSwitch = c.Engine, c.Route, c.KillSwitch
	writeState(st)
	lan := LANPrefix()
	if err := c.Validate(lan); err != nil {
		st.Phase, st.Error = "error", err.Error()
		writeState(st)
		log.Printf("%s: invalid config: %v", name, err)
		return 1
	}
	log.Printf("starting %q: engine=%s route=%s killswitch=%v", name, c.Engine, c.Route, c.KillSwitch)
	cgnatConflict := c.CGNATConflict() // checked after the kill switch is in place

	// Start clean, then block first (kill switch) so nothing leaks while the
	// tunnel comes up.
	if l, err := netlink.LinkByName(Iface); err == nil {
		_ = netlink.LinkDel(l)
	}
	removeRules()
	flushTable()
	fail := func(err error) int {
		st.Phase, st.Error = "error", err.Error()
		writeState(st)
		log.Printf("%s: %v", name, err)
		if !c.KillSwitch {
			Cleanup()
			writeState(st)
		}
		return 1
	}
	if c.KillSwitch {
		if err := applyKillRoutes(c); err != nil {
			return fail(err)
		}
	}
	if err := applyRules(c, lan); err != nil {
		return fail(err)
	}
	if err := applyFirewall(c); err != nil {
		return fail(err)
	}
	if cgnatConflict != nil { // e.g. Tailscale came up first at boot; retried every 15 s
		return fail(cgnatConflict)
	}
	t, err := startTunnel(c)
	if err != nil {
		return fail(err)
	}
	FlushConntrack()
	st.Phase = "connecting"
	writeState(st)
	log.Printf("%s is up on %s; waiting for a handshake", name, Iface)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	started := time.Now()
	lastResolve := time.Now()
	lastRepair := time.Time{}
	for {
		select {
		case s := <-sig:
			log.Printf("stopping (%v)", s)
			keep := c.KillSwitch && Active() == name
			t.close()
			if keep {
				log.Printf("kill switch stays on: %s is still the active config", name)
				st.Phase, st.Error = "error", "the tunnel service stopped; the kill switch is blocking traffic"
				writeState(st)
			} else {
				Cleanup()
			}
			FlushConntrack()
			return 0
		case <-tick.C:
			if _, err := netlink.LinkByName(Iface); err != nil {
				return fail(errors.New("the tunnel interface disappeared"))
			}
			if time.Since(lastRepair) >= 20*time.Second {
				lastRepair = time.Now()
				if !rulesIntact(c, lan) {
					log.Printf("routing rules missing; re-adding")
					if err := applyRules(c, lan); err != nil {
						log.Printf("re-adding rules: %v", err)
					}
				}
				if !tunnelRoutesIntact(c, t.link) {
					log.Printf("tunnel routes missing; re-adding")
					_ = applyTunnelRoutes(c, t.link)
					if c.KillSwitch {
						_ = applyKillRoutes(c)
					}
				}
				if !firewallIntact(c) {
					log.Printf("firewall/NAT rules missing (QCMAP flush?); re-adding")
					if err := applyFirewall(c); err != nil {
						log.Printf("re-adding firewall: %v", err)
					}
				}
			}
			hs := t.lastHandshake()
			phase := "connecting"
			if !hs.IsZero() && time.Since(hs) < 180*time.Second {
				phase = "up"
			}
			stale := hs.IsZero() && time.Since(started) > 30*time.Second || !hs.IsZero() && time.Since(hs) > 180*time.Second
			if stale && time.Since(lastResolve) > 2*time.Minute {
				lastResolve = time.Now()
				t.reresolve()
			}
			if phase != st.Phase || st.Error != "" {
				if phase == "up" && st.Phase != "up" {
					log.Printf("handshake with the server done")
				} else if phase == "connecting" && st.Phase == "up" {
					log.Printf("no handshake for 3 minutes")
				}
				st.Phase, st.Error = phase, ""
				writeState(st)
			}
		}
	}
}

// Show prints the interface's peers (for `qmanager-core wg-show`).
func Show(w io.Writer) error {
	cl, err := wgctrl.New()
	if err != nil {
		return err
	}
	defer cl.Close()
	d, err := cl.Device(Iface)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "interface %s (%s) public key %s listen port %d fwmark %#x\n", d.Name, d.Type, d.PublicKey, d.ListenPort, d.FirewallMark)
	for _, p := range d.Peers {
		hs := "never"
		if !p.LastHandshakeTime.IsZero() {
			hs = time.Since(p.LastHandshakeTime).Round(time.Second).String() + " ago"
		}
		fmt.Fprintf(w, "peer %s endpoint %v handshake %s rx %d tx %d allowed %v\n", p.PublicKey, p.Endpoint, hs, p.ReceiveBytes, p.TransmitBytes, p.AllowedIPs)
	}
	return nil
}
