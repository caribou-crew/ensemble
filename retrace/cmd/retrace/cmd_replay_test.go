package main

// cmd_replay_test.go drives `retrace replay` and `retrace revalidate`
// through the BUILT binary (never `go run`, which collapses every non-zero
// child to 1 — see global-constraints.md), against a bundle produced by
// `retrace run` + `retrace ref accept` rather than a hand-built one. The
// exit codes ARE the contract here — 2 for a client deviation, 1 for
// drift, 3 for no reference — so they are asserted as real process exit
// codes.

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/retrace/diff"
	"github.com/caribou-crew/ensemble/retrace/replay"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// TestHelperReplayDeviates makes the recorded call AND one the recording
// never saw. It never inspects the status it gets back — that is the
// point: a test suite that would have "passed" is failed by retrace on
// the strength of the unmatched call alone.
func TestHelperReplayDeviates(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "replay-deviate" {
		return
	}
	proxy := os.Getenv("RETRACE_PROXY_URL")
	for _, p := range []string{"/cart", "/admin/purge"} {
		resp, err := http.Get(proxy + p)
		if err != nil {
			fmt.Fprintln(os.Stderr, "helper fetch:", err)
			os.Exit(9)
		}
		resp.Body.Close()
	}
	os.Exit(0)
}

// TestHelperReplayChecksHandshakeEnv asserts, inside the replayed test
// command, that replay exports the same three variables `retrace run`
// does and that the marker door behind RETRACE_MARKER_URL is live.
func TestHelperReplayChecksHandshakeEnv(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "replay-env" {
		return
	}
	for _, k := range []string{"RETRACE_RUN_DIR", "RETRACE_PROXY_URL", "RETRACE_MARKER_URL"} {
		if os.Getenv(k) == "" {
			fmt.Fprintln(os.Stderr, "helper: "+k+" is not set")
			os.Exit(9)
		}
	}
	resp, err := http.Post(os.Getenv("RETRACE_MARKER_URL")+"/group", "application/json",
		strings.NewReader(`{"name":"login"}`))
	if err != nil || resp.StatusCode != http.StatusNoContent {
		fmt.Fprintln(os.Stderr, "helper marker door:", err, resp)
		os.Exit(9)
	}
	resp.Body.Close()
	// And the recorded call, so the replay itself is clean.
	got, err := http.Get(os.Getenv("RETRACE_PROXY_URL") + "/cart")
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper fetch:", err)
		os.Exit(9)
	}
	got.Body.Close()
	os.Exit(0)
}

// TestHelperMakesNoCalls is the app that never talks to the replay server:
// a hard-coded base URL that ignores RETRACE_PROXY_URL, a runner that
// skipped its suite, a `--` command that exits early. It exits 0, exactly
// as a green suite does.
func TestHelperMakesNoCalls(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "no-calls" {
		return
	}
	os.Exit(0)
}

// TestHelperFetchesFromEnv issues the one call named by RETRACE_TEST_CALL
// ("METHOD path[?query] [body]"). The recording leg and the replay leg run
// the SAME helper with different values, so the two calls differ only in
// the thing retrace.yaml is supposed to make irrelevant.
func TestHelperFetchesFromEnv(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "call-from-env" {
		return
	}
	spec := strings.SplitN(os.Getenv("RETRACE_TEST_CALL"), " ", 3)
	if len(spec) < 2 {
		fmt.Fprintln(os.Stderr, "helper: RETRACE_TEST_CALL must be \"METHOD path [body]\"")
		os.Exit(9)
	}
	var body io.Reader
	if len(spec) == 3 {
		body = strings.NewReader(spec[2])
	}
	req, err := http.NewRequest(spec[0], os.Getenv("RETRACE_PROXY_URL")+spec[1], body)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper request:", err)
		os.Exit(9)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper fetch:", err)
		os.Exit(9)
	}
	resp.Body.Close()
	os.Exit(0)
}

// recordAndAccept records one run of web/checkout against upstream and
// promotes it into the committed reference bundle.
func recordAndAccept(t *testing.T, bin, cwd, upstreamURL string) {
	t.Helper()
	runOnce(t, bin, cwd, "web", "checkout", upstreamURL)
	acc := runRetrace(t, bin, cwd, "", "ref", "accept", "--flow", "checkout", "--app", "web")
	if acc.code != 0 {
		t.Fatalf("ref accept: exit = %d\nstdout: %s\nstderr: %s", acc.code, acc.stdout, acc.stderr)
	}
}

