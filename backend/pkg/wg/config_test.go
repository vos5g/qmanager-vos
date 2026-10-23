package wg

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

var lan = netip.MustParsePrefix("192.168.225.0/24")

// Throwaway keys generated for these tests only.
const (
	tPriv = "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk="
	tPub  = "xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg="
	tPSK  = "FpCyhws9cxwWoV4xELtfJvjJN+zQVRPISllRWgeopVE="
)

const sample = `[Interface]
# Device: example
PrivateKey = ` + tPriv + `
Address = 10.64.1.2/32, fc00:bbbb:bbbb:bb01::1:2/128
DNS = 10.64.0.1, corp.example
PostUp = iptables -I OUTPUT -j REJECT
MTU = 1380

[Peer]
PublicKey = ` + tPub + `
PresharedKey = ` + tPSK + `
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = gb-lon-wg-001.example.net:51820
PersistentKeepalive = 25
`

func TestParseValidateRoundTrip(t *testing.T) {
	c, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	c.Name = "Mullvad London"
	if err := c.Validate(lan); err != nil {
		t.Fatal(err)
	}
	if len(c.Ignored) != 2 || !strings.HasPrefix(c.Ignored[0], "DNS search domain") || !strings.HasPrefix(c.Ignored[1], "PostUp") {
		t.Fatalf("ignored = %q", c.Ignored)
	}
	if c.Engine != EngineUserspace || c.Route != RouteLAN || !c.KillSwitch {
		t.Fatalf("defaults: %+v", c)
	}
	if got := c.allowedV4(); len(got) != 1 || got[0].String() != "0.0.0.0/0" {
		t.Fatalf("allowedV4 = %v", got)
	}
	c.Engine, c.Route, c.KillSwitch = EngineKernel, RouteAll, false
	out := string(c.Marshal())
	if strings.Contains(out, "PostUp") || strings.Contains(out, "corp.example") {
		t.Fatalf("hook or search domain kept:\n%s", out)
	}
	c2, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	c2.Name = c.Name
	if err := c2.Validate(lan); err != nil {
		t.Fatal(err)
	}
	if c2.Engine != EngineKernel || c2.Route != RouteAll || c2.KillSwitch || c2.MTU != 1380 ||
		c2.Peers[0].PresharedKey != tPSK || c2.Peers[0].Keepalive != 25 || c2.PrivateKey != tPriv {
		t.Fatalf("round trip lost data: %+v", c2)
	}
}

func TestValidateErrors(t *testing.T) {
	base := func() *Config {
		c, _ := Parse(sample)
		c.Name = "x"
		return c
	}
	cases := map[string]func(c *Config){
		"name":       func(c *Config) { c.Name = "../etc" },
		"private":    func(c *Config) { c.PrivateKey = "abc" },
		"lan":        func(c *Config) { c.Addresses = []string{"192.168.225.7/32"} },
		"v6 only":    func(c *Config) { c.Addresses = []string{"fd00::2/128"} },
		"mtu":        func(c *Config) { c.MTU = 9000 },
		"no peer":    func(c *Config) { c.Peers = nil },
		"public":     func(c *Config) { c.Peers[0].PublicKey = "nope" },
		"endpoint":   func(c *Config) { c.Peers[0].Endpoint = "host-without-port" },
		"no ep":      func(c *Config) { c.Peers[0].Endpoint = "" },
		"allowed":    func(c *Config) { c.Peers[0].AllowedIPs = []string{"10.0.0.0/33"} },
		"no allowed": func(c *Config) { c.Peers[0].AllowedIPs = nil },
		"bad host":   func(c *Config) { c.Peers[0].Endpoint = "a_b;rm:51820" },
		"own key": func(c *Config) {
			k, _ := wgtypes.ParseKey(tPriv)
			c.Peers[0].PublicKey = k.PublicKey().String()
		},
	}
	for n, f := range cases {
		c := base()
		f(c)
		if err := c.Validate(lan); err == nil {
			t.Errorf("%s: expected an error", n)
		}
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "hello", "[Interface]\nFoo = bar\n", "PrivateKey = x\n", "[Wat]\n"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("%q: expected an error", s)
		}
	}
	if _, err := Parse(strings.Repeat("#", MaxConfigBytes+1)); err == nil {
		t.Error("oversized file accepted")
	}
}

func TestNameFromFilename(t *testing.T) {
	for in, want := range map[string]string{
		"Mullvad gb-lon 1.conf": "Mullvad gb-lon 1",
		"proton(UK#3).conf":     "proton-UK-3",
		"..conf":                "",
		"wg0.conf":              "wg0",
	} {
		if got := NameFromFilename(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestStore(t *testing.T) {
	d := t.TempDir()
	Dir, ConfigDir, ActiveFile = d, filepath.Join(d, "configs"), filepath.Join(d, "ACTIVE")
	c, _ := Parse(sample)
	c.Name = "Home"
	if err := c.Validate(lan); err != nil {
		t.Fatal(err)
	}
	if err := Save(c); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(ConfigDir, "Home.conf"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode: %v %v", fi, err)
	}
	if !Exists("home") || len(List()) != 1 {
		t.Fatal("list/exists")
	}
	got, err := Load("Home")
	if err != nil || got.PrivateKey != tPriv {
		t.Fatalf("load: %v", err)
	}
	if err := SetActive("Home"); err != nil || Active() != "Home" {
		t.Fatal("active")
	}
	if err := SetActive(""); err != nil || Active() != "" {
		t.Fatal("clear active")
	}
	if err := Delete("Home"); err != nil || Exists("Home") {
		t.Fatal("delete")
	}
	if _, err := Load("Home"); err != ErrNotFound {
		t.Fatalf("load deleted: %v", err)
	}
}

func TestUsesCGNAT(t *testing.T) {
	c, _ := Parse(sample)
	if c.UsesCGNAT() {
		t.Fatal("10.64.x flagged")
	}
	c.Addresses = []string{"100.64.44.189/32"}
	if !c.UsesCGNAT() {
		t.Fatal("100.64.44.189 not flagged")
	}
	c.Addresses, c.DNS = []string{"10.2.0.2/32"}, []string{"100.64.0.7"}
	if !c.UsesCGNAT() {
		t.Fatal("CGNAT DNS not flagged")
	}
}
