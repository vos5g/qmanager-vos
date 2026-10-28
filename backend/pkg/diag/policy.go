package diag

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
)

// Audited source containers from maigougou/vos5g-all-band-unlock (MIT).
var supported = map[string]bool{
	"adec6f0d011f96d0091ddd407dc6fcb7068981553bdcaf106d05d50a67d773a4": true,
	"8d326546fe91c2b78e9ada3b9a88678bef51b821ef3aac54efac40003a1b7c45": true,
	"1502c1d9409f817ea962199b0d65437f96081d79db7d437ef4a102c2f4062858": true,
}

func Hash(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }

type Policy struct {
	Original     []byte
	Policy       []byte
	Hardware     []byte
	Target       []byte
	TargetPolicy []byte
	Supported    bool
	Unlocked     bool
}

func xmlRoot(data []byte, want string) error {
	d := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	seen := false
	for {
		t, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		switch v := t.(type) {
		case xml.StartElement:
			if depth == 0 {
				if seen || v.Name.Local != want || v.Name.Space != "" {
					return errors.New("unexpected policy XML root")
				}
				seen = true
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.Directive:
			return errors.New("XML directives are unsupported")
		case xml.CharData:
			if depth == 0 && len(bytes.TrimSpace(v)) != 0 {
				return errors.New("text outside XML root")
			}
		}
	}
	if !seen || depth != 0 {
		return errors.New("incomplete XML")
	}
	return nil
}
func ParsePolicy(data []byte) (*Policy, error) {
	if len(data) < 176 || len(data) > MaxFile {
		return nil, errors.New("invalid modem_config size")
	}
	plen := uint64(binary.LittleEndian.Uint32(data[168:]))
	hlen := uint64(binary.LittleEndian.Uint32(data[172:]))
	if 176+plen+hlen != uint64(len(data)) {
		return nil, errors.New("modem_config length mismatch")
	}
	p := &Policy{Original: append([]byte{}, data...), Policy: data[176 : 176+plen], Hardware: data[176+plen:], Supported: supported[Hash(data)]}
	if e := xmlRoot(p.Policy, "policy"); e != nil {
		return nil, e
	}
	if e := xmlRoot(p.Hardware, "hw_bands_filter"); e != nil {
		return nil, e
	}
	nl := "\n"
	if bytes.Contains(p.Policy, []byte("\r\n")) {
		nl = "\r\n"
	}
	start, end := []byte("  <select>"+nl), []byte("  </select>"+nl)
	if bytes.Count(p.Policy, start) != 1 || bytes.Count(p.Policy, end) != 1 {
		return nil, errors.New("expected one final policy selector")
	}
	i := bytes.Index(p.Policy, start)
	j := bytes.Index(p.Policy, end) + len(end)
	if j <= i {
		return nil, errors.New("invalid policy selector")
	}
	selector := []byte("  <select>" + nl + "    <case>" + nl + "      <true />" + nl + "      <then>" + nl + "        <rf_bands base=\"hardware\" />" + nl + "      </then>" + nl + "    </case>" + nl + "  </select>" + nl)
	p.Unlocked = bytes.Equal(p.Policy[i:j], selector)
	p.TargetPolicy = append(append(append([]byte{}, p.Policy[:i]...), selector...), p.Policy[j:]...)
	if e := xmlRoot(p.TargetPolicy, "policy"); e != nil {
		return nil, e
	}
	p.Target = append(append(append([]byte{}, data[:176]...), p.TargetPolicy...), p.Hardware...)
	binary.LittleEndian.PutUint32(p.Target[168:], uint32(len(p.TargetPolicy)))
	return p, nil
}
