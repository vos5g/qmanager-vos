package api

// vosclean: APN username, password and authentication type.
//
// qcmd answers Quectel's AT+QICSGP from `cricli get_profile` / `set_profile`,
// which sets the APN, PDP type and credentials of profile 1 in one call, so the
// modem reconnects once per save.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

type apnAuth struct {
	user, pass string
	auth       int // 0 none, 1 PAP, 2 CHAP, 3 PAP or CHAP
}

var (
	reQicsgp   = regexp.MustCompile(`\+QICSGP:\s*(\d+),"([^"]*)","([^"]*)","([^"]*)",(\d+)`)
	reAPNName  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,62}$`)
	reAPNCred  = regexp.MustCompile(`^[!#-~]{0,64}$`) // printable ASCII without spaces or quotes
	pdpContext = map[string]int{"IP": 1, "IPV4": 1, "IPV6": 2, "IPV4V6": 3}
)

func parseQicsgp(resp string) (apnAuth, bool) {
	m := reQicsgp.FindStringSubmatch(resp)
	if m == nil {
		return apnAuth{}, false
	}
	a, _ := strconv.Atoi(m[5])
	return apnAuth{user: m[3], pass: m[4], auth: a}, true
}

func (s *Server) readAPNAuth() (apnAuth, bool) {
	resp, err := s.atClient.Exec(`AT+QICSGP=1`)
	if err != nil || strings.Contains(resp, "ERROR") {
		return apnAuth{}, false
	}
	return parseQicsgp(resp)
}

// apnAuthCommand validates the form and builds the AT+QICSGP write. keep is
// the stored password, used when password is nil.
func apnAuthCommand(cid int, pdpType, apn string, auth int, user string, password *string, keep string) (string, string) {
	if cid != 1 {
		return "", "only the internet profile (CID 1) can be changed on this modem"
	}
	ctx, ok := pdpContext[strings.ToUpper(pdpType)]
	if !ok {
		return "", "unknown PDP type " + pdpType
	}
	if !reAPNName.MatchString(apn) {
		return "", "the APN may only contain letters, digits, dots, dashes and underscores"
	}
	if auth < 0 || auth > 3 {
		return "", "unknown authentication type"
	}
	pass := keep
	if password != nil {
		pass = *password
	}
	if auth == 0 {
		user, pass = "", ""
	}
	if !reAPNCred.MatchString(user) || !reAPNCred.MatchString(pass) {
		return "", "username and password: up to 64 characters, without spaces or quotes"
	}
	return fmt.Sprintf(`AT+QICSGP=1,%d,"%s","%s","%s",%d`, ctx, apn, user, pass, auth), ""
}

func (s *Server) saveAPNWithAuth(w http.ResponseWriter, cid int, pdpType, apn string, auth int, user string, password *string) {
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	keep := ""
	if password == nil {
		if cur, ok := s.readAPNAuth(); ok {
			keep = cur.pass
		}
	}
	cmd, problem := apnAuthCommand(cid, pdpType, strings.TrimSpace(apn), auth, strings.TrimSpace(user), password, keep)
	if problem != "" {
		reply(map[string]any{"success": false, "error": "invalid_input", "detail": problem})
		return
	}
	resp, err := s.atClient.Exec(cmd)
	if err != nil || strings.Contains(resp, "ERROR") {
		reply(map[string]any{"success": false, "error": "at_error"})
		return
	}
	reply(map[string]any{"success": true, "cid": cid})
}
