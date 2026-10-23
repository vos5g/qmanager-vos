package api

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// recAT records the AT commands it receives.
type recAT struct {
	mu   sync.Mutex
	cmds []string
	fail map[string]int // command -> number of times to answer ERROR
}

func (r *recAT) Exec(cmd string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cmds = append(r.cmds, cmd)
	if r.fail[cmd] > 0 {
		r.fail[cmd]--
		return "ERROR", nil
	}
	return "OK", nil
}

func (r *recAT) ExecWithTimeout(cmd string, _ time.Duration) (string, error) { return r.Exec(cmd) }

func TestVosReconnectRadio(t *testing.T) {
	vosRadioOffWait = time.Millisecond
	rec := &recAT{}
	s := &Server{atClient: rec}
	if err := s.vosReconnectRadio(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rec.cmds, " "); got != "AT+CFUN=0 AT+CFUN=1" {
		t.Fatalf("commands: %s", got)
	}

	// radio-on is retried until the modem accepts it
	rec = &recAT{fail: map[string]int{"AT+CFUN=1": 1}}
	s = &Server{atClient: rec}
	if err := s.vosReconnectRadio(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rec.cmds, " "); got != "AT+CFUN=0 AT+CFUN=1 AT+CFUN=1" {
		t.Fatalf("retry: %s", got)
	}

	// if the radio can't be turned off, nothing else is sent
	rec = &recAT{fail: map[string]int{"AT+CFUN=0": 1}}
	s = &Server{atClient: rec}
	if err := s.vosReconnectRadio(); err == nil || len(rec.cmds) != 1 {
		t.Fatalf("off failure: %v %v", err, rec.cmds)
	}
}
