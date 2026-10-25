package api

// vosclean: Software Update from GitHub releases (vos5g/qmanager-vos).
//
// Every release tagged vos-v* carries qmanager-core-armv7 and SHA256SUMS.txt.
// Installing one downloads it next to the running binary, checks the size, the
// SHA-256 and that it is an ARM executable, keeps the running binary as
// qmanager-core.prev and swaps the new one in. QManager then exits; systemd
// (Restart=always, set by `vosclean qm`) starts the new binary.
//
// A binary that never comes up must not leave the stick without its web UI, so
// before restarting QManager leaves a marker file and a one-shot systemd timer
// (outside QManager's own cgroup). A new build removes the marker once it has
// run for 45 s; if the marker is still there after 3 minutes the timer puts
// qmanager-core.prev back and restarts it. Releases older than v1.2.0 don't
// know about the marker, so installing one of those skips the guard.
//
// "Roll back" swaps qmanager-core and qmanager-core.prev, so it can be undone
// the same way.
//
//	GET  /cgi-bin/quecmanager/system/update.sh             status + release list
//	POST {action:"check"}                                  refresh the list now
//	POST {action:"install", tag:"vos-v1.2.0"}              install that release
//	POST {action:"rollback"}                               swap with the previous binary

import (
	"bufio"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"qmanager-backend/pkg/daemon"
)

const (
	ghRepo           = "vos5g/qmanager-vos"
	ghTagPrefix      = "vos-"
	qmAssetName      = "qmanager-core-armv7"
	sumsAssetName    = "SHA256SUMS.txt"
	tsTinyAssetName  = "tailscaled-tiny-armv7"
	updaterSince     = "v1.2.0" // first release that clears the update marker
	updateGuardDelay = 180      // seconds before the guard rolls back
)

// updateHealthy is how long a new build runs before it disarms the guard.
var updateHealthy = 45 * time.Second

