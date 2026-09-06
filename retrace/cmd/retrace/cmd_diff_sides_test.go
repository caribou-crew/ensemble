package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/refs"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

const (
	comparisonSHAA  = "1111111111111111111111111111111111111111"
	comparisonSHAB  = "2222222222222222222222222222222222222222"
	comparisonSHA64 = "3333333333333333333333333333333333333333333333333333333333333333"
)

// writeComparisonRecording uses the production recording writers so selector
// resolution and manifest validation see the same layout they see after a
// real capture. The zero-call wire plane is explicitly recorded: empty is a
// fact here, rather than the unsafe legacy meaning "not assessed".
func writeComparisonRecording(t *testing.T, root, app, flow, runID, sha string) {
	t.Helper()
	p, err := runs.Create(runs.RunsRoot(root), app, flow, runID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := runs.Manifest{
		App: app, Flow: flow, RunID: runID, Mode: runs.ModeStandalone,
		Git:     runs.Git{SHA: sha, Branch: "main"},
		Capture: runs.CaptureTrust{Status: trace.VerdictOK, Summary: "capture looks complete"},
		Wire:    runs.Counts{Recorded: true},
	}
	if err := runs.WriteManifest(p, &m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
}

func acceptComparisonRecording(t *testing.T, root, app, flow, runID string) {
	t.Helper()
	if _, err := refs.Accept(refs.AcceptOptions{
		Cwd: root, RunsRoot: runs.RunsRoot(root), App: app, Flow: flow, RunID: runID,
	}); err != nil {
		t.Fatalf("Accept: %v", err)
	}
}

type comparisonSummaryJSON struct {
	A struct {
		RunID string `json:"runId"`
	} `json:"a"`
	B struct {
		RunID string `json:"runId"`
	} `json:"b"`
	Provenance *struct {
		A struct {
			Root        string   `json:"root"`
			Selector    string   `json:"selector"`
			Kind        string   `json:"kind"`
			App         string   `json:"app"`
			Flow        string   `json:"flow"`
			RunID       string   `json:"runId"`
			RecordedGit runs.Git `json:"recordedGit"`
		} `json:"a"`
		B struct {
			Root        string   `json:"root"`
			Selector    string   `json:"selector"`
			Kind        string   `json:"kind"`
			App         string   `json:"app"`
			Flow        string   `json:"flow"`
			RunID       string   `json:"runId"`
			RecordedGit runs.Git `json:"recordedGit"`
		} `json:"b"`
		ComparisonConfig struct {
			Scope       string `json:"scope"`
			Dir         string `json:"dir"`
			Fingerprint string `json:"fingerprint"`
		} `json:"comparisonConfig"`
	} `json:"provenance"`
}

func decodeComparisonSummary(t *testing.T, out string) comparisonSummaryJSON {
	t.Helper()
	var got comparisonSummaryJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode diff summary: %v\n%s", err, out)
	}
	return got
}

// TestSideResolverIsolatesAnExplicitRoot is the narrow mutation seam for the
// same behavior the CLI test below exercises. SearchRoots deliberately holds
// two matching trees: replacing the singleton root with that shared list must
// make this test fail with ambiguity rather than compiling into a false pass.
func TestSideResolverIsolatesAnExplicitRoot(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	writeConfig(t, rootA, "app: web\n")
	writeConfig(t, rootB, "app: web\n")
	writeComparisonRecording(t, rootA, "web", "checkout", "20260906T100000Z-1111111", comparisonSHAA)
	writeComparisonRecording(t, rootB, "web", "checkout", "20260906T110000Z-2222222", comparisonSHAB)

	got, err := resolveComparisonSide(comparisonSideRequest{
		Label: "a", Selector: "latest", Root: rootA, RootSet: true,
		SearchRoots: []string{rootA, rootB}, LegacyDefaultApp: "web", Flow: "checkout",
	})
	if err != nil {
		t.Fatalf("resolveComparisonSide: %v", err)
	}
	if got.Root != rootA || got.Ref.RunID != "20260906T100000Z-1111111" {
		t.Fatalf("explicit side resolved to %s/%s, want %s/%s", got.Root, got.Ref.RunID, rootA, "20260906T100000Z-1111111")
	}
}

// TestSideResolverComparesTheEntireManifestCommit is the direct mutation seam
// for exact identity. These two full SHA-1 values share their first 39
// characters, so any prefix comparison accepts the wrong recording.
func TestSideResolverComparesTheEntireManifestCommit(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "app: web\n")
	writeComparisonRecording(t, root, "web", "checkout", "20260906T100000Z-1111111", comparisonSHAA)
	want := strings.Repeat("1", 39) + "2"
	_, err := resolveComparisonSide(comparisonSideRequest{
		Label: "a", Selector: "latest", Root: root, RootSet: true,
		Commit: want, CommitSet: true, SearchRoots: []string{root},
		LegacyDefaultApp: "web", Flow: "checkout",
	})
	if err == nil || !strings.Contains(err.Error(), comparisonSHAA) {
		t.Fatalf("full identity mismatch was not refused with the actual manifest SHA: %v", err)
	}
}

