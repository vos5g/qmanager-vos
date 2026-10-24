package daemon

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Replies as the stick gives them; identifiers replaced with placeholders.
const productInfoReply = "Model:VOS5-GC-1\r\nSW_VERSION: RXMG1.20.00.326_0R05\r\nRFC_ID: 681\r\nMMW_RFC_ID: 0\r\n" +
	"SN number: 000000000\r\nIMEI: 000000000000000\r\nIMSI: 000000000000000\r\nHW_VERSION: 1.0\r\n" +
	"SN number2: 000000000\r\nQCN_VERSION:ZX56_681_0.92-12\r\nSKU: RXM-G1 BR23\r\n\r\nOK\r\n"

func thermReply(pa, modem, mmw0 int) string {
	return strings.Join([]string{
		"1.pa level:" + itoa(pa), "2.pa_fr1 level:0", "3.modem level:" + itoa(modem),
		"4.mmw0 level:" + itoa(mmw0), "5.mmw1 level:0", "6.mmw2 level:0", "7.mmw3 level:0", "", "OK",
	}, "\r\n")
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestParseProductInfo(t *testing.T) {
	pi := parseProductInfo(productInfoReply)
	if pi == nil || pi.Model != "VOS5-GC-1" || pi.SKU != "RXM-G1 BR23" || pi.RFCID != 681 || pi.MMWRFCID != 0 || pi.HWVersion != "1.0" {
		t.Fatalf("got %+v", pi)
	}
	if pi.mmwFitted() {
		t.Fatal("MMW_RFC_ID 0 reported as fitted")
	}
	if parseProductInfo("ERROR") != nil {
		t.Fatal("ERROR parsed")
	}
}

func TestParseThrottle(t *testing.T) {
	st := parseThrottle(thermReply(0, 0, 0), false)
	if st == nil || st.Level != 0 || st.Source != "" || len(st.Sensors) != 7 {
		t.Fatalf("idle: %+v", st)
	}
	st = parseThrottle(thermReply(1, 2, 3), false)
	if st.Level != 2 || st.Source != "modem" {
		t.Fatalf("mmw counted while not fitted: %+v", st)
	}
	st = parseThrottle(thermReply(1, 2, 3), true)
	if st.Level != 3 || st.Source != "mmw0" {
		t.Fatalf("fitted: %+v", st)
	}
	if parseThrottle("ERROR", false) != nil {
		t.Fatal("ERROR parsed")
	}
}

type scriptAT struct{ replies map[string]string }

func (s *scriptAT) Exec(cmd string) (string, error) { return s.replies[cmd], nil }

func TestAddHardwareInfoEvents(t *testing.T) {
	hwProduct, hwThrottle = nil, nil
	hwProductTry, hwThrottleAt = time.Time{}, time.Time{}
	ev := "/tmp/qmanager_events.json"
	old, hadOld := os.ReadFile(ev)
	defer func() {
		if hadOld == nil {
			_ = os.WriteFile(ev, old, 0644)
		} else {
			_ = os.Remove(ev)
		}
	}()
	_ = os.Remove(ev)

	at := &scriptAT{replies: map[string]string{"AT+PRODUCTINFO": productInfoReply, "AT+CEITHERM?": thermReply(0, 0, 0)}}
	p := NewPoller(at, time.Second)
	base := time.Now().Unix()
	step := func(i int, reply string) map[string]interface{} {
		at.replies["AT+CEITHERM?"] = reply
		d := map[string]interface{}{}
		p.addHardwareInfo(d, base+int64(i)*int64(throttleInterval/time.Second))
		return d
	}
	d := step(0, thermReply(0, 0, 0))
	if d["module_sku"] != "RXM-G1 BR23" || d["mmwave"].(map[string]interface{})["fitted"] != false {
		t.Fatalf("device: %v", d)
	}
	for k := range d {
		if strings.Contains(strings.ToLower(k), "imei") || strings.Contains(strings.ToLower(k), "sn") {
			t.Fatalf("identifier leaked as %q", k)
		}
	}
	step(1, thermReply(0, 2, 0))
	step(2, thermReply(0, 2, 0))
	step(3, thermReply(0, 0, 0))
	step(4, thermReply(0, 0, 3)) // mmWave not fitted: ignored
	b, _ := os.ReadFile(ev)
	got := string(b)
	if strings.Count(got, `"thermal_throttle"`) != 2 || !strings.Contains(got, "level 2, modem chip") || !strings.Contains(got, "back to full speed") {
		t.Fatalf("events:\n%s", got)
	}
}
