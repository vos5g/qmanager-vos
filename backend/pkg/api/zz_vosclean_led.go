package api

// vosclean: Status Light (the stick's LED).
//
// Tri Cascade's `ceiled` service drives the LED from the registration state:
// green through the PMIC's LED driver (spmi0-08 0xC240 enable, spmi0-09 0xBD46
// output), red on GPIO 15. It only writes them when the state changes. To turn
// the light off, QManager saves the current register values, stops ceiled and
// clears the outputs; to turn it back on it writes the saved values back and
// starts ceiled again. The same writes are in /oem/script/led_off.sh and
// green_led_keep_on.sh.
//
// Modes: "on" (normal), "off", "schedule" (off between two local times, e.g.
// overnight). On shutdown QManager puts the light back on, and after a reboot
// ceiled starts normally until QManager applies the setting again.
//
//	GET  /cgi-bin/quecmanager/system/led.sh
//	POST {action:"save", mode:"on|off|schedule", off_from:"HH:MM", off_to:"HH:MM"}

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // the stick has no zoneinfo; schedules use the browser's zone
)

var (
	ledConfFile  = "/data/vosclean/qm/etc/led.json"
	ledSavedFile = "/tmp/qmanager_led_saved.json"
	ledRegmap    = "/sys/kernel/debug/regmap"
	ledGPIO      = "/sys/class/gpio/gpio15/value"
	ledService   = func(action string) error {
		out, err := exec.Command("systemctl", action, "ceiled").CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl %s ceiled: %v: %s", action, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	ledServiceActive = func() bool {
		return exec.Command("systemctl", "is-active", "--quiet", "ceiled").Run() == nil
	}
)

// ledReg is one LED register: regmap device + address.
type ledReg struct{ Dev, Addr string }

// The registers that switch the light. Only these are touched.
var ledRegs = []ledReg{{"spmi0-08", "0xC240"}, {"spmi0-09", "0xbd46"}}

// Values ceiled uses for "green on" if nothing was saved (green_led_keep_on.sh).
var ledDefaultOn = map[string]string{"spmi0-08 0xC240": "0x1", "spmi0-09 0xbd46": "0x80"}

type ledConf struct {
	Mode    string `json:"mode"` // on | off | schedule
	OffFrom string `json:"off_from"`
	OffTo   string `json:"off_to"`
	// IANA zone the times are in (the browser's when saved). The stick's own
	// clock runs in UTC, so "23:00" means the user's 23:00, not the stick's.
	Timezone string `json:"timezone"`
}

func (c ledConf) location() *time.Location {
	if c.Timezone != "" {
		if loc, err := time.LoadLocation(c.Timezone); err == nil {
			return loc
		}
	}
	return time.Local
}

type ledState struct {
	mu   sync.Mutex
	conf ledConf
	off  bool // QManager has the light switched off right now
	err  string
}

var leds = &ledState{conf: ledConf{Mode: "on", OffFrom: "23:00", OffTo: "07:00"}}

