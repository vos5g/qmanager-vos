package api

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVosWriteFileDurable(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.conf")
	if err := os.WriteFile(p, []byte("old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := vosWriteFileDurable(p, []byte("new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	st, _ := os.Stat(p)
	if string(b) != "new\n" || st.Mode().Perm() != 0600 {
		t.Fatalf("got %q mode %v", b, st.Mode())
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".*tmp-*"))
	if len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
	if err := vosWriteFileDurable(filepath.Join(dir, "missing", "x"), []byte("x"), 0644); err == nil {
		t.Fatal("expected an error for a missing directory")
	}
}
