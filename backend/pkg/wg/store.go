package wg

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Paths are variables so tests can point them elsewhere.
var (
	Dir        = "/data/vosclean/wireguard"
	ConfigDir  = Dir + "/configs"
	ActiveFile = Dir + "/ACTIVE" // name of the config to keep connected (also at boot)
)

// ErrNotFound means there is no saved config with that name.
var ErrNotFound = errors.New("no saved config with that name")

func configPath(name string) string { return filepath.Join(ConfigDir, name+".conf") }

// writeDurable writes via a temp file, fsync and rename, then fsyncs the
// directory, so a reset leaves the old or the new file, never an empty one.
func writeDurable(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(data); err == nil {
		if err = f.Chmod(perm); err == nil {
			err = f.Sync()
		}
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func ensureDirs() error {
	if err := os.MkdirAll(ConfigDir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(Dir, 0o700)
	return os.Chmod(ConfigDir, 0o700)
}

// List returns the saved config names, sorted.
func List() []string {
	m, _ := filepath.Glob(filepath.Join(ConfigDir, "*.conf"))
	var names []string
	for _, p := range m {
		n := strings.TrimSuffix(filepath.Base(p), ".conf")
		if ValidName(n) {
			names = append(names, n)
		}
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	return names
}

// Exists reports whether a config with that name is saved (case-insensitive,
// so two configs never differ only by case).
func Exists(name string) bool {
	for _, n := range List() {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

// Load reads and parses a saved config.
func Load(name string) (*Config, error) {
	if !ValidName(name) {
		return nil, ErrNotFound
	}
	b, err := os.ReadFile(configPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c, err := Parse(string(b))
	if err != nil {
		return nil, err
	}
	c.Name = name
	return c, nil
}

// Save writes c (already validated) to disk, root-only.
func Save(c *Config) error {
	if !ValidName(c.Name) {
		return errors.New("invalid name")
	}
	if err := ensureDirs(); err != nil {
		return err
	}
	return writeDurable(configPath(c.Name), c.Marshal(), 0o600)
}

// Delete removes a saved config.
func Delete(name string) error {
	if !ValidName(name) {
		return ErrNotFound
	}
	err := os.Remove(configPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err == nil {
		if d, err := os.Open(ConfigDir); err == nil {
			_ = d.Sync()
			_ = d.Close()
		}
	}
	return err
}

// Active returns the config that should be connected, or "".
func Active() string {
	b, err := os.ReadFile(ActiveFile)
	if err != nil {
		return ""
	}
	n := strings.TrimSpace(string(b))
	if !ValidName(n) {
		return ""
	}
	return n
}

// SetActive records (name != "") or clears (name == "") the active config.
func SetActive(name string) error {
	if name == "" {
		err := os.Remove(ActiveFile)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err == nil {
			if d, err := os.Open(Dir); err == nil {
				_ = d.Sync()
				_ = d.Close()
			}
		}
		return err
	}
	if err := ensureDirs(); err != nil {
		return err
	}
	return writeDurable(ActiveFile, []byte(name+"\n"), 0o600)
}
