// deviations.go — a deviation is a recorded human decision to tolerate a
// specific difference between two apps' runs: "these two are expected to
// differ here, and here is why". Task 11 owns the ledger that loads,
// resolves and matches them. The TYPES live here because this package's
// own structs reference them, and a struct field whose type is undeclared
// does not compile.
package diff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/caribou-crew/ensemble/retrace/rules"
)

// Deviation is one entry in the ledger file named by config.Deviations.
// Status is "proposed" | "approved": teams that want the ceremony gate on
// approved; teams that do not, approve on write.
type Deviation struct {
	ID     string    `json:"id"`
	Status string    `json:"status"`
	Apps   [2]string `json:"apps"`
	Method string    `json:"method"`
	Path   string    `json:"path"`
	Reason string    `json:"reason"`
}

// ToleratedNote is what a consumer sees on a difference a Deviation
// covered: the difference still happened and is still reported, it just
// does not count against the verdict. Never drop the difference itself —
// "tolerated" and "absent" must never look the same to a reviewer.
type ToleratedNote struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// --- the ledger --------------------------------------------------------
//
// Ported in spirit from flowlens' src/deviations.mjs. The ceremony is the
// point: an agent can append a `proposed` entry — visible, git-diffable and
// completely inert — and only a human flipping it to `approved` makes
// retrace honor it. Teams that do not want the ceremony approve on write.

// deviationStatuses is the closed set. A status outside it is a typo, and a
// typo must not silently become "not approved, therefore harmless": the
// harm is the opposite direction — a reviewer who typed "aproved" believes
// a difference is sanctioned when it is still counting.
var deviationStatuses = map[string]bool{"proposed": true, "approved": true}

// LoadDeviations reads the ledger file named by config.Deviations.
//
// A file that is NAMED but absent is an error, not an empty ledger: a
// config pointing at a path that does not exist is a misconfiguration, and
// the honest failure is "this diff could not be evaluated" (exit 3), not a
// silent run with nothing tolerated. Contrast readOverlay in retrace/config,
// where a missing file genuinely means "no rule has been reviewed yet" —
// that file is machine-owned and nobody names it.
//
// DisallowUnknownFields matches the strictness the wire-rule overlay and
// retrace.yaml both use: a mis-shaped entry (a typo'd key) must fail loudly
// rather than decode into a zero-value Deviation, which — with an empty
// Method and Path — would be the match-everything tolerance nobody wrote.
//
// Every validation error names the entry's INDEX, because the ledger is
// hand-edited and "some entry is wrong" is not a repairable message.
func LoadDeviations(file string) ([]Deviation, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("deviations ledger %s: %w", file, err)
	}
	var out []Deviation
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("deviations ledger %s: %w", file, err)
	}
	for i, d := range out {
		where := fmt.Sprintf("deviations ledger %s[%d]", file, i)
		switch {
		case strings.TrimSpace(d.ID) == "":
			return nil, fmt.Errorf("%s: id is required — a deviation nobody can refer to cannot be reviewed or revoked", where)
		case !deviationStatuses[d.Status]:
			return nil, fmt.Errorf("%s (%s): status is %q, want \"proposed\" or \"approved\"", where, d.ID, d.Status)
		case strings.TrimSpace(d.Apps[0]) == "" || strings.TrimSpace(d.Apps[1]) == "":
			return nil, fmt.Errorf("%s (%s): apps must name BOTH sides of the comparison this deviation covers, got %q and %q", where, d.ID, d.Apps[0], d.Apps[1])
		case strings.TrimSpace(d.Method) == "":
			return nil, fmt.Errorf("%s (%s): method is required — an empty method would tolerate every verb on that path", where, d.ID)
		case strings.TrimSpace(d.Path) == "":
			return nil, fmt.Errorf("%s (%s): path is required — an empty path glob matches every call", where, d.ID)
		case strings.TrimSpace(d.Reason) == "":
			return nil, fmt.Errorf("%s (%s): reason is required — an unexplained deviation is indistinguishable from one added to silence a regression, and deviations outlive the person who added them", where, d.ID)
		}
	}
	return out, nil
}

// ResolveDeviations narrows a ledger to the entries that apply to ONE
// comparison: approved, and covering this app pair in either direction.
//
// The pair is unordered because the deviation is a fact about the two apps,
// not about which one a reviewer happened to type first — resolving it one
// way and not the other would make `retrace diff --a web --b web-next` and
// its mirror disagree about what is sanctioned.
//
// Returning a filtered slice (never a flag on the entries) is what keeps
// FindDeviation from having to re-check approval: an unapproved entry
// cannot be in the list it searches.
func ResolveDeviations(ds []Deviation, appA, appB string) []Deviation {
	var out []Deviation
	for _, d := range ds {
		if d.Status != "approved" {
			continue
		}
		if (d.Apps[0] == appA && d.Apps[1] == appB) || (d.Apps[0] == appB && d.Apps[1] == appA) {
			out = append(out, d)
		}
	}
	return out
}

// FindDeviation returns the first entry covering one call, or nil.
//
// ds must already be through ResolveDeviations. Method comparison is
// case-insensitive; Path is the same '/'-segmented glob dialect the rest of
// this tree uses (rules.MatchPathGlob), so "/orders/*/legacy" covers a
// route with an id in it — the normal case for a deviation.
//
// nil means "no deviation covers this", which is the SAFE answer: a caller
// that gets nil reports the difference. There is no zero-value Deviation
// that could be returned instead — an empty one has an empty Method and
// Path, which under MatchPathGlob would match everything.
func FindDeviation(ds []Deviation, method, path string) *Deviation {
	for i := range ds {
		if !strings.EqualFold(ds[i].Method, method) {
			continue
		}
		if rules.MatchPathGlob(ds[i].Path, path) {
			return &ds[i]
		}
	}
	return nil
}

// applyDeviations annotates every call in calls that an approved deviation
// covers. It ANNOTATES; it never removes. The difference still happened and
// is still reported to every consumer — "tolerated" and "absent" must never
// look the same to a reviewer — it just stops counting as a finding (see
// countOf in summary.go, which skips a Call carrying a note).
func applyDeviations(calls []Call, ds []Deviation) []Call {
	if len(ds) == 0 {
		return calls
	}
	for i := range calls {
		if d := FindDeviation(ds, calls[i].Method, calls[i].Path); d != nil {
			calls[i].Tolerated = &ToleratedNote{ID: d.ID, Reason: d.Reason}
		}
	}
	return calls
}