func TestReplayExitsTwoAndReportsTheUnmatchedRequest(t *testing.T) {
	// The spec's "client deviation caught in CI" scenario, end to end
	// through a real HTTP client.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	recordAndAccept(t, bin, cwd, upstream.URL)

	args := append([]string{"replay", "--ref", "checkout", "--app", "web"},
		selfCmd(t, "TestHelperReplayDeviates")...)
	res := runRetrace(t, bin, cwd, "replay-deviate", args...)
	if res.code != exitGate {
		t.Fatalf("exit = %d, want %d — the test command exited 0, so only the unmatched call can fail this\nstdout: %s\nstderr: %s",
			res.code, exitGate, res.stdout, res.stderr)
	}
	out := res.stdout + res.stderr
	if !strings.Contains(out, "/admin/purge") {
		t.Fatalf("the report never names the unmatched call:\n%s", out)
	}
	if !strings.Contains(out, "/cart") {
		t.Fatalf("the report never names the nearest recorded exchange:\n%s", out)
	}

	// And the miss is durable, in the replay run directory.
	dir := newestReplayRunDir(t, cwd, "web", "checkout")
	b, err := os.ReadFile(filepath.Join(dir, "misses.jsonl"))
	if err != nil {
		t.Fatalf("misses.jsonl was not written to %s: %v", dir, err)
	}
	var m replay.Miss
	if err := json.Unmarshal([]byte(strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)[0]), &m); err != nil {
		t.Fatalf("misses.jsonl line is not a Miss: %v\n%s", err, b)
	}
	if m.Path != "/admin/purge" {
		t.Fatalf("misses.jsonl records %q, want /admin/purge", m.Path)
	}
}

func TestReplayExitsZeroWhenEveryCallMatched(t *testing.T) {
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	recordAndAccept(t, bin, cwd, upstream.URL)
	recorded := hits.Load()

	args := append([]string{"replay", "--ref", "checkout", "--app", "web"},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	// The whole point of replay: the live stack was never involved. Without
	// this, a replay that quietly proxied would pass this test.
	if hits.Load() != recorded {
		t.Fatalf("the upstream saw %d call(s) during replay — replay must answer from the bundle, never from the live stack", hits.Load()-recorded)
	}
}

func TestReplayExportsTheSameHandshakeEnvAsRun(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	recordAndAccept(t, bin, cwd, upstream.URL)

	args := append([]string{"replay", "--ref", "checkout", "--app", "web"},
		selfCmd(t, "TestHelperReplayChecksHandshakeEnv")...)
	res := runRetrace(t, bin, cwd, "replay-env", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0 — the helper exits 9 when a handshake variable is missing\nstdout: %s\nstderr: %s",
			res.code, res.stdout, res.stderr)
	}
	dir := newestReplayRunDir(t, cwd, "web", "checkout")
	if _, err := os.Stat(filepath.Join(dir, "groups.jsonl")); err != nil {
		t.Fatalf("the marker the helper posted never reached the run directory: %v", err)
	}
}

// TestHelperReplayCallsCartFiveTimes calls the SAME recorded exchange five
// times, where the reference (recorded via TestHelperFetchesThroughProxy)
// called it once. Every call still matches under Match's subset rule (a
// repeated identical call is served, deliberately, so a poll-until-ready
// flow does not hang) — so this is invisible to plain `retrace replay`, and
// is exactly the call-count-drift scenario --assert-requests exists to
// catch.
func TestHelperReplayCallsCartFiveTimes(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "replay-callcount" {
		return
	}
	proxy := os.Getenv("RETRACE_PROXY_URL")
	for i := 0; i < 5; i++ {
		resp, err := http.Get(proxy + "/cart")
		if err != nil {
			fmt.Fprintln(os.Stderr, "helper fetch:", err)
			os.Exit(9)
		}
		resp.Body.Close()
	}
	os.Exit(0)
}

// TestHelperReplaySendsAnExtraHeader makes the ONE recorded call, but with a
// header the reference never carried. The call still matches (Key is
// method+path+query only, and Match never diffs headers at all) — again
// invisible to plain `retrace replay`.
func TestHelperReplaySendsAnExtraHeader(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "replay-extra-header" {
		return
	}
	req, err := http.NewRequest(http.MethodGet, os.Getenv("RETRACE_PROXY_URL")+"/cart", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper request:", err)
		os.Exit(9)
	}
	req.Header.Set("X-New-Client-Attribute", "v2")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper fetch:", err)
		os.Exit(9)
	}
	resp.Body.Close()
	os.Exit(0)
}

