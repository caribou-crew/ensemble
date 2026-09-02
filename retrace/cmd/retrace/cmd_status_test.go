package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/serve"
)

func TestDedupeItemsKeepsLatestPerFlow(t *testing.T) {
	items := []serve.Item{
		{App: "web", Flow: "card-views", RunID: "20260101T000000Z-a", Verdict: "changed"},
		{App: "web", Flow: "card-views", RunID: "20260901T000000Z-b", Verdict: "pass"}, // newer
		{App: "rn-ios", Flow: "card-views", RunID: "20260501T000000Z-c", Verdict: "failed"},
	}
	got := dedupeItems(items)
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2 (one per app/flow): %+v", len(got), got)
	}
	// web/card-views should be the NEWER run (pass), not the older changed one.
	for _, it := range got {
		if it.App == "web" && it.Flow == "card-views" && it.Verdict != "pass" {
			t.Errorf("web/card-views verdict = %q, want pass (the newer run wins)", it.Verdict)
		}
	}
}

func TestStatusOnAnEmptyProjectReadsCleanly(t *testing.T) {
	// No .retrace/runs and no repo.yaml: the readout says so and exits 0
	// (nothing recorded is not a failure — a fresh project, not a red one).
	cwd := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	var out, errb bytes.Buffer
	code := cmdStatus(nil, &out, &errb)
	if code != exitOK {
		t.Fatalf("exit = %d, want 0 on an empty project; stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "no flows found") {
		t.Errorf("stdout = %q, want the 'no flows found' readout", out.String())
	}
}
