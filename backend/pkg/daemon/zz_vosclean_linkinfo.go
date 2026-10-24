package daemon

// vosclean: LTE link details from Compal's AT+BINFO (passed through by qcmd).
//
// AT+BINFO prints, besides the serving cell and per-antenna levels:
//   RX0 rsrp: -105 dBm ... RX3 rsrp: -256 dBm     (-256 = that receive chain is idle)
//   TX POWER INFO:
//   tx_pwr: 21 dBm,pa_gain_state 0
//   uplink modulation num_layer  1 num_mimo_layers 0 num_config_layers 0
//   uplink modulation layer 0  16QAM
//   downlink modulation num_layer  1 num_mimo_layers 1 num_config_layers 2
//   downlink modulation layer 0  64QAM
//   PCC CQI 12 0
// These explain slow uploads (transmit power near the 23 dBm maximum, low
// uplink modulation) and slow downloads (low CQI, one layer) better than
// signal strength does. They also replace the hard-coded "4x4" MIMO label
// with what the modem is really doing: "LTE <layers>x<receive chains>".

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type lteLink struct {
	TxPower      *int   `json:"tx_power"`      // dBm, the UE's current transmit power
	ULModulation string `json:"ul_modulation"` // e.g. "16QAM"
	DLModulation string `json:"dl_modulation"` // modulation of the first downlink layer
	DLLayers     int    `json:"dl_layers"`     // layers in use now (MIMO rank)
	DLConfLayers int    `json:"dl_conf_layers"`
	CQI          *int   `json:"cqi"`       // channel quality indicator, codeword 0 (0-15)
	RxChains     int    `json:"rx_chains"` // receive chains with a signal
	RxRSRP       []*int `json:"rx_rsrp"`   // per chain, nil when idle
}

const linkInterval = 10 * time.Second

var (
	linkLast *lteLink
	linkAt   time.Time

	reTxPwr  = regexp.MustCompile(`tx_pwr:\s*(-?\d+)\s*dBm`)
	reULMod  = regexp.MustCompile(`uplink modulation layer 0\s+(\S+)`)
	reDLMod  = regexp.MustCompile(`downlink modulation layer 0\s+(\S+)`)
	reDLLay  = regexp.MustCompile(`downlink modulation num_layer\s+(\d+)\s+num_mimo_layers\s+(\d+)\s+num_config_layers\s+(\d+)`)
	reCQI    = regexp.MustCompile(`PCC CQI\s+(\d+)`)
	reRxRSRP = regexp.MustCompile(`RX(\d) rsrp:\s*(-?\d+)\s*dBm`)
)

func parseBINFOLink(resp string) *lteLink {
	if !strings.Contains(resp, "LTE PCC INFO") {
		return nil // not on LTE (or the modem didn't answer)
	}
	l := &lteLink{RxRSRP: make([]*int, 4)}
	if m := reTxPwr.FindStringSubmatch(resp); m != nil {
		if v, err := strconv.Atoi(m[1]); err == nil {
			l.TxPower = &v
		}
	}
	if m := reULMod.FindStringSubmatch(resp); m != nil {
		l.ULModulation = m[1]
	}
	if m := reDLMod.FindStringSubmatch(resp); m != nil {
		l.DLModulation = m[1]
	}
	if m := reDLLay.FindStringSubmatch(resp); m != nil {
		l.DLLayers, _ = strconv.Atoi(m[2])
		l.DLConfLayers, _ = strconv.Atoi(m[3])
		if l.DLLayers == 0 { // single layer is reported as num_layer 1, num_mimo_layers 0
			l.DLLayers, _ = strconv.Atoi(m[1])
		}
	}
	if m := reCQI.FindStringSubmatch(resp); m != nil {
		if v, err := strconv.Atoi(m[1]); err == nil {
			l.CQI = &v
		}
	}
	for _, m := range reRxRSRP.FindAllStringSubmatch(resp, -1) {
		i, _ := strconv.Atoi(m[1])
		v, err := strconv.Atoi(m[2])
		if err != nil || i < 0 || i > 3 {
			continue
		}
		if v > -200 { // -256 marks an idle chain
			vv := v
			l.RxRSRP[i] = &vv
			l.RxChains++
		}
	}
	// The modem reports modulation of an idle layer as BPSK; don't show that.
	if l.DLLayers == 0 {
		l.DLModulation = ""
	}
	return l
}

// mimoLabel is "LTE <layers>x<receive chains>", the format the UI expects.
func (l *lteLink) mimoLabel() string {
	if l == nil || l.RxChains == 0 || l.DLLayers == 0 {
		return ""
	}
	return fmt.Sprintf("LTE %dx%d", l.DLLayers, l.RxChains)
}

// addLinkInfo reads AT+BINFO every linkInterval while on LTE and adds the
// link details to the lte block, the per-antenna RSRP and the MIMO label.
func (p *Poller) addLinkInfo(status map[string]interface{}, now time.Time) {
	lte, _ := status["lte"].(map[string]interface{})
	if lte == nil {
		return
	}
	hwMu.Lock()
	if linkLast == nil || now.Sub(linkAt) >= linkInterval {
		linkAt = now
		linkLast = nil
		if resp, err := p.atClient.Exec("AT+BINFO"); err == nil && !strings.Contains(resp, "ERROR") {
			linkLast = parseBINFOLink(resp)
		}
	}
	l := linkLast
	hwMu.Unlock()
	if l == nil {
		return
	}
	lte["tx_power"] = l.TxPower
	lte["ul_modulation"] = l.ULModulation
	lte["dl_modulation"] = l.DLModulation
	lte["dl_layers"] = l.DLLayers
	lte["dl_conf_layers"] = l.DLConfLayers
	lte["cqi"] = l.CQI
	lte["rx_chains"] = l.RxChains
	if lbl := l.mimoLabel(); lbl != "" {
		if dev, ok := status["device"].(map[string]interface{}); ok {
			dev["mimo"] = lbl
		}
	}
}
