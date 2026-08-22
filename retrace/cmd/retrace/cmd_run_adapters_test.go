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

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// skipOrFatal is t.Skip everywhere EXCEPT under CI (set by GitHub Actions,
// and any other CI system that follows the same convention), where it is
// t.Fatal instead. A contributor without node/pnpm on PATH must still be
// able to run the rest of the Go suite locally — but a CI runner missing
// them is a toolchain regression, not an environment nobody expected node
// in. Skipping quietly there is a zero value ("no adapters test ran") that
// reads as "fine": R-AG's assertions are the only place this repo can catch
// R-AC's start-record silent-loss mode, and a CI config that stopped
// installing node/pnpm must fail loudly, not report green having run
// nothing.
func skipOrFatal(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Fatalf("%s (CI is set — the toolchain to run this test is expected to be present)", reason)
	}
	t.Skip(reason)
}

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
//
// F-9 (task-17-review.md): the group call carries `quiet: true` and a
// second checkpoint is written with a `.trim` marker beside it, so the test
// below can assert the PRESENCE of both `Group.Quiet` and `Checkpoint.Trim`
// in the manifest — Step 5 previously only ever produced quiet=false and
// Trim=false, so a manifest builder that silently dropped either field would
// have passed every assertion this test made.
func adapterE2EScript(jsDistIndex, groupName, checkpointName, trimmedCheckpointName string) string {
	// A minimal valid 1x1 transparent PNG, so image.DecodeConfig on the Go
	// side has real geometry to read.
	const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	// F-3 (task-17-review.md): a request AFTER endGroup() too, so the test
	// can assert that hop attributes to "" — not the group's name. Without
	// this second request, a dropped/zero-value `end` record is invisible:
	// DeriveGroups' closeAt(finishedAt) fallback closes the group at the
	// run's own finish time, which is always inside [StartedAt, after], so
	// an EndedAt bounds check alone cannot see it (R-AG's original gap).
	return fmt.Sprintf(`
import { group, endGroup, shotsDir } from %q;
import * as fs from 'node:fs';
import * as path from 'node:path';

const proxyUrl = process.env.RETRACE_PROXY_URL;
if (!proxyUrl) throw new Error('RETRACE_PROXY_URL is not set — retrace run did not hand off its env');

await group(%q, { quiet: true });
const during = await fetch(proxyUrl + '/cart');
await during.arrayBuffer();
await endGroup();
const after = await fetch(proxyUrl + '/after-end');
await after.arrayBuffer();

const dir = shotsDir();
if (!dir) throw new Error('shotsDir() returned null inside a run');
fs.mkdirSync(dir, { recursive: true });
fs.writeFileSync(path.join(dir, %q + '.png'), Buffer.from(%q, 'base64'));
fs.writeFileSync(path.join(dir, %q + '.png'), Buffer.from(%q, 'base64'));
fs.writeFileSync(path.join(dir, %q + '.trim'), '');
`, jsDistIndex, groupName, checkpointName, pngB64, trimmedCheckpointName, pngB64, trimmedCheckpointName)
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
		skipOrFatal(t, "node not on PATH")
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	jsPkgDir := filepath.Join(repoRoot, "adapters", "js")
	if _, err := os.Stat(jsPkgDir); err != nil {
		skipOrFatal(t, fmt.Sprintf("adapters/js not present at %s: %v", jsPkgDir, err))
	}
	if _, err := exec.LookPath("pnpm"); err != nil {
		skipOrFatal(t, "pnpm not on PATH")
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
	const trimmedCheckpointName = "cart-trimmed"
	scriptPath := filepath.Join(cwd, "adapter-e2e.mjs")
	script := adapterE2EScript(jsDistIndex, groupName, checkpointName, trimmedCheckpointName)
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
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
	// F-9 (task-17-review.md): the adapter script passes `{ quiet: true }` —
	// a manifest builder that dropped GroupRecord.Quiet on the way from the
	// JSONL record to the derived Group would pass every other assertion in
	// this test (quiet does not affect StartedAt/EndedAt/attribution at all)
	// and only this check can see it.
	if !g.Quiet {
		t.Errorf("group.quiet = false, want true — the adapter called group(%q, { quiet: true })", groupName)
	}
	// A zero-value or epoch-millis-decoded timestamp fails this bounds
	// check even though it would still produce exactly one group.
	if g.StartedAt.Before(before) || g.StartedAt.After(after) {
		t.Errorf("group.startedAt = %s, want within [%s, %s]", g.StartedAt, before, after)
	}
	if g.EndedAt.Before(g.StartedAt) || g.EndedAt.After(after) {
		t.Errorf("group.endedAt = %s, want within [%s, %s]", g.EndedAt, g.StartedAt, after)
	}

	// Attribution: the near-side hop (made between group() and endGroup())
	// must fall inside the group's derived interval — the thing groups
	// exist for, per R-AG — and the far-side hop (made AFTER endGroup())
	// must fall OUTSIDE it. The far-side check is F-3: a dropped or
	// zero-value `end` record still closes the group at the run's finish
	// time via DeriveGroups' closeAt(finishedAt) fallback, which keeps
	// EndedAt inside [StartedAt, after] and swallows this hop too — the
	// EndedAt bounds check above cannot see that defect, only this can.
	runID := m.RunID
	wirePath := filepath.Join(runs.RunsRoot(cwd), "web", "checkout", runID, "wire.jsonl")
	hops, _, err := runs.ReadHops(wirePath)
	if err != nil {
		t.Fatalf("ReadHops: %v", err)
	}
	if len(hops) != 2 {
		t.Fatalf("wire hops = %d, want 2", len(hops))
	}
	var duringHop, afterHop *trace.Hop
	for i := range hops {
		switch hops[i].Path {
		case "/cart":
			duringHop = &hops[i]
		case "/after-end":
			afterHop = &hops[i]
		}
	}
	if duringHop == nil || afterHop == nil {
		t.Fatalf("expected hops for /cart and /after-end, got %+v", hops)
	}
	if part := runs.GroupAt(m.Groups, duringHop.T.Start); part != groupName {
		t.Errorf("near-side hop attributed to group %q, want %q", part, groupName)
	}
	if part := runs.GroupAt(m.Groups, afterHop.T.Start); part != "" {
		t.Errorf("far-side hop (after endGroup) attributed to group %q, want \"\" (unattributed) — "+
			"the end record failed to close the group before this hop", part)
	}

	if len(m.Checkpoints) != 2 {
		t.Fatalf("checkpoints = %+v, want exactly two", m.Checkpoints)
	}
	var cp, trimmedCp *runs.Checkpoint
	for i := range m.Checkpoints {
		switch m.Checkpoints[i].Name {
		case checkpointName:
			cp = &m.Checkpoints[i]
		case trimmedCheckpointName:
			trimmedCp = &m.Checkpoints[i]
		}
	}
	if cp == nil || trimmedCp == nil {
		t.Fatalf("expected checkpoints named %q and %q, got %+v", checkpointName, trimmedCheckpointName, m.Checkpoints)
	}
	if cp.Trim {
		t.Errorf("checkpoint.trim = true, want false — no .trim marker was written for %q", checkpointName)
	}
	if cp.Width == 0 || cp.Height == 0 {
		t.Errorf("checkpoint geometry = %dx%d, want a decoded PNG size", cp.Width, cp.Height)
	}
	// F-9 (task-17-review.md): the test above only ever produced Trim=false,
	// the zero value — a manifest builder that always reported Trim=false
	// regardless of whether a `.trim` marker existed would still pass it.
	// This is the PRESENCE arm: trimmedCheckpointName has a real `.trim`
	// marker beside its shot, and only this check can catch a builder that
	// never looks for one.
	if !trimmedCp.Trim {
		t.Errorf("checkpoint.trim = false, want true — a %q.trim marker was written for %q", trimmedCheckpointName, trimmedCheckpointName)
	}
	if trimmedCp.Width == 0 || trimmedCp.Height == 0 {
		t.Errorf("trimmed checkpoint geometry = %dx%d, want a decoded PNG size", trimmedCp.Width, trimmedCp.Height)
	}
}
