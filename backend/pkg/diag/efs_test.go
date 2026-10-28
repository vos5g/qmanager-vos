package diag

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"testing"
)

// A simulated modem exercises real HDLC frames, response validation and FD cleanup.
func simulated(t *testing.T, respond func(uint16, []byte) []byte) *Client {
	t.Helper()
	client, server := net.Pipe()
	c := &Client{conn: client, reader: bufio.NewReader(client), ctx: context.Background()}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		r := bufio.NewReader(server)
		for {
			f, e := r.ReadBytes(0x7e)
			if e != nil {
				return
			}
			data, e := unframe(f[:len(f)-1])
			if e != nil || len(data) < 4 {
				return
			}
			out := respond(le.Uint16(data[2:]), data[4:])
			if _, e = server.Write(frame(append(append([]byte{}, data[:4]...), out...))); e != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { client.Close(); <-done })
	return c
}
func statResponse(p []byte) []byte {
	mode := uint32(0040755)
	if bytes.Contains(p, []byte(".xml")) {
		mode = 0100644
	}
	return words(0, mode, 0, 1, 0, 0, 0)
}
func TestEFSReadAndClose(t *testing.T) {
	payload := bytes.Repeat([]byte{0x7d, 0x7e}, 700)
	closed := false
	c := simulated(t, func(op uint16, b []byte) []byte {
		switch op {
		case 16:
			return statResponse(b)
		case 2:
			return words(7, 0)
		case 4:
			offset := int(le.Uint32(b[8:]))
			end := offset + 1024
			if end > len(payload) {
				end = len(payload)
			}
			return append(words(7, uint32(offset), uint32(end-offset), 0), payload[offset:end]...)
		case 3:
			closed = true
			return words(0)
		}
		t.Errorf("unexpected op %d", op)
		return nil
	})
	b, e := c.ReadFile("/policyman/test.xml")
	if e != nil || !bytes.Equal(b, payload) || !closed {
		t.Fatalf("read=%d close=%v err=%v", len(b), closed, e)
	}
}
func TestEFSRejectsShortReadAndCloses(t *testing.T) {
	closed := false
	c := simulated(t, func(op uint16, b []byte) []byte {
		switch op {
		case 16:
			return statResponse(b)
		case 2:
			return words(7, 0)
		case 4:
			return append(words(7, 0, 10, 0), 1)
		case 3:
			closed = true
			return words(0)
		}
		return nil
	})
	if _, e := c.ReadFile("/policyman/test.xml"); e == nil || !closed {
		t.Fatal("short read accepted or FD leaked")
	}
}
func TestEFSRejectsSymlinkBeforeOpen(t *testing.T) {
	opened := false
	c := simulated(t, func(op uint16, b []byte) []byte {
		if op == 16 {
			return words(0, 0120777, 0, 1, 0, 0, 0)
		}
		opened = true
		return nil
	})
	if _, e := c.ReadFile("/policyman/test.xml"); e == nil || opened {
		t.Fatal("followed an EFS symlink")
	}
}
func TestEFSShortWrite(t *testing.T) {
	closed := false
	c := simulated(t, func(op uint16, b []byte) []byte {
		switch op {
		case 16:
			return statResponse(b)
		case 2:
			if le.Uint32(b) != 0x201 {
				t.Error("must not create policy")
			}
			return words(9, 0)
		case 5:
			return words(9, 0, 0, 0)
		case 3:
			closed = true
			return words(0)
		}
		return nil
	})
	if e := c.WritePolicy([]byte("policy")); e == nil || !closed {
		t.Fatal("short write accepted or FD leaked")
	}
}