// TestExplicitSideRootsResolveTheSameAppIndependently catches the isolation
// guard being removed or one side accidentally reusing the other's root. Both
// trees contain web@latest, the legacy multi-root form below is ambiguous, and
// the explicit form must select the exact run named by each side.
func TestExplicitSideRootsResolveTheSameAppIndependently(t *testing.T) {
	bin := buildRetrace(t)
	policy, rootA, rootB := t.TempDir(), t.TempDir(), t.TempDir()
	writeConfig(t, policy, "app: policy-app\n")
	writeConfig(t, rootA, "app: web\n")
	writeConfig(t, rootB, "app: web\n")
	writeComparisonRecording(t, rootA, "web", "checkout", "20260906T100000Z-1111111", comparisonSHAA)
	writeComparisonRecording(t, rootB, "web", "checkout", "20260906T110000Z-2222222", comparisonSHAB)
	acceptComparisonRecording(t, rootA, "web", "checkout", "20260906T100000Z-1111111")

	ambiguous := runRetrace(t, bin, policy, "", "diff", "--flow", "checkout", "--images=false",
		"--root", rootA, "--root", rootB, "--a", "web@latest", "--b", "web@latest")
	if ambiguous.code == exitOK || !strings.Contains(ambiguous.stderr, "more than one root") {
		t.Fatalf("legacy selector did not preserve cross-root ambiguity: exit=%d\nstdout=%s\nstderr=%s", ambiguous.code, ambiguous.stdout, ambiguous.stderr)
	}

	res := runRetrace(t, bin, policy, "", "diff", "--flow", "checkout", "--json", "--images=false",
		"--root", rootA, "--root", rootB,
		"--a-root", rootA, "--b-root", rootB,
		"--a", "reference", "--b", "latest",
		"--a-commit", comparisonSHAA, "--b-commit", comparisonSHAB)
	if res.code != exitOK {
		t.Fatalf("explicit comparison: exit=%d\nstdout=%s\nstderr=%s", res.code, res.stdout, res.stderr)
	}
	got := decodeComparisonSummary(t, res.stdout)
	if got.Provenance == nil {
		t.Fatal("summary has no comparison provenance")
	}
	if got.A.RunID != "20260906T100000Z-1111111" || got.B.RunID != "20260906T110000Z-2222222" {
		t.Fatalf("resolved runs = %q/%q, want the reference from A and latest from B", got.A.RunID, got.B.RunID)
	}
	if got.Provenance.A.Root != rootA || got.Provenance.B.Root != rootB {
		t.Errorf("provenance roots = %q/%q, want %q/%q", got.Provenance.A.Root, got.Provenance.B.Root, rootA, rootB)
	}
	if got.Provenance.A.Selector != "reference" || got.Provenance.B.Selector != "latest" {
		t.Errorf("selectors = %q/%q, want reference/latest", got.Provenance.A.Selector, got.Provenance.B.Selector)
	}
	if got.Provenance.A.Kind != "bundle" || got.Provenance.B.Kind != "run" {
		t.Errorf("resolved kinds = %q/%q, want bundle/run", got.Provenance.A.Kind, got.Provenance.B.Kind)
	}
	if got.Provenance.A.App != "web" || got.Provenance.B.App != "web" ||
		got.Provenance.A.Flow != "checkout" || got.Provenance.B.Flow != "checkout" {
		t.Errorf("resolved identities = %+v / %+v", got.Provenance.A, got.Provenance.B)
	}
	if got.Provenance.A.RecordedGit.SHA != comparisonSHAA || got.Provenance.B.RecordedGit.SHA != comparisonSHAB {
		t.Errorf("recorded git identities = %+v/%+v", got.Provenance.A.RecordedGit, got.Provenance.B.RecordedGit)
	}
	actualPolicy, err := filepath.EvalSymlinks(policy)
	if err != nil {
		t.Fatal(err)
	}
	if got.Provenance.ComparisonConfig.Scope != "comparison-time" || got.Provenance.ComparisonConfig.Dir != actualPolicy {
		t.Errorf("comparison config provenance = %+v, want comparison-time policy at %s", got.Provenance.ComparisonConfig, actualPolicy)
	}
	if len(got.Provenance.ComparisonConfig.Fingerprint) != 64 {
		t.Errorf("fingerprint = %q, want a full SHA-256 hex digest", got.Provenance.ComparisonConfig.Fingerprint)
	}

	text := runRetrace(t, bin, policy, "", "diff", "--flow", "checkout", "--images=false",
		"--root", rootA, "--root", rootB,
		"--a-root", rootA, "--b-root", rootB,
		"--a", "reference", "--b", "latest",
		"--a-commit", comparisonSHAA, "--b-commit", comparisonSHAB)
	if text.code != exitOK {
		t.Fatalf("text comparison: exit=%d\nstderr=%s", text.code, text.stderr)
	}
	for _, want := range []string{"SIDE A:", rootA, "SIDE B:", rootB, "COMPARISON CONFIG (comparison-time):", got.Provenance.ComparisonConfig.Fingerprint} {
		if !strings.Contains(text.stdout, want) {
			t.Errorf("text report does not contain %q:\n%s", want, text.stdout)
		}
	}
}

