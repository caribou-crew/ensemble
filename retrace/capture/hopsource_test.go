package capture

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/config"
)

// script writes an executable shell script in dir and returns a command that
// runs it. Scripts rather than inline shell so the test reads like the thing
// a user actually configures.
func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return "./" + name
}

func hopLine(seq int, to string) string {
	return `{"schema":"ensemble/1","seq":` + itoa(seq) + `,"to":"` + to + `","t":{}}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestTheWindowDisarmNamesIsTheOneExportDumps is the contract between the two
// commands: a tracing backend names the interval that just closed, and export
// has no other way to learn which one to read. Without the handoff every
// export would dump whatever the backend's idea of "recent" is — traffic from
// the previous run included.
func TestTheWindowDisarmNamesIsTheOneExportDumps(t *testing.T) {
	dir := t.TempDir()
	h := HopSource{
		Kind:   config.HopSourceCommand,
		Arm:    script(t, dir, "on.sh", "echo armed > armed.txt\n"),
		Disarm: script(t, dir, "off.sh", `printf '{"windowId": "w-42"}\n'`),
		Export: script(t, dir, "dump.sh", `printf '{"schema":"ensemble/1","seq":1,"to":"%s","t":{}}\n' "$RETRACE_HOP_WINDOW"`),
		Dir:    dir,
	}
	if err := h.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "armed.txt")); err != nil {
		t.Fatalf("arm did not run: %v", err)
	}
	hops, err := h.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(hops) != 1 || hops[0].To != "w-42" {
		t.Fatalf("hops = %+v, want one hop carrying the window id disarm printed", hops)
	}
}

// TestAnAbsentWindowIsUnsetRatherThanEmpty: a backend with no such identifier
// prints nothing, and export must be able to tell that apart from a window
// literally named "". Exporting the window called "" would dump nothing, or
// everything, depending on the backend — and neither failure says why.
func TestAnAbsentWindowIsUnsetRatherThanEmpty(t *testing.T) {
	dir := t.TempDir()
	h := HopSource{
		Kind:   config.HopSourceCommand,
		Disarm: script(t, dir, "off.sh", "exit 0\n"),
		Export: script(t, dir, "dump.sh",
			`printf '{"schema":"ensemble/1","seq":1,"to":"%s","t":{}}\n' "${RETRACE_HOP_WINDOW-absent}"`),
		Dir: dir,
	}
	hops, err := h.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(hops) != 1 || hops[0].To != "absent" {
		t.Fatalf("hops = %+v, want the variable unset (not set to the empty string)", hops)
	}
}

// TestADisarmThatTalksOverItsOwnContractIsRefused. A disarm that logs a line
// and then prints the object would parse under a lenient reader and hand
// export a window id from whatever the last line happened to be — the wrong
// window, exported confidently.
func TestADisarmThatTalksOverItsOwnContractIsRefused(t *testing.T) {
	dir := t.TempDir()
	h := HopSource{
		Kind:   config.HopSourceCommand,
		Disarm: script(t, dir, "off.sh", "echo 'closing window...'\nprintf '{\"windowId\": \"w-1\"}\\n'\n"),
		Export: script(t, dir, "dump.sh", "printf '"+hopLine(1, "svc")+"\\n'"),
		Dir:    dir,
	}
	_, err := h.Collect(context.Background())
	if err == nil {
		t.Fatal("a chatty disarm was accepted")
	}
	if !strings.Contains(err.Error(), "stderr") {
		t.Errorf("the refusal does not say where diagnostics belong: %v", err)
	}
}

// TestAnUnreadableExportLineFailsRatherThanShrinkingTheChain. runs.ReadHops
// skips a bad line because a run that died mid-write leaves a truncated tail.
// A command's stdout is not a casualty of anything: a line it cannot read
// means the exporter broke its own contract, and skipping it would report a
// shorter chain than the backend holds with nothing saying so.
func TestAnUnreadableExportLineFailsRatherThanShrinkingTheChain(t *testing.T) {
	dir := t.TempDir()
	h := HopSource{
		Kind:   config.HopSourceCommand,
		Export: script(t, dir, "dump.sh", "printf '"+hopLine(1, "svc")+"\\nnot json\\n'"),
		Dir:    dir,
	}
	hops, err := h.Collect(context.Background())
	if err == nil {
		t.Fatalf("a malformed line was skipped and %d hop(s) reported", len(hops))
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("the failure does not say which line: %v", err)
	}
}

func TestAFailingExportIsAnError(t *testing.T) {
	dir := t.TempDir()
	h := HopSource{
		Kind:   config.HopSourceCommand,
		Export: script(t, dir, "dump.sh", "echo 'the backend is down' >&2\nexit 7\n"),
		Dir:    dir,
	}
	if _, err := h.Collect(context.Background()); err == nil {
		t.Fatal("an export that exited 7 was read as an empty chain")
	}
}

// TestAFileSourceResolvesAgainstTheConfigNotTheProcess: the path is written
// in retrace.yaml, so it means what it means from there. Resolving against
// the process's working directory would make the same config read a different
// fixture depending on which directory the suite was started from.
func TestAFileSourceResolvesAgainstTheConfigNotTheProcess(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hops.jsonl"), []byte(hopLine(1, "cart")+"\n"+hopLine(2, "inventory")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := HopSource{Kind: config.HopSourceFile, File: "hops.jsonl", Dir: dir}
	hops, err := h.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(hops) != 2 || hops[1].To != "inventory" {
		t.Fatalf("hops = %+v, want both fixture hops", hops)
	}
}

// TestAFixtureThatIsNotThereIsARefusalNotAnEmptyChain. runs.ReadHops reads a
// missing file as an empty run, which is right for a run directory nobody
// wrote and exactly wrong for a path a config names: a typo would record a
// chain-less run that looks like a stack making no downstream calls.
func TestAFixtureThatIsNotThereIsARefusalNotAnEmptyChain(t *testing.T) {
	h := HopSource{Kind: config.HopSourceFile, File: "hops.jsonl", Dir: t.TempDir()}
	hops, err := h.Collect(context.Background())
	if err == nil {
		t.Fatalf("a missing fixture produced %d hop(s) and no error", len(hops))
	}
	if !strings.Contains(err.Error(), "hops.jsonl") {
		t.Errorf("the refusal does not name the path it looked for: %v", err)
	}
}

// TestAFixtureWithAnUnreadableLineIsRefused — the same ruling as the export
// stream, at the other source. A fixture is not a truncated casualty.
func TestAFixtureWithAnUnreadableLineIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hops.jsonl"), []byte(hopLine(1, "cart")+"\nnot json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := HopSource{Kind: config.HopSourceFile, File: "hops.jsonl", Dir: dir}
	if _, err := h.Collect(context.Background()); err == nil {
		t.Fatal("a fixture with an unreadable line was silently shortened")
	}
}

// TestHopsFromAnExternalSourceAreRedactedByRetraceItself. The producer applied
// whatever rules IT was configured with; a recording is committed and shared,
// so retrace applies its own key list to everything reaching its disk,
// whoever produced it.
func TestHopsFromAnExternalSourceAreRedactedByRetraceItself(t *testing.T) {
	cwd := t.TempDir()
	s, err := StartStandalone(Options{
		Cwd: cwd, App: "web", Flow: "checkout",
		Upstream: "http://127.0.0.1:1", Redact: destroyEntries("x-team-token"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	hop := trace.Hop{Schema: "ensemble/1", Seq: 1, To: "cart"}
	hop.Req.Headers = map[string]string{"x-team-token": "super-secret-value"}
	if err := s.RecordExternalHops([]trace.Hop{hop}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.Paths.HopsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "super-secret-value") {
		t.Errorf("an external hop reached disk unredacted:\n%s", raw)
	}
	if !s.HopsRecorded() {
		t.Error("a standalone run with a collected chain reports no hop plane")
	}
}

// TestAStandaloneRunWithNoHopSourceHasNoHopPlane is the other half: absent
// means absent. manifest.hops present-and-zero says "we looked and there was
// nothing"; nil says "nobody looked", and a standalone run with no configured
// source is the second.
func TestAStandaloneRunWithNoHopSourceHasNoHopPlane(t *testing.T) {
	s := &Session{Mode: "standalone"}
	if s.HopsRecorded() {
		t.Error("a standalone run with no hop source claims a hop plane")
	}
}

// TestAnExportedHopInTheWrongSchemaIsRefused. The schema is checked here and
// not left to the reader: runs.ReadHops silently skips a hop whose schema it
// does not know, so a chain exported in some other tracing shape would be
// written to hops.jsonl in full and read back empty — a run whose file and
// whose counts disagree, with nothing between them saying why.
func TestAnExportedHopInTheWrongSchemaIsRefused(t *testing.T) {
	dir := t.TempDir()
	h := HopSource{
		Kind: config.HopSourceCommand,
		Export: script(t, dir, "dump.sh",
			`printf '{"schema":"otel/1","seq":1,"to":"cart","t":{}}\n'`),
		Dir: dir,
	}
	hops, err := h.Collect(context.Background())
	if err == nil {
		t.Fatalf("a foreign schema was accepted: %+v", hops)
	}
	if !strings.Contains(err.Error(), "otel/1") || !strings.Contains(err.Error(), trace.SchemaVersion) {
		t.Errorf("the refusal names neither what arrived nor what is required: %v", err)
	}
}