var (
	ghReleasesURL     = "https://api.github.com/repos/" + ghRepo + "/releases?per_page=30"
	qmBinPath         = "/data/vosclean/qm/bin/qmanager-core"
	updateStateFile   = "/data/vosclean/qm/etc/update_state.json"
	updatePendingFile = "/data/vosclean/qm/etc/update_pending"
	updateRolledFile  = "/data/vosclean/qm/etc/update_rolled_back"
	ghHTTP            = &http.Client{Timeout: 20 * time.Second}
	ghDownloadHTTP    = &http.Client{Timeout: 10 * time.Minute}
	currentVersion    = daemon.Version
	// restartSelf ends this process; systemd starts the binary now in place.
	restartSelf = func() {
		go func() {
			time.Sleep(1500 * time.Millisecond)
			_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		}()
	}
	// scheduleUpdateGuard starts the one-shot rollback timer described above.
	scheduleUpdateGuard = func() error {
		b := qmBinPath
		script := fmt.Sprintf(`P=%s; B=%s
[ -f "$P" ] || exit 0
if [ -x "$B.prev" ]; then mv -f "$B.prev" "$B"; fi
mv -f "$P" %s
systemctl restart qmanager`, updatePendingFile, b, updateRolledFile)
		out, err := exec.Command("systemd-run", "--unit=qmanager-update-guard-"+strconv.FormatInt(time.Now().Unix(), 10),
			"--collect", "--timer-property=AccuracySec=1s", // the default is up to a minute late
			fmt.Sprintf("--on-active=%d", updateGuardDelay), "/bin/sh", "-c", script).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemd-run: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	freeBytes = func(dir string) uint64 {
		var st syscall.Statfs_t
		if syscall.Statfs(dir, &st) != nil {
			return 0
		}
		return uint64(st.Bavail) * uint64(st.Bsize)
	}
)

// ---------------------------------------------------------------------------
// GitHub releases
// ---------------------------------------------------------------------------

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

type ghRelease struct {
	Tag        string    `json:"tag_name"`
	Name       string    `json:"name"`
	Body       string    `json:"body"`
	Published  string    `json:"published_at"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Assets     []ghAsset `json:"assets"`
	HTMLURL    string    `json:"html_url"`
}

func (r ghRelease) asset(name string) *ghAsset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

func (r ghRelease) version() string { return strings.TrimPrefix(r.Tag, ghTagPrefix) }

var ghCache struct {
	sync.Mutex
	list []ghRelease
	at   time.Time
	err  string
}

// fetchReleases lists vos-v* releases, newest first (cached 10 minutes:
// GitHub allows 60 unauthenticated requests an hour).
func fetchReleases(force bool) ([]ghRelease, error) {
	ghCache.Lock()
	defer ghCache.Unlock()
	if !force && ghCache.list != nil && time.Since(ghCache.at) < 10*time.Minute {
		return ghCache.list, nil
	}
	req, err := http.NewRequest(http.MethodGet, ghReleasesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "QManager-VOS")
	resp, err := ghHTTP.Do(req)
	if err != nil {
		ghCache.err = err.Error()
		return ghCache.list, fmt.Errorf("can't reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		ghCache.err = fmt.Sprintf("GitHub answered HTTP %d", resp.StatusCode)
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			ghCache.err += " (rate limit: try again in an hour)"
		}
		return ghCache.list, errors.New(ghCache.err)
	}
	var all []ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&all); err != nil {
		return ghCache.list, err
	}
	var list []ghRelease
	for _, r := range all {
		if r.Draft || !strings.HasPrefix(r.Tag, ghTagPrefix+"v") || r.asset(qmAssetName) == nil {
			continue
		}
		list = append(list, r)
	}
	sort.SliceStable(list, func(i, j int) bool { return compareVersions(list[i].version(), list[j].version()) > 0 })
	ghCache.list, ghCache.at, ghCache.err = list, time.Now(), ""
	return list, nil
}

// compareVersions compares "v1.2.0"-style versions; a pre-release suffix
// ("-rc1") sorts before the release. Unparseable versions sort lowest.
func compareVersions(a, b string) int {
	pa, sa, oka := parseVersion(a)
	pb, sb, okb := parseVersion(b)
	switch {
	case !oka && !okb:
		return strings.Compare(a, b)
	case !oka:
		return -1
	case !okb:
		return 1
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case sa == sb:
		return 0
	case sa == "":
		return 1
	case sb == "":
		return -1
	}
	return strings.Compare(sa, sb)
}

func parseVersion(v string) ([3]int, string, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimPrefix(v, ghTagPrefix), "v")
	suffix := ""
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v, suffix = v[:i], v[i+1:]
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return out, "", false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, "", false
		}
		out[i] = n
	}
	return out, suffix, true
}

func isReleaseVersion(v string) bool {
	_, _, ok := parseVersion(v)
	return ok
}

// sha256From reads "<hex>  <name>" lines and returns the hash for name.
func sha256From(sums, name string) string {
	sc := bufio.NewScanner(strings.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

func ghGet(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "QManager-VOS")
	resp, err := ghHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// downloadAsset fetches a release asset to dest, checking its size and the
// SHA-256 listed in the release's SHA256SUMS.txt. progress gets bytes so far.
func downloadAsset(rel ghRelease, name, dest string, progress func(done, total int64)) error {
	a := rel.asset(name)
	if a == nil {
		return fmt.Errorf("%s has no %s", rel.Tag, name)
	}
	sa := rel.asset(sumsAssetName)
	if sa == nil {
		return fmt.Errorf("%s has no %s to check the download against", rel.Tag, sumsAssetName)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	sums, err := ghGet(ctx, sa.URL, 64<<10)
	if err != nil {
		return fmt.Errorf("checksums: %w", err)
	}
	want := sha256From(string(sums), name)
	if want == "" {
		return fmt.Errorf("%s doesn't list %s", sumsAssetName, name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "QManager-VOS")
	resp, err := ghDownloadHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil {
		return err
	}
	h := sha256.New()
	var done int64
	buf := make([]byte, 64<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				os.Remove(dest)
				return fmt.Errorf("write: %w (disk full?)", werr)
			}
			h.Write(buf[:n])
			done += int64(n)
			if done > a.Size+1 {
				f.Close()
				os.Remove(dest)
				return errors.New("download larger than announced")
			}
			if progress != nil {
				progress(done, a.Size)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(dest)
			return fmt.Errorf("download: %w", rerr)
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(dest)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(dest)
		return err
	}
	if done != a.Size {
		os.Remove(dest)
		return fmt.Errorf("download incomplete: %d of %d bytes", done, a.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(dest)
		return fmt.Errorf("checksum mismatch for %s", name)
	}
	return nil
}

// latestAsset finds the newest release that has the named asset.
func latestAsset(name string) (ghRelease, error) {
	list, err := fetchReleases(false)
	if err != nil && len(list) == 0 {
		return ghRelease{}, err
	}
	for _, r := range list {
		if !r.Prerelease && r.asset(name) != nil && r.asset(sumsAssetName) != nil {
			return r, nil
		}
	}
	return ghRelease{}, fmt.Errorf("no release on github.com/%s has %s", ghRepo, name)
}

// checkARMExecutable makes sure a download is an ARM Linux executable.
var checkARMExecutable = func(path string) error {
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("not an executable: %w", err)
	}
	defer f.Close()
	if f.Machine != elf.EM_ARM || f.Class != elf.ELFCLASS32 {
		return fmt.Errorf("built for %v, not this stick's ARMv7", f.Machine)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Install / roll back
// ---------------------------------------------------------------------------

type updateState struct {
	PreviousVersion string `json:"previous_version"`
	InstalledAt     int64  `json:"installed_at"`
	LastResult      string `json:"last_result"`
}

type updateProgress struct {
	mu      sync.Mutex
	State   string `json:"state"` // idle | downloading | installing | restarting | error
	Target  string `json:"target"`
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	Message string `json:"message"`
}

var upd = &updateProgress{State: "idle"}

func (u *updateProgress) set(state, target, msg string) {
	u.mu.Lock()
	u.State, u.Target, u.Message = state, target, msg
	if state != "downloading" {
		u.Done, u.Total = 0, 0
	}
	u.mu.Unlock()
}

func (u *updateProgress) busy() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.State == "downloading" || u.State == "installing" || u.State == "restarting"
}

func loadUpdateState() updateState {
	var s updateState
	if b, err := os.ReadFile(updateStateFile); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func saveUpdateState(s updateState) {
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := vosWriteFileDurable(updateStateFile, b, 0644); err != nil {
		log.Printf("update: save state: %v", err)
	}
}

// supportsGuard: does this version clear the update marker once running?
func supportsGuard(v string) bool {
	return !isReleaseVersion(v) || compareVersions(v, updaterSince) >= 0
}

// installRelease downloads, verifies and swaps in rel, then restarts.
func installRelease(rel ghRelease) {
	target := rel.version()
	a := rel.asset(qmAssetName)
	dir := qmBinPath[:strings.LastIndexByte(qmBinPath, '/')]
	need := uint64(a.Size) + 4<<20
	if free := freeBytes(dir); free < need {
		// The previous binary is the only thing worth freeing.
		if fi, err := os.Stat(qmBinPath + ".prev"); err == nil && free+uint64(fi.Size()) >= need {
			_ = os.Remove(qmBinPath + ".prev")
		} else {
			upd.set("error", target, fmt.Sprintf("not enough space on /data: %d MB free, %d MB needed", free>>20, need>>20))
			return
		}
	}
	upd.mu.Lock()
	upd.State, upd.Target, upd.Message, upd.Done, upd.Total = "downloading", target, "", 0, a.Size
	upd.mu.Unlock()
	tmp := qmBinPath + ".download"
	err := downloadAsset(rel, qmAssetName, tmp, func(done, total int64) {
		upd.mu.Lock()
		upd.Done, upd.Total = done, total
		upd.mu.Unlock()
	})
	if err == nil {
		err = checkARMExecutable(tmp)
	}
	if err != nil {
		os.Remove(tmp)
		upd.set("error", target, err.Error())
		return
	}
	upd.set("installing", target, "")
	if err := swapInBinary(tmp, target); err != nil {
		os.Remove(tmp)
		upd.set("error", target, err.Error())
		return
	}
	upd.set("restarting", target, "")
	restartSelf()
}

// swapInBinary makes newPath the running binary, keeping the current one as
// .prev, records the versions and arms the rollback guard when the target
// version knows how to disarm it.
func swapInBinary(newPath, target string) error {
	cur := currentVersion()
	if err := os.Rename(qmBinPath, qmBinPath+".prev"); err != nil {
		return fmt.Errorf("keep previous binary: %w", err)
	}
	if err := os.Rename(newPath, qmBinPath); err != nil {
		_ = os.Rename(qmBinPath+".prev", qmBinPath)
		return fmt.Errorf("install: %w", err)
	}
	syscall.Sync()
	saveUpdateState(updateState{PreviousVersion: cur, InstalledAt: time.Now().Unix(), LastResult: "installed " + target})
	os.Remove(updateRolledFile)
	if supportsGuard(target) {
		if err := vosWriteFileDurable(updatePendingFile, []byte(target+"\n"), 0644); err == nil {
			if gerr := scheduleUpdateGuard(); gerr != nil {
				log.Printf("update: rollback guard not armed: %v", gerr)
				os.Remove(updatePendingFile)
			}
		}
	}
	return nil
}

// rollback swaps the running binary with qmanager-core.prev.
func rollback() error {
	st := loadUpdateState()
	if _, err := os.Stat(qmBinPath + ".prev"); err != nil {
		return errors.New("no previous version is kept on the stick")
	}
	tmp := qmBinPath + ".swap"
	if err := os.Rename(qmBinPath, tmp); err != nil {
		return err
	}
	if err := os.Rename(qmBinPath+".prev", qmBinPath); err != nil {
		_ = os.Rename(tmp, qmBinPath)
		return err
	}
	if err := os.Rename(tmp, qmBinPath+".prev"); err != nil {
		return err
	}
	syscall.Sync()
	target := st.PreviousVersion
	saveUpdateState(updateState{PreviousVersion: currentVersion(), InstalledAt: time.Now().Unix(), LastResult: "rolled back to " + target})
	os.Remove(updateRolledFile)
	if target != "" && supportsGuard(target) {
		if err := vosWriteFileDurable(updatePendingFile, []byte(target+"\n"), 0644); err == nil {
			if gerr := scheduleUpdateGuard(); gerr != nil {
				os.Remove(updatePendingFile)
			}
		}
	}
	upd.set("restarting", target, "")
	restartSelf()
	return nil
}

// StartUpdater clears the rollback marker once this build has run for a while.
func StartUpdater() {
	if _, err := os.Stat(updatePendingFile); err != nil {
		return
	}
	go func() {
		time.Sleep(updateHealthy)
		if err := os.Remove(updatePendingFile); err == nil {
			log.Printf("update: %s is running, rollback guard disarmed", currentVersion())
		}
	}()
}

// ---------------------------------------------------------------------------
// HTTP
// ---------------------------------------------------------------------------

const notesLimit = 6000

func updateSnapshot(list []ghRelease, listErr error) map[string]any {
	cur := currentVersion()
	st := loadUpdateState()
	_, prevErr := os.Stat(qmBinPath + ".prev")
	_, pendErr := os.Stat(updatePendingFile)
	rolled, _ := os.ReadFile(updateRolledFile)

	var rels []map[string]any
	latest := ""
	for _, r := range list {
		v := r.version()
		if latest == "" && !r.Prerelease {
			latest = v
		}
		notes := r.Body
		if len(notes) > notesLimit {
			notes = notes[:notesLimit] + "…"
		}
		title := r.Name
		if title == "" {
			title = r.Tag
		}
		rels = append(rels, map[string]any{
			"tag": r.Tag, "version": v, "title": title, "notes": notes, "published_at": r.Published,
			"prerelease": r.Prerelease, "size": r.asset(qmAssetName).Size, "url": r.HTMLURL,
			"current": v == cur, "has_updater": supportsGuard(v),
		})
	}
	if rels == nil {
		rels = []map[string]any{}
	}
	upd.mu.Lock()
	progress := map[string]any{"state": upd.State, "target": upd.Target, "done": upd.Done, "total": upd.Total, "message": upd.Message}
	upd.mu.Unlock()
	errStr := ""
	if listErr != nil {
		errStr = listErr.Error()
	}
	ghCache.Lock()
	checkedAt := ghCache.at.Unix()
	if ghCache.at.IsZero() {
		checkedAt = 0
	}
	ghCache.Unlock()
	return map[string]any{
		"success":          true,
		"current_version":  cur,
		"release_build":    isReleaseVersion(cur),
		"latest_version":   latest,
		"update_available": latest != "" && isReleaseVersion(cur) && compareVersions(latest, cur) > 0,
		"previous_version": st.PreviousVersion,
		"can_rollback":     prevErr == nil,
		"verifying":        pendErr == nil,
		"rolled_back_from": strings.TrimSpace(string(rolled)),
		"releases":         rels,
		"progress":         progress,
		"error":            errStr,
		"checked_at":       checkedAt,
		"repo":             ghRepo,
	}
}

// HandleUpdate serves the Software Update page.
func (s *Server) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		list, err := fetchReleases(false)
		writeJSON(w, http.StatusOK, updateSnapshot(list, err))
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "error": "method_not_allowed"})
		return
	}
	var body struct {
		Action string `json:"action"`
		Tag    string `json:"tag"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "invalid_json"})
		return
	}
	fail := func(detail string) {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "update_failed", "detail": detail})
	}
	switch body.Action {
	case "check":
		list, err := fetchReleases(true)
		writeJSON(w, http.StatusOK, updateSnapshot(list, err))
	case "install":
		if upd.busy() {
			fail("an update is already running")
			return
		}
		list, err := fetchReleases(false)
		if err != nil && len(list) == 0 {
			fail(err.Error())
			return
		}
		var rel *ghRelease
		for i := range list {
			if list[i].Tag == body.Tag {
				rel = &list[i]
			}
		}
		if rel == nil {
			fail("unknown release " + body.Tag)
			return
		}
		if rel.version() == currentVersion() {
			fail(rel.version() + " is already running")
			return
		}
		upd.set("downloading", rel.version(), "")
		go installRelease(*rel)
		writeJSON(w, http.StatusOK, updateSnapshot(list, nil))
	case "rollback":
		if upd.busy() {
			fail("an update is already running")
			return
		}
		if err := rollback(); err != nil {
			fail(err.Error())
			return
		}
		list, _ := fetchReleases(false)
		writeJSON(w, http.StatusOK, updateSnapshot(list, nil))
	default:
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "unknown_action"})
	}
}
