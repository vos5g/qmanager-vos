package daemon

import (
	"testing"
	"time"
)

func TestParseSARAnd5GDisp(t *testing.T) {
	if s := parseSAR("SAR DSI = 3\r\nOK\r\n"); s == nil || s.Level != 3 {
		t.Fatalf("sar: %+v", s)
	}
	if parseSAR("ERROR") != nil {
		t.Fatal("ERROR parsed")
	}
	na := parse5GDisp("Upperlayer Ind:0\r\nRestrictDCNR:0\r\nENDC ENABLED on the device:1\r\nServing network:LTE \r\nOK\r\n")
	if na == nil || na.UpperLayerInd || na.RestrictDCNR || !na.ENDCEnabled || na.Serving != "LTE" {
		t.Fatalf("5gdisp: %+v", na)
	}
	if na := parse5GDisp("Upperlayer Ind:1\nRestrictDCNR:1\nENDC ENABLED on the device:0\nServing network:LTE\nOK"); !na.UpperLayerInd || !na.RestrictDCNR || na.ENDCEnabled {
		t.Fatalf("5gdisp flags: %+v", na)
	}
}

func TestAddRadioInfoIntervals(t *testing.T) {
	sarLast, nrLast = nil, nil
	at := &scriptAT{replies: map[string]string{
		"AT+CEISAR?": "SAR DSI = 3\r\nOK",
		"AT+5GDISP?": "Upperlayer Ind:1\r\nRestrictDCNR:0\r\nENDC ENABLED on the device:1\r\nServing network:LTE\r\nOK",
	}}
	p := NewPoller(at, time.Second)
	t0 := time.Now()
	d := map[string]interface{}{}
	p.addRadioInfo(d, t0)
	if d["sar"].(*sarState).Level != 3 || !d["nr_availability"].(*nrAvailability).UpperLayerInd {
		t.Fatalf("first: %v", d)
	}
	at.replies["AT+CEISAR?"] = "SAR DSI = 0\r\nOK"
	p.addRadioInfo(d, t0.Add(5*time.Second)) // within sarInterval: cached
	if d["sar"].(*sarState).Level != 3 {
		t.Fatal("re-read before the interval")
	}
	p.addRadioInfo(d, t0.Add(11*time.Second))
	if d["sar"].(*sarState).Level != 0 {
		t.Fatal("not re-read after the interval")
	}
}
