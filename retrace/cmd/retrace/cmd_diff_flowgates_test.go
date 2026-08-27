package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// TestAPerFlowGateReachesTheDiffThroughTheRealConfigPath is the wiring test
// for `flows.<name>.gates`. Every unit test in retrace/config and
// retrace/diff builds its maps in Go; if diff.Build kept reading cfg.Gates
// directly — the shape ResolveGates replaced — all of them would still pass
// and the config key would do nothing at all. That is the failure mode this
// project has hit before: a setting that parses, validates, and is never
// read.
//
// The perf plane carries the test because it needs no wire or pixel
// difference to make its point. These fixtures configure no
// `perf_budget_ms`, so perf is gated-but-unmeasurable — and a gate named in
// `fail_on` that could not be evaluated is exit 2, deterministically, on
// identical runs. A flow-scoped gate that never arrives leaves the run at
// exit 0.
func TestAPerFlowGateReachesTheDiffThroughTheRealConfigPath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	// No top-level `gates:` at all — so anything that gates here can only
	// have come from the flow. The date rule keeps the two runs from
	// differing on a clock reading, which would move the verdict for a
	// reason unrelated to the gate.
	writeConfig(t, cwd, `app: web
wire_rules:
  - headers:
      date: http-date
fail_on: [perf]
flows:
  checkout:
    gates:
      perf:
        budget_pct: 10
  browse: {}
`)

	aID := runOnce(t, bin, cwd, "web", "checkout", upstream.URL)
	bID := runOnce(t, bin, cwd, "web", "checkout", upstream.URL)

	res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web", "--a", aID, "--b", bID)
	if res.code != 2 {
		t.Fatalf("exit = %d, want 2 — `flows.checkout.gates.perf` never reached the diff, so a gate this project asked to break the build was silently absent\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "perf NOT EVALUATED") {
		t.Errorf("the text report never names the flow-scoped gate:\n%s", res.stdout)
	}

	// ...and the same fact on the agent contract, which reads the Summary
	// separately from the text report.
	jsonRes := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web", "--a", aID, "--b", bID, "--json")
	var got struct {
		UnmeasuredGates []string `json:"unmeasuredGates"`
	}
	if err := json.Unmarshal([]byte(jsonRes.stdout), &got); err != nil {
		t.Fatalf("--json is not parseable: %v\n%s", err, jsonRes.stdout)
	}
	// pixel rides along unconditionally — applyDefaults gates it from
	// thresholds.gate whether or not `gates:` mentions it, and these fixtures
	// take no screenshots. Only perf is in fail_on, which is why only perf
	// decides the exit code above.
	if !slices.Contains(got.UnmeasuredGates, "perf") {
		t.Errorf("unmeasuredGates = %v, want it to include perf", got.UnmeasuredGates)
	}
}

// TestAPerFlowGateDoesNotLeakToAnotherFlow is the other half, and it is not
// a formality: a resolution that ignored the flow name — merging every
// flow's gates together, or reading whichever one the map happened to yield
// — would pass the test above and gate the whole project on one flow's
// budget. The two runs are the SAME captures compared under a different
// --flow, so nothing but the flow name differs.
func TestAPerFlowGateDoesNotLeakToAnotherFlow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, `app: web
wire_rules:
  - headers:
      date: http-date
fail_on: [perf]
flows:
  checkout:
    gates:
      perf:
        budget_pct: 10
  browse: {}
`)

	aID := runOnce(t, bin, cwd, "web", "browse", upstream.URL)
	bID := runOnce(t, bin, cwd, "web", "browse", upstream.URL)

	res := runRetrace(t, bin, cwd, "", "diff", "--flow", "browse", "--app", "web", "--a", aID, "--b", bID)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0 — browse gates no planes, so checkout's perf budget leaked across flows\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	if strings.Contains(res.stdout, "perf NOT EVALUATED") {
		t.Errorf("browse reported checkout's gate:\n%s", res.stdout)
	}
}

// TestAMisspelledPlaneInAFlowsGatesIsAConfigError. A typo caught at the top
// level and waved through one level down is worse than one caught nowhere:
// the correctly-spelled plane sits right above it in the same file, so it
// reads as though it works. Driven through the binary because the failure
// has to be an exit code and a message a person sees, not a returned error.
func TestAMisspelledPlaneInAFlowsGatesIsAConfigError(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, `app: web
flows:
  checkout:
    gates:
      pixle:
        budget_pct: 10
`)

	res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web")
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d — a typo'd plane loaded clean and silently gated nothing\nstdout: %s\nstderr: %s", res.code, exitUsage, res.stdout, res.stderr)
	}
	all := res.stdout + res.stderr
	if !strings.Contains(all, "pixle") || !strings.Contains(all, "checkout") {
		t.Errorf("the error names neither the typo nor the flow it is in:\n%s", all)
	}
}

// TestCheckpointsOnANonPixelPlaneIsRefusedAtTheCLI. `checkpoints:` under
// wire/hop/perf is inert — those planes have no per-item unit to key on — so
// without this it loads clean, does nothing, and tells the user nothing.
func TestCheckpointsOnANonPixelPlaneIsRefusedAtTheCLI(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, `app: web
gates:
  wire:
    budget_pct: 2
    checkpoints:
      cart: 8
`)

	res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web")
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d — `gates.wire.checkpoints` loaded clean and did nothing\nstdout: %s\nstderr: %s", res.code, exitUsage, res.stdout, res.stderr)
	}
	if all := res.stdout + res.stderr; !strings.Contains(all, "wire") {
		t.Errorf("the error does not name the offending plane:\n%s", all)
	}
}
