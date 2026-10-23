package api

// vosclean: durable writes for settings on flash.
//
// /data is UBIFS. os.WriteFile truncates and rewrites in place, and a
// write+rename without fsync can leave a zero-length file after a reset. That
// happened on 23 Oct: a Kernel Modules toggle rewrote vosclean.conf, the stick
// restarted within a minute, and the file came back empty, which switched the
// hardening off at the next boot. Every settings write on flash goes through
// vosWriteFileDurable: temp file in the same directory, fsync, rename over the
// target, fsync the directory. A reset leaves either the old or the new
// contents, never an empty file.

import (
	"os"
	"path/filepath"
)

func vosWriteFileDurable(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
