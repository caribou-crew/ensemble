package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// TestHelperCapturesAtDeclaredScreen fetches through the proxy like the
// ordinary helper and additionally writes a device.json — the thing a real
// browser adapter does. The geometry comes from RETRACE_TEST_SCREEN so one
// helper covers "the right screen" and "the wrong screen".
func TestHelperCapturesAtDeclaredScreen(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "screen" {
		return
	}
	resp, err := http.Get(os.Getenv("RETRACE_PROXY_URL") + "/cart")
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper fetch:", err)
		os.Exit(9)
	}
	resp.Body.Close()

	var w, h int
	if _, err := fmt.Sscanf(os.Getenv("RETRACE_TEST_SCREEN"), "%dx%d", &w, &h); err != nil {
		fmt.Fprintln(os.Stderr, "helper screen:", err)
		os.Exit(9)
	}
	body, _ := json.Marshal(map[string]any{"kind": "browser", "id": "harness", "width": w, "height": h})
	if err := os.WriteFile(filepath.Join(os.Getenv("RETRACE_RUN_DIR"), "device.json"), body, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "helper device:", err)
		os.Exit(9)
	}
	os.Exit(0)
}

// runAtScreen records one run whose adapter reports the given geometry, and
// returns the retrace result unexamined — these tests are about the exit code.
func runAtScreen(t *testing.T, bin, cwd, app, flow, upstreamURL, screen string) runResult {
	t.Helper()
	args := append([]string{"run", "--flow", flow, "--app", app, "--upstream", upstreamURL},
		selfCmd(t, "TestHelperCapturesAtDeclaredScreen")...)
	t.Setenv("RETRACE_TEST_SCREEN", screen)
	return runRetrace(t, bin, cwd, "screen", args...)
}

func canonicalConfig(t *testing.T, cwd, flowBlock string) {
	t.Helper()
	writeConfig(t, cwd, "app: web\nflows:\n  checkout:\n"+flowBlock)
}

func TestARunAtTheDeclaredScreenIsRecordedNormally(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	bin, cwd := buildRetrace(t), t.TempDir()
	canonicalConfig(t, cwd, "    canonical:\n      width: 390\n      height: 844\n      strict: true\n")

	res := runAtScreen(t, bin, cwd, "web", "checkout", upstream.URL, "390x844")
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0 — this run is exactly the size the flow declared\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
}

func TestAStrictCanonicalMismatchRefusesTheRunButKeepsIt(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	bin, cwd := buildRetrace(t), t.TempDir()
	canonicalConfig(t, cwd, "    canonical:\n      width: 390\n      height: 844\n      strict: true\n")

	res := runAtScreen(t, bin, cwd, "web", "checkout", upstream.URL, "1206x2622")
	// Exit 2, not 3. The recording is intact and readable; what failed is the
	// flow's precondition. Exit 3 means "could not evaluate", which would tell
	// CI to throw away a perfectly usable run.
	if res.code != exitGate {
		t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", res.code, exitGate, res.stdout, res.stderr)
	}
	all := res.stdout + res.stderr
	for _, want := range []string{"390x844", "1206x2622", "canonical"} {
		if !strings.Contains(all, want) {
			t.Errorf("the message does not mention %q:\n%s", want, all)
		}
	}

	// ...and the run directory survives, manifest and all. A refusal that
	// deleted its own evidence would leave nobody able to see what size it
	// actually was.
	ids := runs.ListRuns(runs.RunsRoot(cwd), "web", "checkout")
	if len(ids) != 1 {
		t.Fatalf("run directories = %v, want exactly one — a refused run must still be on disk", ids)
	}
	m := readRunManifest(t, cwd, "web", "checkout", ids[0])
	if m.Device == nil || m.Device.Width != 1206 {
		t.Errorf("manifest device = %+v, want the 1206-wide screen it was really captured at", m.Device)
	}
}

func TestANonStrictCanonicalMismatchRecordsTheDriftAndCarriesOn(t *testing.T) {
	// The ratchet. Writing down an expectation must not silently start
	// failing builds, or nobody dares write one down.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	bin, cwd := buildRetrace(t), t.TempDir()
	canonicalConfig(t, cwd, "    canonical:\n      width: 390\n      height: 844\n")

	res := runAtScreen(t, bin, cwd, "web", "checkout", upstream.URL, "1206x2622")
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0 without `strict: true`\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "canonical") {
		t.Errorf("the drift was not reported on stderr:\n%s", res.stderr)
	}

	// The half that survives once the scrollback is gone: manifest.device
	// carries the size the run was really captured at, so a reader months
	// later can still see it beside the flow's declared canonical.
	ids := runs.ListRuns(runs.RunsRoot(cwd), "web", "checkout")
	m := readRunManifest(t, cwd, "web", "checkout", ids[0])
	if m.Device == nil || m.Device.Width != 1206 || m.Device.Height != 2622 {
		t.Errorf("manifest device = %+v, want the 1206x2622 it drifted to", m.Device)
	}
}

