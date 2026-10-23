// Package wg holds the stick's WireGuard VPN client: saved configs (wg-quick
// format), and the tunnel service that `qmanager-core wg-run` runs as the
// systemd unit vos-wg.
//
// Configs live in /data/vosclean/wireguard/configs/<name>.conf (root only).
// They are ordinary wg-quick files plus one comment line with the stick's own
// options, so a saved file still works with wg-quick elsewhere:
//
//	# QManager: engine=userspace route=lan killswitch=on
//
// PostUp/PreDown and the other wg-quick hooks run shell commands, so they are
// never kept: Parse drops them and reports them in Config.Ignored.
package wg

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const (
	EngineUserspace = "userspace"
	EngineKernel    = "kernel"
	RouteLAN        = "lan" // devices behind the stick
	RouteAll        = "all" // devices behind the stick and the stick itself

	MaxConfigBytes = 16 << 10
)

// Peer is one [Peer] section.
type Peer struct {
	PublicKey    string   `json:"public_key"`
	PresharedKey string   `json:"-"` // never sent to the browser
	Endpoint     string   `json:"endpoint"`
	AllowedIPs   []string `json:"allowed_ips"`
	Keepalive    int      `json:"keepalive"`
}

// Config is one saved tunnel.
type Config struct {
	Name       string   `json:"name"`
	PrivateKey string   `json:"-"` // never sent to the browser
	Addresses  []string `json:"addresses"`
	DNS        []string `json:"dns"`
	MTU        int      `json:"mtu"`
	ListenPort int      `json:"listen_port"`
	Peers      []Peer   `json:"peers"`
	Engine     string   `json:"engine"`
	Route      string   `json:"route"`
	KillSwitch bool     `json:"kill_switch"`
	Ignored    []string `json:"ignored,omitempty"` // lines dropped by Parse
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,31}$`)

// ValidName reports whether name can be used as a config (and file) name.
func ValidName(name string) bool {
	return nameRe.MatchString(name) && !strings.HasSuffix(name, " ") && !strings.Contains(name, "..")
}

// NameFromFilename turns "Mullvad gb-lon 1.conf" into a valid name, or "".
func NameFromFilename(fn string) string {
	fn = strings.TrimSuffix(strings.TrimSuffix(fn, ".conf"), ".CONF")
	var b strings.Builder
	for _, r := range fn {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.', r == ' ':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	s := strings.Trim(b.String(), " .-_")
	for strings.Contains(s, "..") {
		s = strings.ReplaceAll(s, "..", ".")
	}
	if len(s) > 32 {
		s = strings.TrimRight(s[:32], " ")
	}
	if !ValidName(s) {
		return ""
	}
	return s
}

func splitList(v string) []string {
	var out []string
	for _, f := range strings.Split(v, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// Parse reads a wg-quick file. It checks syntax only; call Validate after.
func Parse(text string) (*Config, error) {
	if len(text) > MaxConfigBytes {
		return nil, errors.New("the file is larger than 16 KB, so it isn't a WireGuard config")
	}
	c := &Config{Engine: EngineUserspace, Route: RouteLAN, KillSwitch: true}
	section := ""
	var peer *Peer
	sc := bufio.NewScanner(strings.NewReader(strings.TrimPrefix(text, "\ufeff")))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if rest, ok := strings.CutPrefix(line, "# QManager:"); ok {
			for _, kv := range strings.Fields(rest) {
				k, v, _ := strings.Cut(kv, "=")
				switch k {
				case "engine":
					if v == EngineKernel || v == EngineUserspace {
						c.Engine = v
					}
				case "route":
					if v == RouteAll || v == RouteLAN {
						c.Route = v
					}
				case "killswitch":
					c.KillSwitch = v != "off"
				}
			}
			continue
		}
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			switch strings.ToLower(line) {
			case "[interface]":
				section = "interface"
			case "[peer]":
				section = "peer"
				c.Peers = append(c.Peers, Peer{})
				peer = &c.Peers[len(c.Peers)-1]
			default:
				return nil, fmt.Errorf("line %d: unknown section %s", n, line)
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected Key = Value", n)
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		bad := func(what string) error { return fmt.Errorf("line %d: %s is not a valid %s", n, v, what) }
		atoi := func(what string) (int, error) {
			i, err := strconv.Atoi(v)
			if err != nil {
				return 0, bad(what)
			}
			return i, nil
		}
		switch section {
		case "interface":
			switch k {
			case "privatekey":
				c.PrivateKey = v
			case "address":
				c.Addresses = append(c.Addresses, splitList(v)...)
			case "dns":
				for _, d := range splitList(v) {
					if _, err := netip.ParseAddr(d); err == nil {
						c.DNS = append(c.DNS, d)
					} else {
						c.Ignored = append(c.Ignored, "DNS search domain "+d)
					}
				}
			case "mtu":
				m, err := atoi("MTU")
				if err != nil {
					return nil, err
				}
				c.MTU = m
			case "listenport":
				p, err := atoi("port")
				if err != nil {
					return nil, err
				}
				c.ListenPort = p
			case "postup", "postdown", "preup", "predown", "saveconfig", "table", "fwmark":
				c.Ignored = append(c.Ignored, strings.TrimSpace(sc.Text()))
			default:
				return nil, fmt.Errorf("line %d: unknown setting %q in [Interface]", n, k)
			}
		case "peer":
			switch k {
			case "publickey":
				peer.PublicKey = v
			case "presharedkey":
				peer.PresharedKey = v
			case "endpoint":
				peer.Endpoint = v
			case "allowedips":
				peer.AllowedIPs = append(peer.AllowedIPs, splitList(v)...)
			case "persistentkeepalive":
				if strings.EqualFold(v, "off") {
					peer.Keepalive = 0
					continue
				}
				ka, err := atoi("keepalive")
				if err != nil {
					return nil, err
				}
				peer.Keepalive = ka
			default:
				return nil, fmt.Errorf("line %d: unknown setting %q in [Peer]", n, k)
			}
		default:
			return nil, fmt.Errorf("line %d: setting outside [Interface] or [Peer]", n)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if section == "" {
		return nil, errors.New("no [Interface] section: this isn't a WireGuard config")
	}
	return c, nil
}

// ValidKey reports whether s is a base64 WireGuard key (32 bytes).
func ValidKey(s string) bool {
	b, err := base64.StdEncoding.DecodeString(s)
	return err == nil && len(b) == 32
}

// SplitEndpoint splits host:port (IPv6 in brackets).
func SplitEndpoint(ep string) (host string, port int, err error) {
	h, p, err := net.SplitHostPort(ep)
	if err != nil {
		return "", 0, err
	}
	port, err = strconv.Atoi(p)
	if err != nil || port < 1 || port > 65535 || h == "" {
		return "", 0, errors.New("bad port")
	}
	return h, port, nil
}

var hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,62}[A-Za-z0-9])?)*\.?$`)