// TestSideCommitAssertionsRequireTheFullRecordedIdentity kills a prefix
// comparison: legacy selectors may still use SHA prefixes, but an assertion
// must compare the entire manifest Git.SHA and must refuse absent evidence.
func TestSideCommitAssertionsRequireTheFullRecordedIdentity(t *testing.T) {
	bin := buildRetrace(t)
	policy, rootA, rootB := t.TempDir(), t.TempDir(), t.TempDir()
	writeConfig(t, policy, "app: web\n")
	writeConfig(t, rootA, "app: web\n")
	writeConfig(t, rootB, "app: web\n")
	writeComparisonRecording(t, rootA, "web", "checkout", "20260906T100000Z-1111111", comparisonSHAA)
	writeComparisonRecording(t, rootB, "web", "checkout", "20260906T110000Z-2222222", "")

	for _, tc := range []struct {
		name, side, commit, want string
	}{
		{"prefix is not an exact assertion", "a", comparisonSHAA[:7], "40 or 64 hexadecimal"},
		{"different full identity", "a", strings.Repeat("1", 39) + "2", comparisonSHAA},
		{"missing recorded identity", "b", comparisonSHAB, "does not record a git commit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"diff", "--flow", "checkout", "--images=false", "--a-root", rootA, "--b-root", rootB, "--a", "latest", "--b", "latest"}
			args = append(args, "--"+tc.side+"-commit", tc.commit)
			res := runRetrace(t, bin, policy, "", args...)
			if res.code == exitOK {
				t.Fatalf("commit assertion unexpectedly evaluated a comparison\nstdout=%s", res.stdout)
			}
			if !strings.Contains(res.stderr, tc.want) {
				t.Errorf("stderr does not name the recorded identity/refusal %q:\n%s", tc.want, res.stderr)
			}
		})
	}

	// Legacy SHA-prefix selection remains deliberately additive: without an
	// assertion it keeps selecting by the run-directory suffix.
	legacy := runRetrace(t, bin, policy, "", "diff", "--flow", "checkout", "--images=false",
		"--a-root", rootA, "--b-root", rootA, "--a", comparisonSHAA[:7], "--b", comparisonSHAA[:7])
	if legacy.code != exitOK {
		t.Fatalf("legacy SHA-prefix selector changed: exit=%d\nstderr=%s", legacy.code, legacy.stderr)
	}

	shortRoot := t.TempDir()
	writeConfig(t, shortRoot, "app: web\n")
	writeComparisonRecording(t, shortRoot, "web", "checkout", "20260906T120000Z-abcdef0", "abcdef0")
	short := runRetrace(t, bin, policy, "", "diff", "--flow", "checkout", "--images=false",
		"--a-root", shortRoot, "--b-root", shortRoot, "--a", "latest", "--b", "latest", "--a-commit", "abcdef0")
	if short.code == exitOK || !strings.Contains(short.stderr, "40 or 64 hexadecimal") {
		t.Fatalf("a short assertion matching a short manifest identity was accepted: exit=%d\nstdout=%s\nstderr=%s", short.code, short.stdout, short.stderr)
	}

	sha256Root := t.TempDir()
	writeConfig(t, sha256Root, "app: web\n")
	writeComparisonRecording(t, sha256Root, "web", "checkout", "20260906T130000Z-3333333", comparisonSHA64)
	sha256 := runRetrace(t, bin, policy, "", "diff", "--flow", "checkout", "--images=false",
		"--a-root", sha256Root, "--b-root", sha256Root, "--a", "latest", "--b", "latest",
		"--a-commit", comparisonSHA64, "--b-commit", comparisonSHA64)
	if sha256.code != exitOK {
		t.Fatalf("full SHA-256 assertion was refused: exit=%d\nstderr=%s", sha256.code, sha256.stderr)
	}
}

