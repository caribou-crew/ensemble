package runs

import (
	"path/filepath"
	"testing"
	"time"
)

// TestSourcesByURLGroupsLocalRunsByTheirCIRunURL is the primary contract:
// a CI-run-centric UI (the sync candidate list) needs to answer "is this
// GitHub Actions run already pulled, and as what" from local state alone —
// no network call, no re-download. RunURL is the join key because it is
// the one field both a sync.Candidate and a synced run's source.json
// agree on (both ultimately come from `gh run list`'s own "url").
func TestSourcesByURLGroupsLocalRunsByTheirCIRunURL(t *testing.T) {
	root := t.TempDir()
	const url1 = "https://github.com/org/repo/actions/runs/111"
	const url2 = "https://github.com/org/repo/actions/runs/222"

	// Two flows pulled from the SAME CI run — the multi-flow-per-run case
	// the sync panel's chooser exists for.
	p1 := createRun(t, root, "web", "checkout", runIDAt(fixedNow))
	writeSourceFor(t, p1, url1)
	p2 := createRun(t, root, "web", "search", runIDAt(fixedNow.Add(time.Minute)))
	writeSourceFor(t, p2, url1)

	// One flow pulled from a DIFFERENT CI run.
	p3 := createRun(t, root, "web", "checkout", runIDAt(fixedNow.Add(2*time.Minute)))
	writeSourceFor(t, p3, url2)

	// A locally recorded run — never synced, no source.json at all — must
	// not appear under any URL.
	createRun(t, root, "web", "checkout", runIDAt(fixedNow.Add(3*time.Minute)))

	got, err := SourcesByURL(root)
	if err != nil {
		t.Fatalf("SourcesByURL: %v", err)
	}

	want1 := []string{
		filepath.Join("web", "checkout", filepath.Base(p1.RunDir)),
		filepath.Join("web", "search", filepath.Base(p2.RunDir)),
	}
	if !sameSet(got[url1], want1) {
		t.Errorf("SourcesByURL[%q] = %v, want %v", url1, got[url1], want1)
	}
	want2 := []string{filepath.Join("web", "checkout", filepath.Base(p3.RunDir))}
	if !sameSet(got[url2], want2) {
		t.Errorf("SourcesByURL[%q] = %v, want %v", url2, got[url2], want2)
	}
	if len(got) != 2 {
		t.Errorf("SourcesByURL returned %d URLs, want exactly 2 (a locally recorded run must not add a third)", len(got))
	}
}

// A brand-new runs root (nothing ever recorded) must not error — the
// answer to "no local runs at all" is the same empty map as "no CI-sourced
// runs among what's local", not a failure the caller has to special-case.
func TestSourcesByURLOnEmptyRootReturnsEmptyMap(t *testing.T) {
	got, err := SourcesByURL(t.TempDir())
	if err != nil {
		t.Fatalf("SourcesByURL: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("SourcesByURL on an empty root = %v, want empty", got)
	}
}

func createRun(t *testing.T, root, app, flow, runID string) Paths {
	t.Helper()
	p, err := Create(root, app, flow, runID)
	if err != nil {
		t.Fatalf("Create(%s, %s, %s): %v", app, flow, runID, err)
	}
	return p
}

func writeSourceFor(t *testing.T, p Paths, url string) {
	t.Helper()
	if err := WriteSource(p, Source{Kind: SourceKindCI, RunURL: url, SyncedAt: fixedNow}); err != nil {
		t.Fatalf("WriteSource: %v", err)
	}
}

func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	for _, w := range want {
		if !seen[w] {
			return false
		}
	}
	return true
}
