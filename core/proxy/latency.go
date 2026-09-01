package proxy

import (
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"
)

// LatencyRule injects artificial delay for one target+path prefix. Either
// FixedMs or the P50/P95/P99 distribution is used; a rule only fires when
// Enabled ("armed") — pulled APM profiles are stored disarmed by design.
type LatencyRule struct {
	Target  string  `json:"target"` // service name; "*" matches any
	Path    string  `json:"path"`   // prefix; longest prefix wins
	FixedMs float64 `json:"fixedMs,omitempty"`
	P50     float64 `json:"p50,omitempty"`
	P95     float64 `json:"p95,omitempty"`
	P99     float64 `json:"p99,omitempty"`
	Enabled bool    `json:"enabled"`
	// Source describes where this rule's values came from: empty for a
	// manually `set` rule, otherwise a human-readable Datadog query +
	// window (e.g. "datadog:p{P}:trace...{...} (last 60m)"). Purely
	// informational — DelayFor ignores it.
	Source string `json:"source,omitempty"`
}

// LatencyStore holds live-editable delay rules. All methods are safe for
// concurrent use from the proxy hot path and the control API.
type LatencyStore struct {
	mu       sync.Mutex
	rules    []LatencyRule
	uniform  func() float64            // injectable for tests; nil -> math/rand/v2
	onChange func(rules []LatencyRule) // optional; see OnChange
}

func NewLatencyStore(uniform func() float64) *LatencyStore {
	if uniform == nil {
		uniform = rand.Float64
	}
	return &LatencyStore{uniform: uniform}
}

// OnChange registers fn to be called, with a snapshot equivalent to
// Rules(), after every mutation (Set/Remove/Reset/ArmAll) — the hook a
// caller uses to persist rules to disk without core/proxy itself knowing
// anything about files. fn runs outside the store's lock, after the
// mutation is already visible to DelayFor/Rules; pass nil to disable. Not
// called for read-only methods.
func (s *LatencyStore) OnChange(fn func(rules []LatencyRule)) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

// notify snapshots the current rules and invokes onChange, if any, outside
// the lock — called by every mutator after it unlocks.
func (s *LatencyStore) notify() {
	s.mu.Lock()
	fn := s.onChange
	rules := append([]LatencyRule(nil), s.rules...)
	s.mu.Unlock()
	if fn != nil {
		fn(rules)
	}
}

// Set upserts a rule keyed by (target, path).
func (s *LatencyStore) Set(rule LatencyRule) {
	s.mu.Lock()
	found := false
	for i := range s.rules {
		if s.rules[i].Target == rule.Target && s.rules[i].Path == rule.Path {
			s.rules[i] = rule
			found = true
			break
		}
	}
	if !found {
		s.rules = append(s.rules, rule)
	}
	s.mu.Unlock()
	s.notify()
}

func (s *LatencyStore) Remove(target, path string) {
	s.mu.Lock()
	kept := s.rules[:0]
	for _, r := range s.rules {
		if !(r.Target == target && r.Path == path) {
			kept = append(kept, r)
		}
	}
	s.rules = kept
	s.mu.Unlock()
	s.notify()
}

// Rules returns a copy, sorted for stable display.
func (s *LatencyStore) Rules() []LatencyRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LatencyRule, len(s.rules))
	copy(out, s.rules)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Target != out[j].Target {
			return out[i].Target < out[j].Target
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// Reset removes every rule.
func (s *LatencyStore) Reset() {
	s.mu.Lock()
	s.rules = nil
	s.mu.Unlock()
	s.notify()
}

// ArmAll toggles every rule's Enabled flag.
func (s *LatencyStore) ArmAll(enabled bool) {
	s.mu.Lock()
	for i := range s.rules {
		s.rules[i].Enabled = enabled
	}
	s.mu.Unlock()
	s.notify()
}

// DelayFor picks the winning rule for a request: an exact-target rule beats
// a wildcard, and within a target the longest matching path prefix wins.
func (s *LatencyStore) DelayFor(target, path string) time.Duration {
	return s.delayFor(target, path, true)
}

// DelayForExact is DelayFor without the "*" wildcard fallback — only a rule
// naming target exactly can inject delay. Used for a passthrough target: a
// stack-wide latency rule must not silently reach a real remote environment
// just because it happened to match everything else.
func (s *LatencyStore) DelayForExact(target, path string) time.Duration {
	return s.delayFor(target, path, false)
}

func (s *LatencyStore) delayFor(target, path string, allowWildcard bool) time.Duration {
	s.mu.Lock()
	var best *LatencyRule
	bestLen, bestExact := -1, false
	for i := range s.rules {
		r := &s.rules[i]
		if !r.Enabled {
			continue
		}
		exact := r.Target == target
		if !exact {
			if !allowWildcard || r.Target != "*" {
				continue
			}
		}
		if !strings.HasPrefix(path, r.Path) {
			continue
		}
		if (exact && !bestExact) || (exact == bestExact && len(r.Path) > bestLen) {
			best, bestLen, bestExact = r, len(r.Path), exact
		}
	}
	if best == nil {
		s.mu.Unlock()
		return 0
	}
	rule := *best
	u := s.uniform()
	s.mu.Unlock()

	ms := rule.FixedMs
	if ms == 0 && rule.P50+rule.P95+rule.P99 > 0 {
		ms = sampleQuantiles(u, rule.P50, rule.P95, rule.P99)
	}
	return time.Duration(ms * float64(time.Millisecond))
}

// sampleQuantiles draws from a piecewise-linear CDF anchored at
// (0,0) (0.5,p50) (0.95,p95) (0.99,p99) (1,p99).
func sampleQuantiles(u, p50, p95, p99 float64) float64 {
	type anchor struct{ q, v float64 }
	anchors := []anchor{{0, 0}, {0.5, p50}, {0.95, p95}, {0.99, p99}, {1, p99}}
	for i := 1; i < len(anchors); i++ {
		lo, hi := anchors[i-1], anchors[i]
		if u <= hi.q {
			if hi.q == lo.q {
				return hi.v
			}
			frac := (u - lo.q) / (hi.q - lo.q)
			return lo.v + frac*(hi.v-lo.v)
		}
	}
	return p99
}
