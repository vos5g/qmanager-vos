package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"qmanager-backend/pkg/diag"
)

var modemToolsMu sync.Mutex
var toolsRoot = "/data/vosclean/qm"
var toolsOEM = "/oem/modem_config"
var toolsPID = "/etc/usb/boot_hsusb_comp"
var toolsPIDBacking = "/systemrw/boot_hsusb_comp"
var toolsUSBGadget = "/sys/kernel/config/usb_gadget/g1"
var toolsUSBLegacy = "/sys/class/android_usb/android0"

const diagHelper = "/data/vosclean/qm/bin/vos5g-diag-bridge"
const policyPath = "/policyman/carrier_policy.xml"

type usbOption struct {
	PID         string `json:"pid"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Only compositions retaining USB networking for SSH are selectable.
// QMI-only (9091) and MBIM-only compositions must never be destinations.
var usbOptions = []usbOption{
	{"9063", "Factory default (9063)", "ECM / RNDIS / MBIM tethering."},
	{"9064", "QMI + ECM + MBIM (9064)", "QMI/RMNET with DIAG, ADB and AT; retains ECM for macOS and MBIM for Windows. Host chooses the configuration."},
	{"9165", "Development / DIAG (9165)", "ECM networking with DIAG, ADB and AT serial."},
}

func knownUSB(pid string) bool {
	for _, o := range usbOptions {
		if o.PID == pid {
			return true
		}
	}
	return false
}

// The PID file is a bind mount: rename would replace the mount, not its backing inode.
// Stage+sync on systemrw, then copy through the mount with a retained rollback file.
func setUSB(pid string) error {
	if !knownUSB(pid) {
		return errors.New("USB mode blocked: choose a composition that retains USB networking for SSH")
	}
	old, e := os.ReadFile(toolsPID)
	if e != nil {
		return e
	}
	// Permit recovery from an older QMI-only setting, but never selecting it.
	if current := strings.TrimSpace(string(old)); !knownUSB(current) && current != "9091" {
		return errors.New("unknown current USB composition")
	}
	a, e := os.Stat(toolsPID)
	if e != nil {
		return e
	}
	b, e := os.Stat(toolsPIDBacking)
	if e != nil {
		return e
	}
	if !os.SameFile(a, b) {
		return errors.New("unexpected USB composition bind mount")
	}
	if e = os.MkdirAll(filepath.Join(toolsRoot, "etc"), 0700); e != nil {
		return e
	}
	if e = vosWriteFileDurable(filepath.Join(toolsRoot, "etc/usb-mode.previous"), old, 0600); e != nil {
		return e
	}
	staged := filepath.Join(filepath.Dir(toolsPIDBacking), ".qmanager-usb-mode")
	if e = vosWriteFileDurable(staged, []byte(pid+"\n"), 0600); e != nil {
		return e
	}
	defer os.Remove(staged)
	write := func(data []byte) error {
		f, e := os.OpenFile(toolsPID, os.O_WRONLY|os.O_TRUNC, 0)
		if e != nil {
			return e
		}
		if _, e = f.Write(data); e != nil {
			f.Close()
			return e
		}
		e = f.Sync()
		ce := f.Close()
		if e != nil {
			return e
		}
		return ce
	}
	if e = write([]byte(pid + "\n")); e == nil {
		var got []byte
		got, e = os.ReadFile(toolsPIDBacking)
		if e == nil && strings.TrimSpace(string(got)) != pid {
			e = errors.New("USB read-back mismatch")
		}
	}
	if e != nil {
		if rollback := write(old); rollback != nil {
			return fmt.Errorf("USB write failed: %v; rollback failed: %v", e, rollback)
		}
		return e
	}
	return nil
}

// The kernel gadget identifier is the running composition. The boot selector
// may already contain a different next-boot value. Do not query atcli here: its
// shared reply channel can return another poller's radio-status response.
func currentUSBPID() string {
	if vosReadTrim(filepath.Join(toolsUSBGadget, "UDC")) != "" {
		return parseUSBPID(vosReadTrim(filepath.Join(toolsUSBGadget, "idProduct")))
	}
	if vosReadTrim(filepath.Join(toolsUSBLegacy, "enable")) == "1" {
		return parseUSBPID(vosReadTrim(filepath.Join(toolsUSBLegacy, "idProduct")))
	}
	return ""
}

func parseUSBPID(raw string) string {
	pid := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(raw)), "0X")
	if len(pid) != 4 {
		return ""
	}
	for i := range pid {
		if strings.IndexByte("0123456789ABCDEF", pid[i]) < 0 {
			return ""
		}
	}
	return pid
}

func currentUSBLabel(pid string) string {
	if pid == "" {
		return "Unavailable"
	}
	for _, mode := range usbOptions {
		if mode.PID == pid {
			return mode.Label
		}
	}
	return "USB composition (" + pid + ")"
}

func (s *Server) HandleUSBMode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	modemToolsMu.Lock()
	defer modemToolsMu.Unlock()
	if r.Method == http.MethodGet {
		pid := currentUSBPID()
		writeJSON(w, 200, map[string]any{"success": true, "next_pid": vosReadTrim(toolsPID), "current_pid": pid, "current": currentUSBLabel(pid), "options": usbOptions})
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var req struct {
		PID     string `json:"pid"`
		Confirm bool   `json:"confirm"`
	}
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); e != nil || !req.Confirm || !knownUSB(req.PID) {
		writeJSON(w, 400, map[string]any{"success": false, "detail": "Choose a USB mode that retains USB networking for SSH and confirm the change."})
		return
	}
	if e := setUSB(req.PID); e != nil {
		toolsError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "next_pid": req.PID, "reboot_required": true})
}
func toolsError(w http.ResponseWriter, e error) {
	writeJSON(w, 400, map[string]any{"success": false, "detail": e.Error()})
}

// No raw DIAG commands or arbitrary EFS writes are exposed by this endpoint.
func (s *Server) HandleModemEFS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	if r.URL.Query().Get("action") == "status" {
		writeJSON(w, 200, map[string]any{"success": true, "installed": fileExists(diagHelper), "available": fileExists("/dev/diag")})
		return
	}
	p := r.URL.Query().Get("path")
	action := r.URL.Query().Get("action")
	if !diag.SafePath(p) || (action != "list" && action != "read") {
		toolsError(w, errors.New("Choose a path under /policyman or /nv/item_files/modem."))
		return
	}
	modemToolsMu.Lock()
	defer modemToolsMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	c, e := diag.Start(ctx, diagHelper)
	if e != nil {
		toolsError(w, e)
		return
	}
	defer c.Close()
	if action == "list" {
		entries, e := c.List(p)
		if e != nil {
			toolsError(w, e)
			return
		}
		writeJSON(w, 200, map[string]any{"success": true, "entries": entries})
		return
	}
	data, e := c.ReadFile(p)
	if e != nil {
		toolsError(w, e)
		return
	}
	text := ""
	if utf8.Valid(data) && !bytes.ContainsRune(data, 0) {
		text = string(data)
	}
	writeJSON(w, 200, map[string]any{"success": true, "path": p, "bytes": len(data), "sha256": diag.Hash(data), "text": text, "base64": base64.StdEncoding.EncodeToString(data)})
}

type bandBundle struct {
	ID               string `json:"id"`
	Identity         string `json:"identity"`
	Boot             string `json:"boot"`
	Stage            string `json:"stage"`
	OriginalHash     string `json:"original_hash"`
	TargetHash       string `json:"target_hash"`
	PolicyHash       string `json:"policy_hash"`
	TargetPolicyHash string `json:"target_policy_hash"`
	ArchiveHash      string `json:"archive_hash"`
}

func bandStatePath() string    { return filepath.Join(toolsRoot, "etc/band-unlock.json") }
func bandDir(id string) string { return filepath.Join(toolsRoot, "band-backups", id) }
func saveBand(st *bandBundle) error {
	b, e := json.MarshalIndent(st, "", "  ")
	if e != nil {
		return e
	}
	return vosWriteFileDurable(bandStatePath(), b, 0600)
}
func loadBand() (*bandBundle, error) {
	b, e := os.ReadFile(bandStatePath())
	if e != nil {
		return nil, e
	}
	var st bandBundle
	if e = json.Unmarshal(b, &st); e != nil {
		return nil, e
	}
	if st.ID == "" || path.Base(st.ID) != st.ID || strings.Contains(st.ID, "..") {
		return nil, errors.New("invalid backup identifier")
	}
	return &st, nil
}
func bandIdentity() (string, error) {
	for _, p := range []string{"/sys/kernel/config/usb_gadget/g1/strings/0x409/serialnumber", "/sys/class/android_usb/android0/iSerial"} {
		if b, e := os.ReadFile(p); e == nil && len(bytes.TrimSpace(b)) > 0 {
			return diag.Hash(b), nil
		}
	}
	return "", errors.New("cannot identify this physical modem")
}
func bandWriteOEM(data []byte) error {
	st, e := os.Stat(toolsOEM)
	if e != nil {
		return e
	}
	// Same directory + fsync + rename preserves the original file until commit.
	if e = vosWriteFileDurable(toolsOEM, data, st.Mode().Perm()); e != nil {
		return e
	}
	b, e := os.ReadFile(toolsOEM)
	if e != nil {
		return e
	}
	if !bytes.Equal(b, data) {
		return errors.New("OEM policy read-back mismatch")
	}
	return nil
}
func bandWriteEFS(c *diag.Client, data []byte) error {
	if e := c.WritePolicy(data); e != nil {
		return e
	}
	b, e := c.ReadFile(policyPath)
	if e != nil {
		return e
	}
	if !bytes.Equal(b, data) {
		return errors.New("live EFS policy read-back mismatch")
	}
	return nil
}
func backupPolicyTree(c *diag.Client, remote, local string, depth int) error {
	if depth > 8 {
		return errors.New("policy backup tree is too deep")
	}
	if e := os.MkdirAll(local, 0700); e != nil {
		return e
	}
	entries, e := c.List(remote)
	if e != nil {
		return e
	}
	for _, entry := range entries {
		dst := filepath.Join(local, path.Base(entry.Path))
		switch entry.Type {
		case "directory":
			e = backupPolicyTree(c, entry.Path, dst, depth+1)
		case "file", "item_file":
			var b []byte
			b, e = c.ReadFile(entry.Path)
			if e == nil {
				e = vosWriteFileDurable(dst, b, 0600)
			}
		default:
			return errors.New("unsupported EFS entry in backup; refusing to change policy")
		}
		if e != nil {
			return e
		}
	}
	return nil
}
func prepareBand(c *diag.Client, p *diag.Policy) (*bandBundle, error) {
	identity, e := bandIdentity()
	if e != nil {
		return nil, e
	}
	if !strings.Contains(vosReadTrim("/oem/cei_module_ver_vos_id"), "326.73_0R19") {
		return nil, errors.New("unsupported VOS firmware; inspection is available")
	}
	live, e := c.ReadFile(policyPath)
	if e != nil {
		return nil, e
	}
	if !bytes.Equal(live, p.Policy) {
		return nil, errors.New("OEM and live EFS policies differ; refusing unlock")
	}
	var fs syscall.Statfs_t
	if e = syscall.Statfs(toolsRoot, &fs); e != nil {
		return nil, e
	}
	if fs.Bavail*uint64(fs.Bsize) < 40*1024*1024 {
		return nil, errors.New("at least 40 MiB free on /data is required for the backup")
	}
	id := time.Now().UTC().Format("20060102T150405.000000000Z")
	dir := bandDir(id)
	if e = os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	if e = vosWriteFileDurable(filepath.Join(dir, "INCOMPLETE"), []byte("Backup has not completed. Do not use it for restore.\n"), 0600); e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if e = exec.CommandContext(ctx, "tar", "-czf", filepath.Join(dir, "oem.tar.gz"), "-C", "/", "oem").Run(); e != nil {
		return nil, fmt.Errorf("OEM archive: %w", e)
	}
	if e = os.Chmod(filepath.Join(dir, "oem.tar.gz"), 0600); e != nil {
		return nil, e
	}
	if e = backupPolicyTree(c, "/policyman", filepath.Join(dir, "policyman"), 0); e != nil {
		return nil, e
	}
	files := map[string][]byte{"modem_config.original": p.Original, "modem_config.hardware": p.Target, "carrier_policy.original.xml": live, "carrier_policy.hardware.xml": p.TargetPolicy}
	// Archive and every EFS entry are hashed in a private manifest.
	hashes := map[string]string{}
	for name, b := range files {
		if e = vosWriteFileDurable(filepath.Join(dir, name), b, 0600); e != nil {
			return nil, e
		}
	}
	e = filepath.Walk(dir, func(f string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			b, e := os.ReadFile(f)
			if e != nil {
				return e
			}
			rel, _ := filepath.Rel(dir, f)
			if rel != "INCOMPLETE" {
				hashes[rel] = diag.Hash(b)
			}
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	st := &bandBundle{ID: id, Identity: identity, Boot: vosReadTrim("/proc/sys/kernel/random/boot_id"), Stage: "backed_up", OriginalHash: diag.Hash(p.Original), TargetHash: diag.Hash(p.Target), PolicyHash: diag.Hash(live), TargetPolicyHash: diag.Hash(p.TargetPolicy), ArchiveHash: hashes["oem.tar.gz"]}
	manifest, _ := json.MarshalIndent(map[string]any{"device": st, "files": hashes}, "", "  ")
	if e = vosWriteFileDurable(filepath.Join(dir, "manifest.json"), manifest, 0600); e != nil {
		return nil, e
	}
	if e = os.Remove(filepath.Join(dir, "INCOMPLETE")); e != nil {
		return nil, e
	}
	if e = saveBand(st); e != nil {
		return nil, e
	}
	return st, nil
}
func applyBand(c *diag.Client, st *bandBundle, restore bool) error {
	identity, e := bandIdentity()
	if e != nil {
		return e
	}
	if identity != st.Identity {
		return errors.New("backup belongs to another device")
	}
	dir := bandDir(st.ID)
	if fileExists(filepath.Join(dir, "INCOMPLETE")) {
		return errors.New("backup is incomplete")
	}
	// Validate the whole backup, including OEM archive, before a persistent write.
	var manifest struct {
		Files map[string]string `json:"files"`
	}
	b, e := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if e != nil {
		return e
	}
	if e = json.Unmarshal(b, &manifest); e != nil {
		return e
	}
	if len(manifest.Files) < 5 {
		return errors.New("incomplete backup manifest")
	}
	for name, hash := range manifest.Files {
		if filepath.IsAbs(name) || filepath.Clean(name) != name || strings.HasPrefix(name, "..") {
			return errors.New("unsafe backup path")
		}
		b, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil || diag.Hash(b) != hash {
			return fmt.Errorf("backup checksum failed: %s", name)
		}
	}
	original, e := os.ReadFile(filepath.Join(dir, "modem_config.original"))
	if e != nil {
		return e
	}
	target, e := os.ReadFile(filepath.Join(dir, "modem_config.hardware"))
	if e != nil {
		return e
	}
	origPolicy, e := os.ReadFile(filepath.Join(dir, "carrier_policy.original.xml"))
	if e != nil {
		return e
	}
	targetPolicy, e := os.ReadFile(filepath.Join(dir, "carrier_policy.hardware.xml"))
	if e != nil {
		return e
	}
	if diag.Hash(original) != st.OriginalHash || diag.Hash(target) != st.TargetHash || diag.Hash(origPolicy) != st.PolicyHash || diag.Hash(targetPolicy) != st.TargetPolicyHash {
		return errors.New("backup does not match device state record")
	}
	oldOEM, e := os.ReadFile(toolsOEM)
	if e != nil {
		return e
	}
	oldEFS, e := c.ReadFile(policyPath)
	if e != nil {
		return e
	}
	if (!bytes.Equal(oldOEM, original) && !bytes.Equal(oldOEM, target)) || (!bytes.Equal(oldEFS, origPolicy) && !bytes.Equal(oldEFS, targetPolicy)) {
		return errors.New("current policy is neither the saved original nor the target; use recovery backup")
	}
	desiredOEM, desiredEFS := target, targetPolicy
	if restore {
		desiredOEM, desiredEFS = original, origPolicy
	}
	st.Stage = "writing"
	if e = saveBand(st); e != nil {
		return e
	}
	if e = bandWriteEFS(c, desiredEFS); e == nil {
		e = bandWriteOEM(desiredOEM)
	}
	if e != nil {
		// Even a partially failed EFS write needs rollback. Keep the bundle in all cases.
		re := bandWriteEFS(c, oldEFS)
		ro := bandWriteOEM(oldOEM)
		st.Stage = "failed"
		_ = saveBand(st)
		return fmt.Errorf("policy update failed: %v; EFS rollback: %v; OEM rollback: %v. Backup retained at %s", e, re, ro, dir)
	}
	st.Stage = "pending_unlock_reboot"
	if restore {
		st.Stage = "pending_restore_reboot"
	}
	st.Boot = vosReadTrim("/proc/sys/kernel/random/boot_id")
	return saveBand(st)
}
func (s *Server) HandleBandUnlock(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	modemToolsMu.Lock()
	defer modemToolsMu.Unlock()
	pbytes, e := os.ReadFile(toolsOEM)
	if e != nil {
		toolsError(w, e)
		return
	}
	p, e := diag.ParsePolicy(pbytes)
	if e != nil {
		toolsError(w, e)
		return
	}
	st, stateErr := loadBand()
	if stateErr != nil && !os.IsNotExist(stateErr) {
		toolsError(w, stateErr)
		return
	}
	if r.Method == http.MethodGet && r.URL.Query().Get("action") == "backup" {
		if st == nil {
			toolsError(w, errors.New("no backup exists"))
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", `attachment; filename="vos5g-band-backup-`+st.ID+`.tar.gz"`)
		w.Header().Set("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "tar", "-czf", "-", "-C", bandDir(st.ID), ".")
		cmd.Stdout = w
		_ = cmd.Run() // A disconnect aborts the private archive stream.
		return
	}
	if r.Method == http.MethodGet {
		// Verification runs only after a recorded intentional policy change and a new boot.
		verifyError := ""
		if st != nil && strings.HasPrefix(st.Stage, "pending_") && st.Boot != vosReadTrim("/proc/sys/kernel/random/boot_id") {
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			c, err := diag.Start(ctx, diagHelper)
			if err == nil {
				live, re := c.ReadFile(policyPath)
				identity, ie := bandIdentity()
				wantOEM, wantEFS := st.TargetHash, st.TargetPolicyHash
				if st.Stage == "pending_restore_reboot" {
					wantOEM, wantEFS = st.OriginalHash, st.PolicyHash
				}
				if re == nil && ie == nil && identity == st.Identity && diag.Hash(pbytes) == wantOEM && diag.Hash(live) == wantEFS {
					if st.Stage == "pending_restore_reboot" {
						st.Stage = "restored"
					} else {
						st.Stage = "verified"
					}
					err = saveBand(st)
				} else {
					err = errors.New("post-reboot policy verification failed; backup retained")
				}
				c.Close()
			}
			cancel()
			if err != nil {
				verifyError = err.Error()
			}
		}
		writeJSON(w, 200, map[string]any{"success": true, "supported": p.Supported, "unlocked": p.Unlocked, "helper_installed": fileExists(diagHelper), "backup": st, "verification_error": verifyError})
		return
	}
	var req struct {
		Action  string `json:"action"`
		Confirm bool   `json:"confirm"`
	}
	if e = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); e != nil || !req.Confirm || (req.Action != "unlock" && req.Action != "restore") {
		toolsError(w, errors.New("confirm unlock or restore"))
		return
	}
	if req.Action == "unlock" && (!p.Supported || p.Unlocked) {
		toolsError(w, errors.New("policy is unsupported or already unlocked"))
		return
	}
	if req.Action == "unlock" && st != nil && st.Stage != "restored" {
		toolsError(w, errors.New("a previous operation already has a backup; restore or verify it before starting again"))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c, e := diag.Start(ctx, diagHelper)
	if e != nil {
		toolsError(w, e)
		return
	}
	defer c.Close()
	if req.Action == "unlock" {
		st, e = prepareBand(c, p)
		if e != nil {
			toolsError(w, e)
			return
		}
	} else if st == nil {
		toolsError(w, errors.New("no backup exists for this device"))
		return
	}
	if e = applyBand(c, st, req.Action == "restore"); e != nil {
		toolsError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "backup": st, "reboot_required": true})
}