func TestExplicitEmptySideFlagsAreInputErrors(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")
	writeComparisonRecording(t, cwd, "web", "checkout", "20260906T100000Z-1111111", comparisonSHAA)

	for _, flag := range []string{"--a-root=", "--a-app=", "--a-commit="} {
		t.Run(flag, func(t *testing.T) {
			res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--images=false",
				flag, "--a", "latest", "--b", "latest")
			if res.code == exitOK {
				t.Fatalf("%s silently fell back to a default\nstdout=%s", flag, res.stdout)
			}
			name := strings.TrimSuffix(flag, "=")
			if !strings.Contains(res.stderr, name) || !strings.Contains(res.stderr, "empty") {
				t.Errorf("stderr does not identify %s as explicitly empty:\n%s", name, res.stderr)
			}
		})
	}
}

func TestExplicitRootsDefaultEachSideAppFromThatRoot(t *testing.T) {
	bin := buildRetrace(t)
	policy, rootA, rootB := t.TempDir(), t.TempDir(), t.TempDir()
	writeConfig(t, policy, "app: unrelated-policy-app\n")
	writeConfig(t, rootA, "app: web\n")
	writeConfig(t, rootB, "app: mobile\n")
	writeComparisonRecording(t, rootA, "web", "checkout", "20260906T100000Z-1111111", comparisonSHAA)
	writeComparisonRecording(t, rootB, "mobile", "checkout", "20260906T110000Z-2222222", comparisonSHAB)

	res := runRetrace(t, bin, policy, "", "diff", "--flow", "checkout", "--json", "--images=false",
		"--a-root", rootA, "--b-root", rootB, "--a", "latest", "--b", "latest")
	if res.code != exitOK {
		t.Fatalf("exit=%d\nstdout=%s\nstderr=%s", res.code, res.stdout, res.stderr)
	}
	got := decodeComparisonSummary(t, res.stdout)
	if got.Provenance == nil || got.Provenance.A.App != "web" || got.Provenance.B.App != "mobile" {
		t.Fatalf("per-root default apps not reported: %+v", got.Provenance)
	}
}

func TestSideSpecificAppsSelectNonDefaultRecordings(t *testing.T) {
	bin := buildRetrace(t)
	policy, rootA, rootB := t.TempDir(), t.TempDir(), t.TempDir()
	writeConfig(t, policy, "app: policy\n")
	writeConfig(t, rootA, "app: web\n")
	writeConfig(t, rootB, "app: mobile\n")
	writeComparisonRecording(t, rootA, "admin", "checkout", "20260906T100000Z-1111111", comparisonSHAA)
	writeComparisonRecording(t, rootB, "ios", "checkout", "20260906T110000Z-2222222", comparisonSHAB)

	res := runRetrace(t, bin, policy, "", "diff", "--flow", "checkout", "--json", "--images=false",
		"--a-root", rootA, "--b-root", rootB,
		"--a-app", "admin", "--a", "admin@latest",
		"--b-app", "ios", "--b", "latest")
	if res.code != exitOK {
		t.Fatalf("side-specific apps: exit=%d\nstdout=%s\nstderr=%s", res.code, res.stdout, res.stderr)
	}
	got := decodeComparisonSummary(t, res.stdout)
	if got.Provenance == nil || got.Provenance.A.App != "admin" || got.Provenance.B.App != "ios" {
		t.Fatalf("side-specific app identities = %+v, want admin/ios", got.Provenance)
	}
}

