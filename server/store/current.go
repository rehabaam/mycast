package store

import (
	"sync"

	"github.com/rehabaam/mycast/netatmo"
)

// CurrentCache holds the most recent live station reading so the API can serve
// it without calling Netatmo. The scheduler writes it; HTTP handlers read it.
//
// It lives here rather than inside the Netatmo client so that the client stays
// a plain transport, and how long a cached reading is trusted stays a decision
// for whoever serves it. The zero value is an empty cache, ready to use.
type CurrentCache struct {
	mu  sync.RWMutex
	cur *netatmo.Current
}

// Put records cur as the latest reading. The cache keeps its own copy.
func (c *CurrentCache) Put(cur *netatmo.Current) {
	cp := cur.Clone()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cur = cp
}

// Latest returns a copy of the latest reading. ok is false until Put has been
// called.
func (c *CurrentCache) Latest() (cur *netatmo.Current, ok bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.cur == nil {
		return nil, false
	}
	return c.cur.Clone(), true
}
