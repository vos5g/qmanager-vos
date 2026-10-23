package api

// vosclean: "Reconnect to network" and "Restart stick".
//
// Upstream reconnects with AT+COPS=2 / AT+COPS=0 (deregister, re-register), but
// the Compal modem has no equivalent and qcmd answers OK without doing
// anything. Turning the radio off and on (AT+CFUN=0 / AT+CFUN=1, which qcmd
// maps to cricli set_op_mode) re-registers and starts a new data session; on
// the stick it is back online in about 15 seconds, with a new IPv4 address.

import (
	"errors"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var (
	vosReconnectMu  sync.Mutex
	vosRadioOffWait = 3 * time.Second
	vosRebootDelay  = 1500 * time.Millisecond
)

// vosReconnectRadio turns the radio off, waits, and turns it back on. It
// returns once the modem has accepted both commands; registration and the data
// session follow by themselves.
func (s *Server) vosReconnectRadio() error {
	if !vosReconnectMu.TryLock() {
		return errors.New("a reconnect is already in progress")
	}
	defer vosReconnectMu.Unlock()
	if resp, err := s.atClient.Exec("AT+CFUN=0"); err != nil || strings.Contains(resp, "ERROR") {
		return errors.New("the modem refused to turn the radio off")
	}
	time.Sleep(vosRadioOffWait)
	var lastErr error
	for i := 0; i < 3; i++ { // the radio must come back on, whatever happens
		resp, err := s.atClient.Exec("AT+CFUN=1")
		if err == nil && !strings.Contains(resp, "ERROR") {
			return nil
		}
		lastErr = errors.New("the modem refused to turn the radio back on")
		time.Sleep(2 * time.Second)
	}
	return lastErr
}

// vosRebootSoon reboots after the HTTP response has gone out, so the page gets
// its answer before the connection drops.
func vosRebootSoon(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		time.Sleep(vosRebootDelay)
		if err := exec.Command("systemctl", "reboot").Run(); err != nil {
			_ = exec.Command("reboot").Run()
		}
	}()
}
