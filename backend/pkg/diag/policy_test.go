package diag

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func fixture() []byte {
	p := []byte("<policy>\r\n  <other />\r\n  <select>\r\n    <case><true/><then><rf_bands list=\"regional\"/></then></case>\r\n  </select>\r\n</policy>\r\n")
	h := []byte("<hw_bands_filter><rule /></hw_bands_filter>\r\n")
	header := make([]byte, 176)
	copy(header, []byte("SYNTHETIC"))
	binary.LittleEndian.PutUint32(header[168:], uint32(len(p)))
	binary.LittleEndian.PutUint32(header[172:], uint32(len(h)))
	return append(append(header, p...), h...)
}
func TestPolicyPreservesHardwareAndHeader(t *testing.T) {
	b := fixture()
	p, e := ParsePolicy(b)
	if e != nil {
		t.Fatal(e)
	}
	if p.Supported || p.Unlocked {
		t.Fatal("synthetic source must remain inspect-only")
	}
	next, e := ParsePolicy(p.Target)
	if e != nil {
		t.Fatal(e)
	}
	if !next.Unlocked || !bytes.Equal(p.Hardware, next.Hardware) || !bytes.Equal(p.Target[:168], b[:168]) || !bytes.Equal(p.Target[172:176], b[172:176]) {
		t.Fatal("changed outside selector/length")
	}
	if !bytes.Equal(next.Target, p.Target) {
		t.Fatal("not idempotent")
	}
}
func TestMalformedPolicyRejected(t *testing.T) {
	b := fixture()
	for _, v := range [][]byte{nil, b[:175], append(append([]byte{}, b...), 0)} {
		if _, e := ParsePolicy(v); e == nil {
			t.Fatal("accepted invalid container")
		}
	}
	b = fixture()
	binary.LittleEndian.PutUint32(b[168:], 0xffffffff)
	if _, e := ParsePolicy(b); e == nil {
		t.Fatal("accepted overflowed size")
	}
}
func TestHDLC(t *testing.T) {
	if crc([]byte("123456789")) != 0x906e {
		t.Fatal("CRC does not match X25 test vector")
	}
	b := []byte{0x4b, 19, 2, 0, 0x7e, 0x7d, 0}
	f := frame(b)
	v, e := unframe(f[:len(f)-1])
	if e != nil || !bytes.Equal(v, b) {
		t.Fatal(e)
	}
	f[0] ^= 1
	if _, e = unframe(f[:len(f)-1]); e == nil {
		t.Fatal("accepted corrupt CRC")
	}
}
func TestSafePath(t *testing.T) {
	for _, p := range []string{"/policyman", "/policyman/carrier_policy.xml", "/nv/item_files/modem/mmode"} {
		if !SafePath(p) {
			t.Fatal(p)
		}
	}
	for _, p := range []string{"/", "/policyman/../nv", "/policymanx", "/nv/item_files/modem/../../imei", "/policyman/a\x00b", "/policyman//a", "/policyman/a\n"} {
		if SafePath(p) {
			t.Fatal(p)
		}
	}
}
