package api

// vosclean: real values for the About Device page on the VOS 5G / SG500M2-X.
// Upstream returns placeholders (device IP 127.0.0.1, WAN "-", kernel
// "Linux Host"). Everything here is read locally, except the public IP, which
// is looked up through Cloudflare's trace endpoint, only when the page is
// opened, and cached for ten minutes.

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

type vosAboutInfo struct {
	firmware, buildDate    string
	deviceIP, lanSubnet    string
	wanIPv4, wanIPv6       string
	publicIPv4, publicIPv6 string
	kernel, osVersion      string
	lteRelease, nrRelease  string
}

var (
	vosPublicMu   sync.Mutex
	vosPublicV4   string
	vosPublicV6   string
	vosPublicTime time.Time
)

func vosReadTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// "202412101209" -> "2024-12-10 12:09"
func vosFormatStamp(s string) string {
	if len(s) >= 12 && regexp.MustCompile(`^\d{12}`).MatchString(s) {
		return s[0:4] + "-" + s[4:6] + "-" + s[6:8] + " " + s[8:10] + ":" + s[10:12]
	}
	return s
}

func vosIfaceAddrs(name string) (v4 []*net.IPNet, v6 []*net.IPNet) {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return nil, nil
	}
	addrs, _ := ifc.Addrs()
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || !ipn.IP.IsGlobalUnicast() {
			continue
		}
		if ipn.IP.To4() != nil {
			v4 = append(v4, ipn)
		} else if !strings.HasPrefix(ipn.IP.String(), "fd7a:115c:a1e0:") { // skip Tailscale ULA
			v6 = append(v6, ipn)
		}
	}
	return v4, v6
}

func vosCloudflareTrace(ctx context.Context, url string) string {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return ""
	}
	resp, err := (&http.Client{Timeout: 4 * time.Second}).Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 4096))
	for sc.Scan() {
		if ip, ok := strings.CutPrefix(sc.Text(), "ip="); ok {
			if net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	return ""
}

func vosPublicIPs() (string, string) {
	vosPublicMu.Lock()
	defer vosPublicMu.Unlock()
	if time.Since(vosPublicTime) < 10*time.Minute {
		return vosPublicV4, vosPublicV6
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	var v4, v6 string
	wg.Add(2)
	go func() { defer wg.Done(); v4 = vosCloudflareTrace(ctx, "https://1.1.1.1/cdn-cgi/trace") }()
	go func() { defer wg.Done(); v6 = vosCloudflareTrace(ctx, "https://[2606:4700:4700::1111]/cdn-cgi/trace") }()
	wg.Wait()
	vosPublicV4, vosPublicV6, vosPublicTime = v4, v6, time.Now()
	return v4, v6
}

func dashIfEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func vosAbout(basebandRev string) vosAboutInfo {
	info := vosAboutInfo{lteRelease: "Rel 15", nrRelease: "Rel 15"} // Snapdragon X55: 3GPP Release 15

	// Firmware: vendor package version (e.g. RXMG1.20.00.326.73_0R19) plus the baseband revision from ATI
	pkg := vosReadTrim("/oem/cei_module_ver_vos_id")
	if pkg == "" {
		pkg = vosReadTrim("/oem/cei_module_ver_id")
	}
	bb := strings.TrimSpace(strings.TrimPrefix(basebandRev, "Revision:"))
	switch {
	case pkg != "" && bb != "" && bb != "-" && bb != pkg:
		info.firmware = pkg + " (baseband " + bb + ")"
	case pkg != "":
		info.firmware = pkg
	default:
		info.firmware = bb
	}
	info.buildDate = vosFormatStamp(vosReadTrim("/etc/version"))

	// LAN side: the USB bridge
	if v4, _ := vosIfaceAddrs("bridge0"); len(v4) > 0 {
		info.deviceIP = v4[0].IP.String()
		info.lanSubnet = (&net.IPNet{IP: v4[0].IP.Mask(v4[0].Mask), Mask: v4[0].Mask}).String()
	}
	// WAN side: the cellular data interfaces
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if !strings.HasPrefix(ifc.Name, "rmnet_data") {
			continue
		}
		v4, v6 := vosIfaceAddrs(ifc.Name)
		if info.wanIPv4 == "" && len(v4) > 0 {
			info.wanIPv4 = v4[0].IP.String()
		}
		if info.wanIPv6 == "" && len(v6) > 0 {
			info.wanIPv6 = v6[0].IP.String()
		}
	}
	info.publicIPv4, info.publicIPv6 = vosPublicIPs()

	// Kernel and OS
	kernel := vosReadTrim("/proc/sys/kernel/osrelease")
	if built := vosKernelBuildDate(vosReadTrim("/proc/version")); built != "" {
		kernel += " (built " + built + ")"
	}
	info.kernel = kernel
	osName := ""
	if f, err := os.Open("/etc/os-release"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
				osName = strings.Trim(v, `"`)
			}
		}
		f.Close()
	}
	if osName != "" {
		info.osVersion = "Qualcomm LE Linux (" + osName + ")"
	}
	return info
}

var vosKernelStampRe = regexp.MustCompile(`#\d+ [A-Z ]*?(\w{3} \w{3} +\d+ [\d:]+ \w+ \d{4})`)

// vosKernelBuildDate extracts the build date from /proc/version, e.g.
// "... #1 PREEMPT Tue Dec 10 04:37:41 UTC 2024" -> "2024-12-10".
func vosKernelBuildDate(procVersion string) string {
	m := vosKernelStampRe.FindStringSubmatch(procVersion)
	if m == nil {
		return ""
	}
	t, err := time.Parse("Mon Jan 2 15:04:05 MST 2006", strings.Join(strings.Fields(m[1]), " "))
	if err != nil {
		return ""
	}
	return t.Format("2006-01-02")
}
