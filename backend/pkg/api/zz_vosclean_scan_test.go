package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
)

func TestCellScanStatusShape(t *testing.T) {
	fixture := "+QSCAN: \"NR5G\",234,10,648768,101,-80,-11,40,30,1A2B3C4D5,3039,-,78\n+QSCAN: \"LTE\",234,10,347,201,-88,-14,-,-,1234567,1F4,-,1\n+QSCAN: \"LTE\",234,10,3725,202,-99,-13,-,-,-,-,-,8\n\nOK"
	if err := os.WriteFile("/tmp/qmanager_scan_results.txt", []byte(fixture), 0644); err != nil {
		t.Skip("cannot write fixture")
	}
	defer os.Remove("/tmp/qmanager_scan_results.txt")
	s := &Server{}
	rec := httptest.NewRecorder()
	s.HandleCellScanStatus(rec, httptest.NewRequest("GET", "/x", nil))
	var out struct {
		Status  string           `json:"status"`
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s", rec.Body.String())
	if out.Status != "complete" || len(out.Results) != 3 {
		t.Fatalf("status=%s n=%d", out.Status, len(out.Results))
	}
	nr := out.Results[0]
	if nr["networkType"] != "NR5G" || nr["earfcn"].(float64) != 648768 || nr["signalStrength"].(float64) != -80 || nr["provider"] != "O2 UK" || nr["band"].(float64) != 78 || nr["tac"].(float64) != 12345 {
		t.Fatalf("nr row %v", nr)
	}
}
