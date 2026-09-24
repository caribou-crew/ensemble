package serve

import (
	"sync"

	"github.com/caribou-crew/ensemble/retrace/diff"
)

type summaryEntry struct {
	once sync.Once
	sum  diff.Summary
	err  error
}

// summaryCache memoizes run-scoped diffs; keys carry both run identities and
// the config pointer, so entries never go stale. Failed builds are not kept.
type summaryCache struct {
	mu      sync.Mutex
	entries map[string]*summaryEntry
}

var summaries = &summaryCache{entries: map[string]*summaryEntry{}}

func (c *summaryCache) get(key string, build func() (diff.Summary, error)) (diff.Summary, error) {
	c.mu.Lock()
	e, ok := c.entries[key]
	if !ok {
		e = &summaryEntry{}
		c.entries[key] = e
	}
	c.mu.Unlock()
	e.once.Do(func() {
		e.sum, e.err = build()
		if e.err != nil {
			c.mu.Lock()
			if c.entries[key] == e {
				delete(c.entries, key)
			}
			c.mu.Unlock()
		}
	})
	return e.sum, e.err
}

func (c *summaryCache) flush() {
	c.mu.Lock()
	c.entries = map[string]*summaryEntry{}
	c.mu.Unlock()
}
