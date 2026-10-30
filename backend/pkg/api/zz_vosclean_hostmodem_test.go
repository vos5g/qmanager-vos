package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func hostModemTestEnv(t *testing.T) (dir string, events *[]string) {
	d := t.TempDir()
	oldState, oldMark, oldScript, oldConf, oldEvent := hmStateFile, hmMarkerFile, hmScript, kmConfPath, hmEvent
	t.Cleanup(func() { hmStateFile, hmMarkerFile, hmScript, kmConfPath, hmEvent = oldState, oldMark, oldScript, oldConf, oldEvent })
	hmStateFile = filepath.Join(d, "hostmodem")
	hmMarkerFile = filepath.Join(d, "hostmodem-released")
	hmScript = filepath.Join(d, "hostmodem.sh")
	kmConfPath = filepath.Join(d, "vosclean.conf")
	var ev []string
	hmEvent = func(_ time.Time, typ, _, msg string) { ev = append(ev, typ+": "+msg) }
	return d, &ev
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestHostModemState(t *testing.T) {
	_, _ = hostModemTestEnv(t)
	if hostModemActive() || hostModemSetting() != "off" {
		t.Fatal("nothing on disk: inactive and off")
	}
	mustWrite(t, kmConfPath, "QM_ENABLE=yes\nHOST_MODEM=auto\n")
	mustWrite(t, hmStateFile, "mode=host since=1793326622\n")
	if hostModemActive() {
		t.Fatal("host mode without the marker is a stale state file")
	}
	mustWrite(t, hmMarkerFile, "")
	if !hostModemActive() || hostModemSetting() != "auto" {
		t.Fatal("host mode with the marker is active")
	}
	if st := readHostModemState(); st.Since != 1793326622 {
		t.Fatalf("since %d", st.Since)
	}
	mustWrite(t, hmStateFile, "mode=stick since=1\n")
	if hostModemActive() {
		t.Fatal("stick mode is not active")
	}
}

func TestHostModemHandler(t *testing.T) {
	_, _ = hostModemTestEnv(t)
	s := &Server{}
	rec := httptest.NewRecorder()
	s.HandleHostModem(rec, httptest.NewRequest("GET", "/", nil))
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["supported"] != false || got["enabled"] != false || got["active"] != false {
		t.Fatalf("GET %v", got)
	}
	rec = httptest.NewRecorder()
	s.HandleHostModem(rec, httptest.NewRequest("POST", "/", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST without enabled: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.HandleHostModem(rec, httptest.NewRequest("POST", "/", strings.NewReader(`{"enabled":true}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unsupported") {
		t.Fatalf("POST on an old vosclean: %d %s", rec.Code, rec.Body.String())
	}
}

func TestHostModemQuietsAlerts(t *testing.T) {
	out := notifyTestEnv(t)
	_, events := hostModemTestEnv(t)
	writeTestJSON(t, smsConfigPath, map[string]any{"enabled": true, "recipient_phone": "+447700900111", "threshold_minutes": 1})
	writeTestJSON(t, routingConfigPath, map[string]any{"events": map[string]any{
		"connection_lost":     map[string]any{"sms": true},
		"connection_restored": map[string]any{"sms": true},
	}})
	n := newNotifier()
	clock := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	n.now = func() time.Time { return clock }
	n.uptime = func() time.Duration { return time.Hour }
	n.bootID = func() string { return "boot-a" }
	conn, host := true, false
	n.connected = func() (bool, bool) { return conn, true }
	n.hostModem = func() bool { return host }

	n.tick()
	host, conn = true, false // the router takes the modem: the stick goes offline on purpose
	for i := 0; i < 10; i++ {
		clock = clock.Add(time.Minute)
		n.tick()
	}
	host, conn = false, true
	clock = clock.Add(time.Minute)
	n.tick()
	if len(*out) != 0 {
		t.Fatalf("alerts while a USB host had the connection: %v", *out)
	}
	if len(*events) != 2 || !strings.Contains((*events)[0], "handed over") || !strings.Contains((*events)[1], "again") {
		t.Fatalf("events %v", *events)
	}
}
