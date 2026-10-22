package api

import "testing"

func TestStLatencyStats(t *testing.T) {
	got := stLatencyStats([]float64{30, 10, 20, 100, 25, 15, 22, 18})
	// sorted: 10 15 18 20 22 25 30 100 -> middle half 18 20 22 25 -> 21.25
	if got["iqm"] != 21.3 || got["low"] != 10.0 || got["high"] != 100.0 {
		t.Fatalf("unexpected stats: %v", got)
	}
	if e := stLatencyStats(nil); e["iqm"] != 0.0 {
		t.Fatalf("empty: %v", e)
	}
	if one := stLatencyStats([]float64{42}); one["iqm"] != 42.0 || one["jitter"] != 0.0 {
		t.Fatalf("single: %v", one)
	}
}

func TestStHostPort(t *testing.T) {
	if h, p := stHostPort("speedtest.example.net:8080"); h != "speedtest.example.net" || p != 8080 {
		t.Fatal(h, p)
	}
	if h, p := stHostPort("noport.example"); h != "noport.example" || p != 8080 {
		t.Fatal(h, p)
	}
}
