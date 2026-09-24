package serve

import (
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
)

func findSurface(t *testing.T, ss []Surface, app, flow string) Surface {
	t.Helper()
	for _, s := range ss {
		if s.App == app && s.Flow == flow {
			return s
		}
	}
	t.Fatalf("no surface %s/%s in %+v", app, flow, ss)
	return Surface{}
}

func findRun(t *testing.T, s Surface, runID string) SurfaceRun {
	t.Helper()
	for _, r := range s.Runs {
		if r.RunID == runID {
			return r
		}
	}
	t.Fatalf("no run %s in surface %s/%s: %+v", runID, s.App, s.Flow, s.Runs)
	return SurfaceRun{}
}

// A run's manifest.Capture and len(Checkpoints) must reach the surface list
// unchanged — the branch report picks the newest USABLE run by reading
// exactly these two fields (reportData.isUsable), without diffing every
// candidate first.
func TestListSurfacesCarriesCaptureVerdictAndCheckpointCountFromTheManifest(t *testing.T) {
	cwd := t.TempDir()
	recordRun(t, cwd, "web", "cart", runA, map[string][]byte{"cart": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/cart", 200, `{"total":1}`)})

	ss, err := ListSurfaces(deps(t, cwd))
	if err != nil {
		t.Fatalf("ListSurfaces: %v", err)
	}
	r := findRun(t, findSurface(t, ss, "web", "cart"), runA)
	if r.Capture.Status != trace.VerdictOK {
		t.Fatalf("Capture.Status = %q, want %q", r.Capture.Status, trace.VerdictOK)
	}
	if r.Checkpoints != 1 {
		t.Fatalf("Checkpoints = %d, want 1", r.Checkpoints)
	}
}

// Before anything is accepted, Baseline.Kind must not read as "bundle" —
// that would tell a client a comparison exists when refs.Resolve has not
// actually found one to promote.
func TestListSurfacesReportsBaselineKindNoneBeforeAnyRunExists(t *testing.T) {
	cwd := t.TempDir()
	recordRun(t, cwd, "web", "cart", runA, map[string][]byte{"cart": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/cart", 200, `{"total":1}`)})
	// A dirty run (uncommitted git changes) is not ELIGIBLE as a fallback
	// reference — see refs.candidateFor — so with nothing accepted and only
	// this one dirty run recorded, Resolve has no candidate at all.

	ss, err := ListSurfaces(deps(t, cwd))
	if err != nil {
		t.Fatalf("ListSurfaces: %v", err)
	}
	s := findSurface(t, ss, "web", "cart")
	if s.Baseline.Kind == "bundle" {
		t.Fatalf("Baseline.Kind = %q before any accept, want anything but bundle", s.Baseline.Kind)
	}
}

// After `retrace ref accept`, Baseline must name the accepted run — this is
// what lets the dashboard tell "no baseline yet" apart from "compare away"
// without diffing (reportData.hasBaseline).
func TestListSurfacesReportsBaselineKindBundleAfterAccept(t *testing.T) {
	cwd := t.TempDir()
	recordRun(t, cwd, "web", "cart", runA, map[string][]byte{"cart": shotPNG(t, white)},
		[]trace.Hop{hop(1, "GET", "/cart", 200, `{"total":1}`)})
	acceptRef(t, cwd, "web", "cart", runA)

	ss, err := ListSurfaces(deps(t, cwd))
	if err != nil {
		t.Fatalf("ListSurfaces: %v", err)
	}
	s := findSurface(t, ss, "web", "cart")
	if s.Baseline.Kind != "bundle" {
		t.Fatalf("Baseline.Kind = %q, want bundle", s.Baseline.Kind)
	}
	if s.Baseline.RunID != runA {
		t.Fatalf("Baseline.RunID = %q, want %q", s.Baseline.RunID, runA)
	}
}
