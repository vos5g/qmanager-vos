package daemon

import (
	"path/filepath"
	"testing"
)

func TestConnUptime(t *testing.T) {
	f := filepath.Join(t.TempDir(), "since")
	if got := connUptime(true, 100, f, "boot-a"); got != 0 {
		t.Fatalf("first sight should start at 0, got %d", got)
	}
	if got := connUptime(true, 160, f, "boot-a"); got != 60 {
		t.Fatalf("want 60, got %d", got)
	}
	if got := connUptime(true, 50, f, "boot-b"); got != 0 { // new boot: restart the count
		t.Fatalf("new boot should restart, got %d", got)
	}
	if got := connUptime(false, 70, f, "boot-b"); got != 0 {
		t.Fatalf("down should be 0, got %d", got)
	}
	if got := connUptime(true, 80, f, "boot-b"); got != 0 { // reconnected: new count
		t.Fatalf("reconnect should restart, got %d", got)
	}
	if got := connUptime(true, 95, f, "boot-b"); got != 15 {
		t.Fatalf("want 15, got %d", got)
	}
}