// TestHelperReplaySendsASecret sends a request carrying a secret header the
// config redacts, so the persisted wire can be checked for the redaction.
func TestHelperReplaySendsASecret(t *testing.T) {
	if os.Getenv("RETRACE_TEST_HELPER") != "replay-secret" {
		return
	}
	req, _ := http.NewRequest(http.MethodGet, os.Getenv("RETRACE_PROXY_URL")+"/cart", nil)
	req.Header.Set("X-Secret-Token", "super-secret-value-123")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper fetch:", err)
		os.Exit(9)
	}
	resp.Body.Close()
	os.Exit(0)
}

// TestReplayPersistsRedactedWire pins that the observed wire replay persists
// as wire.jsonl (what makes a shots-less run syncable) never contains a
// secret the config marks for redaction — the file is synced and committed
// as a reference, so a raw secret there is a leak.
func TestReplayPersistsRedactedWire(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	// Encrypt-mode rules need a team key at record time; a throwaway 32-byte
	// hex key satisfies it (subprocesses inherit the parent env).
	t.Setenv("RETRACE_RECORDING_KEY", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	// redact the secret header (key rule); the helper sends it live on replay.
	// The config mixes a MASK rule (x-secret-token) with an ENCRYPT rule
	// (pan) — the real-world shape. The observed-wire redactor must build
	// with a nil data key (replay holds none), which it can only do by
	// dropping the encrypt rule; a regression here (building with the encrypt
	// rule) fails redactor construction and NO wire.jsonl is persisted,
	// making the run unsyncable. So this pins both: wire IS persisted, and
	// the mask secret is redacted.
	writeConfig(t, cwd, "app: web\nredact:\n  - x-secret-token\n  - field: pan\n    mode: encrypt\n    why: test\nwire_rules:\n  - headers:\n      date: http-date\n")
	recordAndAccept(t, bin, cwd, upstream.URL)

	args := append([]string{"replay", "--ref", "checkout", "--app", "web"},
		selfCmd(t, "TestHelperReplaySendsASecret")...)
	res := runRetrace(t, bin, cwd, "replay-secret", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", res.code, res.stderr)
	}
	dir := newestReplayRunDir(t, cwd, "web", "checkout")
	wire, err := os.ReadFile(filepath.Join(dir, "wire.jsonl"))
	if err != nil {
		t.Fatalf("no persisted wire.jsonl: %v", err)
	}
	if strings.Contains(string(wire), "super-secret-value-123") {
		t.Fatalf("the persisted wire.jsonl leaked the secret header value:\n%s", wire)
	}
}

// assertRequestsReport is the subset of replayReport this file's
// --assert-requests tests decode, mirroring TestReplayJsonReportsEveryMissAndTheTestExitCode's
// pattern of decoding only the fields under test.
type assertRequestsReport struct {
	MissCount   int         `json:"missCount"`
	Extra       []diff.Call `json:"extra"`
	RequestDiff *struct {
		Paired    int     `json:"paired"`
		Changed   int     `json:"changed"`
		BudgetPct float64 `json:"budgetPct"`
	} `json:"requestDiff"`
}

func TestReplayAssertRequestsCatchesCallCountDrift(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	recordAndAccept(t, bin, cwd, upstream.URL) // records ONE call to /cart

	args := append([]string{"replay", "--ref", "checkout", "--app", "web", "--assert-requests", "--json"},
		selfCmd(t, "TestHelperReplayCallsCartFiveTimes")...)
	res := runRetrace(t, bin, cwd, "replay-callcount", args...)
	if res.code != exitGate {
		t.Fatalf("exit = %d, want %d — 5 calls against a 1-call reference is a deviation --assert-requests must catch\nstdout: %s\nstderr: %s",
			res.code, exitGate, res.stdout, res.stderr)
	}
	var doc assertRequestsReport
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("--json stdout is not one JSON document: %v\n%s", err, res.stdout)
	}
	if doc.MissCount != 0 {
		t.Fatalf("missCount = %d, want 0 — every call DID match a recorded exchange; this is a surplus, not a miss", doc.MissCount)
	}
	if len(doc.Extra) != 4 {
		t.Fatalf("extra = %+v, want 4 surplus calls (5 made, 1 recorded)", doc.Extra)
	}
	for _, c := range doc.Extra {
		if c.Method != "GET" || c.Path != "/cart" {
			t.Fatalf("extra call = %+v, want GET /cart", c)
		}
	}
}

