package api

import (
	"strings"
	"testing"
)

func TestParseQicsgp(t *testing.T) {
	c, ok := parseQicsgp("+QICSGP: 3,\"three.co.uk\",\"bob\",\"pw\",2\n\nOK")
	if !ok || c.user != "bob" || c.pass != "pw" || c.auth != 2 {
		t.Fatalf("got %+v %v", c, ok)
	}
	if _, ok := parseQicsgp("ERROR"); ok {
		t.Fatal("parsed ERROR")
	}
}

func TestAPNAuthCommand(t *testing.T) {
	pw := "s3cret"
	empty := ""
	cmd, p := apnAuthCommand(1, "IPV4V6", "three.co.uk", 1, "bob", &pw, "old")
	if p != "" || cmd != `AT+QICSGP=1,3,"three.co.uk","bob","s3cret",1` {
		t.Fatalf("set: %q %q", cmd, p)
	}
	if cmd, _ = apnAuthCommand(1, "IPV4", "x", 3, "bob", nil, "old"); cmd != `AT+QICSGP=1,1,"x","bob","old",3` {
		t.Fatalf("keep: %q", cmd)
	}
	if cmd, _ = apnAuthCommand(1, "IPV4V6", "x", 2, "bob", &empty, "old"); cmd != `AT+QICSGP=1,3,"x","bob","",2` {
		t.Fatalf("clear: %q", cmd)
	}
	if cmd, _ = apnAuthCommand(1, "IPV6", "x", 0, "bob", &pw, "old"); cmd != `AT+QICSGP=1,2,"x","","",0` {
		t.Fatalf("none drops credentials: %q", cmd)
	}
	for _, bad := range []struct {
		cid       int
		pdp, apn  string
		auth      int
		user, pwd string
	}{
		{2, "IPV4V6", "x", 1, "u", "p"},
		{1, "PPP", "x", 1, "u", "p"},
		{1, "IPV4V6", `x","y`, 1, "u", "p"},
		{1, "IPV4V6", "x", 5, "u", "p"},
		{1, "IPV4V6", "x", 1, "u v", "p"},
		{1, "IPV4V6", "x", 1, "u", `p"`},
		{1, "IPV4V6", "x", 1, strings.Repeat("a", 65), "p"},
	} {
		pwd := bad.pwd
		if cmd, p := apnAuthCommand(bad.cid, bad.pdp, bad.apn, bad.auth, bad.user, &pwd, ""); p == "" {
			t.Errorf("accepted %+v -> %s", bad, cmd)
		}
	}
}
