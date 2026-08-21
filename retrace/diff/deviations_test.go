package diff

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

func writeLedger(t *testing.T, dir, name string, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func ledgerJSON(t *testing.T, ds ...Deviation) string {
	t.Helper()
	b, err := json.MarshalIndent(ds, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func dev(id, status, appA, appB, method, path string) Deviation {
	return Deviation{ID: id, Status: status, Apps: [2]string{appA, appB}, Method: method, Path: path, Reason: "the two checkouts differ here on purpose"}
}

// TestOnlyApprovedDeviationsApply — an agent may append `proposed` entries
// freely: they are visible, git-diffable and INERT. Only a human flipping
// the status to `approved` makes retrace honor one.
func TestOnlyApprovedDeviationsApply(t *testing.T) {
	ds := []Deviation{
		dev("d-proposed", "proposed", "web", "web-next", "GET", "/only-a"),
		dev("d-approved", "approved", "web", "web-next", "GET", "/only-b"),
	}
	got := ResolveDeviations(ds, "web", "web-next")
	if len(got) != 1 || got[0].ID != "d-approved" {
		t.Fatalf("ResolveDeviations = %+v, want only d-approved — a proposed entry must be inert", got)
	}
	// And the inert one must not sneak back in through FindDeviation on the
	// UNRESOLVED list either: resolution is the gate, but a caller reaching
	// past it must not find a tolerance that no human approved.
	if d := FindDeviation(got, "GET", "/only-a"); d != nil {
		t.Fatalf("FindDeviation found %+v for the proposed entry's path — proposed must never match", d)
	}
	if d := FindDeviation(got, "GET", "/only-b"); d == nil || d.ID != "d-approved" {
		t.Fatalf("FindDeviation = %+v, want d-approved", d)
	}
}

// TestAppPairMatchingIsOrderIndependent — a deviation between web and
// web-next covers the comparison in either direction; which side a reviewer
// happened to name first is not a semantic difference. The fixture is
// asymmetric in the dimension under test: appA != appB, and a third app is
// present that must NOT match, so "everything matches" also fails.
func TestAppPairMatchingIsOrderIndependent(t *testing.T) {
	ds := []Deviation{
		dev("d1", "approved", "web", "web-next", "GET", "/x"),
		dev("d2", "approved", "web", "legacy", "GET", "/y"),
	}
	forward := ResolveDeviations(ds, "web", "web-next")
	if len(forward) != 1 || forward[0].ID != "d1" {
		t.Fatalf("ResolveDeviations(web, web-next) = %+v, want only d1", forward)
	}
	reverse := ResolveDeviations(ds, "web-next", "web")
	if len(reverse) != 1 || reverse[0].ID != "d1" {
		t.Fatalf("ResolveDeviations(web-next, web) = %+v, want only d1 — the pair is unordered", reverse)
	}
	if got := ResolveDeviations(ds, "web", "unrelated"); len(got) != 0 {
		t.Fatalf("ResolveDeviations(web, unrelated) = %+v, want none — a pair that shares ONE app is not the pair", got)
	}
	if got := ResolveDeviations(ds, "legacy", "web-next"); len(got) != 0 {
		t.Fatalf("ResolveDeviations(legacy, web-next) = %+v, want none", got)
	}
}

// TestAMalformedEntryIsAnErrorNamingItsIndex — the ledger is hand-edited,
// so a typo must fail loudly at the index a human can find, never decode
// into a zero-value Deviation that tolerates something nobody wrote down.
func TestAMalformedEntryIsAnErrorNamingItsIndex(t *testing.T) {
	dir := t.TempDir()
	good := dev("d0", "approved", "web", "web-next", "GET", "/x")
	cases := []struct {
		name string
		bad  Deviation
		want string
	}{
		{"no id", Deviation{Status: "approved", Apps: [2]string{"web", "web-next"}, Method: "GET", Path: "/y", Reason: "r"}, "id"},
		{"unknown status", Deviation{ID: "d1", Status: "blessed", Apps: [2]string{"web", "web-next"}, Method: "GET", Path: "/y", Reason: "r"}, "status"},
		{"empty status", Deviation{ID: "d1", Apps: [2]string{"web", "web-next"}, Method: "GET", Path: "/y", Reason: "r"}, "status"},
		{"one app", Deviation{ID: "d1", Status: "approved", Apps: [2]string{"web", ""}, Method: "GET", Path: "/y", Reason: "r"}, "apps"},
		{"no method", Deviation{ID: "d1", Status: "approved", Apps: [2]string{"web", "web-next"}, Path: "/y", Reason: "r"}, "method"},
		{"no path", Deviation{ID: "d1", Status: "approved", Apps: [2]string{"web", "web-next"}, Method: "GET", Reason: "r"}, "path"},
		{"no reason", Deviation{ID: "d1", Status: "approved", Apps: [2]string{"web", "web-next"}, Method: "GET", Path: "/y"}, "reason"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The bad entry is at index 1, never 0: an implementation that
			// hardcoded the index, or reported the count instead, would pass
			// a single-entry fixture.
			file := writeLedger(t, dir, "dev-"+strings.ReplaceAll(c.name, " ", "-")+".json", ledgerJSON(t, good, c.bad))
			got, err := LoadDeviations(file)
			if err == nil {
				t.Fatalf("LoadDeviations = %+v, nil — want a rejection", got)
			}
			if got != nil {
				t.Fatalf("LoadDeviations returned %+v alongside its error — a rejected ledger must yield no entries", got)
			}
			if !strings.Contains(err.Error(), "[1]") {
				t.Fatalf("error = %q, want it to name the offending index [1]", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %q, want it to name the offending field %q", err, c.want)
			}
		})
	}

	t.Run("an unknown key", func(t *testing.T) {
		file := writeLedger(t, dir, "unknown-key.json", `[{"id":"d1","status":"approved","apps":["web","web-next"],"method":"GET","path":"/y","reason":"r","reasonn":"typo"}]`)
		if _, err := LoadDeviations(file); err == nil {
			t.Fatal("LoadDeviations accepted an unknown key — a typo'd key must fail loudly, as it does for the wire-rule overlay")
		}
	})

	t.Run("a ledger named but absent", func(t *testing.T) {
		if _, err := LoadDeviations(filepath.Join(dir, "not-here.json")); err == nil {
			t.Fatal("LoadDeviations accepted a missing file — a config that NAMES a ledger and cannot find it is a misconfiguration, not an empty ledger")
		}
	})

	t.Run("a well-formed ledger loads", func(t *testing.T) {
		file := writeLedger(t, dir, "ok.json", ledgerJSON(t, good, dev("d1", "proposed", "web", "web-next", "POST", "/z")))
		got, err := LoadDeviations(file)
		if err != nil {
			t.Fatalf("LoadDeviations: %v", err)
		}
		if len(got) != 2 || got[0].ID != "d0" || got[1].Status != "proposed" {
			t.Fatalf("LoadDeviations = %+v, want both entries in file order", got)
		}
	})
}

// TestASanctionedDeviationAnnotatesButDoesNotHide — a matched deviation
// ANNOTATES; it never removes the entry. The difference still happened, is
// still reported to every consumer, and only stops counting as a finding.
//
// Both arms are exercised: a call only side A made (Missing) and a call
// only side B made (Extra). Mutating one arm alone is how a mirrored defect
// survives a perfect scorecard (global-constraints.md, mutation-set
// symmetry), so the fixture and the assertions cover both.
func TestASanctionedDeviationAnnotatesButDoesNotHide(t *testing.T) {
	a := []trace.Hop{hop(1, "GET", "/only-a", 200, "", ""), hop(2, "GET", "/shared", 200, "", "")}
	b := []trace.Hop{hop(1, "GET", "/only-b", 200, "", ""), hop(2, "GET", "/shared", 200, "", "")}
	ds := []Deviation{
		{ID: "d-missing", Status: "approved", Apps: [2]string{"web", "web-next"}, Method: "GET", Path: "/only-a", Reason: "legacy prefetch, dropped on purpose"},
		{ID: "d-extra", Status: "approved", Apps: [2]string{"web", "web-next"}, Method: "GET", Path: "/only-b", Reason: "new telemetry beacon, approved"},
	}
	w := DiffWire(a, b, Options{Deviations: ResolveDeviations(ds, "web", "web-next")})

	if len(w.Missing) != 1 || w.Missing[0].Path != "/only-a" {
		t.Fatalf("Wire.Missing = %+v, want the tolerated call still listed — tolerated and absent must never look the same", w.Missing)
	}
	if w.Missing[0].Tolerated == nil {
		t.Fatal("Wire.Missing[0].Tolerated is nil, want the annotation")
	}
	if w.Missing[0].Tolerated.ID != "d-missing" || !strings.Contains(w.Missing[0].Tolerated.Reason, "legacy prefetch") {
		t.Fatalf("Missing[0].Tolerated = %+v, want d-missing and its reason", w.Missing[0].Tolerated)
	}
	if len(w.Extra) != 1 || w.Extra[0].Path != "/only-b" {
		t.Fatalf("Wire.Extra = %+v, want the tolerated call still listed", w.Extra)
	}
	if w.Extra[0].Tolerated == nil {
		t.Fatal("Wire.Extra[0].Tolerated is nil, want the annotation")
	}
	if w.Extra[0].Tolerated.ID != "d-extra" || !strings.Contains(w.Extra[0].Tolerated.Reason, "telemetry beacon") {
		t.Fatalf("Extra[0].Tolerated = %+v, want d-extra and its reason", w.Extra[0].Tolerated)
	}

	// ...and the "stops counting as a finding" half, through the real
	// counter every consumer's verdict is derived from.
	c := countOf(Summary{Wire: w})
	if c.WireMissing != 0 {
		t.Fatalf("Counts.WireMissing = %d, want 0 — a sanctioned deviation must not count as a finding", c.WireMissing)
	}
	if c.WireExtra != 0 {
		t.Fatalf("Counts.WireExtra = %d, want 0 — a sanctioned deviation must not count as a finding", c.WireExtra)
	}
	if changed(Summary{Wire: w, Counts: c}) {
		t.Fatal("changed() is true on a wire plane whose only deltas are sanctioned — the deviation did not reach the verdict")
	}
}

// TestAnUnsanctionedCallIsStillAFinding is the other half of the rule
// symmetry: the SAME fixture shape with a deviation that does not cover the
// call must still count. Without it, "tolerate everything" passes the test
// above.
func TestAnUnsanctionedCallIsStillAFinding(t *testing.T) {
	a := []trace.Hop{hop(1, "GET", "/only-a", 200, "", "")}
	b := []trace.Hop{hop(1, "GET", "/only-b", 200, "", "")}
	for _, c := range []struct {
		name string
		ds   []Deviation
	}{
		{"no ledger at all", nil},
		{"a deviation on another path", []Deviation{dev("d1", "approved", "web", "web-next", "GET", "/elsewhere")}},
		{"a deviation on another method", []Deviation{dev("d1", "approved", "web", "web-next", "POST", "/only-a")}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := DiffWire(a, b, Options{Deviations: ResolveDeviations(c.ds, "web", "web-next")})
			if len(w.Missing) != 1 || w.Missing[0].Tolerated != nil {
				t.Fatalf("Wire.Missing = %+v (tolerated=%+v), want an untolerated finding", w.Missing, w.Missing[0].Tolerated)
			}
			if len(w.Extra) != 1 || w.Extra[0].Tolerated != nil {
				t.Fatalf("Wire.Extra = %+v, want an untolerated finding", w.Extra)
			}
			counts := countOf(Summary{Wire: w})
			if counts.WireMissing != 1 || counts.WireExtra != 1 {
				t.Fatalf("Counts = %+v, want the uncovered calls counted", counts)
			}
		})
	}
}

// TestADeviationMatchesAPathGlob — deviations are authored against routes,
// and a route with an id in it is the normal case.
func TestADeviationMatchesAPathGlob(t *testing.T) {
	a := []trace.Hop{hop(1, "GET", "/orders/1234/legacy", 200, "", "")}
	ds := []Deviation{dev("d1", "approved", "web", "web-next", "GET", "/orders/*/legacy")}
	w := DiffWire(a, nil, Options{Deviations: ResolveDeviations(ds, "web", "web-next")})
	if len(w.Missing) != 1 || w.Missing[0].Tolerated == nil {
		t.Fatalf("Wire.Missing = %+v, want the glob to match", w.Missing)
	}
}

// TestOptionsForLoadsTheLedgerNamedByConfig is the SEAM — the assembly
// point every Build caller goes through. A ledger that loads correctly but
// is never handed to the engine is the wiring defect this plan keeps
// paying for, and no unit test of LoadDeviations can catch it.
func TestOptionsForLoadsTheLedgerNamedByConfig(t *testing.T) {
	dir := t.TempDir()
	writeLedger(t, dir, "deviations.json", ledgerJSON(t,
		dev("d-approved", "approved", "web", "web-next", "GET", "/only-a"),
		dev("d-proposed", "proposed", "web", "web-next", "GET", "/only-b"),
		dev("d-otherpair", "approved", "web", "legacy", "GET", "/only-c"),
	))
	cfg := &config.Config{Dir: dir, Deviations: "deviations.json"}
	// The two manifests are what name the app pair — OptionsFor takes them,
	// so the pair comes from the runs being compared, not from a flag.
	o, err := OptionsFor(cfg, runs.Manifest{App: "web"}, runs.Manifest{App: "web-next"})
	if err != nil {
		t.Fatalf("OptionsFor: %v", err)
	}
	if len(o.Deviations) != 1 || o.Deviations[0].ID != "d-approved" {
		t.Fatalf("Options.Deviations = %+v, want only the approved entry for this app pair", o.Deviations)
	}

	t.Run("no deviations key configured is nil, not an error", func(t *testing.T) {
		o, err := OptionsFor(&config.Config{Dir: dir}, runs.Manifest{App: "web"}, runs.Manifest{App: "web-next"})
		if err != nil {
			t.Fatalf("OptionsFor: %v", err)
		}
		if o.Deviations != nil {
			t.Fatalf("Options.Deviations = %+v, want nil when no ledger is configured", o.Deviations)
		}
	})

	t.Run("a configured but unreadable ledger fails the build", func(t *testing.T) {
		cfg := &config.Config{Dir: dir, Deviations: "missing.json"}
		if _, err := OptionsFor(cfg, runs.Manifest{App: "web"}, runs.Manifest{App: "web-next"}); err == nil {
			t.Fatal("OptionsFor accepted a ledger it could not read — a diff that silently tolerates nothing is not the failure a misconfigured ledger deserves")
		}
	})
}