var hhmm = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)$`)

func minutesOf(s string) int {
	m := hhmm.FindStringSubmatch(s)
	if m == nil {
		return -1
	}
	h, _ := strconv.Atoi(m[1])
	mm, _ := strconv.Atoi(m[2])
	return h*60 + mm
}

// wantOff reports whether the light should be off at t.
func (c ledConf) wantOff(t time.Time) bool {
	switch c.Mode {
	case "off":
		return true
	case "schedule":
		from, to := minutesOf(c.OffFrom), minutesOf(c.OffTo)
		if from < 0 || to < 0 || from == to {
			return false
		}
		t = t.In(c.location())
		now := t.Hour()*60 + t.Minute()
		if from < to {
			return now >= from && now < to
		}
		return now >= from || now < to // crosses midnight
	}
	return false
}

func regRead(r ledReg) (string, error) {
	base := ledRegmap + "/" + r.Dev
	if err := os.WriteFile(base+"/address", []byte(r.Addr), 0); err != nil {
		return "", err
	}
	b, err := os.ReadFile(base + "/data")
	if err != nil {
		return "", err
	}
	// "c240: 01"
	s := strings.TrimSpace(string(b))
	if i := strings.LastIndexByte(s, ':'); i >= 0 {
		s = strings.TrimSpace(s[i+1:])
	}
	if _, err := strconv.ParseUint(s, 16, 8); err != nil {
		return "", fmt.Errorf("unexpected register value %q", s)
	}
	return "0x" + s, nil
}

func regWrite(r ledReg, v string) error {
	base := ledRegmap + "/" + r.Dev
	if err := os.WriteFile(base+"/address", []byte(r.Addr), 0); err != nil {
		return err
	}
	return os.WriteFile(base+"/data", []byte(v), 0)
}

// lightsOff saves the current outputs, stops ceiled and clears them.
func lightsOff() error {
	if _, err := os.Stat(ledSavedFile); err != nil {
		saved := map[string]string{}
		for _, r := range ledRegs {
			v, err := regRead(r)
			if err != nil {
				return fmt.Errorf("read LED register: %w", err)
			}
			saved[r.Dev+" "+r.Addr] = v
		}
		if b, err := os.ReadFile(ledGPIO); err == nil {
			saved["gpio15"] = strings.TrimSpace(string(b))
		}
		b, _ := json.Marshal(saved)
		if err := os.WriteFile(ledSavedFile, b, 0644); err != nil {
			return err
		}
	}
	if err := ledService("stop"); err != nil {
		return err
	}
	for _, r := range ledRegs {
		if err := regWrite(r, "0x0"); err != nil {
			return fmt.Errorf("write LED register: %w", err)
		}
	}
	_ = os.WriteFile(ledGPIO, []byte("0"), 0)
	return nil
}

// lightsOn writes the saved outputs back and starts ceiled.
func lightsOn() error {
	saved := map[string]string{}
	if b, err := os.ReadFile(ledSavedFile); err == nil {
		_ = json.Unmarshal(b, &saved)
	}
	var firstErr error
	for _, r := range ledRegs {
		k := r.Dev + " " + r.Addr
		v := saved[k]
		if _, err := strconv.ParseUint(strings.TrimPrefix(v, "0x"), 16, 8); err != nil || v == "" {
			v = ledDefaultOn[k]
		}
		if err := regWrite(r, v); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if g := saved["gpio15"]; g == "0" || g == "1" {
		_ = os.WriteFile(ledGPIO, []byte(g), 0)
	}
	if err := ledService("start"); err != nil && firstErr == nil {
		firstErr = err
	}
	if firstErr == nil {
		os.Remove(ledSavedFile)
	}
	return firstErr
}

// syncLocked makes the light match the setting for time t.
func (s *ledState) syncLocked(t time.Time) {
	want := s.conf.wantOff(t)
	if want == s.off {
		return
	}
	var err error
	if want {
		err = lightsOff()
	} else {
		err = lightsOn()
	}
	if err != nil {
		s.err = err.Error()
		log.Printf("led: %v", err)
		return
	}
	s.err = ""
	s.off = want
}

// StartLED applies the saved Status Light setting and follows the schedule.
func StartLED() {
	leds.mu.Lock()
	if b, err := os.ReadFile(ledConfFile); err == nil {
		_ = json.Unmarshal(b, &leds.conf)
	}
	// QManager restarted while the light was off: it still is.
	if _, err := os.Stat(ledSavedFile); err == nil && !ledServiceActive() {
		leds.off = true
	}
	leds.syncLocked(time.Now())
	leds.mu.Unlock()
	go func() {
		for {
			time.Sleep(30 * time.Second)
			leds.mu.Lock()
			leds.syncLocked(time.Now())
			leds.mu.Unlock()
		}
	}()
}

// StopLED puts the light back to normal before QManager exits.
func StopLED() {
	leds.mu.Lock()
	defer leds.mu.Unlock()
	if leds.off {
		if err := lightsOn(); err == nil {
			leds.off = false
		}
	}
}

func (s *ledState) snapshot() map[string]any {
	now := time.Now().In(s.conf.location())
	return map[string]any{
		"success":     true,
		"mode":        s.conf.Mode,
		"off_from":    s.conf.OffFrom,
		"off_to":      s.conf.OffTo,
		"light_off":   s.off,
		"error":       s.err,
		"device_time": now.Format("15:04"),
		"timezone":    s.conf.Timezone,
		"zone_abbr":   now.Format("MST"),
		"clock_ok":    now.Year() >= 2025,
	}
}

// HandleLED serves the Status Light card.
func (s *Server) HandleLED(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		leds.mu.Lock()
		out := leds.snapshot()
		leds.mu.Unlock()
		writeJSON(w, http.StatusOK, out)
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "error": "method_not_allowed"})
		return
	}
	var body struct {
		Action string `json:"action"`
		ledConf
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil || body.Action != "save" {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "invalid_json"})
		return
	}
	c := body.ledConf
	switch c.Mode {
	case "on", "off", "schedule":
	default:
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "reject_field", "detail": "mode must be on, off or schedule"})
		return
	}
	if c.Mode == "schedule" && (minutesOf(c.OffFrom) < 0 || minutesOf(c.OffTo) < 0 || c.OffFrom == c.OffTo) {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "reject_field", "detail": "times must be HH:MM and different"})
		return
	}
	if c.Timezone != "" {
		if _, err := time.LoadLocation(c.Timezone); err != nil || len(c.Timezone) > 64 {
			writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "reject_field", "detail": "unknown timezone"})
			return
		}
	}
	if minutesOf(c.OffFrom) < 0 {
		c.OffFrom = "23:00"
	}
	if minutesOf(c.OffTo) < 0 {
		c.OffTo = "07:00"
	}
	leds.mu.Lock()
	defer leds.mu.Unlock()
	leds.conf = c
	b, _ := json.MarshalIndent(c, "", "  ")
	if err := vosWriteFileDurable(ledConfFile, b, 0644); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "save_failed", "detail": err.Error()})
		return
	}
	leds.syncLocked(time.Now())
	if leds.err != "" {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "led_apply_failed", "detail": leds.err})
		return
	}
	writeJSON(w, http.StatusOK, leds.snapshot())
}
