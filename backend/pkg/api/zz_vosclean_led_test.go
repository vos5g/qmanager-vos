package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLEDOffOnAndSchedule(t *testing.T) {
	dir := t.TempDir()
	old := []string{ledConfFile, ledSavedFile, ledRegmap, ledGPIO}
	oldSvc, oldActive, oldState := ledService, ledServiceActive, leds
	t.Cleanup(func() {
		ledConfFile, ledSavedFile, ledRegmap, ledGPIO = old[0], old[1], old[2], old[3]
		ledService, ledServiceActive, leds = oldSvc, oldActive, oldState
	})
	ledConfFile = filepath.Join(dir, "led.json")
	ledSavedFile = filepath.Join(dir, "saved.json")
	ledRegmap = filepath.Join(dir, "regmap")
	ledGPIO = filepath.Join(dir, "gpio15")
	os.WriteFile(ledGPIO, []byte("0\n"), 0644)

	// debugfs regmap selects a register by writing "address" and reads it back
	// as "c240: 01" from "data". Plain files can't do that by themselves, so
	// mirror() copies between the data files and this map before each request.
	regs := map[string]string{"spmi0-08 0xC240": "01", "spmi0-09 0xbd46": "80"}
	for _, r := range ledRegs {
		os.MkdirAll(filepath.Join(ledRegmap, r.Dev), 0755)
	}
	mirror := func() {
		for _, r := range ledRegs {
			base := filepath.Join(ledRegmap, r.Dev)
			a, _ := os.ReadFile(base + "/address")
			k := r.Dev + " " + string(a)
			if d, err := os.ReadFile(base + "/data"); err == nil && strings.HasPrefix(string(d), "0x") {
				regs[k] = strings.TrimPrefix(string(d), "0x")
				if len(regs[k]) == 1 {
					regs[k] = "0" + regs[k]
				}
			}
			os.WriteFile(base+"/data", []byte(strings.ToLower(strings.TrimPrefix(string(a), "0x"))+": "+regs[k]), 0644)
		}
	}
	svc := []string{}
	ledService = func(a string) error { svc = append(svc, a); return nil }
	ledServiceActive = func() bool { return len(svc) == 0 || svc[len(svc)-1] == "start" }

	// regRead writes address then reads data: prime data for each register.
	readAll := func() {
		for _, r := range ledRegs {
			os.WriteFile(filepath.Join(ledRegmap, r.Dev, "address"), []byte(r.Addr), 0644)
		}
		mirror()
	}
	readAll()
	v, err := regRead(ledRegs[0])
	if err != nil || v != "0x01" {
		t.Fatalf("regRead %q %v", v, err)
	}

	leds = &ledState{conf: ledConf{Mode: "on", OffFrom: "23:00", OffTo: "07:00"}}
	post := func(body string) map[string]any {
		readAll()
		rr := httptest.NewRecorder()
		(&Server{}).HandleLED(rr, httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body)))
		var out map[string]any
		json.Unmarshal(rr.Body.Bytes(), &out)
		return out
	}
	if out := post(`{"action":"save","mode":"schedule","off_from":"7:00","off_to":"08:00"}`); out["success"] != false {
		t.Fatal("bad time accepted")
	}
	if out := post(`{"action":"save","mode":"blink"}`); out["success"] != false {
		t.Fatal("bad mode accepted")
	}
	out := post(`{"action":"save","mode":"off"}`)
	if out["success"] != true || out["light_off"] != true || len(svc) != 1 || svc[0] != "stop" {
		t.Fatalf("off: %v svc=%v", out, svc)
	}
	// Both outputs were written 0x0 (last write per device is the data file).
	for _, r := range ledRegs {
		d, _ := os.ReadFile(filepath.Join(ledRegmap, r.Dev, "data"))
		if string(d) != "0x0" {
			t.Fatalf("%s data %q", r.Dev, d)
		}
	}
	saved, _ := os.ReadFile(ledSavedFile)
	if !strings.Contains(string(saved), `"spmi0-08 0xC240":"0x01"`) || !strings.Contains(string(saved), `"spmi0-09 0xbd46":"0x80"`) {
		t.Fatalf("saved %s", saved)
	}
	out = post(`{"action":"save","mode":"on"}`)
	if out["light_off"] != false || svc[len(svc)-1] != "start" {
		t.Fatalf("on: %v svc=%v", out, svc)
	}
	d, _ := os.ReadFile(filepath.Join(ledRegmap, "spmi0-09", "data"))
	if string(d) != "0x80" {
		t.Fatalf("restore wrote %q", d)
	}
	if _, err := os.Stat(ledSavedFile); err == nil {
		t.Fatal("saved file kept after restore")
	}

	c := ledConf{Mode: "schedule", OffFrom: "23:00", OffTo: "07:00"}
	at := func(h, m int) time.Time { return time.Date(2026, 10, 25, h, m, 0, 0, time.Local) }
	for _, tc := range []struct {
		h, m int
		off  bool
	}{{22, 59, false}, {23, 0, true}, {3, 0, true}, {6, 59, true}, {7, 0, false}, {12, 0, false}} {
		if c.wantOff(at(tc.h, tc.m)) != tc.off {
			t.Errorf("%02d:%02d want off=%v", tc.h, tc.m, tc.off)
		}
	}
	// Times are in the saved zone: 23:00 London (BST, UTC+1) is 22:00 UTC.
	lon := ledConf{Mode: "schedule", OffFrom: "23:00", OffTo: "07:00", Timezone: "Europe/London"}
	if !lon.wantOff(time.Date(2026, 7, 1, 22, 30, 0, 0, time.UTC)) || lon.wantOff(time.Date(2026, 7, 1, 21, 30, 0, 0, time.UTC)) {
		t.Fatal("timezone not applied")
	}
	if out := post(`{"action":"save","mode":"on","timezone":"Mars/Olympus"}`); out["success"] != false {
		t.Fatal("bad timezone accepted")
	}
	day := ledConf{Mode: "schedule", OffFrom: "09:00", OffTo: "17:30"}
	if !day.wantOff(at(12, 0)) || day.wantOff(at(18, 0)) {
		t.Fatal("daytime window")
	}
}
