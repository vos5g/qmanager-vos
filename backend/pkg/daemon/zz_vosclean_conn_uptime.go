package daemon

// vosclean: connection uptime for the dashboard.
//
// Upstream sent conn_uptime_seconds = 0. This modem has no readable call
// start time: QMI WDS "Get Call Duration" answers "out of call" (error 15) for
// any client but the one that started the call (QCMAP), and nothing on disk
// is rewritten at connect. So QManager times it: when the mobile data
// interface first has an IPv4 address, it stores the moment as seconds since
// boot in /tmp (so a QManager restart keeps the count and NTP clock jumps
// can't skew it), and clears it when the address goes away. QManager starts
// within about 20 s of the modem connecting after boot, so that is the error
// margin on the very first connection.

import (
	"net"
	"os"
	"strconv"
	"strings"
)

const connSinceFile = "/tmp/qmanager_conn_since"

// mobileDataUp: an rmnet_data* interface has a global IPv4 address.
func mobileDataUp() bool {
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, ifc := range ifaces {
		if !strings.HasPrefix(ifc.Name, "rmnet_data") || ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && ipn.IP.IsGlobalUnicast() {
				return true
			}
		}
	}
	return false
}

func bootID() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
}

// connUptime returns how long mobile data has been up, given whether it is up
// now and the seconds since boot. It reads and updates the state file.
func connUptime(up bool, nowUptime int, stateFile, boot string) int {
	if !up {
		_ = os.Remove(stateFile)
		return 0
	}
	if b, err := os.ReadFile(stateFile); err == nil {
		f := strings.Fields(string(b))
		if len(f) == 2 && f[0] == boot {
			if since, err := strconv.Atoi(f[1]); err == nil && since <= nowUptime {
				return nowUptime - since
			}
		}
	}
	_ = os.WriteFile(stateFile, []byte(boot+" "+strconv.Itoa(nowUptime)+"\n"), 0644)
	return 0
}

func connUptimeSeconds(wanIP string) int {
	up := mobileDataUp() || (wanIP != "" && wanIP != "0.0.0.0")
	return connUptime(up, getSystemUptimeSec(), connSinceFile, bootID())
}
