package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qmanager-backend/pkg/daemon"
)

type sent struct{ ch, to, text string }

func notifyTestEnv(t *testing.T) *[]sent {
	d := t.TempDir()
	smsConfigPath = filepath.Join(d, "sms.json")
	emailConfigPath = filepath.Join(d, "email.json")
	routingConfigPath = filepath.Join(d, "routing.json")
	alertLogPath = filepath.Join(d, "alertlog.json")
	smsForwardFile = filepath.Join(d, "fwd.json")
	smsForwardStateFile = filepath.Join(d, "fwdstate.json")
	lastBootIDFile = filepath.Join(d, "bootid")
	rebootHistoryFile = filepath.Join(d, "crash.log")
	daemon.PlannedRebootFile = filepath.Join(d, "planned_reboot")
	var out []sent
	sendSMS = func(to, text string) error { out = append(out, sent{"sms", to, text}); return nil }
	sendEmail = func(from, pw, to, subj, body string) error {
		out = append(out, sent{"email", to, subj + "|" + body})
		return nil
	}
	return &out
}

func writeTestJSON(t *testing.T, p string, v any) {
	b, _ := json.Marshal(v)
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestEmailServer(t *testing.T) {
	for addr, want := range map[string]string{"a@gmail.com": "smtp.gmail.com:587", "a@outlook.com": "smtp-mail.outlook.com:587", "a@yahoo.co.uk": "smtp.mail.yahoo.com:465", "a@example.org": "smtp.example.org:587"} {
		h, p := emailServer(addr)
		if got := h + ":" + itoaPort(p); got != want {
			t.Errorf("%s: %s, want %s", addr, got, want)
		}
	}
}

func itoaPort(p int) string { b, _ := json.Marshal(p); return string(b) }

func TestSMSForwarding(t *testing.T) {
	out := notifyTestEnv(t)
	inbox := []SMSItem{{Sender: "+447700900001", Date: "10/24/26 18:00:00", Text: "old"}}
	listInbox = func() []SMSItem { return inbox }

	writeTestJSON(t, smsForwardFile, map[string]any{"enabled": true, "target_phone": "+447700900999"})
	forwardSMS() // primes: the existing message isn't forwarded
	if len(*out) != 0 {
		t.Fatalf("forwarded the backlog: %v", *out)
	}
	inbox = append(inbox,
		SMSItem{Sender: "+447700900002", Date: "10/24/26 19:00:00", Text: "hello"},
		SMSItem{Sender: "+447700900999", Date: "10/24/26 19:01:00", Text: "from the target"})
	forwardSMS()
	forwardSMS() // second pass: nothing new
	if len(*out) != 1 || (*out)[0].to != "+447700900999" || !strings.Contains((*out)[0].text, "hello") || !strings.Contains((*out)[0].text, "+447700900002") {
		t.Fatalf("forward: %v", *out)
	}

	// a message that keeps failing ends up under failures after 3 tries
	sendSMS = func(string, string) error { return os.ErrDeadlineExceeded }
	inbox = append(inbox, SMSItem{Sender: "+447700900003", Date: "10/24/26 19:05:00", Text: "stuck"})
	for i := 0; i < 4; i++ {
		forwardSMS()
	}
	st := loadForwardState()
	if len(st.Failures) != 1 || st.Failures[0]["sender"] != "+447700900003" {
		t.Fatalf("failures %v", st.Failures)
	}
}

func TestAlerts(t *testing.T) {
	out := notifyTestEnv(t)
	writeTestJSON(t, smsConfigPath, map[string]any{"enabled": true, "recipient_phone": "+447700900111", "threshold_minutes": 5})
	writeTestJSON(t, emailConfigPath, map[string]any{"enabled": true, "sender_email": "a@gmail.com", "recipient_email": "b@example.com", "app_password": "x", "threshold_minutes": 5})
	writeTestJSON(t, routingConfigPath, map[string]any{"events": map[string]any{
		"connection_lost":     map[string]any{"sms": true, "email": true},
		"connection_restored": map[string]any{"sms": false, "email": true},
		"reboot":              map[string]any{"sms": true, "email": false},
	}})
	n := newNotifier()
	clock := time.Date(2026, 10, 24, 18, 0, 0, 0, time.UTC)
	n.now = func() time.Time { return clock }
	n.uptime = func() time.Duration { return time.Hour }
	n.bootID = func() string { return "boot-a" }
	conn := true
	n.connected = func() (bool, bool) { return conn, true }

	n.tick() // first run after install: boot ID recorded, no alert
	conn = false
	n.tick()
	clock = clock.Add(4 * time.Minute)
	n.tick()
	if len(*out) != 0 {
		t.Fatalf("alerted before the threshold: %v", *out)
	}
	clock = clock.Add(2 * time.Minute)
	n.tick() // 6 min down: SMS "lost" (email can't carry it)
	n.tick()
	if len(*out) != 1 || (*out)[0].ch != "sms" || !strings.Contains((*out)[0].text, "no internet") {
		t.Fatalf("lost: %v", *out)
	}
	clock = clock.Add(time.Minute)
	conn = true
	n.tick() // restored: email only (routing)
	if len(*out) != 2 || (*out)[1].ch != "email" || !strings.Contains((*out)[1].text, "restored after 7 min") {
		t.Fatalf("restored: %v", *out)
	}

	// a restart: new boot ID, planned by the user
	n2 := newNotifier()
	n2.now = func() time.Time { return clock }
	n2.uptime = func() time.Duration { return time.Minute }
	n2.bootID = func() string { return "boot-b" }
	n2.connected = func() (bool, bool) { return true, true }
	n2.tick()
	if len(*out) != 3 || (*out)[2].ch != "sms" || !strings.Contains((*out)[2].text, "restarted unexpectedly") {
		t.Fatalf("reboot: %v", *out)
	}
	if b, _ := os.ReadFile(rebootHistoryFile); !strings.Contains(string(b), "|unplanned|") {
		t.Fatalf("history %q", b)
	}
	var log []map[string]any
	b, _ := os.ReadFile(alertLogPath)
	_ = json.Unmarshal(b, &log)
	if len(log) != 3 || log[0]["status"] != "sent" {
		t.Fatalf("alert log %v", log)
	}
}
