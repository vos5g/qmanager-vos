package daemon

// vosclean: two "why is it slow / why no 5G" answers from Compal's AT
// commands (passed straight to the modem by qcmd).
//
// AT+CEISAR? reports the SAR (body-exposure) transmit-power reduction level
// as "SAR DSI = N" (0-20, 0 = full power). On this dongle Tri Cascade's
// startup script sets level 3 at boot, and the ceisar service sets level 4
// for 10 s whenever the built-in accelerometer feels movement, then 0. So a
// stick nobody has touched since boot stays at reduced power. Read every
// sarInterval so the 10 s reductions are visible.
//
// AT+5GDISP? reports whether the serving LTE cell offers 5G (NSA):
//   Upperlayer Ind:<0|1>               cell broadcasts upperLayerIndication (5G available)
//   RestrictDCNR:<0|1>                 network forbids 5G (dual connectivity) for this SIM
//   ENDC ENABLED on the device:<0|1>   5G NSA allowed by the modem's settings
//   Serving network:LTE
// Read every nrAvailInterval; it only changes with the serving cell.

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

type sarState struct {
	Level int `json:"level"` // DSI index; 0 = full transmit power
}

type nrAvailability struct {
	UpperLayerInd bool   `json:"upper_layer_ind"` // the LTE cell advertises 5G
	RestrictDCNR  bool   `json:"restrict_dcnr"`   // the network blocks 5G for this SIM
	ENDCEnabled   bool   `json:"endc_enabled"`    // 5G NSA allowed on the device
	Serving       string `json:"serving"`         // "LTE", "NR5G" ...
}

const (
	sarInterval     = 10 * time.Second
	nrAvailInterval = 30 * time.Second
)

var (
	sarLast  *sarState
	sarAt    time.Time
	nrLast   *nrAvailability
	nrLastAt time.Time

	reSAR    = regexp.MustCompile(`SAR\s+DSI\s*=\s*(\d+)`)
	re5GDisp = regexp.MustCompile(`(?m)^\s*([A-Za-z ]+?)\s*:\s*(.*?)\s*$`)
)

func parseSAR(resp string) *sarState {
	m := reSAR.FindStringSubmatch(resp)
	if m == nil {
		return nil
	}
	v, err := strconv.Atoi(m[1])
	if err != nil {
		return nil
	}
	return &sarState{Level: v}
}

func parse5GDisp(resp string) *nrAvailability {
	if !strings.Contains(resp, "Upperlayer Ind") {
		return nil
	}
	na := &nrAvailability{}
	for _, m := range re5GDisp.FindAllStringSubmatch(resp, -1) {
		val := strings.TrimSpace(m[2])
		switch strings.ToLower(m[1]) {
		case "upperlayer ind":
			na.UpperLayerInd = val == "1"
		case "restrictdcnr":
			na.RestrictDCNR = val == "1"
		case "endc enabled on the device":
			na.ENDCEnabled = val == "1"
		case "serving network":
			na.Serving = val
		}
	}
	return na
}

// addRadioInfo adds device.sar and device.nr_availability to the status file.
func (p *Poller) addRadioInfo(device map[string]interface{}, now time.Time) {
	hwMu.Lock()
	defer hwMu.Unlock()
	if sarLast == nil || now.Sub(sarAt) >= sarInterval {
		sarAt = now
		if resp, err := p.atClient.Exec("AT+CEISAR?"); err == nil && !strings.Contains(resp, "ERROR") {
			if st := parseSAR(resp); st != nil {
				sarLast = st
			}
		}
	}
	if nrLast == nil || now.Sub(nrLastAt) >= nrAvailInterval {
		nrLastAt = now
		if resp, err := p.atClient.Exec("AT+5GDISP?"); err == nil && !strings.Contains(resp, "ERROR") {
			if na := parse5GDisp(resp); na != nil {
				nrLast = na
			}
		}
	}
	if sarLast != nil {
		device["sar"] = sarLast
	} else {
		device["sar"] = nil
	}
	if nrLast != nil {
		device["nr_availability"] = nrLast
	} else {
		device["nr_availability"] = nil
	}
}
