package at

import (
	"testing"
)

// vosclean build: without qcmd the client must fail loudly, never return mock data.
func TestClient(t *testing.T) {
	client := NewClient("/dev/null")

	resp, err := client.Exec("ATI")
	if err == nil || resp != "ERROR" {
		t.Errorf("expected ERROR without qcmd, got %q (err: %v)", resp, err)
	}

	_, errInv := client.Exec("INVALID")
	if errInv == nil {
		t.Errorf("expected error for non-AT command")
	}
}
