package api

import (
	"encoding/json"
	"net/http"
	"os"
	"syscall"
)

// StorageMount describes one mounted filesystem's usage (bytes).
type StorageMount struct {
	MountPoint string `json:"mount_point"`
	Filesystem string `json:"filesystem"`
	TotalBytes uint64 `json:"total_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
	// Percent used, 0-100 (integer floor).
	UsedPercent int `json:"used_percent"`
	// Readable label for the UI ("System", "Firmware", "User data", "RAM").
	Label string `json:"label"`
	// True when the mount is read-only (e.g. rootfs/firmware partitions).
	ReadOnly bool `json:"read_only"`
}

// HandleStorage returns filesystem usage for the mounts that matter on the
// VOS modem: rootfs (/), firmware, /data (or /usrdata on other models), plus RAM.
// Uses statfs (no shell exec) so it is cheap for the poller.
func (s *Server) HandleStorage(w http.ResponseWriter, r *http.Request) {
	mounts := s.collectStorage()
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"storage": mounts,
	})
}

func (s *Server) collectStorage() []StorageMount {
	// Only report real mounted filesystems, using their actual backing device.
	targets := []struct {
		Path  string
		Label string
	}{
		{"/", "System"},
		{"/firmware", "Firmware"},
		{"/data", "Data"},
		{"/usrdata", "User data"},
		{"/tmp", "Temporary files"},
	}
	mountInfo, _ := os.ReadFile("/proc/mounts")
	actual := storageMounts(mountInfo)
	mounts := make([]StorageMount, 0, len(targets))
	for _, t := range targets {
		info, mounted := actual[t.Path]
		if !mounted { // An ordinary directory would report its parent's capacity.
			continue
		}
		var st syscall.Statfs_t
		if err := syscall.Statfs(t.Path, &st); err != nil {
			continue
		}
		// Avoid unsigned underflow if bavail > blocks (shouldn't happen).
		total := st.Blocks * uint64(st.Bsize)
		used := (st.Blocks - st.Bfree) * uint64(st.Bsize)
		free := st.Bavail * uint64(st.Bsize)
		if st.Bavail > st.Blocks {
			free = 0
		}
		pct := 0
		if total > 0 {
			pct = int((used * 100) / total)
		}
		mounts = append(mounts, StorageMount{
			MountPoint:  t.Path,
			Filesystem:  info.Filesystem,
			TotalBytes:  total,
			UsedBytes:   used,
			FreeBytes:   free,
			UsedPercent: pct,
			Label:       t.Label,
			ReadOnly:    info.ReadOnly,
		})
	}
	// Add RAM from /proc/meminfo (MemTotal / MemAvailable).
	if ram, ok := readMemInfo(); ok {
		mounts = append(mounts, StorageMount{
			MountPoint:  "RAM",
			Filesystem:  "meminfo",
			TotalBytes:  ram.total,
			UsedBytes:   ram.total - ram.available,
			FreeBytes:   ram.available,
			UsedPercent: int(((ram.total - ram.available) * 100) / ram.total),
			Label:       "RAM",
		})
	}
	return mounts
}

func storageMounts(data []byte) map[string]StorageMount {
	result := make(map[string]StorageMount)
	for _, line := range splitLines(data) {
		f := splitFields(line)
		if len(f) >= 4 {
			result[f[1]] = StorageMount{Filesystem: f[0], ReadOnly: hasField(f[3], "ro")}
		}
	}
	return result
}

type memInfo struct{ total, available uint64 }

func splitLines(b []byte) []string {
	var out []string
	start := 0
	for i := 0; i <= len(b); i++ {
		if i == len(b) || b[i] == '\n' {
			if i > start {
				out = append(out, string(b[start:i]))
			}
			start = i + 1
		}
	}
	return out
}

func splitFields(s string) []string {
	var out []string
	start := -1
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ' ' || s[i] == '\t' {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	return out
}

func hasField(fields string, want string) bool {
	// mount options are comma-separated, e.g. "ro,relatime,bulk_read".
	for _, f := range splitComma(fields) {
		if f == want {
			return true
		}
	}
	return false
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}

func readMemInfo() (memInfo, bool) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return memInfo{}, false
	}
	var mi memInfo
	_ = parseMemInfoLine(b, "MemTotal:", &mi.total)
	_ = parseMemInfoLine(b, "MemAvailable:", &mi.available)
	return mi, mi.total > 0
}

func parseMemInfoLine(b []byte, key string, out *uint64) error {
	idx := indexOf(b, key)
	if idx < 0 {
		return os.ErrNotExist
	}
	rest := b[idx+len(key):]
	// skip spaces
	i := 0
	for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t') {
		i++
	}
	var val uint64
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		val = val*10 + uint64(rest[i]-'0')
		i++
	}
	*out = val * 1024 // kB -> bytes
	return nil
}

func indexOf(b []byte, s string) int {
	needle := []byte(s)
	for i := 0; i+len(needle) <= len(b); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if b[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
