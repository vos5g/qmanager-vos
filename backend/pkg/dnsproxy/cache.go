package dnsproxy

import (
	"strconv"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// cache keeps recent answers so repeat lookups skip the encrypted round trip.
// Answers are stored packed; TTLs are lowered by the time spent in the cache.
type cache struct {
	mu  sync.Mutex
	max int
	m   map[string]*cacheEntry
	now func() time.Time
}

type cacheEntry struct {
	msg    []byte
	stored time.Time
	exp    time.Time
}

func newCache(max int) *cache {
	return &cache{max: max, m: map[string]*cacheEntry{}, now: time.Now}
}

func cacheKey(name string, t dnsmessage.Type, c dnsmessage.Class) string {
	return name + "|" + strconv.Itoa(int(t)) + "|" + strconv.Itoa(int(c))
}

func (c *cache) clear() {
	c.mu.Lock()
	c.m = map[string]*cacheEntry{}
	c.mu.Unlock()
}

const (
	maxCacheTTL      = 3600
	negativeCacheTTL = 60
)

// put stores a response if it is a cacheable success or NXDOMAIN.
func (c *cache) put(key string, resp []byte) {
	var m dnsmessage.Message
	if err := m.Unpack(resp); err != nil || m.Truncated {
		return
	}
	var ttl uint32
	switch m.RCode {
	case dnsmessage.RCodeSuccess:
		if len(m.Answers) == 0 {
			ttl = negativeCacheTTL
		} else {
			ttl = maxCacheTTL
			for _, a := range m.Answers {
				if a.Header.TTL < ttl {
					ttl = a.Header.TTL
				}
			}
		}
	case dnsmessage.RCodeNameError:
		ttl = negativeCacheTTL
	default:
		return
	}
	if ttl == 0 {
		return
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= c.max {
		// Drop expired entries first, then anything, to stay bounded.
		for k, e := range c.m {
			if now.After(e.exp) {
				delete(c.m, k)
			}
		}
		for k := range c.m {
			if len(c.m) < c.max {
				break
			}
			delete(c.m, k)
		}
	}
	c.m[key] = &cacheEntry{msg: append([]byte(nil), resp...), stored: now, exp: now.Add(time.Duration(ttl) * time.Second)}
}

// get returns a copy of a cached response with the given ID and aged TTLs.
func (c *cache) get(key string, id uint16) []byte {
	now := c.now()
	c.mu.Lock()
	e, ok := c.m[key]
	if ok && !now.Before(e.exp) {
		delete(c.m, key)
		ok = false
	}
	c.mu.Unlock()
	if !ok {
		return nil
	}
	var m dnsmessage.Message
	if err := m.Unpack(e.msg); err != nil {
		return nil
	}
	age := uint32(now.Sub(e.stored) / time.Second)
	m.ID = id
	for _, sec := range [][]dnsmessage.Resource{m.Answers, m.Authorities, m.Additionals} {
		for i := range sec {
			if sec[i].Header.Type == dnsmessage.TypeOPT {
				continue
			}
			if sec[i].Header.TTL > age {
				sec[i].Header.TTL -= age
			} else {
				sec[i].Header.TTL = 1
			}
		}
	}
	out, err := m.Pack()
	if err != nil {
		return nil
	}
	return out
}