func TestReplayAssertRequestsCatchesANewRequestHeader(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	recordAndAccept(t, bin, cwd, upstream.URL)

	args := append([]string{"replay", "--ref", "checkout", "--app", "web", "--assert-requests", "--json"},
		selfCmd(t, "TestHelperReplaySendsAnExtraHeader")...)
	res := runRetrace(t, bin, cwd, "replay-extra-header", args...)
	if res.code != exitGate {
		t.Fatalf("exit = %d, want %d — a header the reference never recorded is a deviation --assert-requests must catch\nstdout: %s\nstderr: %s",
			res.code, exitGate, res.stdout, res.stderr)
	}
	var doc assertRequestsReport
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("--json stdout is not one JSON document: %v\n%s", err, res.stdout)
	}
	if doc.MissCount != 0 {
		t.Fatalf("missCount = %d, want 0 — the call matched; only the header is new", doc.MissCount)
	}
	if len(doc.Extra) != 0 {
		t.Fatalf("extra = %+v, want none — this is a header deviation on a paired call, not a surplus call", doc.Extra)
	}
	if doc.RequestDiff == nil || doc.RequestDiff.Changed != 1 || doc.RequestDiff.Paired != 1 {
		t.Fatalf("requestDiff = %+v, want exactly 1 paired call flagged changed", doc.RequestDiff)
	}
	// Lowercase: DiffHeaders compares case-insensitively and reports the
	// lowered name, the same convention every other header diff in this
	// repo already follows.
	if !strings.Contains(res.stdout+res.stderr, "x-new-client-attribute") {
		t.Fatalf("the report never names the new header:\n%s\n%s", res.stdout, res.stderr)
	}
}

func TestReplayAssertRequestsExitsZeroOnACleanMatch(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	recordAndAccept(t, bin, cwd, upstream.URL)

	args := append([]string{"replay", "--ref", "checkout", "--app", "web", "--assert-requests", "--json"},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0 — the replayed request matches the reference exactly\nstdout: %s\nstderr: %s",
			res.code, res.stdout, res.stderr)
	}
	var doc assertRequestsReport
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("--json stdout is not one JSON document: %v\n%s", err, res.stdout)
	}
	if len(doc.Extra) != 0 {
		t.Fatalf("extra = %+v, want an empty (not absent) array on a clean --assert-requests run", doc.Extra)
	}
	if doc.RequestDiff == nil || doc.RequestDiff.Changed != 0 {
		t.Fatalf("requestDiff = %+v, want changed == 0", doc.RequestDiff)
	}
}

// TestReplayWithoutAssertRequestsIsUnaffectedByCallCountDrift is the
// backward-compat pin: the exact scenario the tests above fail on must
// still be a clean, exit-0 replay when the flag is not passed.
func TestReplayWithoutAssertRequestsIsUnaffectedByCallCountDrift(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	recordAndAccept(t, bin, cwd, upstream.URL)

	args := append([]string{"replay", "--ref", "checkout", "--app", "web", "--json"},
		selfCmd(t, "TestHelperReplayCallsCartFiveTimes")...)
	res := runRetrace(t, bin, cwd, "replay-callcount", args...)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0 — without --assert-requests, 5 hits on one recorded exchange is a plain pass\nstdout: %s\nstderr: %s",
			res.code, res.stdout, res.stderr)
	}
	if strings.Contains(res.stdout, `"extra"`) || strings.Contains(res.stdout, `"requestDiff"`) {
		t.Fatalf("--json carries extra/requestDiff without --assert-requests having been passed:\n%s", res.stdout)
	}
}

func TestReplayWithoutAReferenceExplainsHowToCreateOne(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)

	args := append([]string{"replay", "--ref", "checkout", "--app", "web"},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	res := runRetrace(t, bin, cwd, "fetch", args...)
	// 3, could-not-evaluate — NEVER 0. "No reference" means nothing was
	// checked, which is not the same as "nothing deviated".
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", res.code, exitUsage, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "retrace ref accept") {
		t.Fatalf("stderr does not name the verb that fixes this:\n%s", res.stderr)
	}
	if !strings.Contains(res.stderr, "no runs captured") {
		t.Fatalf("stderr does not carry the resolver's reason:\n%s", res.stderr)
	}
}