func TestANonStrictCanonicalMismatchDoesNotPoisonTheCaptureVerdict(t *testing.T) {
	// The trap this closes, found by writing the test above. Recording the
	// drift as a capture-trust NOTE was the obvious design — and a note makes
	// the verdict "suspect", which `diff` quarantines. Non-strict would then
	// refuse every COMPARISON while strict refuses only the RECORDING, making
	// the lenient setting strictly harsher than the strict one.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	bin, cwd := buildRetrace(t), t.TempDir()
	canonicalConfig(t, cwd, "    canonical:\n      width: 390\n      height: 844\n")

	runAtScreen(t, bin, cwd, "web", "checkout", upstream.URL, "1206x2622")
	ids := runs.ListRuns(runs.RunsRoot(cwd), "web", "checkout")
	m := readRunManifest(t, cwd, "web", "checkout", ids[0])
	if m.Capture.Status != trace.VerdictOK {
		t.Errorf("capture verdict = %q, want ok — a non-strict geometry drift must not make the run undiffable: %+v", m.Capture.Status, m.Capture.Reasons)
	}
}

func TestACanonicalBlockWithNoDimensionsIsAConfigError(t *testing.T) {
	// `canonical: {strict: true}` reads as the strongest possible assertion
	// and means 0x0 — every run would fail it forever, for a reason no
	// message would ever explain.
	bin, cwd := buildRetrace(t), t.TempDir()
	canonicalConfig(t, cwd, "    canonical:\n      strict: true\n")

	res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web")
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d — a dimensionless canonical block loaded clean\nstdout: %s\nstderr: %s", res.code, exitUsage, res.stdout, res.stderr)
	}
	if all := res.stdout + res.stderr; !strings.Contains(all, "canonical") {
		t.Errorf("the error does not name the offending key:\n%s", all)
	}
}

// readRunManifest reads one run's manifest off disk.
func readRunManifest(t *testing.T, cwd, app, flow, id string) runs.Manifest {
	t.Helper()
	p, err := runs.PathsFor(runs.RunsRoot(cwd), app, flow, id)
	if err != nil {
		t.Fatalf("PathsFor: %v", err)
	}
	m, err := runs.ReadManifest(p.ManifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	return m
}

func TestARefusedRunStillReportsWhatItRecorded(t *testing.T) {
	// A refusal that returns exit 2 and an empty --json document says a flow
	// failed without saying anything about it. The geometry the run actually
	// captured at is the single fact an agent needs in order to act, and it
	// is the one thing the refusal is about.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	bin, cwd := buildRetrace(t), t.TempDir()
	canonicalConfig(t, cwd, "    canonical:\n      width: 390\n      height: 844\n      strict: true\n")

	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--json", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperCapturesAtDeclaredScreen")...)
	t.Setenv("RETRACE_TEST_SCREEN", "1206x2622")
	res := runRetrace(t, bin, cwd, "screen", args...)

	if res.code != exitGate {
		t.Fatalf("exit = %d, want %d\nstderr: %s", res.code, exitGate, res.stderr)
	}
	var m runs.Manifest
	if err := json.Unmarshal([]byte(res.stdout), &m); err != nil {
		t.Fatalf("--json produced nothing parseable for a refused run: %v\n%s", err, res.stdout)
	}
	if m.Device == nil || m.Device.Width != 1206 || m.Device.Height != 2622 {
		t.Errorf("device = %+v, want the 1206x2622 the run was refused for", m.Device)
	}
}

func TestACanonicalFlowRefusesARunThatRecordedNoScreenAtAll(t *testing.T) {
	// "Nothing was recorded" is not evidence of the right geometry. A flow
	// that went to the trouble of declaring a screen is exactly the flow that
	// should hear a run reported none — otherwise an adapter that quietly
	// stops writing device.json turns the guard off and nothing says so.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	bin, cwd := buildRetrace(t), t.TempDir()
	canonicalConfig(t, cwd, "    canonical:\n      width: 390\n      height: 844\n      strict: true\n")

	// The ordinary helper: it fetches through the proxy and writes neither a
	// device.json nor any screenshot.
	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)

	if res.code != exitGate {
		t.Fatalf("exit = %d, want %d — a run with no recorded screen passed a canonical check\nstdout: %s\nstderr: %s", res.code, exitGate, res.stdout, res.stderr)
	}
	if all := res.stdout + res.stderr; !strings.Contains(all, "no screen geometry") {
		t.Errorf("the message does not say the run recorded nothing:\n%s", all)
	}
}
