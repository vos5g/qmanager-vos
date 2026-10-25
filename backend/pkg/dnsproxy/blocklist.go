// Package dnsproxy is the small DNS forwarder behind QManager's Ad Blocking and
// Encrypted DNS settings on the VOS stick.
//
// Every LAN client asks dnsmasq (192.168.225.1), and dnsmasq asks the operator
// resolvers in /etc/resolv.conf. vosclean redirects those upstream queries to
// this proxy (127.0.0.1:5335). The proxy answers blocked names itself and sends
// the rest to an encrypted resolver (DoH/DoT) or a plain one. Its own outgoing
// sockets carry a firewall mark that vosclean exempts, so they are not redirected
// back to it.
package dnsproxy

import (
	"bufio"
	"io"
	"sort"
	"strings"
)

// Blocklist is an immutable, sorted set of blocked domains. A domain also
// blocks all of its subdomains. Allowed names win over blocked ones.
type Blocklist struct {
	blocked []string // sorted, unique, lower case
	allowed []string // sorted, unique, lower case
}

// NewBlocklist builds a blocklist from domain slices (any order, duplicates ok).
func NewBlocklist(blocked, allowed []string) *Blocklist {
	return &Blocklist{blocked: sortUnique(blocked), allowed: sortUnique(allowed)}
}

// Len is the number of blocked entries.
func (b *Blocklist) Len() int {
	if b == nil {
		return 0
	}
	return len(b.blocked)
}

// Domains returns the sorted blocked entries (shared, do not modify).
func (b *Blocklist) Domains() []string {
	if b == nil {
		return nil
	}
	return b.blocked
}

// Blocked reports whether name (with or without trailing dot) is blocked.
func (b *Blocklist) Blocked(name string) bool {
	if b == nil || len(b.blocked) == 0 {
		return false
	}
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if name == "" {
		return false
	}
	if matchSuffix(b.allowed, name) {
		return false
	}
	return matchSuffix(b.blocked, name)
}

// matchSuffix checks name and each parent domain against a sorted set.
func matchSuffix(set []string, name string) bool {
	if len(set) == 0 {
		return false
	}
	for {
		i := sort.SearchStrings(set, name)
		if i < len(set) && set[i] == name {
			return true
		}
		dot := strings.IndexByte(name, '.')
		if dot < 0 {
			return false
		}
		name = name[dot+1:]
	}
}

func sortUnique(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = NormalizeDomain(s); s != "" {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	j := 0
	for i, s := range out {
		if i == 0 || s != out[j-1] {
			out[j] = s
			j++
		}
	}
	return out[:j]
}

// NormalizeDomain lower-cases and validates a domain; "" when it isn't one.
func NormalizeDomain(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "*.")
	s = strings.TrimSuffix(s, ".")
	if len(s) == 0 || len(s) > 253 || !strings.Contains(s, ".") {
		return ""
	}
	labels := strings.Split(s, ".")
	if strings.Trim(labels[len(labels)-1], "0123456789") == "" {
		return "" // an IP address, not a name
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return ""
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
				return ""
			}
		}
	}
	return s
}

// ParseList reads a blocklist in hosts, adblock (||domain^) or plain-domain
// format and calls add for every domain. Comments, allow rules (@@), cosmetic
// rules and lines with options are skipped. It returns the number of domains.
func ParseList(r io.Reader, add func(string)) (int, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	n := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == '!' || line[0] == '[' || strings.HasPrefix(line, "@@") {
			continue
		}
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		var d string
		switch {
		case strings.HasPrefix(line, "||"):
			// adblock: ||example.com^  (skip anything with options or paths)
			rest := line[2:]
			end := strings.IndexAny(rest, "^/$*")
			if end < 0 {
				d = rest
			} else if rest[end] == '^' && (end+1 == len(rest)) {
				d = rest[:end]
			} else {
				continue
			}
		default:
			f := strings.Fields(line)
			switch len(f) {
			case 1:
				d = f[0]
			case 2:
				// hosts: 0.0.0.0 example.com
				switch f[0] {
				case "0.0.0.0", "127.0.0.1", "::", "::1", "0:0:0:0:0:0:0:0":
					d = f[1]
				default:
					continue
				}
			default:
				continue
			}
		}
		d = NormalizeDomain(d)
		switch d {
		case "", "localhost.localdomain", "local", "broadcasthost":
			continue
		}
		add(d)
		n++
	}
	return n, sc.Err()
}
