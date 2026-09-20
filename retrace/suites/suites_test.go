package suites

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func testInventory() Inventory {
	flows := []Flow{}
	for i := 1; i <= 5; i++ {
		flows = append(flows, Flow{ID: fmt.Sprintf("flow-%d", i), Title: fmt.Sprintf("Flow %d", i), RequiredPlanes: []string{"functional", "wire", "visual"}})
	}
	return Inventory{Schema: InventorySchema, Suites: []Suite{{ID: "checkout", Title: "Checkout", Version: "v1", Platforms: []string{"web", "ios", "android"}, Features: []Feature{{ID: "shop", Title: "Shop", Flows: flows}}}}}
}
func testAttempt(id, platform string) Attempt {
	return Attempt{Schema: AttemptSchema, SuiteID: "checkout", SuiteVersion: "v1", AttemptID: id, Platform: platform, Git: Git{SHA: strings.Repeat("a", 40), Branch: "main"}, BaselineID: "legacy-v1", PolicyID: "policy-v1", StartedAt: "2026-09-20T01:00:00Z", FinishedAt: "2026-09-20T01:01:00Z", Results: []Result{}}
}
func passResult(flow string) Result {
	return Result{FlowID: flow, Planes: Planes{Functional: "pass", Wire: "pass", Visual: "pass"}}
}
func fullAttempt(id, platform string) Attempt {
	a := testAttempt(id, platform)
	for i := 1; i <= 5; i++ {
		a.Results = append(a.Results, passResult(fmt.Sprintf("flow-%d", i)))
	}
	return a
}
func aggregate(t *testing.T, inv Inventory, attempts ...Attempt) []SuiteOverview {
	t.Helper()
	out, err := Aggregate(inv, attempts)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestExpectedDenominatorAndMissingLanes(t *testing.T) {
	inv := testInventory()
	web, ios, android := fullAttempt("web", "web"), fullAttempt("ios", "ios"), fullAttempt("android", "android")
	android.Results = android.Results[:2]
	build := aggregate(t, inv, web, ios, android)[0].Builds[0]
	if build.Counts != (Counts{Total: 15, Passed: 12, NotRun: 3}) {
		t.Fatalf("12 of 15: %+v", build.Counts)
	}
	build = aggregate(t, inv, web, ios)[0].Builds[0]
	if build.Counts != (Counts{Total: 15, Passed: 10, NotRun: 5}) {
		t.Fatalf("missing lane: %+v", build.Counts)
	}
	if build.Platforms[2].Counts != (Counts{Total: 5, NotRun: 5}) {
		t.Fatalf("missing android: %+v", build.Platforms)
	}
	cell := build.Features[0].Flows[0].Platforms[2]
	if cell.Status != "not-run" || cell.Latest != nil || len(cell.History) != 0 {
		t.Fatalf("missing cell: %+v", cell)
	}
	if build.Features[0].Counts != build.Counts {
		t.Fatal("feature denominator diverged")
	}
}
func TestLatestRetryDisplacesPassAndPreservesHistory(t *testing.T) {
	inv := testInventory()
	old := fullAttempt("old", "web")
	failed := testAttempt("new", "web")
	failed.FinishedAt = "2026-09-20T01:02:00Z"
	failed.Results = []Result{passResult("flow-1")}
	failed.Results[0].Planes.Functional = "failed"
	failed.Results[0].Reason = "retry regressed"
	later := testAttempt("last", "web")
	later.FinishedAt = "2026-09-20T01:03:00Z"
	later.Results = []Result{passResult("flow-1")}
	later.Results[0].Planes.Visual = "not-run"
	for _, tc := range []struct {
		name     string
		attempts []Attempt
		status   string
		latest   string
		count    Counts
	}{
		{"failed", []Attempt{failed, old}, "failed", "new", Counts{Total: 15, Passed: 4, Failed: 1, NotRun: 10}},
		{"incomplete", []Attempt{later, old, failed}, "incomplete", "last", Counts{Total: 15, Passed: 4, Incomplete: 1, NotRun: 10}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			build := aggregate(t, inv, tc.attempts...)[0].Builds[0]
			if build.Counts != tc.count {
				t.Fatalf("counts: %+v", build.Counts)
			}
			cell := build.Features[0].Flows[0].Platforms[0]
			if cell.Status != tc.status || cell.Latest.AttemptID != tc.latest || len(cell.History) != len(tc.attempts) {
				t.Fatalf("latest/history: %+v", cell)
			}
			if cell.History[len(cell.History)-1].AttemptID != "old" {
				t.Fatal("old attempt lost")
			}
			// A later partial report must not erase the old results for other flows.
			if build.Features[0].Flows[1].Platforms[0].Latest.AttemptID != "old" {
				t.Fatal("partial retry erased another flow")
			}
		})
	}
}
func TestRetryTimeComparisonAndDeterministicTie(t *testing.T) {
	inv := testInventory()
	old := fullAttempt("z-old", "web")
	old.FinishedAt = "2026-09-20T02:01:00+01:00"
	newer := fullAttempt("a-new", "web")
	newer.FinishedAt = "2026-09-20T01:02:00Z"
	newer.Results[0].Planes.Wire = "failed"
	out := aggregate(t, inv, old, newer)
	if out[0].Builds[0].Features[0].Flows[0].Platforms[0].Latest.AttemptID != "a-new" {
		t.Fatal("compared time text instead of instant")
	}
	newer.FinishedAt = old.FinishedAt
	first := aggregate(t, inv, old, newer)
	second := aggregate(t, inv, newer, old)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("aggregation depends on input order")
	}
	if first[0].Builds[0].Features[0].Flows[0].Platforms[0].Latest.AttemptID != "z-old" {
		t.Fatal("tie must choose lexical greatest attempt id")
	}
}
func TestBuildIdentitySeparation(t *testing.T) {
	inv := testInventory()
	clean := fullAttempt("clean", "web")
	dirty := fullAttempt("dirty", "ios")
	dirty.Git.Dirty = true
	dirty.WorkspaceID = "snapshot-a"
	dirty2 := dirty
	dirty2.AttemptID = "dirty2"
	dirty2.WorkspaceID = "snapshot-b"
	baseline := fullAttempt("baseline", "ios")
	baseline.BaselineID = "legacy-v2"
	policy := fullAttempt("policy", "android")
	policy.PolicyID = "policy-v2"
	sha := fullAttempt("sha", "ios")
	sha.Git.SHA = strings.Repeat("b", 40)
	out := aggregate(t, inv, clean, dirty, dirty2, baseline, policy, sha)
	if len(out[0].Builds) != 6 {
		t.Fatalf("distinct source/baseline/policy merged: %d", len(out[0].Builds))
	}
	for _, build := range out[0].Builds {
		if build.Counts != (Counts{Total: 15, Passed: 5, NotRun: 10}) {
			t.Fatalf("separate build count: %+v", build.Counts)
		}
	}
	// Even a clean report with a workspace id cannot fold into a dirty report.
	clean.WorkspaceID = dirty.WorkspaceID
	if identity(clean) == identity(dirty) {
		t.Fatal("dirty flag omitted from identity")
	}
	next := clean
	next.SuiteVersion = "v2"
	if identity(clean) == identity(next) {
		t.Fatal("inventory version omitted from identity")
	}
	next = clean
	next.SuiteID = "other"
	if identity(clean) == identity(next) {
		t.Fatal("suite id omitted from identity")
	}
	branch := clean
	branch.AttemptID = "branch"
	branch.Git.Branch = "release"
	branch.Git.SHA = strings.ToUpper(branch.Git.SHA)
	if len(aggregate(t, inv, clean, branch)[0].Builds) != 1 {
		t.Fatal("branch aliases or SHA hex case split identical source")
	}
}
func TestFlowPlatformSubsetAndRequiredPlanes(t *testing.T) {
	inv := testInventory()
	flow := &inv.Suites[0].Features[0].Flows[0]
	flow.Platforms = []string{"web"}
	flow.RequiredPlanes = []string{"functional"}
	web := fullAttempt("web", "web")
	web.Results[0].Planes.Wire = "failed"
	web.Results[0].Planes.Visual = "not-applicable"
	build := aggregate(t, inv, web)[0].Builds[0]
	if build.Counts != (Counts{Total: 13, Passed: 5, NotRun: 8}) {
		t.Fatalf("inapplicable lane counted: %+v", build.Counts)
	}
	cell := build.Features[0].Flows[0].Platforms[0]
	if cell.Status != "pass" || cell.Latest.Planes.Wire != "failed" || cell.Latest.Planes.Visual != "not-applicable" {
		t.Fatalf("non-required states hidden or gated: %+v", cell)
	}
}
func TestRequiredStatusPrecedence(t *testing.T) {
	if cellStatus(nil, Planes{}) != "incomplete" {
		t.Fatal("zero requirement status must fail closed")
	}
	for _, tc := range []struct {
		p    Planes
		want string
	}{
		{Planes{"pass", "pass", "pass"}, "pass"},
		{Planes{"incomplete", "failed", "not-run"}, "failed"},
		{Planes{"pass", "incomplete", "pass"}, "incomplete"},
		{Planes{"pass", "not-run", "pass"}, "incomplete"},
	} {
		if got := cellStatus(planeNames, tc.p); got != tc.want {
			t.Fatalf("%+v got %s", tc.p, got)
		}
	}
}
func TestEmptyInventoryAndNoBuildsAreVisibleArrays(t *testing.T) {
	empty := aggregate(t, Inventory{Schema: InventorySchema, Suites: []Suite{}})
	b, _ := json.Marshal(Response{Suites: empty})
	if string(b) != `{"suites":[]}` {
		t.Fatal(string(b))
	}
	out := aggregate(t, testInventory())
	if len(out) != 1 || out[0].ID != "checkout" || out[0].Builds == nil || len(out[0].Builds) != 0 {
		t.Fatalf("lost unrun suite: %+v", out)
	}
	out = aggregate(t, testInventory(), testAttempt("empty", "web"))
	if out[0].Builds[0].Counts != (Counts{Total: 15, NotRun: 15}) {
		t.Fatal("empty attempt implies coverage")
	}
	b, _ = json.Marshal(out)
	if bytes.Contains(b, []byte("null")) {
		t.Fatalf("null API array: %s", b)
	}
}
func TestValidationRejectsMalformedInventory(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Inventory)
	}{
		{"schema", func(i *Inventory) { i.Schema = "" }}, {"missing suites", func(i *Inventory) { i.Suites = nil }},
		{"path", func(i *Inventory) { i.Suites[0].ID = "../escape" }}, {"duplicate suite", func(i *Inventory) { i.Suites = append(i.Suites, i.Suites[0]) }},
		{"version", func(i *Inventory) { i.Suites[0].Version = " " }}, {"title", func(i *Inventory) { i.Suites[0].Title = "" }},
		{"platform", func(i *Inventory) { i.Suites[0].Platforms = []string{"tv"} }}, {"duplicate platform", func(i *Inventory) { i.Suites[0].Platforms = []string{"web", "web"} }},
		{"no features", func(i *Inventory) { i.Suites[0].Features = nil }}, {"duplicate feature", func(i *Inventory) { i.Suites[0].Features = append(i.Suites[0].Features, i.Suites[0].Features[0]) }},
		{"no flows", func(i *Inventory) { i.Suites[0].Features[0].Flows = nil }}, {"duplicate flow", func(i *Inventory) { f := &i.Suites[0].Features[0]; f.Flows = append(f.Flows, f.Flows[0]) }},
		{"empty required", func(i *Inventory) { i.Suites[0].Features[0].Flows[0].RequiredPlanes = nil }},
		{"unknown required", func(i *Inventory) { i.Suites[0].Features[0].Flows[0].RequiredPlanes = []string{"screenshots"} }},
		{"empty subset", func(i *Inventory) { i.Suites[0].Features[0].Flows[0].Platforms = []string{} }},
		{"outside subset", func(i *Inventory) { i.Suites[0].Features[0].Flows[0].Platforms = []string{"tv"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv := testInventory()
			tc.mutate(&inv)
			if ValidateInventory(inv) == nil {
				t.Fatal("invalid inventory accepted")
			}
		})
	}
}
func TestValidationRejectsMalformedAttempts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Attempt)
	}{
		{"schema", func(a *Attempt) { a.Schema = "" }}, {"suite", func(a *Attempt) { a.SuiteID = "other" }},
		{"version", func(a *Attempt) { a.SuiteVersion = "v0" }}, {"path", func(a *Attempt) { a.AttemptID = "../escape" }},
		{"platform", func(a *Attempt) { a.Platform = "tv" }}, {"sha", func(a *Attempt) { a.Git.SHA = "abcdef0" }},
		{"dirty", func(a *Attempt) { a.Git.Dirty = true }}, {"baseline", func(a *Attempt) { a.BaselineID = "" }},
		{"policy", func(a *Attempt) { a.PolicyID = "" }}, {"start", func(a *Attempt) { a.StartedAt = "yesterday" }},
		{"finish", func(a *Attempt) { a.FinishedAt = "" }}, {"reversed", func(a *Attempt) { a.FinishedAt = "2026-09-19T01:00:00Z" }},
		{"nil results", func(a *Attempt) { a.Results = nil }}, {"duplicate", func(a *Attempt) { a.Results = append(a.Results, a.Results[0]) }},
		{"unknown flow", func(a *Attempt) { a.Results[0].FlowID = "other" }}, {"missing plane", func(a *Attempt) { a.Results[0].Planes.Wire = "" }},
		{"unknown state", func(a *Attempt) { a.Results[0].Planes.Visual = "ok" }}, {"required not applicable", func(a *Attempt) { a.Results[0].Planes.Visual = "not-applicable" }},
		{"reference evidence", func(a *Attempt) {
			a.Results[0].Evidence = &Evidence{App: "app", Flow: "login", RunID: "reference", PairID: "pair"}
		}},
		{"moving evidence", func(a *Attempt) { a.Results[0].Evidence = &Evidence{App: "app", Flow: "login", RunID: "latest"} }},
		{"evidence path", func(a *Attempt) { a.Results[0].Evidence = &Evidence{App: "../secret", Flow: "login", RunID: "latest"} }},
		{"evidence pair", func(a *Attempt) {
			a.Results[0].Evidence = &Evidence{App: "app", Flow: "login", RunID: "run", PairID: "../pair"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := fullAttempt("attempt", "web")
			tc.mutate(&a)
			if ValidateAttempt(testInventory(), a) == nil {
				t.Fatal("invalid attempt accepted")
			}
			if _, err := Aggregate(testInventory(), []Attempt{a}); err == nil {
				t.Fatal("pure aggregation accepted invalid attempt")
			}
		})
	}
	a := fullAttempt("attempt", "web")
	if _, err := Aggregate(testInventory(), []Attempt{a, a}); err == nil {
		t.Fatal("duplicate attempt accepted")
	}
	inv := testInventory()
	inv.Suites[0].Features[0].Flows[0].Platforms = []string{"ios"}
	if ValidateAttempt(inv, a) == nil {
		t.Fatal("unconfigured flow platform accepted")
	}
	a.Git.SHA = strings.Repeat("f", 64)
	if ValidateAttempt(testInventory(), a) != nil {
		t.Fatal("full SHA256 rejected")
	}
}
func TestStrictJSON(t *testing.T) {
	inv := testInventory()
	encoded, _ := json.Marshal(fullAttempt("attempt", "web"))
	valid := string(encoded)
	for _, data := range []string{
		strings.Replace(valid, `"dirty":false,`, "", 1),
		strings.Replace(valid, `,"dirty":false`, "", 1),
		strings.Replace(valid, `"dirty":false`, `"dirty":null`, 1),
		strings.Replace(valid, `"dirty":false`, `"dirty":false,"dirty":true`, 1),
		strings.Replace(valid, `"dirty":false`, `"dirty":false,"Dirty":false`, 1),
		strings.Replace(valid, `"functional":"pass"`, `"functional":"failed","Functional":"pass"`, 1),
		strings.Replace(valid, `"functional":"pass"`, `"Functional":"pass"`, 1),
		strings.Replace(valid, `"branch":"main",`, "", 1),
		strings.Replace(valid, `"wire":"pass",`, "", 1),
		strings.Replace(valid, `"wire":"pass"`, `"wire":null`, 1),
		strings.Replace(valid, `"schema":`, `"unexpected":1,"schema":`, 1),
		valid + ` {}`, `null`, `{}`, `[ ]`,
	} {
		if data == valid {
			continue
		}
		if _, err := DecodeAttempt([]byte(data), inv); err == nil {
			t.Errorf("accepted %s", data)
		}
	}
	if _, err := DecodeAttempt(encoded, inv); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{`{"schema":"retrace/suites/1","suites":null}`, `{"schema":"retrace/suites/1"}`, `{"schema":"retrace/suites/1","suites":[],"suites":[]}`, `{"schema":"retrace/suites/1","suites":[]} {}`} {
		if _, err := DecodeInventory([]byte(data)); err == nil {
			t.Errorf("accepted inventory %s", data)
		}
	}
}
func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}
func setupStore(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	writeJSON(t, filepath.Join(root, InventoryFile), testInventory())
	source := filepath.Join(t.TempDir(), "report.json")
	writeJSON(t, source, fullAttempt("attempt", "web"))
	return root, source
}
func TestImportRoundTripAndImmutableConcurrentPublish(t *testing.T) {
	root, source := setupStore(t)
	const n = 8
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := Import(root, source); errs <- err }()
	}
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("immutable publication succeeded %d times", success)
	}
	path := filepath.Join(root, ".retrace", "suites", "checkout", "attempt.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	different := fullAttempt("attempt", "web")
	different.Results[0].Planes.Wire = "failed"
	writeJSON(t, source, different)
	if _, err := Import(root, source); err == nil {
		t.Fatal("overwritten attempt")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("existing bytes changed")
	}
	out, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Builds[0].Counts != (Counts{Total: 15, Passed: 5, NotRun: 10}) {
		t.Fatalf("roundtrip: %+v", out)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("left temp files: %+v", entries)
	}
}
func TestStoreFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		damage func(t *testing.T, root string)
	}{
		{"bad inventory", func(t *testing.T, r string) { os.WriteFile(filepath.Join(r, InventoryFile), []byte(`{}`), 0600) }},
		{"bad report", func(t *testing.T, r string) {
			os.WriteFile(filepath.Join(r, ".retrace/suites/checkout/broken.json"), []byte(`{}`), 0600)
		}},
		{"mismatched filename", func(t *testing.T, r string) {
			os.Rename(filepath.Join(r, ".retrace/suites/checkout/attempt.json"), filepath.Join(r, ".retrace/suites/checkout/wrong.json"))
		}},
		{"missing inventory with reports", func(t *testing.T, r string) { os.Remove(filepath.Join(r, InventoryFile)) }},
		{"unknown version", func(t *testing.T, r string) {
			a := fullAttempt("old", "web")
			a.SuiteVersion = "old"
			writeJSON(t, filepath.Join(r, ".retrace/suites/checkout/old.json"), a)
		}},
		{"unknown directory", func(t *testing.T, r string) {
			os.MkdirAll(filepath.Join(r, ".retrace/suites/unconfigured"), 0755)
			a := fullAttempt("bad", "web")
			a.SuiteID = "unconfigured"
			writeJSON(t, filepath.Join(r, ".retrace/suites/unconfigured/bad.json"), a)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, source := setupStore(t)
			if _, err := Import(root, source); err != nil {
				t.Fatal(err)
			}
			tc.damage(t, root)
			if _, err := Load(root); err == nil {
				t.Fatal("broken data disappeared from aggregate")
			}
		})
	}
	out, err := Load(t.TempDir())
	if err != nil || out == nil || len(out) != 0 {
		t.Fatalf("unconfigured root: %+v %v", out, err)
	}
}
func TestSymlinkEscapesRejected(t *testing.T) {
	for _, component := range []string{"retrace.suites.json", ".retrace", ".retrace/suites", ".retrace/suites/checkout", ".retrace/suites/checkout/attempt.json"} {
		t.Run(component, func(t *testing.T) {
			root, source := setupStore(t)
			if _, err := Import(root, source); err != nil {
				t.Fatal(err)
			}
			original := filepath.Join(root, component)
			external := filepath.Join(t.TempDir(), "target")
			if err := os.Rename(original, external); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(external, original); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(root); err == nil {
				t.Fatal("symlink read accepted")
			}
			before := fullAttempt("other", "web")
			writeJSON(t, source, before)
			if component != ".retrace/suites/checkout/attempt.json" {
				if _, err := Import(root, source); err == nil {
					t.Fatal("symlink import accepted")
				}
			}
		})
	}
}
func TestImportRejectsInvalidWithoutPublishing(t *testing.T) {
	root, source := setupStore(t)
	a := fullAttempt("../escape", "web")
	writeJSON(t, source, a)
	if _, err := Import(root, source); err == nil {
		t.Fatal("traversal accepted")
	}
	if _, err := os.Stat(filepath.Join(root, ".retrace")); !os.IsNotExist(err) {
		t.Fatal("invalid import wrote storage")
	}
}
