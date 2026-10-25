package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.2.0", "v1.1.0", 1},
		{"vos-v1.10.0", "v1.9.3", 1},
		{"v1.2.0", "v1.2.0", 0},
		{"v1.2.0-rc1", "v1.2.0", -1},
		{"v1.2", "v1.2.0", 0},
		{"dev", "v1.0.0", -1},
		{"v1.0.1", "v1.0.0", 1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compare(%s,%s)=%d want %d", c.a, c.b, got, c.want)
		}
	}
	if supportsGuard("v1.1.0") || !supportsGuard("v1.2.0") || !supportsGuard("dev") {
		t.Fatal("supportsGuard")
	}
}

func TestSha256From(t *testing.T) {
	sums := strings.Repeat("a", 64) + "  qmanager-core-armv7\n" + strings.Repeat("b", 64) + " *tailscaled-tiny-armv7\n"
	if sha256From(sums, "tailscaled-tiny-armv7") != strings.Repeat("b", 64) || sha256From(sums, "nope") != "" {
		t.Fatal("sha256From")
	}
}

// fakeGitHub serves a release list and its assets.
func fakeGitHub(t *testing.T, bin []byte, badSum bool) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(bin)
	hexSum := hex.EncodeToString(sum[:])
	if badSum {
		hexSum = strings.Repeat("0", 64)
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases":
			rel := func(tag string, pre bool) map[string]any {
				return map[string]any{"tag_name": tag, "name": "QManager " + tag, "body": "notes", "draft": false, "prerelease": pre,
					"published_at": "2026-10-25T00:00:00Z", "html_url": srv.URL + "/r/" + tag,
					"assets": []map[string]any{
						{"name": "qmanager-core-armv7", "browser_download_url": srv.URL + "/dl/" + tag + "/bin", "size": len(bin)},
						{"name": "SHA256SUMS.txt", "browser_download_url": srv.URL + "/dl/" + tag + "/sums", "size": 100},
					}}
			}
			_ = json.NewEncoder(w).Encode([]any{rel("vos-v1.1.0", false), rel("vos-v1.3.0-rc1", true), rel("vos-v1.2.0", false),
				map[string]any{"tag_name": "v9.9.9", "assets": []any{}}})
		default:
			if strings.HasSuffix(r.URL.Path, "/bin") {
				_, _ = w.Write(bin)
			} else if strings.HasSuffix(r.URL.Path, "/sums") {
				fmt.Fprintf(w, "%s  qmanager-core-armv7\n", hexSum)
			} else {
				http.NotFound(w, r)
			}
		}
	}))
	return srv
}

func setupUpdateTest(t *testing.T, srvURL string) (dir string, restarted *int, guarded *int) {
	t.Helper()
	dir = t.TempDir()
	old := []string{ghReleasesURL, qmBinPath, updateStateFile, updatePendingFile, updateRolledFile}
	oldCur, oldRestart, oldGuard, oldCheck := currentVersion, restartSelf, scheduleUpdateGuard, checkARMExecutable
	t.Cleanup(func() {
		ghReleasesURL, qmBinPath, updateStateFile, updatePendingFile, updateRolledFile = old[0], old[1], old[2], old[3], old[4]
		currentVersion, restartSelf, scheduleUpdateGuard, checkARMExecutable = oldCur, oldRestart, oldGuard, oldCheck
		ghCache.Lock()
		ghCache.list, ghCache.at = nil, time.Time{}
		ghCache.Unlock()
		upd.set("idle", "", "")
	})
	ghReleasesURL = srvURL + "/releases"
	qmBinPath = filepath.Join(dir, "qmanager-core")
	updateStateFile = filepath.Join(dir, "update_state.json")
	updatePendingFile = filepath.Join(dir, "update_pending")
	updateRolledFile = filepath.Join(dir, "rolled")
	if err := os.WriteFile(qmBinPath, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}
	cur := "v1.1.0"
	currentVersion = func() string { return cur }
	r, g := 0, 0
	restartSelf = func() { r++; cur = "swapped" }
	scheduleUpdateGuard = func() error { g++; return nil }
	checkARMExecutable = func(string) error { return nil }
	ghCache.Lock()
	ghCache.list, ghCache.at = nil, time.Time{}
	ghCache.Unlock()
	return dir, &r, &g
}