func TestExplicitRootWithoutConfigDefaultsToDirectoryName(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "desktop-client")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeComparisonRecording(t, root, "desktop-client", "checkout", "20260906T100000Z-1111111", comparisonSHAA)

	got, err := resolveComparisonSide(comparisonSideRequest{
		Label: "a", Selector: "latest", Root: root, RootSet: true,
		SearchRoots: []string{parent}, LegacyDefaultApp: "other", Flow: "checkout",
	})
	if err != nil {
		t.Fatalf("resolveComparisonSide: %v", err)
	}
	if got.Ref.Manifest.App != "desktop-client" {
		t.Fatalf("default app = %q, want directory name desktop-client", got.Ref.Manifest.App)
	}
}

func TestExplicitSideAppRejectsAConflictingQualifiedSelector(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")
	writeComparisonRecording(t, cwd, "web", "checkout", "20260906T100000Z-1111111", comparisonSHAA)

	res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--images=false",
		"--a-app", "web", "--a", "mobile@latest", "--b", "latest")
	if res.code == exitOK {
		t.Fatalf("conflicting app sources produced a comparison\nstdout=%s", res.stdout)
	}
	for _, want := range []string{"side a", "web", "mobile"} {
		if !strings.Contains(res.stderr, want) {
			t.Errorf("stderr does not name conflict part %q:\n%s", want, res.stderr)
		}
	}
}

func TestExplicitSideRootMustBeAnExistingDirectory(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")
	writeComparisonRecording(t, cwd, "web", "checkout", "20260906T100000Z-1111111", comparisonSHAA)
	missing := filepath.Join(cwd, "missing")
	file := filepath.Join(cwd, "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{missing, file, "   "} {
		res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--images=false",
			"--a-root", bad, "--b-root", cwd, "--a", "latest", "--b", "latest")
		if res.code == exitOK {
			t.Fatalf("invalid side root %s produced a comparison", bad)
		}
		if strings.TrimSpace(bad) == "" {
			if !strings.Contains(res.stderr, "empty") {
				t.Errorf("stderr does not identify blank root as empty:\n%s", res.stderr)
			}
			continue
		}
		if !strings.Contains(res.stderr, bad) || !strings.Contains(res.stderr, "directory") {
			t.Errorf("stderr does not identify invalid root %s:\n%s", bad, res.stderr)
		}
	}
}

func TestComparisonConfigFingerprintChangesWithPolicyButNotItsDirectory(t *testing.T) {
	bin := buildRetrace(t)
	rootA, rootB := t.TempDir(), t.TempDir()
	writeConfig(t, rootA, "app: web\n")
	writeConfig(t, rootB, "app: web\n")
	writeComparisonRecording(t, rootA, "web", "checkout", "20260906T100000Z-1111111", comparisonSHAA)
	writeComparisonRecording(t, rootB, "web", "checkout", "20260906T110000Z-2222222", comparisonSHAB)

	fingerprint := func(policy, body string) string {
		t.Helper()
		writeConfig(t, policy, body)
		res := runRetrace(t, bin, policy, "", "diff", "--flow", "checkout", "--json", "--images=false",
			"--a-root", rootA, "--b-root", rootB, "--a", "latest", "--b", "latest")
		if res.code != exitOK {
			t.Fatalf("diff in %s: exit=%d\nstdout=%s\nstderr=%s", policy, res.code, res.stdout, res.stderr)
		}
		got := decodeComparisonSummary(t, res.stdout)
		if got.Provenance == nil {
			t.Fatal("summary has no comparison provenance")
		}
		return got.Provenance.ComparisonConfig.Fingerprint
	}

	policyA, policyB := t.TempDir(), t.TempDir()
	base := "app: policy\nthresholds:\n  gate: 0.2\n  fine: 0.05\n"
	first := fingerprint(policyA, base)
	if relocated := fingerprint(policyB, base); relocated != first {
		t.Errorf("identical effective config changed when relocated: %s != %s", relocated, first)
	}
	if changed := fingerprint(policyB, "app: policy\nthresholds:\n  gate: 0.3\n  fine: 0.05\n"); changed == first {
		t.Errorf("policy change left fingerprint at %s", first)
	}
}
