package refs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// jwtFixture is JWT-SHAPED but decodes to nothing anyone could use — the
// exact false-positive class design.md accepts at accept time because
// --force plus a rule suggestion keeps the workflow unblocked.
const jwtFixture = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig-part"

func writeHopLine(t *testing.T, path string, hops ...trace.Hop) {
	t.Helper()
	var b strings.Builder
	for _, h := range hops {
		h.Schema = trace.SchemaVersion
		line, err := json.Marshal(h)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	writeFile(t, path, []byte(b.String()))
}

func findingPaths(fs []SecretFinding) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Path
	}
	return out
}

func TestScanFlagsLikelyCredentialsPostRedaction(t *testing.T) {
	dir := t.TempDir()
	writeHopLine(t, filepath.Join(dir, "wire.jsonl"), trace.Hop{
		Seq:  1,
		Path: "/login?token=abc123&page=1",
		Req: trace.Payload{
			Headers: map[string]string{"X-Team-Token": "Bearer aaaaaaaaaaaaaaaaaaaaaaaa"},
			Body:    `{"password":"hunter2"}`,
		},
		Resp: trace.Payload{
			Body: `{"user":"a","session_key":"` + jwtFixture + `","aws":{"key":"AKIAIOSFODNN7EXAMPLE"},"list":[{"client_secret":"s"}]}`,
		},
	})

	got, err := ScanForSecrets(dir)
	if err != nil {
		t.Fatalf("ScanForSecrets: %v", err)
	}
	want := map[string]string{
		"query.token":                     "secret-key",
		"req.header.x-team-token":         "bearer-token",
		"req.body.password":               "secret-key",
		"resp.body.session_key":           "jwt",
		"resp.body.aws.key":               "aws-access-key-id",
		"resp.body.list[0].client_secret": "secret-key",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d findings %v, want %d", len(got), findingPaths(got), len(want))
	}
	for _, f := range got {
		if want[f.Path] != f.Kind {
			t.Errorf("finding %s = kind %q, want %q", f.Path, f.Kind, want[f.Path])
		}
		if f.Suggestion == "" {
			t.Errorf("finding %s carries no suggestion", f.Path)
		}
	}
}

func TestScanIgnoresRedactedAndInnocentValues(t *testing.T) {
	dir := t.TempDir()
	writeHopLine(t, filepath.Join(dir, "wire.jsonl"), trace.Hop{
		Seq:  1,
		Path: "/login?token=" + trace.Redacted,
		Req: trace.Payload{
			// The destroy sentinel, an encrypt marker, and a short Bearer:
			// all things a CLEAN post-redaction capture legitimately holds.
			Headers: map[string]string{
				"Authorization": trace.Redacted,
				"X-Session":     trace.EncryptedPrefix + "AAAA",
				"X-Note":        "Bearer short",
			},
			Body: `{"password":"` + trace.Redacted + `","account":"` + trace.EncryptedPrefix + `BBBB"}`,
		},
		Resp: trace.Payload{Body: `{"greeting":"hey Jude","version":"1.2.3"}`},
	})

	got, err := ScanForSecrets(dir)
	if err != nil {
		t.Fatalf("ScanForSecrets: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("clean capture produced findings: %v", findingPaths(got))
	}
}

func TestScanOfAMissingHopFileIsClean(t *testing.T) {
	// A standalone run has no hops.jsonl at all; the scan must not invent a
	// refusal out of an absent plane.
	got, err := ScanForSecrets(t.TempDir())
	if err != nil || len(got) != 0 {
		t.Fatalf("empty run dir: findings=%v err=%v", got, err)
	}
}

// TestAcceptRefusesOnSecretFindingsAndForceRecordsIt drives the spec's two
// accept scenarios end to end: a staged exchange with an unredacted
// credential refuses (typed error, field path named, reference directory
// untouched), and the same accept under Force promotes with
// acceptedWithSecrets: true in the BUNDLE manifest — while the run's own
// manifest stays unmarked.
func TestAcceptRefusesOnSecretFindingsAndForceRecordsIt(t *testing.T) {
	cwd := t.TempDir()
	root, runID := acceptFixture(t, cwd)
	p, _ := runs.PathsFor(root, "web", "checkout", runID)
	writeHopLine(t, p.WirePath, trace.Hop{
		Seq:  1,
		Path: "/login",
		Resp: trace.Payload{Body: `{"session_key":"` + jwtFixture + `"}`},
	})

	_, err := Accept(acceptOpts(cwd, root, runID))
	if err == nil {
		t.Fatal("accept must refuse a staged bundle carrying a likely credential")
	}
	var scanErr *SecretScanError
	if !errors.As(err, &scanErr) {
		t.Fatalf("want a *SecretScanError, got %T: %v", err, err)
	}
	if len(scanErr.Findings) != 1 || scanErr.Findings[0].Path != "resp.body.session_key" {
		t.Fatalf("findings = %+v, want resp.body.session_key", scanErr.Findings)
	}
	if !strings.Contains(err.Error(), "resp.body.session_key") {
		t.Fatalf("refusal must name the field path: %v", err)
	}
	dir, _ := BundleDir(cwd, "web", "checkout")
	if _, serr := os.Stat(dir); !os.IsNotExist(serr) {
		t.Fatal("a refused accept must leave no reference directory behind")
	}

	// Forced: promoted, findings reported, and the bundle manifest carries
	// the permanent note.
	o := acceptOpts(cwd, root, runID)
	o.Force = true
	res, err := Accept(o)
	if err != nil {
		t.Fatalf("forced Accept: %v", err)
	}
	if len(res.SecretFindings) != 1 {
		t.Fatalf("forced result must carry the findings, got %+v", res.SecretFindings)
	}
	bm, err := runs.ReadManifest(filepath.Join(res.Dir, "manifest.json"))
	if err != nil {
		t.Fatalf("reading the bundle manifest: %v", err)
	}
	if !bm.AcceptedWithSecrets {
		t.Fatal("forced accept must record acceptedWithSecrets: true in the bundle manifest")
	}
	rm, err := runs.ReadManifest(p.ManifestPath)
	if err != nil {
		t.Fatalf("reading the run manifest: %v", err)
	}
	if rm.AcceptedWithSecrets {
		t.Fatal("the RUN's own manifest must stay unmarked — the note belongs to the promotion, not the recording")
	}
}

// TestAcceptOfACleanBundleCarriesNoSecretNote pins the third spec scenario:
// a clean staged bundle accepts exactly as before this change — no findings,
// no manifest note. (acceptFixture's hop files predate the scan entirely, so
// this is also the pre-change-bundle compatibility check.)
func TestAcceptOfACleanBundleCarriesNoSecretNote(t *testing.T) {
	cwd := t.TempDir()
	root, runID := acceptFixture(t, cwd)
	res, err := Accept(acceptOpts(cwd, root, runID))
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if len(res.SecretFindings) != 0 {
		t.Fatalf("clean accept reported findings: %+v", res.SecretFindings)
	}
	m, err := runs.ReadManifest(filepath.Join(res.Dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if m.AcceptedWithSecrets {
		t.Fatal("clean accept must not mark the manifest")
	}
}
