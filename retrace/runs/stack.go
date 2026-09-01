package runs

import (
	"fmt"
	"sort"
	"time"
)

// Stack is what the backend was, at the moment this run was recorded: a
// fingerprint per service, and the last seed applied before it started.
//
// It exists to keep a diff from blaming the client for a change it did not
// make. Two runs against two different builds of a service produce a diff
// that looks exactly like a client regression — the client is the only thing
// the test touched, so it is the only thing anyone thinks to inspect. Without
// this record there is nothing to contradict that reading.
//
// Nothing parses or orders a fingerprint. They are compared for equality and
// nothing else, so a project can put a git sha, an image digest, or a build
// number in one without teaching retrace what any of those mean.
type Stack struct {
	// Services maps service name to fingerprint. A service ensemble could not
	// fingerprint is ABSENT rather than present-and-empty: the diff treats a
	// missing side as no evidence and reports no change, and an empty string
	// on one side against a real value on the other would read as a change
	// that nobody can substantiate. See validateStack, which enforces it.
	Services map[string]string `json:"services,omitempty"`
	// Seed is the last seed applied before the run, or nil if none was. Two
	// runs primed from different data are not comparable as behaviour, and
	// that difference is invisible in every other plane.
	Seed *SeedRef `json:"seed,omitempty"`
	// Passthrough names every service that was in "passthrough" placement
	// during this run — forwarding to a real remote environment instead of
	// a local process. Independent of Services: a passthrough target may
	// have no version fingerprint at all, and answers a different question
	// (is the chain downstream of this service witnessed, not which
	// backend answered). A reader uses it to annotate a run as reduced
	// scope past these specific services, rather than only being able to
	// say "reduced" or "not" for an entire run the way Mode/Hops do.
	Passthrough []string `json:"passthrough,omitempty"`
}

// SeedRef names one applied seed and when it was applied. The timestamp is
// what separates "seeded before both runs" from "reseeded between them",
// which is the distinction that matters when the name is the same.
type SeedRef struct {
	Name      string    `json:"name"`
	AppliedAt time.Time `json:"appliedAt"`
}

// validateStack rejects the encodings that would let a non-change read as a
// change. Called from both WriteManifest and ReadManifest — a manifest hand-
// edited between the two must fail the same way one written wrongly does.
func validateStack(s *Stack) error {
	if s == nil {
		return nil
	}
	for name, fingerprint := range s.Services {
		if name == "" {
			return fmt.Errorf("runs: manifest stack has a service with no name")
		}
		if fingerprint == "" {
			return fmt.Errorf("runs: manifest stack service %q has an empty fingerprint — omit the service instead, or it will read as a change against any run that did fingerprint it", name)
		}
	}
	if s.Seed != nil && s.Seed.Name == "" {
		return fmt.Errorf("runs: manifest stack seed has no name — omit the seed instead, so \"never seeded\" stays distinguishable from \"seeded by something nameless\"")
	}
	for _, name := range s.Passthrough {
		if name == "" {
			return fmt.Errorf("runs: manifest stack has an empty passthrough service name")
		}
	}
	return nil
}

// SameStack reports whether two runs' stacks are indistinguishable, and names
// the services that are demonstrably different.
//
// Only services fingerprinted on BOTH sides are compared. A service one side
// could not fingerprint is an absence of evidence, not a difference — the
// same rule diff.geometryCheck applies to an unrecorded screen, and for the
// same reason: a guard that fires on missing data fires on every run recorded
// before the feature existed, and on every service whose directory is not a
// repository.
//
// The seed is compared by name AND time. The same seed re-applied between two
// runs is a real difference: whatever the first run left behind is gone.
func SameStack(a, b *Stack) (changed []string, seedMoved bool) {
	if a == nil || b == nil {
		return nil, false
	}
	for name, av := range a.Services {
		bv, ok := b.Services[name]
		if !ok || av == "" || bv == "" {
			continue
		}
		if av != bv {
			changed = append(changed, name)
		}
	}
	// Sorted so the reported list is the same on every run: map iteration
	// order is randomized, and a set of offenders that reshuffles between two
	// identical runs makes a report look unstable when nothing moved.
	sort.Strings(changed)
	if a.Seed != nil && b.Seed != nil {
		seedMoved = a.Seed.Name != b.Seed.Name || !a.Seed.AppliedAt.Equal(b.Seed.AppliedAt)
	}
	return changed, seedMoved
}
