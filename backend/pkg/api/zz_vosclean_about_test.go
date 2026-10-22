package api

import "testing"

func TestVosAboutParsers(t *testing.T) {
	pv := "Linux version 4.14.117-perf (oe-user@oe-host) (clang version 6.0.9 for Android NDK) #1 PREEMPT Tue Dec 10 04:37:41 UTC 2024"
	if got := vosKernelBuildDate(pv); got != "2024-12-10" {
		t.Fatalf("kernel build date: got %q", got)
	}
	if got := vosKernelBuildDate("Linux version 6.1 #3 SMP Mon Jan  6 01:02:03 UTC 2025"); got != "2025-01-06" {
		t.Fatalf("kernel build date (padded day): got %q", got)
	}
	if got := vosFormatStamp("202412101209"); got != "2024-12-10 12:09" {
		t.Fatalf("stamp: got %q", got)
	}
	if got := vosFormatStamp("abc"); got != "abc" {
		t.Fatalf("stamp passthrough: got %q", got)
	}
	if dashIfEmpty(" ") != "-" || dashIfEmpty("x") != "x" {
		t.Fatal("dashIfEmpty")
	}
}
