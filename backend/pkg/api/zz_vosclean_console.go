package api

// vosclean: web console. A root shell in the browser, over a WebSocket, for
// when SSH isn't handy. Off by default; turning it on needs a logged-in
// session, and every connection needs one too (checked again every minute,
// so logging out ends open consoles).
//
// The shell runs on a PTY opened natively (no ttyd binary), and the socket
// speaks ttyd's small protocol so a standard xterm.js client works:
//   client -> server  '{...}'          first message: {"columns":N,"rows":N}
//                     '0' + bytes      keyboard input
//                     '1' + '{...}'    resize {"columns":N,"rows":N}
//   server -> client  '0' + bytes      terminal output
//
// Close codes: 4003 console turned off, 4008 idle timeout or session ended,
// 4029 too many consoles open, 1011 the shell couldn't start.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/net/websocket"
)

const (
	consoleFlag     = "/data/vosclean/qm/etc/web_console"
	consoleAuditLog = "/data/vosclean/log/qm-requests.log"
	consoleMax      = 2
	consoleIdle     = 30 * time.Minute
)

var (
	consoleOpen     int32
	consoleKillMu   sync.Mutex
	consoleKillers  = map[int]func(int){}
	consoleKillNext int
)

// consoleCloseAll ends every open console (used when the console is turned off).
func consoleCloseAll(code int) {
	consoleKillMu.Lock()
	fs := make([]func(int), 0, len(consoleKillers))
	for _, f := range consoleKillers {
		fs = append(fs, f)
	}
	consoleKillMu.Unlock()
	for _, f := range fs {
		f(code)
	}
}

func consoleEnabled() bool {
	b, err := os.ReadFile(consoleFlag)
	return err == nil && strings.TrimSpace(string(b)) == "1"
}

func consoleAudit(format string, a ...any) {
	line := fmt.Sprintf(format, a...)
	log.Printf("[console] %s", line)
	if f, err := os.OpenFile(consoleAuditLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); err == nil {
		fmt.Fprintf(f, "%s CONSOLE %s\n", time.Now().UTC().Format("2006-01-02 15:04:05"), line)
		f.Close()
	}
}

// --- PTY --------------------------------------------------------------------

func ioctl(fd, req, arg uintptr) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg); e != 0 {
		return e
	}
	return nil
}

func openPTY() (master, slave *os.File, err error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	var unlock int32
	if err = ioctl(m.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); err != nil {
		m.Close()
		return nil, nil, err
	}
	var n uint32
	if err = ioctl(m.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); err != nil {
		m.Close()
		return nil, nil, err
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		m.Close()
		return nil, nil, err
	}
	return m, s, nil
}

func setWinsize(f *os.File, cols, rows int) {
	if cols <= 0 || rows <= 0 || cols > 1000 || rows > 1000 {
		return
	}
	ws := struct{ Row, Col, X, Y uint16 }{uint16(rows), uint16(cols), 0, 0}
	_ = ioctl(f.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
}

// --- settings endpoint ------------------------------------------------------

// HandleWebConsoleSettings: GET -> {enabled, active}; POST {"enabled":bool}.
func (s *Server) HandleWebConsoleSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodPost {
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "bad_request"})
			return
		}
		var err error
		if body.Enabled {
			err = vosWriteFileDurable(consoleFlag, []byte("1\n"), 0600)
		} else {
			err = os.Remove(consoleFlag)
			if os.IsNotExist(err) {
				err = nil
			}
		}
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "write_failed", "detail": err.Error()})
			return
		}
		if !body.Enabled {
			consoleCloseAll(4003)
		}
		consoleAudit("turned %s from %s", map[bool]string{true: "on", false: "off"}[body.Enabled], r.RemoteAddr)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true, "enabled": consoleEnabled(), "active": atomic.LoadInt32(&consoleOpen),
	})
}

// --- WebSocket --------------------------------------------------------------

// consoleOriginOK: the page must come from this same host (blocks other sites
// from opening a socket with the user's cookie).
func consoleOriginOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	u, err := url.Parse(o)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

