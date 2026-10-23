package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKmModulesLineRegex(t *testing.T) {
	for in, want := range map[string]bool{`MODULES=""`: true, `MODULES="bbr wireguard"`: true, `MODULES="bbr; rm -rf /"`: false, `# MODULES="bbr"`: false} {
		if got := kmModulesRe.MatchString(in); got != want {
			t.Errorf("%q: got %v", in, got)
		}
	}
}

func TestKmSetEnabledKeepsFile(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "vosclean.conf")
	orig := "SSH_PASSWORD_AUTH=no\n# comment\nMODULES=\"bbr\"\nQM_PORT=8080\n"
	if err := os.WriteFile(conf, []byte(orig), 0644); err != nil {
		t.Fatal(err)
	}
	saved := kmConfPath
	kmConfPath = conf
	defer func() { kmConfPath = saved }()
	if err := kmSetEnabled(map[string]bool{"wireguard": true, "bbr": true}, kmFallbackGroups); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(conf)
	got := string(b)
	if !strings.Contains(got, `MODULES="wireguard bbr"`) || !strings.Contains(got, "QM_PORT=8080") || strings.Count(got, "MODULES=") != 1 {
		t.Fatalf("unexpected file:\n%s", got)
	}
	if en := kmEnabled(); !en["wireguard"] || !en["bbr"] || en["xtmatch"] {
		t.Fatalf("read back: %v", en)
	}
}