func postUpdate(t *testing.T, body string) map[string]any {
	t.Helper()
	rr := httptest.NewRecorder()
	(&Server{}).HandleUpdate(rr, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad json %q", rr.Body.String())
	}
	return out
}

func waitIdle(t *testing.T) string {
	t.Helper()
	for i := 0; i < 200; i++ {
		upd.mu.Lock()
		s := upd.State
		upd.mu.Unlock()
		if s != "downloading" && s != "installing" {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("update did not finish")
	return ""
}

func TestUpdateInstallAndRollback(t *testing.T) {
	newBin := []byte("new binary v1.2.0")
	srv := fakeGitHub(t, newBin, false)
	defer srv.Close()
	_, restarted, guarded := setupUpdateTest(t, srv.URL)

	rr := httptest.NewRecorder()
	(&Server{}).HandleUpdate(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	var st map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	rels := st["releases"].([]any)
	if len(rels) != 3 || rels[0].(map[string]any)["version"] != "v1.3.0-rc1" || st["latest_version"] != "v1.2.0" || st["update_available"] != true {
		t.Fatalf("list: %v", st)
	}

	if out := postUpdate(t, `{"action":"install","tag":"vos-v1.1.0"}`); out["success"] != false {
		t.Fatal("reinstalling the running version accepted")
	}
	if out := postUpdate(t, `{"action":"install","tag":"vos-v1.2.0"}`); out["success"] != true {
		t.Fatalf("install: %v", out)
	}
	if s := waitIdle(t); s != "restarting" {
		t.Fatalf("state %s: %s", s, upd.Message)
	}
	got, _ := os.ReadFile(qmBinPath)
	prev, _ := os.ReadFile(qmBinPath + ".prev")
	if string(got) != string(newBin) || string(prev) != "old binary" || *restarted != 1 || *guarded != 1 {
		t.Fatalf("swap: bin=%q prev=%q restarted=%d guarded=%d", got, prev, *restarted, *guarded)
	}
	if _, err := os.Stat(updatePendingFile); err != nil {
		t.Fatal("no pending marker")
	}
	if s := loadUpdateState(); s.PreviousVersion != "v1.1.0" {
		t.Fatalf("state %+v", s)
	}

	// Roll back swaps the two; v1.1.0 predates the guard, so none is armed.
	upd.set("idle", "", "")
	os.Remove(updatePendingFile)
	if out := postUpdate(t, `{"action":"rollback"}`); out["success"] != true {
		t.Fatalf("rollback: %v", out)
	}
	got, _ = os.ReadFile(qmBinPath)
	prev, _ = os.ReadFile(qmBinPath + ".prev")
	if string(got) != "old binary" || string(prev) != string(newBin) || *restarted != 2 || *guarded != 1 {
		t.Fatalf("rollback swap: bin=%q prev=%q restarted=%d guarded=%d", got, prev, *restarted, *guarded)
	}
}

func TestUpdateRejectsBadChecksum(t *testing.T) {
	srv := fakeGitHub(t, []byte("tampered"), true)
	defer srv.Close()
	_, restarted, _ := setupUpdateTest(t, srv.URL)
	if out := postUpdate(t, `{"action":"install","tag":"vos-v1.2.0"}`); out["success"] != true {
		t.Fatalf("install: %v", out)
	}
	if s := waitIdle(t); s != "error" || !strings.Contains(upd.Message, "checksum") {
		t.Fatalf("state %s %q", s, upd.Message)
	}
	got, _ := os.ReadFile(qmBinPath)
	if string(got) != "old binary" || *restarted != 0 {
		t.Fatal("bad download was installed")
	}
	if _, err := os.Stat(qmBinPath + ".download"); err == nil {
		t.Fatal("download left behind")
	}
}

func TestStartUpdaterClearsMarker(t *testing.T) {
	_, _, _ = setupUpdateTest(t, "http://127.0.0.1:1")
	old := updateHealthy
	updateHealthy = 20 * time.Millisecond
	defer func() { updateHealthy = old }()
	if err := os.WriteFile(updatePendingFile, []byte("v1.2.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	StartUpdater()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(updatePendingFile); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("marker not cleared")
}
