package runs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewRunIDIsLexicallyChronological(t *testing.T) {
	t0 := time.Date(2026, 8, 21, 10, 15, 0, 0, time.UTC)
	a := NewRunID(t0, "ab12cd3ef")
	b := NewRunID(t0.Add(time.Second), "ab12cd3ef")
	if a != "20260821T101500Z-ab12cd3" {
		t.Fatalf("run id = %q, want 20260821T101500Z-ab12cd3", a)
	}
	if !(a < b) {
		t.Fatalf("run ids must sort chronologically: %q !< %q", a, b)
	}
}

func TestNewRunIDWithoutShaStillUnique(t *testing.T) {
	t0 := time.Date(2026, 8, 21, 10, 15, 0, 0, time.UTC)
	if got := NewRunID(t0, ""); got != "20260821T101500Z-nogit" {
		t.Fatalf("run id = %q, want 20260821T101500Z-nogit", got)
	}
}

func TestCreateMakesShotsDirAndListingsRoundTrip(t *testing.T) {
	root := RunsRoot(t.TempDir())
	p, err := Create(root, "web", "checkout", "20260821T101500Z-ab12cd3")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if st, err := os.Stat(p.ShotsDir); err != nil || !st.IsDir() {
		t.Fatalf("shots dir not created: %v", err)
	}
	if p.WirePath != filepath.Join(p.RunDir, "wire.jsonl") {
		t.Fatalf("WirePath = %q", p.WirePath)
	}
	if got := ListApps(root); len(got) != 1 || got[0] != "web" {
		t.Fatalf("ListApps = %v", got)
	}
	if got := ListFlows(root, "web"); len(got) != 1 || got[0] != "checkout" {
		t.Fatalf("ListFlows = %v", got)
	}
	if got := ListRuns(root, "web", "checkout"); len(got) != 1 {
		t.Fatalf("ListRuns = %v", got)
	}
}

func TestFindRunSelectors(t *testing.T) {
	root := RunsRoot(t.TempDir())
	for _, id := range []string{"20260821T100000Z-aaa1111", "20260821T110000Z-bbb2222"} {
		if _, err := Create(root, "web", "checkout", id); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	if got := FindRun(root, "web", "checkout", "latest"); got != "20260821T110000Z-bbb2222" {
		t.Fatalf("latest = %q", got)
	}
	if got := FindRun(root, "web", "checkout", "aaa1111"); got != "20260821T100000Z-aaa1111" {
		t.Fatalf("by sha = %q", got)
	}
	if got := FindRun(root, "web", "checkout", "nope"); got != "" {
		t.Fatalf("unknown selector = %q, want empty", got)
	}
}

func TestListingsOfMissingRootAreEmptyNotPanic(t *testing.T) {
	root := filepath.Join(t.TempDir(), "never-created")
	if got := ListApps(root); len(got) != 0 {
		t.Fatalf("ListApps = %v", got)
	}
	if got := ListRuns(root, "web", "checkout"); len(got) != 0 {
		t.Fatalf("ListRuns = %v", got)
	}
}