// Validate checks values and normalises lists. lan is the stick's LAN
// (192.168.225.0/24); a tunnel address inside it would cut the LAN off.
func (c *Config) Validate(lan netip.Prefix) error {
	if !ValidName(c.Name) {
		return errors.New("name: use 1–32 letters, digits, spaces, dots, dashes or underscores, starting with a letter or digit")
	}
	if !ValidKey(c.PrivateKey) {
		return errors.New("private key: missing or not a WireGuard key")
	}
	if len(c.Addresses) == 0 {
		return errors.New("address: the config needs the tunnel address the server gave you (Address = ...)")
	}
	has4 := false
	for i, a := range c.Addresses {
		p, err := netip.ParsePrefix(a)
		if err != nil {
			ip, err2 := netip.ParseAddr(a)
			if err2 != nil {
				return fmt.Errorf("address: %s is not an IP address", a)
			}
			p = netip.PrefixFrom(ip, ip.BitLen())
		}
		c.Addresses[i] = p.String()
		if p.Addr().Is4() {
			has4 = true
			if lan.IsValid() && p.Overlaps(lan) {
				return fmt.Errorf("address: %s overlaps the stick's LAN %s", p, lan)
			}
		}
	}
	if !has4 {
		return errors.New("address: an IPv4 tunnel address is needed (the mobile network here is IPv4 only)")
	}
	for _, d := range c.DNS {
		if _, err := netip.ParseAddr(d); err != nil {
			return fmt.Errorf("DNS: %s is not an IP address", d)
		}
	}
	if c.MTU != 0 && (c.MTU < 1280 || c.MTU > 1500) {
		return errors.New("MTU: use 1280–1500, or leave it empty for 1420")
	}
	if c.ListenPort < 0 || c.ListenPort > 65535 {
		return errors.New("listen port: use 1–65535, or leave it empty")
	}
	if len(c.Peers) == 0 {
		return errors.New("the config needs a [Peer] section (the server)")
	}
	if len(c.Peers) > 16 {
		return errors.New("at most 16 peers")
	}
	own := ""
	if k, err := wgtypes.ParseKey(c.PrivateKey); err == nil {
		own = k.PublicKey().String()
	}
	seen := map[string]bool{}
	withEndpoint := false
	for i := range c.Peers {
		p := &c.Peers[i]
		n := i + 1
		if !ValidKey(p.PublicKey) {
			return fmt.Errorf("peer %d: public key missing or not a WireGuard key", n)
		}
		if p.PublicKey == own {
			return fmt.Errorf("peer %d: that is this stick's own public key; enter the server's public key", n)
		}
		if seen[p.PublicKey] {
			return fmt.Errorf("peer %d: the same public key appears twice", n)
		}
		seen[p.PublicKey] = true
		if p.PresharedKey != "" && !ValidKey(p.PresharedKey) {
			return fmt.Errorf("peer %d: preshared key is not a WireGuard key", n)
		}
		if p.Endpoint != "" {
			h, _, err := SplitEndpoint(p.Endpoint)
			if err != nil {
				return fmt.Errorf("peer %d: endpoint %s should be host:port", n, p.Endpoint)
			}
			if _, err := netip.ParseAddr(h); err != nil && !hostRe.MatchString(h) {
				return fmt.Errorf("peer %d: endpoint host %s is not a valid name or address", n, h)
			}
			withEndpoint = true
		}
		if len(p.AllowedIPs) == 0 {
			return fmt.Errorf("peer %d: allowed IPs are empty, so nothing would use the tunnel (use 0.0.0.0/0 for everything)", n)
		}
		for j, a := range p.AllowedIPs {
			pf, err := netip.ParsePrefix(a)
			if err != nil {
				ip, err2 := netip.ParseAddr(a)
				if err2 != nil {
					return fmt.Errorf("peer %d: allowed IP %s is not an address range", n, a)
				}
				pf = netip.PrefixFrom(ip, ip.BitLen())
			}
			p.AllowedIPs[j] = pf.Masked().String()
		}
		if p.Keepalive < 0 || p.Keepalive > 65535 {
			return fmt.Errorf("peer %d: keepalive should be 0–65535 seconds", n)
		}
	}
	if !withEndpoint {
		return errors.New("no peer has an endpoint (Endpoint = host:port), so the stick wouldn't know where to connect")
	}
	if c.Engine != EngineKernel && c.Engine != EngineUserspace {
		c.Engine = EngineUserspace
	}
	if c.Route != RouteAll && c.Route != RouteLAN {
		c.Route = RouteLAN
	}
	return nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// Marshal writes the config as a wg-quick file (secrets included: the file is
// root-only on the stick and never sent to the browser).
func (c *Config) Marshal() []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# WireGuard config %q, saved by QManager.\n", c.Name)
	fmt.Fprintf(&b, "# QManager: engine=%s route=%s killswitch=%s\n\n", c.Engine, c.Route, onOff(c.KillSwitch))
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", c.PrivateKey)
	fmt.Fprintf(&b, "Address = %s\n", strings.Join(c.Addresses, ", "))
	if len(c.DNS) > 0 {
		fmt.Fprintf(&b, "DNS = %s\n", strings.Join(c.DNS, ", "))
	}
	if c.MTU > 0 {
		fmt.Fprintf(&b, "MTU = %d\n", c.MTU)
	}
	if c.ListenPort > 0 {
		fmt.Fprintf(&b, "ListenPort = %d\n", c.ListenPort)
	}
	for _, p := range c.Peers {
		b.WriteString("\n[Peer]\n")
		fmt.Fprintf(&b, "PublicKey = %s\n", p.PublicKey)
		if p.PresharedKey != "" {
			fmt.Fprintf(&b, "PresharedKey = %s\n", p.PresharedKey)
		}
		if p.Endpoint != "" {
			fmt.Fprintf(&b, "Endpoint = %s\n", p.Endpoint)
		}
		fmt.Fprintf(&b, "AllowedIPs = %s\n", strings.Join(p.AllowedIPs, ", "))
		if p.Keepalive > 0 {
			fmt.Fprintf(&b, "PersistentKeepalive = %d\n", p.Keepalive)
		}
	}
	return []byte(b.String())
}