// HandleWebConsoleWS upgrades /console/ws. Wrapped in RequireAuth by the router.
func (s *Server) HandleWebConsoleWS(w http.ResponseWriter, r *http.Request) {
	if !consoleOriginOK(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	srv := websocket.Server{
		Handshake: func(cfg *websocket.Config, req *http.Request) error {
			for _, p := range cfg.Protocol {
				if p == "tty" {
					cfg.Protocol = []string{"tty"}
					return nil
				}
			}
			cfg.Protocol = nil
			return nil
		},
		Handler: func(ws *websocket.Conn) { consoleSession(ws, r) },
	}
	srv.ServeHTTP(w, r)
}

func consoleSession(ws *websocket.Conn, r *http.Request) {
	defer ws.Close()
	ws.PayloadType = websocket.BinaryFrame
	who := r.RemoteAddr
	if !consoleEnabled() {
		_ = ws.WriteClose(4003)
		return
	}
	if atomic.AddInt32(&consoleOpen, 1) > consoleMax {
		atomic.AddInt32(&consoleOpen, -1)
		_ = ws.WriteClose(4029)
		return
	}
	defer atomic.AddInt32(&consoleOpen, -1)

	// first message: {"columns":N,"rows":N}
	cols, rows := 80, 24
	_ = ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	var first []byte
	if err := websocket.Message.Receive(ws, &first); err == nil && len(first) > 0 && first[0] == '{' {
		var hs struct{ Columns, Rows int }
		if json.Unmarshal(first, &hs) == nil && hs.Columns > 0 {
			cols, rows = hs.Columns, hs.Rows
		}
	}
	_ = ws.SetReadDeadline(time.Time{})

	master, slave, err := openPTY()
	if err != nil {
		consoleAudit("pty failed for %s: %v", who, err)
		_ = ws.WriteClose(1011)
		return
	}
	defer master.Close()
	setWinsize(master, cols, rows)

	home := "/home/root" // root's home on this firmware (no /root)
	if st, err := os.Stat(home); err != nil || !st.IsDir() {
		home = "/"
	}
	// The login profile resets PATH, so source it, then put vosclean and qcmd first.
	cmd := exec.Command("/bin/sh", "-c",
		`. /etc/profile 2>/dev/null; PATH=/data/vosclean/bin:/data/vosclean/qm/bin:$PATH; export PATH PS1; exec /bin/sh -i`)
	cmd.Dir = home
	cmd.Env = []string{"TERM=xterm-256color", "HOME=" + home, "USER=root", "LOGNAME=root", "SHELL=/bin/sh",
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin", `PS1=\u@vos5g:\w\$ `}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		slave.Close()
		consoleAudit("shell failed for %s: %v", who, err)
		_ = ws.WriteClose(1011)
		return
	}
	slave.Close()
	started := time.Now()
	consoleAudit("opened by %s (pid %d)", who, cmd.Process.Pid)

	var once sync.Once
	closeCode := 1000
	var lastInput atomic.Int64
	lastInput.Store(time.Now().UnixNano())
	finish := func(code int) {
		once.Do(func() {
			closeCode = code
			pid := cmd.Process.Pid
			_ = syscall.Kill(-pid, syscall.SIGHUP) // the whole session: shell and its children
			master.Close()
			time.AfterFunc(3*time.Second, func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
		})
	}

	consoleKillMu.Lock()
	consoleKillNext++
	killID := consoleKillNext
	consoleKillers[killID] = finish
	consoleKillMu.Unlock()
	defer func() {
		consoleKillMu.Lock()
		delete(consoleKillers, killID)
		consoleKillMu.Unlock()
	}()

	// shell -> browser
	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		buf := make([]byte, 16*1024)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				msg := make([]byte, n+1)
				msg[0] = '0'
				copy(msg[1:], buf[:n])
				if websocket.Message.Send(ws, msg) != nil {
					finish(1001)
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// browser -> shell
	go func() {
		for {
			var m []byte
			if err := websocket.Message.Receive(ws, &m); err != nil {
				finish(1001)
				return
			}
			if len(m) == 0 {
				continue
			}
			lastInput.Store(time.Now().UnixNano())
			switch m[0] {
			case '0':
				_, _ = master.Write(m[1:])
			case '1':
				var rz struct{ Columns, Rows int }
				if json.Unmarshal(m[1:], &rz) == nil {
					setWinsize(master, rz.Columns, rz.Rows)
				}
			}
		}
	}()

	// session still valid, console still on, not idle
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				switch {
				case !isAuthenticated(r):
					finish(4008)
				case !consoleEnabled():
					finish(4003)
				case time.Since(time.Unix(0, lastInput.Load())) > consoleIdle:
					finish(4008)
				}
			}
		}
	}()

	_ = cmd.Wait()
	close(stop)
	finish(closeCode)
	<-outDone
	_ = ws.WriteClose(closeCode)
	consoleAudit("closed for %s after %s (code %d)", who, time.Since(started).Round(time.Second), closeCode)
}
