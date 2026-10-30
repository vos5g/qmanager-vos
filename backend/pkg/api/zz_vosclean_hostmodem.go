package api

// vosclean: router modem mode (System Settings -> USB mode).
//
// The stick keeps its own data call on the SIM's APN. A USB host that uses the
// stick as its modem (QMI/MBIM, e.g. OpenWrt with 05c6:9064 configuration 1)
// needs that same APN, and the modem refuses its call while the stick holds it.
// vosclean's hostmodem daemon (vos5g-sdx55 vosclean/bin/hostmodem.sh, setting
// HOST_MODEM=auto|off in vosclean.conf) watches USB and hands the connection to
// the host while it uses the modem, and takes it back when USB tethering
// returns. It writes its state to /tmp/vosclean/hostmodem ("mode=host
// since=<epoch>") and keeps the watchdog on hold. This file reports that state,
// switches the setting, keeps the connection alerts quiet while the host has the
// connection (the stick itself is offline on purpose) and logs each hand-over
// in Recent Activity.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"qmanager-backend/pkg/daemon"
)

var (
	hmStateFile  = "/tmp/vosclean/hostmodem"
	hmMarkerFile = "/data/vosclean/state/hostmodem-released"
	hmScript     = "/data/vosclean/bin/hostmodem.sh"
	hmEvent      = daemon.AppendEvent // Recent Activity (tests swap it)
)

type hostModemState struct {
	Mode  string // "host" or "stick"
	Since int64
}

func readHostModemState() hostModemState {
	st := hostModemState{Mode: "stick"}
	b, err := os.ReadFile(hmStateFile)
	if err != nil {
		return st
	}
	for _, f := range strings.Fields(string(b)) {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			continue
		}
		switch k {
		case "mode":
			if v == "host" {
				st.Mode = "host"
			}
		case "since":
			st.Since, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	return st
}

// hostModemActive: a USB host has the data connection right now (the daemon
// released the stick's own call). The marker guards against a stale state file.
func hostModemActive() bool {
	if readHostModemState().Mode != "host" {
		return false
	}
	_, err := os.Stat(hmMarkerFile)
	return err == nil
}

// hostModemSetting reads HOST_MODEM from vosclean.conf (missing = off).
func hostModemSetting() string {
	b, err := os.ReadFile(kmConfPath)
	if err != nil {
		return "off"
	}
	v := "off"
	for _, l := range strings.Split(string(b), "\n") {
		if s, ok := strings.CutPrefix(strings.TrimSpace(l), "HOST_MODEM="); ok {
			v = strings.Trim(strings.TrimSpace(s), `"'`)
		}
	}
	return v
}

func hostModemStatus() map[string]any {
	_, err := os.Stat(hmScript)
	st := readHostModemState()
	active := hostModemActive()
	since := int64(0)
	if active {
		since = st.Since
	}
	return map[string]any{
		"success":   true,
		"supported": err == nil, // vosclean on the stick has the hostmodem daemon (vos5g-sdx55 v1.3+)
		"enabled":   hostModemSetting() == "auto",
		"active":    active,
		"since":     since,
		"usb_pid":   currentUSBPID(),
	}
}

// HandleHostModem: GET status, POST {"enabled": bool}.
func (s *Server) HandleHostModem(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, 200, hostModemStatus())
	case http.MethodPost:
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); e != nil || req.Enabled == nil {
			writeJSON(w, 400, map[string]any{"success": false, "error": "invalid_request", "detail": "Send {\"enabled\": true|false}."})
			return
		}
		if _, err := os.Stat(hmScript); err != nil {
			writeJSON(w, 400, map[string]any{"success": false, "error": "unsupported", "detail": "Update vosclean on the stick (vos5g-sdx55) to use router modem mode."})
			return
		}
		arg := "off"
		if *req.Enabled {
			arg = "on"
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, kmVC, "hostmodem", arg).CombinedOutput(); err != nil {
			writeJSON(w, 500, map[string]any{"success": false, "error": "apply_failed", "detail": strings.TrimSpace(string(out) + " " + err.Error())})
			return
		}
		writeJSON(w, 200, hostModemStatus())
	default:
		w.WriteHeader(405)
	}
}

// hostModemWatch logs hand-overs in Recent Activity; called from the notifier tick.
type hostModemWatch struct {
	known, active bool
}

func (h *hostModemWatch) update(active bool, now time.Time) {
	if h.known && active != h.active {
		if active {
			hmEvent(now, "host_modem", "info", "USB host is using the stick as its modem: the stick's own data connection was handed over")
		} else {
			hmEvent(now, "host_modem", "info", "USB host stopped using the modem: the stick has its data connection again")
		}
	}
	h.known, h.active = true, active
}