// EffectiveMTU is the interface MTU (1420 unless the config says otherwise).
func (c *Config) EffectiveMTU() int {
	if c.MTU > 0 {
		return c.MTU
	}
	return 1420
}

// CGNAT is 100.64.0.0/10, which Tailscale and NetBird also use.
var CGNAT = netip.MustParsePrefix("100.64.0.0/10")

// UsesCGNAT reports whether the config's tunnel addresses or DNS servers are
// in 100.64.0.0/10 (some VPN providers hand those out).
func (c *Config) UsesCGNAT() bool {
	for _, a := range c.Addresses {
		if p, err := netip.ParsePrefix(a); err == nil && p.Overlaps(CGNAT) {
			return true
		}
	}
	for _, d := range c.DNS {
		if a, err := netip.ParseAddr(d); err == nil && CGNAT.Contains(a) {
			return true
		}
	}
	return false
}

// CGNATInUse returns the name of another interface that currently has an
// address in 100.64.0.0/10 (Tailscale's tailscale0, NetBird's wt0), or "".
// Only a connected Tailscale or NetBird holds one; installed alone is fine.
func CGNATInUse() string {
	ifs, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifs {
		if ifc.Name == "vwg0" {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(ipn.IP.To4()); ok && CGNAT.Contains(ip) {
					return ifc.Name
				}
			}
		}
	}
	return ""
}

// ActiveCGNATConfig returns the active config's name if it uses 100.64.0.0/10.
func ActiveCGNATConfig() string {
	n := Active()
	if n == "" {
		return ""
	}
	if c, err := Load(n); err == nil && c.UsesCGNAT() {
		return n
	}
	return ""
}

// CGNATConflict explains why c can't come up right now, or returns nil.
func (c *Config) CGNATConflict() error {
	if !c.UsesCGNAT() {
		return nil
	}
	if ifc := CGNATInUse(); ifc != "" {
		who := ifc
		switch {
		case strings.HasPrefix(ifc, "tailscale"):
			who = "Tailscale"
		case ifc == "wt0":
			who = "NetBird"
		}
		return fmt.Errorf("this config uses 100.64.0.0/10, and %s is connected with an address in that range (%s). Disconnect %s first", who, ifc, who)
	}
	return nil
}