func TestReplayJsonReportsEveryMissAndTheTestExitCode(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	recordAndAccept(t, bin, cwd, upstream.URL)

	args := append([]string{"replay", "--ref", "checkout", "--app", "web", "--json"},
		selfCmd(t, "TestHelperReplayDeviates")...)
	res := runRetrace(t, bin, cwd, "replay-deviate", args...)
	if res.code != exitGate {
		t.Fatalf("exit = %d, want %d\n%s\n%s", res.code, exitGate, res.stdout, res.stderr)
	}
	var doc struct {
		App       string        `json:"app"`
		Flow      string        `json:"flow"`
		MissCount int           `json:"missCount"`
		Misses    []replay.Miss `json:"misses"`
		Ref       struct {
			Kind string `json:"kind"`
		} `json:"ref"`
		Test struct {
			ExitCode int `json:"exitCode"`
		} `json:"test"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("--json stdout is not one JSON document: %v\n%s", err, res.stdout)
	}
	if doc.Ref.Kind != "bundle" {
		t.Fatalf("ref.kind = %q, want \"bundle\"", doc.Ref.Kind)
	}
	if doc.MissCount != 1 || len(doc.Misses) != 1 || doc.Misses[0].Path != "/admin/purge" {
		t.Fatalf("misses = %+v (count %d)", doc.Misses, doc.MissCount)
	}
	// The test command's own code is still reported even though the exit
	// status is retrace's: "the tests passed and the client deviated" is
	// two facts, not one.
	if doc.Test.ExitCode != 0 {
		t.Fatalf("test.exitCode = %d, want 0", doc.Test.ExitCode)
	}
	if doc.App != "web" || doc.Flow != "checkout" {
		t.Fatalf("app/flow = %q/%q", doc.App, doc.Flow)
	}
}

func TestRevalidateReportsDriftAgainstTheLiveStackAndExitsOne(t *testing.T) {
	var moved atomic.Bool
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if moved.Load() {
			w.Write([]byte(`{"total":199,"currency":"EUR"}`))
			return
		}
		w.Write([]byte(`{"total":199,"currency":"USD"}`))
	}))
	defer live.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	recordAndAccept(t, bin, cwd, live.URL)

	// Clean first: the recording still holds, so exit 0. Without this arm
	// the drift arm below could pass on a revalidate that flags anything.
	clean := runRetrace(t, bin, cwd, "", "revalidate", "--ref", "checkout", "--app", "web", "--upstream", live.URL)
	if clean.code != 0 {
		t.Fatalf("revalidate against an unchanged stack: exit = %d, want 0\nstdout: %s\nstderr: %s", clean.code, clean.stdout, clean.stderr)
	}

	moved.Store(true)
	res := runRetrace(t, bin, cwd, "", "revalidate", "--ref", "checkout", "--app", "web", "--upstream", live.URL, "--json")
	if res.code != exitDiff {
		t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", res.code, exitDiff, res.stdout, res.stderr)
	}
	var rep replay.RevalReport
	if err := json.Unmarshal([]byte(res.stdout), &rep); err != nil {
		t.Fatalf("--json stdout is not a RevalReport: %v\n%s", err, res.stdout)
	}
	if rep.Verdict != replay.VerdictDrift || rep.Checked != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if len(rep.Drifts) != 1 || len(rep.Drifts[0].Fields) != 1 || rep.Drifts[0].Fields[0].Path != "currency" {
		t.Fatalf("drifts = %+v, want the currency field named", rep.Drifts)
	}
}

func TestRevalidateWithoutAReferenceExplainsHowToCreateOne(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	res := runRetrace(t, bin, cwd, "", "revalidate", "--ref", "checkout", "--app", "web", "--upstream", "http://127.0.0.1:1")
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", res.code, exitUsage, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "retrace ref accept") {
		t.Fatalf("stderr does not name the verb that fixes this:\n%s", res.stderr)
	}
}

// newestReplayRunDir returns the replay run directory `retrace replay`
// created. Replays live under their OWN root, never the runs root: a
// replay is not a recording, and a directory with no manifest sitting in
// the runs root would become what `--b latest` resolves to.
func newestReplayRunDir(t *testing.T, cwd, app, flow string) string {
	t.Helper()
	root := replaysRoot(cwd)
	ids := runs.ListRuns(root, app, flow)
	if len(ids) == 0 {
		t.Fatalf("no replay run directory under %s/%s/%s", root, app, flow)
	}
	p, err := runs.PathsFor(root, app, flow, ids[len(ids)-1])
	if err != nil {
		t.Fatal(err)
	}
	return p.RunDir
}

// TestReplayRunDirsNeverPolluteTheRunsRoot pins that separation: a replay
// must not add a manifest-less directory to the runs root, where
// `retrace diff --b latest` would resolve to it.
func TestReplayRunDirsNeverPolluteTheRunsRoot(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	recordAndAccept(t, bin, cwd, upstream.URL)
	before := runs.ListRuns(runs.RunsRoot(cwd), "web", "checkout")

	args := append([]string{"replay", "--ref", "checkout", "--app", "web"},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	if res := runRetrace(t, bin, cwd, "fetch", args...); res.code != 0 {
		t.Fatalf("replay: exit = %d\n%s\n%s", res.code, res.stdout, res.stderr)
	}
	after := runs.ListRuns(runs.RunsRoot(cwd), "web", "checkout")
	if len(after) != len(before) {
		t.Fatalf("the runs root gained %d directory/ies during a replay: %v -> %v", len(after)-len(before), before, after)
	}
}

func TestAReplayInWhichTheClientCalledNothingIsNotAPass(t *testing.T) {
	// F1. `retrace replay` gated only on the miss count, so a run in which
	// the client made ZERO calls — a hard-coded base URL that ignores
	// RETRACE_PROXY_URL, a harness reading the wrong env var, a suite that
	// was skipped — exited 0 and printed "every call matched the
	// recording". Two different worlds (everything matched / nothing was
	// asked) produced an identical verdict, on the one product whose whole
	// value is that absence is never agreement.
	//
	// Exit 3, not 2: a miss means the recording and reality disagree,
	// which is a finding; zero served means nothing was compared, which is
	// the absence of one. `revalidate` already separates those.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	recordAndAccept(t, bin, cwd, upstream.URL)

	args := append([]string{"replay", "--ref", "checkout", "--app", "web"},
		selfCmd(t, "TestHelperMakesNoCalls")...)
	res := runRetrace(t, bin, cwd, "no-calls", args...)
	if res.code != exitUsage {
		t.Fatalf("exit = %d, want %d (could not evaluate) — the test command exited 0 without calling anything, and that is not a pass\nstdout: %s\nstderr: %s",
			res.code, exitUsage, res.stdout, res.stderr)
	}
	out := res.stdout + res.stderr
	if strings.Contains(out, "every call matched") {
		t.Fatalf("the report affirmatively states the recording was honoured, over a run in which nothing was asked:\n%s", out)
	}
	if !strings.Contains(out, "never called: GET /cart") {
		t.Fatalf("the report does not name the recorded exchange nothing ever called:\n%s", out)
	}

	// The same fact in the --json CI contract, which had no field able to
	// express it at all.
	jsonArgs := append([]string{"replay", "--ref", "checkout", "--app", "web", "--json"},
		selfCmd(t, "TestHelperMakesNoCalls")...)
	jres := runRetrace(t, bin, cwd, "no-calls", jsonArgs...)
	if jres.code != exitUsage {
		t.Fatalf("--json exit = %d, want %d\n%s\n%s", jres.code, exitUsage, jres.stdout, jres.stderr)
	}
	var doc struct {
		Exchanges int           `json:"exchanges"`
		Served    int           `json:"served"`
		Unused    []replay.Key  `json:"unused"`
		MissCount int           `json:"missCount"`
		Misses    []replay.Miss `json:"misses"`
	}
	if err := json.Unmarshal([]byte(jres.stdout), &doc); err != nil {
		t.Fatalf("--json stdout is not one JSON document: %v\n%s", err, jres.stdout)
	}
	if doc.Served != 0 {
		t.Fatalf("served = %d, want 0", doc.Served)
	}
	if len(doc.Unused) != 1 || doc.Unused[0].Path != "/cart" {
		t.Fatalf("unused = %+v, want the recorded GET /cart", doc.Unused)
	}
	// missCount alone is the reason this was invisible: it is 0 here and 0
	// on a genuinely clean replay.
	if doc.MissCount != 0 {
		t.Fatalf("missCount = %d, want 0 — this run is not a miss, it is an absence", doc.MissCount)
	}

	// The mirror, so "zero served" cannot be satisfied by never serving
	// anything: the same bundle, a command that DOES call it, exits 0 and
	// reports what it served.
	okArgs := append([]string{"replay", "--ref", "checkout", "--app", "web", "--json"},
		selfCmd(t, "TestHelperFetchesThroughProxy")...)
	ok := runRetrace(t, bin, cwd, "fetch", okArgs...)
	if ok.code != 0 {
		t.Fatalf("a replay that DID call the bundle: exit = %d, want 0\n%s\n%s", ok.code, ok.stdout, ok.stderr)
	}
	doc.Unused = nil
	if err := json.Unmarshal([]byte(ok.stdout), &doc); err != nil {
		t.Fatalf("--json stdout is not one JSON document: %v\n%s", err, ok.stdout)
	}
	if doc.Served != 1 || len(doc.Unused) != 0 {
		t.Fatalf("served = %d, unused = %+v, want 1 served and nothing unused", doc.Served, doc.Unused)
	}
}

func TestListenRefusesANonLoopbackAddressBeforeItBinds(t *testing.T) {
	// R-I. The flag's help has always said "loopback only" and nothing
	// enforced it: `--listen 0.0.0.0:9000` bound, then 403'd every request
	// with an httpguard DNS-rebinding message that has nothing to do with
	// what the operator did. A flag must not describe a guarantee that is
	// not made, and a replay server answers with recorded traffic — bodies
	// and headers lifted verbatim out of a bundle.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	recordAndAccept(t, bin, cwd, upstream.URL)

	port := freePort(t)
	before := len(runs.ListRuns(replaysRoot(cwd), "web", "checkout"))

	// One body, called from every case below — including the one that may
	// have to skip, which is why the skip cannot live out here.
	refuses := func(t *testing.T, addr string) {
		t.Helper()
		args := append([]string{"replay", "--ref", "checkout", "--app", "web", "--listen", addr},
			selfCmd(t, "TestHelperFetchesThroughProxy")...)
		res := runRetrace(t, bin, cwd, "fetch", args...)
		if res.code == 0 {
			t.Fatalf("--listen %s was accepted\nstdout: %s\nstderr: %s", addr, res.stdout, res.stderr)
		}
		if !strings.Contains(res.stderr, addr) || !strings.Contains(res.stderr, "loopback") {
			t.Fatalf("stderr does not name the address and why it was refused:\n%s", res.stderr)
		}
		if !strings.Contains(res.stderr, "ssh -L") {
			t.Fatalf("stderr does not point at the way to reach it from another host:\n%s", res.stderr)
		}
		// NO LISTENER WAS OPENED. Two independent facts: nothing is
		// accepting on the address that was asked for, and the refusal
		// landed before the run directory is created — which is before
		// net.Listen, and before the test command runs.
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, time.Second)
		if err == nil {
			conn.Close()
			t.Fatalf("something is accepting on port %s after the refusal", port)
		}
		if got := len(runs.ListRuns(replaysRoot(cwd), "web", "checkout")); got != before {
			t.Fatalf("a replay run directory was created (%d -> %d) — the refusal came after the server was set up", before, got)
		}
	}

	for _, addr := range []string{"0.0.0.0:" + port, ":" + port} {
		t.Run(addr, func(t *testing.T) { refuses(t, addr) })
	}

	// The real-interface case is its own subtest, and hostIP is called
	// INSIDE it. It used to sit in the range expression above — which the
	// parent goroutine evaluates before the loop, so hostIP's t.Skip
	// Goexit'd the PARENT: on a machine with no non-loopback IPv4 (a
	// hermetic CI container) not one subtest ran, the 0.0.0.0 and :port
	// cases and the over-refusal mirror below all vanished, and the suite
	// reported PASS. A guard that silently removes unrelated assertions is
	// worse than the missing interface it guards against.
	t.Run("a real non-loopback interface", func(t *testing.T) {
		refuses(t, hostIP(t)+":"+port)
	})

	// The over-refusal mirror, which is not optional: the ordinary case
	// still binds AND SERVES. A refusal that swallowed 127.0.0.1 would
	// satisfy every assertion above.
	for _, addr := range []string{"127.0.0.1:0", "localhost:0", "[::1]:0"} {
		t.Run("still binds "+addr, func(t *testing.T) {
			args := append([]string{"replay", "--ref", "checkout", "--app", "web", "--listen", addr},
				selfCmd(t, "TestHelperFetchesThroughProxy")...)
			res := runRetrace(t, bin, cwd, "fetch", args...)
			if res.code != 0 {
				t.Fatalf("--listen %s: exit = %d, want 0 — the helper fetches the recorded call, so this only passes if the server bound and answered\nstdout: %s\nstderr: %s",
					addr, res.code, res.stdout, res.stderr)
			}
			if !strings.Contains(res.stdout, "every call matched") {
				t.Fatalf("--listen %s did not serve the recorded call:\n%s", addr, res.stdout)
			}
		})
	}
}

// freePort returns a port nothing is listening on, by binding and
// releasing it.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	return port
}

// hostIP is a non-loopback address this machine actually has, so the
// refusal is exercised against a real interface and not only against the
// wildcard.
func hostIP(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() || n.IP.To4() == nil {
			continue
		}
		return n.IP.String()
	}
	t.Skip("no non-loopback IPv4 interface on this machine")
	return ""
}

// replayCall runs one leg of TestHelperFetchesFromEnv.
func replayCall(t *testing.T, bin, cwd, call string, args ...string) runResult {
	t.Helper()
	t.Setenv("RETRACE_TEST_CALL", call)
	return runRetrace(t, bin, cwd, "call-from-env", args...)
}

// recordAndAcceptCall records ONE call of web/checkout and promotes it.
func recordAndAcceptCall(t *testing.T, bin, cwd, upstreamURL, call string) {
	t.Helper()
	settlePastRunIDResolution()
	args := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstreamURL},
		selfCmd(t, "TestHelperFetchesFromEnv")...)
	if res := replayCall(t, bin, cwd, call, args...); res.code != 0 {
		t.Fatalf("retrace run: exit = %d\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	acc := runRetrace(t, bin, cwd, "", "ref", "accept", "--flow", "checkout", "--app", "web")
	if acc.code != 0 {
		t.Fatalf("ref accept: exit = %d\nstdout: %s\nstderr: %s", acc.code, acc.stdout, acc.stderr)
	}
}

func TestRetraceYamlDecidesWhatReplayMatches(t *testing.T) {
	// F4 and R-J. replayOptions is the ONLY production constructor of
	// replay.Options, and it could be replaced with `replay.Options{}` —
	// no wire rules, no path normalization — with the whole suite green,
	// because every test of those keys hand-built an Options. A test whose
	// input production can never construct is a test of a hypothetical.
	//
	// So each case below drives config -> CLI -> match outcome through the
	// built binary: the replayed call DIFFERS from the recorded one in
	// exactly the dimension the config declares irrelevant, and the only
	// thing that can make it a hit is retrace.yaml reaching the matcher.
	// One key per case, so a mutation to one is not masked by another.
	for _, c := range []struct {
		name, config, recorded, replayed string
	}{
		{
			name:     "wire_rules",
			config:   dateRuleConfig + "  - body:\n      token: uuid\n",
			recorded: `POST /orders {"token":"2f1c2a54-0d3e-4b7a-9c1f-8e5d6a7b0c11","qty":1}`,
			replayed: `POST /orders {"token":"9b8a7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c66","qty":1}`,
		},
		{
			name:     "path_normalize",
			config:   dateRuleConfig + "path_normalize:\n  - pattern: \"/orders/[0-9]+\"\n    replacement: \"/orders/:id\"\n",
			recorded: "GET /orders/42",
			replayed: "GET /orders/99",
		},
		{
			name:     "query_ignore",
			config:   dateRuleConfig + "query_ignore:\n  - t\n",
			recorded: "GET /cart?t=1730000000",
			replayed: "GET /cart?t=1730000999",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"items":[]}`))
			}))
			defer upstream.Close()

			bin := buildRetrace(t)
			cwd := t.TempDir()
			writeConfig(t, cwd, c.config)
			recordAndAcceptCall(t, bin, cwd, upstream.URL, c.recorded)

			args := append([]string{"replay", "--ref", "checkout", "--app", "web", "--json"},
				selfCmd(t, "TestHelperFetchesFromEnv")...)
			res := replayCall(t, bin, cwd, c.replayed, args...)
			if res.code != 0 {
				t.Fatalf("replaying %q against a recording of %q: exit = %d, want 0 — %s from retrace.yaml never reached the matcher\nstdout: %s\nstderr: %s",
					c.replayed, c.recorded, res.code, c.name, res.stdout, res.stderr)
			}
			var doc struct {
				Served    int `json:"served"`
				MissCount int `json:"missCount"`
			}
			if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
				t.Fatalf("--json stdout is not one JSON document: %v\n%s", err, res.stdout)
			}
			// Exit 0 alone is not enough: a replay that served nothing now
			// exits 3, but a future one that stopped counting would not.
			if doc.Served != 1 || doc.MissCount != 0 {
				t.Fatalf("served = %d, missCount = %d, want the differing call served from the bundle", doc.Served, doc.MissCount)
			}

			// The mirror: a call that differs in a dimension the config
			// says NOTHING about is still a miss, so the pass above is the
			// config doing work rather than the matcher having gone loose.
			deviant := append([]string{"replay", "--ref", "checkout", "--app", "web"},
				selfCmd(t, "TestHelperFetchesFromEnv")...)
			dres := replayCall(t, bin, cwd, "GET /never-recorded", deviant...)
			if dres.code != exitGate {
				t.Fatalf("an unrecorded call: exit = %d, want %d\nstdout: %s\nstderr: %s", dres.code, exitGate, dres.stdout, dres.stderr)
			}
		})
	}
}
