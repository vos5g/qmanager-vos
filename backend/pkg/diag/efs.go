// Package diag implements the Qualcomm EFS2 wire protocol over a local DIAG bridge.
// Protocol references: P1sec/QCSuper 2.1.3 and maigougou/vos5g-all-band-unlock.
// The GPL bridge remains a separately installed executable, not linked into QManager.
package diag

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"path"
	"strings"
	"time"
)

const MaxFile = 2 * 1024 * 1024
const Address = "127.0.0.1:43555"

var le = binary.LittleEndian

type Client struct {
	conn   net.Conn
	reader *bufio.Reader
	cmd    *exec.Cmd
	ctx    context.Context
}
type Entry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size uint32 `json:"size"`
}

// Start refuses an already occupied port: never attach to an unrelated DIAG session.
func Start(ctx context.Context, helper string) (*Client, error) {
	probe, err := net.Listen("tcp4", Address)
	if err != nil {
		return nil, errors.New("DIAG is busy; close other diagnostic sessions first")
	}
	probe.Close()
	cmd := exec.CommandContext(ctx, helper)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ARM DIAG helper: %w", err)
	}
	c := &Client{cmd: cmd, ctx: ctx}
	for i := 0; i < 60; i++ {
		conn, err := net.DialTimeout("tcp4", Address, 100*time.Millisecond)
		if err == nil {
			c.conn = conn
			c.reader = bufio.NewReaderSize(conn, 65536)
			return c, nil
		}
		select {
		case <-ctx.Done():
			c.Close()
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	c.Close()
	return nil, errors.New("DIAG helper did not become ready")
}
func (c *Client) Close() {
	if c.conn != nil {
		c.conn.Close()
	}
	if c.cmd != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
}
func crc(data []byte) uint16 {
	v := uint16(0xffff)
	for _, b := range data {
		v ^= uint16(b)
		for i := 0; i < 8; i++ {
			if v&1 != 0 {
				v = (v >> 1) ^ 0x8408
			} else {
				v >>= 1
			}
		}
	}
	return ^v
}
func frame(data []byte) []byte {
	v := crc(data)
	data = append(append([]byte{}, data...), byte(v), byte(v>>8))
	var out []byte
	for _, b := range data {
		if b == 0x7d || b == 0x7e {
			out = append(out, 0x7d, b^0x20)
		} else {
			out = append(out, b)
		}
	}
	return append(out, 0x7e)
}
func unframe(data []byte) ([]byte, error) {
	var out []byte
	escape := false
	for _, b := range data {
		if escape {
			out = append(out, b^0x20)
			escape = false
		} else if b == 0x7d {
			escape = true
		} else {
			out = append(out, b)
		}
	}
	if escape || len(out) < 3 || le.Uint16(out[len(out)-2:]) != crc(out[:len(out)-2]) {
		return nil, errors.New("invalid DIAG frame")
	}
	return out[:len(out)-2], nil
}
func words(v ...uint32) []byte {
	b := make([]byte, 4*len(v))
	for i, n := range v {
		le.PutUint32(b[i*4:], n)
	}
	return b
}
func (c *Client) request(op uint16, payload []byte) ([]byte, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := c.ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.conn.SetDeadline(deadline)
	req := append([]byte{0x4b, 19, byte(op), byte(op >> 8)}, payload...)
	if _, err := c.conn.Write(frame(req)); err != nil {
		return nil, err
	}
	for {
		// Bound individual frames even when a broken helper never sends a delimiter.
		var raw []byte
		for {
			b, err := c.reader.ReadByte()
			if err != nil {
				return nil, err
			}
			if b == 0x7e {
				break
			}
			raw = append(raw, b)
			if len(raw) > 65536 {
				return nil, errors.New("oversized DIAG frame")
			}
		}
		data, err := unframe(raw)
		if err != nil {
			continue
		}
		if len(data) > 0 && (data[0] == 0x13 || data[0] == 0x14 || data[0] == 0x15) {
			return nil, errors.New("modem rejected EFS command")
		}
		if len(data) >= 4 && bytes.Equal(data[:4], req[:4]) {
			return data[4:], nil
		}
	}
}
func check(data []byte, size, errnoOffset int) error {
	if len(data) < size {
		return fmt.Errorf("short EFS response: got %d bytes, expected %d", len(data), size)
	}
	if n := le.Uint32(data[errnoOffset:]); n != 0 {
		return fmt.Errorf("EFS error 0x%x", n)
	}
	return nil
}
func (c *Client) closeFD(op uint16, fd uint32) error {
	b, e := c.request(op, words(fd))
	if e != nil {
		return e
	}
	return check(b, 4, 0)
}
func (c *Client) openFile(p string, flags, mode uint32) (uint32, error) {
	b, e := c.request(2, append(words(flags, mode), append([]byte(p), 0)...))
	if e != nil {
		return 0, e
	}
	if e = check(b, 8, 4); e != nil {
		return 0, e
	}
	return le.Uint32(b), nil
}
func (c *Client) ReadFile(p string) (data []byte, err error) {
	if err := c.checkPath(p, false); err != nil {
		return nil, err
	}
	fd, e := c.openFile(p, 0, 0)
	if e != nil {
		return nil, e
	}
	defer func() {
		if e := c.closeFD(3, fd); err == nil {
			err = e
		}
	}()
	for {
		b, e := c.request(4, words(fd, 1024, uint32(len(data))))
		if e != nil {
			return nil, e
		}
		if e = check(b, 16, 12); e != nil {
			return nil, e
		}
		n := int(le.Uint32(b[8:]))
		if le.Uint32(b) != fd || le.Uint32(b[4:]) != uint32(len(data)) || n > 1024 || n != len(b)-16 {
			return nil, errors.New("invalid EFS read length/offset")
		}
		data = append(data, b[16:]...)
		if len(data) > MaxFile {
			return nil, errors.New("EFS file exceeds 2 MiB limit")
		}
		if n < 1024 {
			return data, nil
		}
	}
}

// WritePolicy deliberately exposes no general EFS write operation.
func (c *Client) WritePolicy(data []byte) (err error) {
	if len(data) == 0 || len(data) > MaxFile {
		return errors.New("invalid policy size")
	}
	// Preserve the original regular file's mode; do not follow a symlink.
	st, e := c.request(16, append([]byte("/policyman/carrier_policy.xml"), 0))
	if e != nil {
		return e
	}
	if e = check(st, 8, 0); e != nil {
		return e
	}
	mode := le.Uint32(st[4:])
	if mode&0170000 != 0100000 {
		return errors.New("carrier policy is not a regular file")
	}
	fd, e := c.openFile("/policyman/carrier_policy.xml", 0x201, mode)
	if e != nil {
		return e
	} // WRONLY|TRUNC; never create a missing policy.
	defer func() {
		if e := c.closeFD(3, fd); err == nil {
			err = e
		}
	}()
	for offset := 0; offset < len(data); {
		end := offset + 1024
		if end > len(data) {
			end = len(data)
		}
		b, e := c.request(5, append(words(fd, uint32(offset)), data[offset:end]...))
		if e != nil {
			return e
		}
		if e = check(b, 16, 12); e != nil {
			return e
		}
		if le.Uint32(b) != fd || le.Uint32(b[4:]) != uint32(offset) || int(le.Uint32(b[8:])) != end-offset {
			return errors.New("short EFS write")
		}
		offset = end
	}
	return nil
}
func (c *Client) List(p string) (entries []Entry, err error) {
	if err := c.checkPath(p, true); err != nil {
		return nil, err
	}
	b, e := c.request(11, append([]byte(p), 0))
	if e != nil {
		return nil, e
	}
	if e = check(b, 8, 4); e != nil {
		return nil, e
	}
	fd := le.Uint32(b)
	defer func() {
		if e := c.closeFD(13, fd); err == nil {
			err = e
		}
	}()
	entries = []Entry{}
	for seq := uint32(1); seq <= 2048; seq++ {
		b, e = c.request(12, words(fd, seq))
		if e != nil {
			return nil, e
		}
		if e = check(b, 36, 8); e != nil {
			return nil, e
		}
		if le.Uint32(b) != fd || le.Uint32(b[4:]) != seq {
			return nil, errors.New("invalid EFS directory sequence")
		}
		name := strings.TrimRight(string(b[36:]), "\x00")
		if name == "" {
			return entries, nil
		}
		if name == "." || name == ".." {
			continue
		}
		target := name
		if !strings.HasPrefix(target, "/") {
			target = path.Join(p, target)
		}
		if path.Dir(target) != p || path.Clean(target) != target || strings.ContainsRune(target, 0) {
			return nil, errors.New("unsafe EFS directory entry")
		}
		typ := "other"
		switch le.Uint32(b[16:]) & 0170000 {
		case 0040000:
			typ = "directory"
		case 0100000:
			typ = "file"
		case 0160000:
			typ = "item_file"
		case 0120000:
			typ = "symlink"
		}
		entries = append(entries, Entry{target, typ, le.Uint32(b[20:])})
	}
	return nil, errors.New("EFS directory exceeds 2048 entries")
}

// LSTAT every component so a path inside the allowed trees cannot follow a
// symlink into unrelated NV or another device node.
func (c *Client) checkPath(p string, directory bool) error {
	if !SafePath(p) {
		return errors.New("EFS path outside allowed radio trees")
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	current := ""
	for i, part := range parts {
		current += "/" + part
		b, e := c.request(16, append([]byte(current), 0))
		if e != nil {
			return e
		}
		if e = check(b, 8, 0); e != nil {
			return e
		}
		kind := le.Uint32(b[4:]) & 0170000
		if i < len(parts)-1 || directory {
			if kind != 0040000 {
				return errors.New("EFS path contains a symlink or non-directory")
			}
		} else if kind != 0100000 && kind != 0160000 {
			return errors.New("EFS path is not a regular or item file")
		}
	}
	return nil
}

// SafePath restricts the browser to radio capability/policy trees, with no traversal.
func SafePath(p string) bool {
	if len(p) > 512 || strings.ContainsRune(p, 0) || path.Clean(p) != p || strings.ContainsAny(p, "\\\r\n") {
		return false
	}
	for _, root := range []string{"/policyman", "/nv/item_files/modem"} {
		if p == root || strings.HasPrefix(p, root+"/") {
			return true
		}
	}
	return false
}
