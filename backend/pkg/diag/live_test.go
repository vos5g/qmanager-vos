package diag

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"
)

// Opt-in, read-only check. Never writes EFS/OEM or changes USB.
func TestLiveEFS(t *testing.T) {
	helper := os.Getenv("VOS_DIAG_TEST_HELPER")
	if helper == "" {
		t.Skip("set VOS_DIAG_TEST_HELPER on the modem to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c, e := Start(ctx, helper)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	original, e := os.ReadFile("/oem/modem_config")
	if e != nil {
		t.Fatal(e)
	}
	p, e := ParsePolicy(original)
	if e != nil {
		t.Fatal(e)
	}
	live, e := c.ReadFile("/policyman/carrier_policy.xml")
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(live, p.Policy) {
		t.Fatal("OEM policy differs from live EFS")
	}
	t.Logf("OEM/live policy match; supported=%v unlocked=%v", p.Supported, p.Unlocked)
	for _, item := range []string{"/policyman/early_mcc_scan", "/nv/item_files/modem/mmode/nr_band_pref"} {
		b, err := c.ReadFile(item)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("Read item file %s (%d bytes)", item, len(b))
	}
	for _, dir := range []string{"/policyman", "/nv/item_files/modem", "/nv/item_files/modem/lte", "/nv/item_files/modem/nr5g", "/nv/item_files/modem/mmode", "/nv/item_files/modem/lte/rrc", "/nv/item_files/modem/nr5g/RRC", "/nv/item_files/modem/lte/rrc/cap"} {
		entries, e := c.List(dir)
		if e != nil {
			t.Fatal(e)
		}
		for _, entry := range entries {
			t.Logf("%s %s %d bytes", entry.Type, entry.Path, entry.Size)
		}
	}
}
