package runs

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
)

// jsonKeys marshals v and returns its top-level JSON object keys, sorted.
//
// Review finding 5 (Major): a round-trip test alone is invariant to a
// renamed json tag, because it reads back through the same Go struct — it
// cannot catch the class of bug this exists to pin. Fifteen downstream
// tasks, including a TypeScript mirror, read these keys by name; this
// asserts the exact marshalled key set for every wire type in this
// package against a literal expected list, not a round trip.
func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal(%T): %v", v, err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("value is not a JSON object: %v (got %s)", err, b)
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func assertJSONKeys(t *testing.T, v any, want []string) {
	t.Helper()
	got := jsonKeys(t, v)
	wantSorted := append([]string(nil), want...)
	sort.Strings(wantSorted)
	if !reflect.DeepEqual(got, wantSorted) {
		t.Fatalf("%T JSON keys = %v, want %v", v, got, wantSorted)
	}
}

// TestWireJSONKeysMatchContract pins the on-disk key name of every
// exported field on every wire type in retrace/runs. Every optional field
// is populated so omitempty tags can't hide a rename by omitting the key
// entirely.
func TestWireJSONKeysMatchContract(t *testing.T) {
	full := Manifest{
		Schema: Schema, App: "web", Flow: "checkout", RunID: "r1", Mode: ModeEnsemble,
		Git:        Git{SHA: "abc1234", Branch: "main", Dirty: true},
		StartedAt:  ts("2026-08-21T10:00:00Z"),
		FinishedAt: ts("2026-08-21T10:01:00Z"),
		Checkpoints: []Checkpoint{{
			Name: "cart", File: "shots/cart.png", Width: 390, Height: 844, Trim: true,
		}},
		Groups: []Group{{
			Name: "browse", StartedAt: ts("2026-08-21T10:00:00Z"), EndedAt: ts("2026-08-21T10:00:10Z"), Quiet: true,
		}},
		Capture: CaptureTrust{
			Status: trace.VerdictDegraded,
			Reasons: []TrustReason{{
				Code: "gap", Status: trace.VerdictDegraded, Detail: "propagation gap", Hint: "check bff",
			}},
			Gaps: []Gap{{
				From: ts("2026-08-21T10:00:00Z"), To: ts("2026-08-21T10:00:05Z"), Seconds: 5,
			}},
			Summary: "propagation gap at bff",
			Hint:    "check bff",
		},
		Wire: Counts{Calls: 12},
		Hops: &Counts{Calls: 40},
		Test: Test{Command: "go test ./...", ExitCode: 0, DurationMs: 1500.5},
		Env:  Env{Go: "1.25", Platform: "darwin/arm64", Retrace: "dev"},
	}

	assertJSONKeys(t, full, []string{
		"schema", "app", "flow", "runId", "mode", "git", "startedAt", "finishedAt",
		"checkpoints", "groups", "capture", "wire", "hops", "test", "env",
	})
	assertJSONKeys(t, full.Git, []string{"sha", "branch", "dirty"})
	assertJSONKeys(t, full.Checkpoints[0], []string{"name", "file", "width", "height", "trim"})
	assertJSONKeys(t, full.Groups[0], []string{"name", "startedAt", "endedAt", "quiet"})
	assertJSONKeys(t, full.Capture, []string{"status", "reasons", "gaps", "summary", "hint"})
	assertJSONKeys(t, full.Capture.Reasons[0], []string{"code", "status", "detail", "hint"})
	assertJSONKeys(t, full.Capture.Gaps[0], []string{"from", "to", "seconds"})
	assertJSONKeys(t, full.Wire, []string{"calls"})
	assertJSONKeys(t, *full.Hops, []string{"calls"})
	assertJSONKeys(t, full.Test, []string{"command", "exitCode", "durationMs"})
	assertJSONKeys(t, full.Env, []string{"go", "platform", "retrace"})

	assertJSONKeys(t, GroupRecord{Phase: "start", Name: "browse", TS: ts("2026-08-21T10:00:00Z"), Quiet: true},
		[]string{"phase", "name", "ts", "quiet"})
}

// TestManifestHopsPointerDistinguishesAbsentFromZero pins the one
// deliberately three-state field: Hops is nil in standalone mode (key
// absent), and a *Counts{} in ensemble mode when the chain was recorded
// and empty (key present, "calls":0) — manifest.go's comment calls this
// distinction load-bearing, and nothing else in the suite tests it
// (review finding 5).
func TestManifestHopsPointerDistinguishesAbsentFromZero(t *testing.T) {
	standalone := Manifest{Mode: ModeStandalone, Capture: CaptureTrust{Status: trace.VerdictOK, Summary: "ok"}}
	b, err := json.Marshal(standalone)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(b), `"hops"`) {
		t.Fatalf("standalone manifest must omit hops entirely, got %s", b)
	}

	recorded := Manifest{Mode: ModeEnsemble, Hops: &Counts{}, Capture: CaptureTrust{Status: trace.VerdictOK, Summary: "ok"}}
	b2, err := json.Marshal(recorded)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(b2), `"hops":{"calls":0}`) {
		t.Fatalf(`a recorded-but-empty chain must serialize as "hops":{"calls":0}, got %s`, b2)
	}
}
