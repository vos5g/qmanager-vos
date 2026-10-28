package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qmanager-backend/pkg/diag"
)

func TestUSBModeValidationAndBindWrite(t *testing.T) {
	oldRoot, oldPID, oldBacking := toolsRoot, toolsPID, toolsPIDBacking
	t.Cleanup(func() { toolsRoot, toolsPID, toolsPIDBacking = oldRoot, oldPID, oldBacking })
	dir := t.TempDir()
	toolsRoot = dir
	toolsPIDBacking = filepath.Join(dir, "backing")
	toolsPID = filepath.Join(dir, "mount")
	if e := os.WriteFile(toolsPIDBacking, []byte("9063\n"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.Link(toolsPIDBacking, toolsPID); e != nil {
		t.Fatal(e)
	}
	if e := setUSB("9064;reboot"); e == nil {
		t.Fatal("accepted injection")
	}
	if e := setUSB("9064"); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(toolsPIDBacking)
	if string(b) != "9064\n" {
		t.Fatalf("backing inode not changed: %q", b)
	}
	previous, _ := os.ReadFile(filepath.Join(dir, "etc/usb-mode.previous"))
	if string(previous) != "9063\n" {
		t.Fatal("missing recovery copy")
	}
	os.Remove(toolsPID)
	if err := os.WriteFile(toolsPID, []byte("9063\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if e := setUSB("9165"); e == nil {
		t.Fatal("accepted unexpected mount")
	}
}
func TestUSBModeRequiresExplicitConfirmation(t *testing.T) {
	s := &Server{}
	for _, body := range []string{`{"pid":"9064"}`, `{"pid":"0000","confirm":true}`, `not json`} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.HandleUSBMode(w, r)
		if w.Code != 400 {
			t.Fatalf("accepted unconfirmed or invalid request %s", body)
		}
	}
}
func TestBandStateRejectsTraversal(t *testing.T) {
	old := toolsRoot
	toolsRoot = t.TempDir()
	t.Cleanup(func() { toolsRoot = old })
	if err := os.MkdirAll(filepath.Join(toolsRoot, "etc"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bandStatePath(), []byte(`{"id":"../other-device"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, e := loadBand(); e == nil {
		t.Fatal("accepted backup traversal")
	}
}
func TestLivePolicyBackupReadOnly(t *testing.T) {
	helper := os.Getenv("VOS_DIAG_TEST_HELPER")
	if helper == "" {
		t.Skip("opt-in read-only modem test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c, e := diag.Start(ctx, helper)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	dir := t.TempDir()
	if e = backupPolicyTree(c, "/policyman", dir, 0); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(dir, "carrier_policy.xml"))
	if e != nil {
		t.Fatal(e)
	}
	source, e := c.ReadFile("/policyman/carrier_policy.xml")
	if e != nil || !bytes.Equal(b, source) {
		t.Fatal("policy backup mismatch", e)
	}
	t.Log("Complete /policyman tree, including item files, backed up and read back without modem writes")
}

// Test actual API requests against a valid temporary bind-file fixture. A
// rejected destination must leave both the selector and its backing inode alone.
func TestUSBModesBlockLossOfSSH(t *testing.T) {
	oldRoot, oldPID, oldBacking := toolsRoot, toolsPID, toolsPIDBacking
	t.Cleanup(func() { toolsRoot, toolsPID, toolsPIDBacking = oldRoot, oldPID, oldBacking })
	toolsRoot = t.TempDir()
	toolsPIDBacking = filepath.Join(toolsRoot, "backing")
	toolsPID = filepath.Join(toolsRoot, "mount")
	if e := os.WriteFile(toolsPIDBacking, []byte("9063\n"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.Link(toolsPIDBacking, toolsPID); e != nil {
		t.Fatal(e)
	}
	s := &Server{}
	for _, pid := range []string{"9091", "905B", "905b", "9058", "0000", "9064;reboot"} {
		body := fmt.Sprintf(`{"pid":%q,"confirm":true}`, pid)
		rec := httptest.NewRecorder()
		s.HandleUSBMode(rec, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		if rec.Code != 400 {
			t.Fatalf("accepted mode %s: %d", pid, rec.Code)
		}
		b, e := os.ReadFile(toolsPIDBacking)
		if e != nil || string(b) != "9063\n" {
			t.Fatalf("blocked mode %s changed selector: %q %v", pid, b, e)
		}
	}
	// A legacy unsafe setting can still be changed back to a safe composition.
	if e := os.WriteFile(toolsPIDBacking, []byte("9091\n"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := setUSB("9063"); e != nil {
		t.Fatalf("could not recover from legacy QMI-only mode: %v", e)
	}
	b, _ := os.ReadFile(toolsPIDBacking)
	if string(b) != "9063\n" {
		t.Fatalf("recovery did not restore USB networking: %q", b)
	}
}

func TestUSBCurrentUsesActiveGadgetNotBootSelector(t *testing.T) {
	oldGadget, oldLegacy, oldPID := toolsUSBGadget, toolsUSBLegacy, toolsPID
	t.Cleanup(func() { toolsUSBGadget, toolsUSBLegacy, toolsPID = oldGadget, oldLegacy, oldPID })
	dir := t.TempDir()
	toolsUSBGadget = filepath.Join(dir, "gadget")
	toolsUSBLegacy = filepath.Join(dir, "legacy")
	toolsPID = filepath.Join(dir, "next-boot")
	for _, d := range []string{toolsUSBGadget, toolsUSBLegacy} {
		if e := os.MkdirAll(d, 0700); e != nil {
			t.Fatal(e)
		}
	}
	write := func(p, v string) {
		t.Helper()
		if e := os.WriteFile(p, []byte(v), 0600); e != nil {
			t.Fatal(e)
		}
	}
	write(filepath.Join(toolsUSBGadget, "UDC"), "a600000.dwc3\n")
	write(filepath.Join(toolsUSBGadget, "idProduct"), "0x9063\n")
	write(toolsPID, "9064\n")
	rec := httptest.NewRecorder()
	(&Server{}).HandleUSBMode(rec, httptest.NewRequest("GET", "/", nil))
	var result struct {
		Current    string `json:"current"`
		CurrentPID string `json:"current_pid"`
		NextPID    string `json:"next_pid"`
	}
	if e := json.Unmarshal(rec.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	if result.CurrentPID != "9063" || result.NextPID != "9064" || result.Current != "Factory default (9063)" {
		t.Fatalf("running/next-boot mode confused: %+v", result)
	}
	// A disabled gadget's saved identifier does not prove it is currently running.
	write(filepath.Join(toolsUSBGadget, "UDC"), "\n")
	if pid := currentUSBPID(); pid != "" {
		t.Fatalf("reported unbound gadget as active: %q", pid)
	}
	write(filepath.Join(toolsUSBLegacy, "enable"), "1\n")
	write(filepath.Join(toolsUSBLegacy, "idProduct"), "9165\n")
	if pid := currentUSBPID(); pid != "9165" {
		t.Fatalf("legacy gadget not detected: %q", pid)
	}
	write(filepath.Join(toolsUSBLegacy, "idProduct"), "<LTE PCC INFO>:EARFCN 99\nOK")
	if pid := currentUSBPID(); pid != "" {
		t.Fatalf("accepted unrelated modem response: %q", pid)
	}
}

func TestUSBPIDRejectsUnrelatedResponses(t *testing.T) {
	for _, v := range []string{"AT+USBCOMP?\n+USBCOMP? = user(9063)\nOK", "<LTE PCC INFO>:EARFCN 99\nOK", "9063 garbage", "0x9063\nOK", "+063", ""} {
		if pid := parseUSBPID(v); pid != "" {
			t.Fatalf("accepted non-USB identifier %q: %q", v, pid)
		}
	}
	for _, v := range []string{"9063", "0x9063\n", " 0X9063 "} {
		if parseUSBPID(v) != "9063" {
			t.Fatalf("rejected valid USB identifier %q", v)
		}
	}
}
