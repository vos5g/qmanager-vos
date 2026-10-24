package daemon

// vosclean: module details and thermal throttling from Compal's own AT
// commands (qcmd passes them straight to the modem).
//
// AT+PRODUCTINFO names the module SKU and its RF configuration. MMW_RFC_ID 0
// means no mmWave RF card is loaded: the module has the mm0-mm2 ports and the
// firmware has the mmWave code, but no antenna modules are fitted, so the
// factory left mmWave off. The reply also carries the IMEI, IMSI and serial
// numbers; only the fields below are kept.
//
// AT+CEITHERM? gives the modem's thermal mitigation level per sensor
// ("3.modem level:0"). 0 is full speed; higher levels mean the modem is
// cutting power or throughput to cool down. The mmw* sensors only matter when
// mmWave is fitted.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type productInfo struct {
	Model     string // VOS5-GC-1
	SKU       string // RXM-G1 BR23
	HWVersion string
	RFCID     int
	MMWRFCID  int
}

type throttleSensor struct {
	Name  string `json:"name"`
	Level int    `json:"level"`
}

type throttleState struct {
	Level   int              `json:"level"`   // highest level among the sensors that count
	Source  string           `json:"source"`  // sensor with that level ("" at 0)
	Sensors []throttleSensor `json:"sensors"` // every sensor the modem reports
}

const throttleInterval = 30 * time.Second

var (
	hwMu         sync.Mutex
	hwProduct    *productInfo
	hwProductTry time.Time
	hwThrottle   *throttleState
	hwThrottleAt time.Time

	reProductLine = regexp.MustCompile(`^\s*([A-Za-z_ 0-9]+?)\s*:\s*(.*?)\s*$`)
	reThermLine   = regexp.MustCompile(`^\s*\d+\.\s*([A-Za-z0-9_]+)\s+level\s*:\s*(\d+)`)
)

func parseProductInfo(resp string) *productInfo {
	if !strings.Contains(resp, "RFC_ID") && !strings.Contains(resp, "SKU") {
		return nil
	}
	pi := &productInfo{RFCID: -1, MMWRFCID: -1}
	for _, line := range strings.Split(resp, "\n") {
		m := reProductLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		val := strings.TrimSpace(m[2])
		switch strings.ToUpper(strings.TrimSpace(m[1])) {
		case "MODEL":
			pi.Model = val
		case "SKU":
			pi.SKU = val
		case "HW_VERSION":
			pi.HWVersion = val
		case "RFC_ID":
			if v, err := strconv.Atoi(val); err == nil {
				pi.RFCID = v
			}
		case "MMW_RFC_ID":
			if v, err := strconv.Atoi(val); err == nil {
				pi.MMWRFCID = v
			}
		}
	}
	return pi
}

func parseThrottle(resp string, mmwFitted bool) *throttleState {
	st := &throttleState{Sensors: []throttleSensor{}}
	for _, line := range strings.Split(resp, "\n") {
		m := reThermLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		lvl, _ := strconv.Atoi(m[2])
		name := strings.ToLower(m[1])
		st.Sensors = append(st.Sensors, throttleSensor{Name: name, Level: lvl})
		if strings.HasPrefix(name, "mmw") && !mmwFitted {
			continue
		}
		if lvl > st.Level {
			st.Level, st.Source = lvl, name
		}
	}
	if len(st.Sensors) == 0 {
		return nil
	}
	return st
}

// product reads AT+PRODUCTINFO once (it never changes), retrying a failed
// read at most once a minute.
func (p *Poller) product() *productInfo {
	hwMu.Lock()
	defer hwMu.Unlock()
	if hwProduct != nil || time.Since(hwProductTry) < time.Minute {
		return hwProduct
	}
	hwProductTry = time.Now()
	if resp, err := p.atClient.Exec("AT+PRODUCTINFO"); err == nil {
		hwProduct = parseProductInfo(resp)
	}
	return hwProduct
}

func (pi *productInfo) mmwFitted() bool { return pi != nil && pi.MMWRFCID > 0 }

// throttle reads AT+CEITHERM? every throttleInterval and returns the last
// reading (nil until the modem has answered once).
func (p *Poller) throttle(now time.Time) *throttleState {
	pi := p.product()
	hwMu.Lock()
	defer hwMu.Unlock()
	if hwThrottle != nil && now.Sub(hwThrottleAt) < throttleInterval {
		return hwThrottle
	}
	hwThrottleAt = now
	if resp, err := p.atClient.Exec("AT+CEITHERM?"); err == nil && !strings.Contains(resp, "ERROR") {
		if st := parseThrottle(resp, pi.mmwFitted()); st != nil {
			hwThrottle = st
		}
	}
	return hwThrottle
}

// addHardwareInfo adds the module, mmWave and throttling fields to the
// device block of the status file, and records throttling changes as events.
func (p *Poller) addHardwareInfo(device map[string]interface{}, now int64) {
	pi := p.product()
	if pi != nil {
		device["module_sku"] = pi.SKU
		device["hw_version"] = pi.HWVersion
		device["mmwave"] = map[string]interface{}{
			"fitted":      pi.mmwFitted(),
			"rfc_id":      pi.MMWRFCID,
			"sub6_rfc_id": pi.RFCID,
		}
	}
	st := p.throttle(time.Unix(now, 0))
	if st == nil {
		device["thermal_throttle"] = nil
		return
	}
	device["thermal_throttle"] = st

	p.evMu.Lock()
	defer p.evMu.Unlock()
	prev := p.lastThrottle
	p.lastThrottle = st.Level
	switch {
	case prev < 0: // first reading
	case st.Level > 0 && prev == 0:
		p.recordEvent(now, "thermal_throttle", "warning",
			fmt.Sprintf("Modem is throttling to cool down (level %d, %s)", st.Level, sensorLabel(st.Source)))
	case st.Level > prev:
		p.recordEvent(now, "thermal_throttle", "warning",
			fmt.Sprintf("Throttling increased to level %d (%s)", st.Level, sensorLabel(st.Source)))
	case st.Level == 0 && prev > 0:
		p.recordEvent(now, "thermal_throttle", "info", "Throttling ended, back to full speed")
	}
}

func sensorLabel(name string) string {
	switch name {
	case "pa":
		return "power amplifier"
	case "pa_fr1":
		return "5G power amplifier"
	case "modem":
		return "modem chip"
	}
	if strings.HasPrefix(name, "mmw") {
		return "mmWave " + strings.TrimPrefix(name, "mmw")
	}
	return name
}
