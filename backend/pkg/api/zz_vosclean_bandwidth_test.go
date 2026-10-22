package api

import (
	"testing"
	"time"
)

func TestParseWWANStats(t *testing.T) {
	out := "rc: 0, error: 0\nIPV4 (TX/RX) > bytes: ( 353983215 / 3044417262 ), packets: ( 763590 / 2717691 ), packets dropped: ( 0 / 0 )\n\nrc: 1, error: 1\n"
	rx, tx, ok := parseWWANStats(out)
	if !ok || rx != 3044417262 || tx != 353983215 {
		t.Fatalf("v4 only: %d %d %v", rx, tx, ok)
	}
	out += "IPV6 (TX/RX) > bytes: ( 100 / 200 ), packets: ( 1 / 2 )\n"
	if rx, tx, _ = parseWWANStats(out); rx != 3044417462 || tx != 353983315 {
		t.Fatalf("v4+v6: %d %d", rx, tx)
	}
	if _, _, ok = parseWWANStats("rc: 1, error: 1"); ok {
		t.Fatal("no counters should be !ok")
	}
}

func TestBwRate(t *testing.T) {
	t0 := time.Now()
	a := bwSample{rx: 1000, tx: 500, at: t0, ok: true}
	b := bwSample{rx: 126000, tx: 13000, at: t0.Add(time.Second), ok: true}
	if rx, tx := bwRate(a, b); rx != 1e6 || tx != 1e5 {
		t.Fatalf("rate: %v %v", rx, tx)
	}
	if rx, _ := bwRate(b, a); rx != 0 { // counter reset
		t.Fatal("reset should give 0")
	}
}
