package api

// vosclean: SMS forwarding and alerts.
//
// Upstream saved these settings but nothing on the stick acted on them. This
// runs one loop (every 30 s) that:
//
//   - forwards new incoming texts to a phone number (Forwarding page), using
//     the same sms_tool the SMS Center uses. Messages already in the inbox when
//     forwarding is switched on are not forwarded. A message that fails three
//     times is listed under failures and skipped.
//   - sends the Alerts page's alerts: connection lost (SMS only: email needs the
//     internet), connection restored and stick restarted, over SMS and/or
//     email as routed. "Lost" goes out once the outage has lasted the channel's
//     threshold; "restored" only after an outage that long. The connection
//     state is the watchdog's probe, which bypasses the VPN.
//   - email is sent directly over SMTP with TLS (no msmtp needed); the server
//     is picked from the sender's address (Gmail, Outlook, Yahoo, iCloud, else
//     smtp.<domain>:587).
//
// Reboots are detected from the kernel boot ID once the clock is set; the
// cause is "user" or "watchdog" when a planned-reboot marker was left on
// flash, else "unplanned". Each is added to the Recent Reboots history.

import (
	"crypto/sha1"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"qmanager-backend/pkg/daemon"
)

var (
	smsForwardFile      = "/data/vosclean/qm/etc/sms_forwarding.json"
	smsForwardStateFile = "/data/vosclean/qm/etc/sms_forward_state.json"
	lastBootIDFile      = "/data/vosclean/qm/etc/last_boot_id"
	rebootHistoryFile   = "/data/vosclean/qm/etc/crash.log"
	watchdogStatusFile  = "/tmp/qmanager_watchdog_status.json"

	rePhone = regexp.MustCompile(`^\+?[0-9]{3,16}$`)
	reEmail = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

const (
	notifyEvery    = 30 * time.Second
	forwardTries   = 3
	maxSeen        = 500
	maxFailures    = 50
	pendingTimeout = time.Hour
	deviceName     = "VOS 5G"
)

// ---------- senders (swappable in tests) ----------

var sendSMS = func(number, text string) error {
	bin := smsToolBin()
	if bin == "" {
		return errors.New("sms_tool missing")
	}
	out, err := exec.Command(bin, "send", number, text).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

var sendEmail = smtpSend

// emailServer picks the SMTP server for a sender address.
func emailServer(sender string) (host string, port int) {
	domain := strings.ToLower(sender[strings.LastIndex(sender, "@")+1:])
	switch domain {
	case "gmail.com", "googlemail.com":
		return "smtp.gmail.com", 587
	case "outlook.com", "hotmail.com", "hotmail.co.uk", "live.com", "live.co.uk", "msn.com":
		return "smtp-mail.outlook.com", 587
	case "yahoo.com", "yahoo.co.uk", "ymail.com":
		return "smtp.mail.yahoo.com", 465
	case "icloud.com", "me.com", "mac.com":
		return "smtp.mail.me.com", 587
	case "gmx.com", "gmx.co.uk", "gmx.net":
		return "mail.gmx.com", 587
	}
	return "smtp." + domain, 587
}

func smtpSend(from, password, to, subject, body string) error {
	host, port := emailServer(from)
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	tlsCfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	var conn net.Conn
	var err error
	if port == 465 {
		conn, err = tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", addr, tlsCfg)
	} else {
		conn, err = net.DialTimeout("tcp", addr, 15*time.Second)
	}
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(45 * time.Second))
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if port != 465 {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New(host + " does not offer STARTTLS")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return err
		}
	}
	if err := c.Auth(smtp.PlainAuth("", from, password, host)); err != nil {
		return fmt.Errorf("login failed: %w", err)
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	msg := "From: QManager <" + from + ">\r\nTo: " + to + "\r\nSubject: " + subject +
		"\r\nDate: " + time.Now().UTC().Format(time.RFC1123Z) +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" +
		strings.ReplaceAll(body, "\n", "\r\n") + "\r\n"
	if _, err := wc.Write([]byte(msg)); err != nil {
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// ---------- alert log ----------

var alertLogMu sync.Mutex

func logAlert(trigger, status, recipient, channel string) {
	alertLogMu.Lock()
	defer alertLogMu.Unlock()
	var lines []map[string]any
	if b, err := os.ReadFile(alertLogPath); err == nil {
		_ = json.Unmarshal(b, &lines)
	}
	lines = append([]map[string]any{{
		"timestamp": time.Now().Format("2006-01-02 15:04:05"),
		"trigger":   trigger,
		"status":    status,
		"recipient": recipient,
		"channel":   channel,
	}}, lines...)
	if len(lines) > 100 {
		lines = lines[:100]
	}
	if b, err := json.Marshal(lines); err == nil {
		_ = os.WriteFile(alertLogPath, b, 0644)
	}
}

// ---------- alert channels ----------

type alertChannels struct {
	smsOn       bool
	phone       string
	smsMinutes  int
	mailOn      bool
	from, to    string
	password    string
	mailMinutes int
	routes      map[string]map[string]bool
}

func loadAlertChannels() alertChannels {
	sms := qmReadJSONFile(smsConfigPath)
	mail := qmReadJSONFile(emailConfigPath)
	a := alertChannels{
		smsOn: qmBool(sms["enabled"]), phone: qmStr(sms["recipient_phone"]), smsMinutes: qmCfgInt(sms, "threshold_minutes", 5),
		mailOn: qmBool(mail["enabled"]), from: qmStr(mail["sender_email"]), to: qmStr(mail["recipient_email"]),
		password: qmStr(mail["app_password"]), mailMinutes: qmCfgInt(mail, "threshold_minutes", 5),
		routes: map[string]map[string]bool{},
	}
	if ev, ok := qmReadJSONFile(routingConfigPath)["events"].(map[string]any); ok {
		for event, v := range ev {
			m, _ := v.(map[string]any)
			a.routes[event] = map[string]bool{"sms": qmBool(m["sms"]), "email": qmBool(m["email"])}
		}
	}
	return a
}

func (a alertChannels) smsReady() bool { return a.smsOn && rePhone.MatchString(a.phone) }
func (a alertChannels) mailReady() bool {
	return a.mailOn && reEmail.MatchString(a.from) && reEmail.MatchString(a.to) && a.password != ""
}

// routed reports whether an event goes to a channel that is ready.
func (a alertChannels) routed(event, channel string) bool {
	if !a.routes[event][channel] {
		return false
	}
	if channel == "sms" {
		return a.smsReady()
	}
	return channel == "email" && event != "connection_lost" && a.mailReady()
}

func (a alertChannels) send(channel, trigger, text string) error {
	var err error
	recipient := a.phone
	if channel == "sms" {
		err = sendSMS(a.phone, text)
	} else {
		recipient = a.to
		err = sendEmail(a.from, a.password, a.to, deviceName+": "+trigger, text)
	}
	status := "sent"
	if err != nil {
		status = "failed"
		log.Printf("[Alerts] %s %s alert failed: %v", trigger, channel, err)
	}
	logAlert(trigger, status, recipient, channel)
	return err
}

// ---------- notifier ----------

type pendingAlert struct {
	event, channel, trigger, text string
	since                         time.Time
}

type notifier struct {
	mu        sync.Mutex
	downSince time.Time
	sentLost  map[string]bool // channel -> "lost" sent for this outage
	pending   []pendingAlert
	bootDone  bool
	now       func() time.Time
	uptime    func() time.Duration
	bootID    func() string
	connected func() (ok, known bool)
}

var notify = newNotifier()

func newNotifier() *notifier {
	return &notifier{
		sentLost: map[string]bool{},
		now:      time.Now,
		uptime: func() time.Duration {
			b, _ := os.ReadFile("/proc/uptime")
			f, _ := strconv.ParseFloat(strings.Fields(string(b) + " 0")[0], 64)
			return time.Duration(f * float64(time.Second))
		},
		bootID: func() string {
			b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
			return strings.TrimSpace(string(b))
		},
		connected: func() (bool, bool) {
			raw := qmReadJSONFile(watchdogStatusFile)
			if len(raw) == 0 {
				return false, false
			}
			return qmBool(raw["is_connected"]), true
		},
	}
}

// StartNotifier runs SMS forwarding and alerts.
func StartNotifier() {
	go func() {
		t := time.NewTicker(notifyEvery)
		defer t.Stop()
		for range t.C {
			notify.tick()
			forwardSMS()
		}
	}()
}

func fmtDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	if d < time.Hour {
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
}

func (n *notifier) queue(a alertChannels, event, trigger, text string) {
	for _, ch := range []string{"sms", "email"} {
		if a.routed(event, ch) {
			n.pending = append(n.pending, pendingAlert{event, ch, trigger, text, n.now()})
		}
	}
}

func (n *notifier) tick() {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now()
	a := loadAlertChannels()

	ok, known := n.connected()
	if known {
		switch {
		case !ok && n.downSince.IsZero():
			n.downSince = now
			n.sentLost = map[string]bool{}
		case !ok:
			down := now.Sub(n.downSince)
			if !n.sentLost["sms"] && a.routed("connection_lost", "sms") && down >= time.Duration(a.smsMinutes)*time.Minute {
				text := fmt.Sprintf("%s: no internet connection for %s (since %s).", deviceName, fmtDuration(down), n.downSince.Format("15:04"))
				if a.send("sms", "Connection lost", text) == nil {
					n.sentLost["sms"] = true
				}
			}
		case ok && !n.downSince.IsZero():
			down := now.Sub(n.downSince)
			text := fmt.Sprintf("%s: connection restored after %s (down %s-%s).", deviceName, fmtDuration(down), n.downSince.Format("15:04"), now.Format("15:04"))
			for _, ch := range []string{"sms", "email"} {
				min := a.smsMinutes
				if ch == "email" {
					min = a.mailMinutes
				}
				if a.routed("connection_restored", ch) && down >= time.Duration(min)*time.Minute {
					n.pending = append(n.pending, pendingAlert{"connection_restored", ch, "Connection restored", text, now})
				}
			}
			n.downSince = time.Time{}
		}
	}

	if !n.bootDone && now.Year() >= 2025 {
		n.bootDone = true
		n.checkBoot(a, now)
	}

	// Deliver queued alerts (email waits for the connection).
	keep := n.pending[:0]
	for _, p := range n.pending {
		if now.Sub(p.since) > pendingTimeout {
			continue
		}
		if p.channel == "email" && !ok {
			keep = append(keep, p)
			continue
		}
		if a.send(p.channel, p.trigger, p.text) != nil {
			keep = append(keep, p)
		}
	}
	n.pending = keep
}

// checkBoot records a restart (once the clock is right) and alerts about it.
func (n *notifier) checkBoot(a alertChannels, now time.Time) {
	id := n.bootID()
	prev, _ := os.ReadFile(lastBootIDFile)
	if id == "" || strings.TrimSpace(string(prev)) == id {
		return
	}
	_ = vosWriteFileDurable(lastBootIDFile, []byte(id), 0644)
	if len(prev) == 0 {
		return // first run after install: not a restart we saw happen
	}
	cause := daemon.TakePlannedReboot()
	bootAt := now.Add(-n.uptime())
	line := fmt.Sprintf("%d|%s|\n", bootAt.Unix(), cause)
	if f, err := os.OpenFile(rebootHistoryFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		_, _ = f.WriteString(line)
		f.Close()
	}
	why := map[string]string{"user": "restarted from QManager", "watchdog": "restarted by the watchdog"}[cause]
	if why == "" {
		why = "restarted unexpectedly (power loss, unplugged or crashed)"
	}
	n.queue(a, "reboot", "Stick restarted", fmt.Sprintf("%s %s at %s.", deviceName, why, bootAt.Format("2006-01-02 15:04")))
}

// sendTest sends a test alert on one channel right away.
func sendTestAlert(channel string) error {
	a := loadAlertChannels()
	text := deviceName + ": test alert from QManager. Alerts on this channel work."
	switch channel {
	case "sms":
		if !a.smsReady() {
			return errors.New("enable SMS alerts and enter a phone number first")
		}
	case "email":
		if !a.mailReady() {
			return errors.New("enable email alerts and fill in both addresses and the app password first")
		}
	default:
		return errors.New("unknown channel")
	}
	return a.send(channel, "Test", text)
}

// ---------- SMS forwarding ----------

type forwardState struct {
	Primed   bool             `json:"primed"`
	Seen     []string         `json:"seen"`
	Tries    map[string]int   `json:"tries"`
	Failures []map[string]any `json:"failures"`
}

var (
	forwardMu   sync.Mutex
	listInbox   = func() []SMSItem { return smsInbox() }
	forwardSave = func(st forwardState) {
		if b, err := json.Marshal(st); err == nil {
			_ = vosWriteFileDurable(smsForwardStateFile, b, 0600)
		}
	}
)

func smsInbox() []SMSItem {
	bin := smsToolBin()
	if bin == "" {
		return nil
	}
	out, err := exec.Command(bin, "recv").Output()
	if err != nil {
		return nil
	}
	return parseSmsToolOutput(string(out))
}

func loadForwardSettings() (enabled bool, target string) {
	cfg := qmReadJSONFile(smsForwardFile)
	target = qmStr(cfg["target_phone"])
	if target == "" {
		target = qmStr(cfg["number"]) // older builds saved "number"
	}
	return qmBool(cfg["enabled"]), target
}

func loadForwardState() forwardState {
	st := forwardState{Tries: map[string]int{}}
	if b, err := os.ReadFile(smsForwardStateFile); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if st.Tries == nil {
		st.Tries = map[string]int{}
	}
	if st.Failures == nil {
		st.Failures = []map[string]any{}
	}
	return st
}

func smsKey(m SMSItem) string {
	h := sha1.Sum([]byte(m.Sender + "\x00" + m.Date + "\x00" + m.Text))
	return hex.EncodeToString(h[:8])
}

func forwardSMS() {
	forwardMu.Lock()
	defer forwardMu.Unlock()
	enabled, target := loadForwardSettings()
	st := loadForwardState()
	if !enabled || !rePhone.MatchString(target) {
		if st.Primed {
			st.Primed = false
			forwardSave(st)
		}
		return
	}
	msgs := listInbox()
	if msgs == nil {
		return
	}
	seen := map[string]bool{}
	for _, k := range st.Seen {
		seen[k] = true
	}
	changed := false
	mark := func(k string) {
		if !seen[k] {
			seen[k] = true
			st.Seen = append(st.Seen, k)
			changed = true
		}
		delete(st.Tries, k)
	}
	if !st.Primed { // just switched on: the current inbox isn't forwarded
		for _, m := range msgs {
			mark(smsKey(m))
		}
		st.Primed, changed = true, true
	}
	for _, m := range msgs {
		k := smsKey(m)
		if seen[k] {
			continue
		}
		if strings.TrimLeft(m.Sender, "+") == strings.TrimLeft(target, "+") {
			mark(k) // don't bounce the target's own texts back to it
			continue
		}
		text := fmt.Sprintf("From %s: %s", m.Sender, m.Text) // the format the Forwarding page previews
		if r := []rune(text); len(r) > 600 {
			text = string(r[:597]) + "..."
		}
		err := sendSMS(target, text)
		if err == nil {
			mark(k)
			continue
		}
		st.Tries[k]++
		changed = true
		if st.Tries[k] >= forwardTries {
			st.Failures = append([]map[string]any{{"sender": m.Sender, "timestamp": m.Date, "last_error": err.Error()}}, st.Failures...)
			if len(st.Failures) > maxFailures {
				st.Failures = st.Failures[:maxFailures]
			}
			mark(k)
		}
	}
	if len(st.Seen) > maxSeen {
		st.Seen = st.Seen[len(st.Seen)-maxSeen:]
	}
	if changed {
		forwardSave(st)
	}
}

// HandleSMSForwarding serves the Forwarding page.
//
//	GET  -> {settings:{enabled,target_phone}, failures:[{sender,timestamp,last_error}], failure_count}
//	POST {action:"save_settings", enabled, target_phone} | {action:"send_test"} | {action:"clear_failures"}
func (s *Server) HandleSMSForwarding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	reply := func() {
		enabled, target := loadForwardSettings()
		st := loadForwardState()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":       true,
			"settings":      map[string]any{"enabled": enabled, "target_phone": target},
			"failures":      st.Failures,
			"failure_count": len(st.Failures),
		})
	}
	if r.Method != http.MethodPost {
		reply()
		return
	}
	var body struct {
		Action      string `json:"action"`
		Enabled     *bool  `json:"enabled"`
		TargetPhone string `json:"target_phone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "invalid_json"})
		return
	}
	switch body.Action {
	case "save_settings", "":
		target := strings.ReplaceAll(strings.TrimSpace(body.TargetPhone), " ", "")
		enabled := body.Enabled != nil && *body.Enabled
		if target != "" && !rePhone.MatchString(target) {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "invalid_phone", "detail": "Use digits only, optionally starting with +"})
			return
		}
		if enabled && target == "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "invalid_phone", "detail": "Enter the number to forward to"})
			return
		}
		writeJSONFile(smsForwardFile, map[string]any{"enabled": enabled, "target_phone": target})
	case "send_test":
		enabled, target := loadForwardSettings()
		if !enabled || !rePhone.MatchString(target) {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "not_configured", "detail": "Turn forwarding on and save a number first"})
			return
		}
		if err := sendSMS(target, deviceName+": test message from QManager. SMS forwarding works."); err != nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "send_failed", "detail": err.Error()})
			return
		}
	case "clear_failures":
		forwardMu.Lock()
		st := loadForwardState()
		st.Failures = []map[string]any{}
		forwardSave(st)
		forwardMu.Unlock()
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "unknown_action"})
		return
	}
	reply()
}
