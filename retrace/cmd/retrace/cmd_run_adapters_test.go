package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

// adapterE2EScript is the "small Node script" Task 17 Step 5 calls for: it
// imports the BUILT @caribou-crew/retrace-js package (not its TypeScript
// source — a real Node process, exactly like a real test runner, has no
// TS loader) and exercises the exact three things this task's adapters
// promise: a flow-part marker, one proxied request, and a checkpoint
// screenshot.
//
// jsDistIndex is an absolute path to adapters/js/dist/index.js. Node's ESM
// resolver accepts an absolute path directly as an import specifier, no
// file:// prefix required.
func adapterE2EScript(jsDistIndex, groupName, checkpointName string) string {
	// A minimal valid 1x1 transparent PNG, so image.DecodeConfig on the Go
	// side has real geometry to read.
	const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	return fmt.Sprintf(`
import { group, endGroup, shotsDir } from %q;
import * as fs from 'node:fs';
import * as path from 'node:path';

const proxyUrl = process.env.RETRACE_PROXY_URL;
if (!proxyUrl) throw new Error('RETRACE_PROXY_URL is not set — retrace run did not hand off its env');

await group(%q);
const res = await fetch(proxyUrl + '/cart');
await res.arrayBuffer();
await endGroup();

const dir = shotsDir();
if (!dir) throw new Error('shotsDir() returned null inside a run');
fs.mkdirSync(dir, { recursive: true });
fs.writeFileSync(path.join(dir, %q + '.png'), Buffer.from(%q, 'base64'));
`, jsDistIndex, groupName, checkpointName, pngB64)
}

// TestARunWithMarkersAndAScreenshotProducesGroupsAndCheckpoints is Task 17
// Step 5's end-to-end proof: it drives `retrace run`'s real env handshake
// through the built @caribou-crew/retrace-js package, exactly the way a
// real test suite would. It is the spec's "Playwright fixture" scenario
// reduced to its testable core, without requiring a browser in CI.
//
// R-AG (task-17-rulings.md): the assertions below check the derived group's
// NAME and BOUNDS, and hop ATTRIBUTION — never a bare count. A `ts` encoded
// as anything other than an RFC3339 string with a zone (e.g. epoch millis)
// would still produce a manifest with exactly one group interval — the
// count assertion a looser test would use cannot see that defect at all
// (runs.DeriveGroups opens a group at the zero time and it still closes at
// the `end` marker) — so this is the only place in the task that can catch
// R-AC's failure mode.
func TestARunWithMarkersAndAScreenshotProducesGroupsAndCheckpoints(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	jsPkgDir := filepath.Join(repoRoot, "adapters", "js")
	if _, err := os.Stat(jsPkgDir); err != nil {
		t.Skipf("adapters/js not present at %s: %v", jsPkgDir, err)
	}
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm not on PATH")
	}

	// The script imports dist/, so building it is a precondition of the
	// test, not an assumption.
	build := exec.Command("pnpm", "--filter", "@caribou-crew/retrace-js", "build")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("pnpm --filter @caribou-crew/retrace-js build: %v\n%s", err, out)
	}
	jsDistIndex := filepath.Join(jsPkgDir, "dist", "index.js")
	if _, err := os.Stat(jsDistIndex); err != nil {
		t.Fatalf("expected %s to exist after build: %v", jsDistIndex, err)
	}

	var upstreamHit bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")

	const groupName = "checkout-part"
	const checkpointName = "cart"
	scriptPath := filepath.Join(cwd, "adapter-e2e.mjs")
	if err := os.WriteFile(scriptPath, []byte(adapterE2EScript(jsDistIndex, groupName, checkpointName)), 0o644); err != nil {
		t.Fatalf("write adapter script: %v", err)
	}

	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("node vanished after LookPath check: %v", err)
	}

	before := time.Now()
	args := []string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL,
		"--", nodePath, scriptPath}
	res := runRetrace(t, bin, cwd, "", args...)
	after := time.Now()
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	if !upstreamHit {
		t.Fatal("the script's request never reached the upstream through RETRACE_PROXY_URL")
	}

	m := onlyManifest(t, cwd, "web", "checkout")

	if len(m.Groups) != 1 {
		t.Fatalf("groups = %+v, want exactly one", m.Groups)
	}
	g := m.Groups[0]
	if g.Name != groupName {
		t.Errorf("group name = %q, want %q", g.Name, groupName)
	}
	// A zero-value or epoch-millis-decoded timestamp fails this bounds
	// check even though it would still produce exactly one group.
	if g.StartedAt.Before(before) || g.StartedAt.After(after) {
		t.Errorf("group.startedAt = %s, want within [%s, %s]", g.StartedAt, before, after)
	}
	if g.EndedAt.Before(g.StartedAt) || g.EndedAt.After(after) {
		t.Errorf("group.endedAt = %s, want within [%s, %s]", g.EndedAt, g.StartedAt, after)
	}

	// Attribution: the one hop the script made through RETRACE_PROXY_URL
	// must fall inside the group's derived interval — the thing groups
	// exist for, per R-AG.
	runID := m.RunID
	wirePath := filepath.Join(runs.RunsRoot(cwd), "web", "checkout", runID, "wire.jsonl")
	hops, _, err := runs.ReadHops(wirePath)
	if err != nil {
		t.Fatalf("ReadHops: %v", err)
	}
	if len(hops) != 1 {
		t.Fatalf("wire hops = %d, want 1", len(hops))
	}
	if part := runs.GroupAt(m.Groups, hops[0].T.Start); part != groupName {
		t.Errorf("hop attributed to group %q, want %q", part, groupName)
	}

	if len(m.Checkpoints) != 1 {
		t.Fatalf("checkpoints = %+v, want exactly one", m.Checkpoints)
	}
	cp := m.Checkpoints[0]
	if cp.Name != checkpointName {
		t.Errorf("checkpoint name = %q, want %q", cp.Name, checkpointName)
	}
	if cp.Trim {
		t.Errorf("checkpoint.trim = true, want false — no .trim marker was written")
	}
	if cp.Width == 0 || cp.Height == 0 {
		t.Errorf("checkpoint geometry = %dx%d, want a decoded PNG size", cp.Width, cp.Height)
	}
}
