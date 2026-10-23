package api

import "testing"

func TestParseQNWCFGTimeAdvance(t *testing.T) {
	for in, want := range map[string]string{
		"+QNWCFG: \"lte_time_advance\",1,19\n\nOK": "19",
		"+QNWCFG: \"lte_time_advance\",1,-\n\nOK":  "",
		"ERROR": "",
	} {
		if got := parseQNWCFGTimeAdvance(in, "lte_time_advance"); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}
