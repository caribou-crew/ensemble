package orchestrator

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// gitRepo builds a throwaway repository with one commit. Every test here
// needs a real one: the fingerprint's whole job is to notice states of a
// working tree that no faked git output would reproduce faithfully.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "retrace test"},
		{"config", "commit.gpgsign", "false"},
	} {
		run(t, dir, args...)
	}
	write(t, filepath.Join(dir, "main.go"), "package main\n")
	run(t, dir, "add", "main.go")
	run(t, dir, "commit", "-qm", "first")
	return dir
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestACleanRepoFingerprintsAsItsCommit(t *testing.T) {
	dir := gitRepo(t)
	got := serviceVersion(context.Background(), "", dir)
	if got == "" {
		t.Fatal("a clean git checkout produced no fingerprint")
	}
	if strings.Contains(got, "+") {
		t.Errorf("fingerprint %q claims uncommitted changes in a clean tree", got)
	}
	// Stability is the property that matters: two runs of an untouched stack
	// must agree, or every diff reports the backend as changed.
	if again := serviceVersion(context.Background(), "", dir); again != got {
		t.Errorf("fingerprint is not stable: %q then %q", got, again)
	}
}

func TestTwoDifferentUncommittedEditsFingerprintDifferently(t *testing.T) {
	// The false negative the dirty digest exists to prevent. A bare HEAD sha
	// reports these as the same stack, and editing without committing is the
	// normal state of the machine retrace runs on — so the plain-sha version
	// of this feature would be wrong most of the time it was consulted.
	dir := gitRepo(t)
	clean := serviceVersion(context.Background(), "", dir)

	write(t, filepath.Join(dir, "main.go"), "package main // one\n")
	first := serviceVersion(context.Background(), "", dir)

	write(t, filepath.Join(dir, "main.go"), "package main // two\n")
	second := serviceVersion(context.Background(), "", dir)

	if first == clean {
		t.Error("an uncommitted edit did not change the fingerprint")
	}
	if first == second {
		t.Errorf("two different edits to the same file share a fingerprint: %q", first)
	}
	if !strings.HasPrefix(first, strings.Split(clean, "+")[0]) {
		t.Errorf("a dirty fingerprint %q dropped the commit %q it is based on", first, clean)
	}
}

func TestAnUntrackedFileChangesTheFingerprint(t *testing.T) {
	// `git diff HEAD` never shows an untracked file, so the digest covers the
	// porcelain listing too. A new file is exactly how a service grows an
	// endpoint that changes what the wire diff sees.
	dir := gitRepo(t)
	clean := serviceVersion(context.Background(), "", dir)
	write(t, filepath.Join(dir, "extra.go"), "package main\n")
	if dirty := serviceVersion(context.Background(), "", dir); dirty == clean {
		t.Errorf("an untracked file left the fingerprint at %q", clean)
	}
}

func TestADirectoryThatIsNotARepoHasNoFingerprint(t *testing.T) {
	// Absence, not a fabricated value. Retrace reads "" as "no evidence about
	// this service" and says nothing about it, which is the honest report.
	if got := serviceVersion(context.Background(), "", t.TempDir()); got != "" {
		t.Errorf("fingerprint = %q for a plain directory, want empty", got)
	}
}

func TestAConfiguredCommandWinsOverGit(t *testing.T) {
	dir := gitRepo(t)
	got := serviceVersion(context.Background(), "echo build-4711", dir)
	if got != "build-4711" {
		t.Errorf("fingerprint = %q, want the command's output — a repo must not override an explicit version:", got)
	}
}

func TestAConfiguredCommandRunsInTheServiceDirectory(t *testing.T) {
	// Same working directory as build and on_healthy. A command reading a
	// VERSION file beside the source is the obvious thing to write, and it
	// would silently produce "" from anywhere else.
	dir := t.TempDir()
	write(t, filepath.Join(dir, "VERSION"), "9.9.9\n")
	if got := serviceVersion(context.Background(), "cat VERSION", dir); got != "9.9.9" {
		t.Errorf("fingerprint = %q, want 9.9.9 — the command did not run in the service directory", got)
	}
}

func TestAFailingCommandYieldsNoFingerprintRatherThanAnError(t *testing.T) {
	// serviceVersion has no error return on purpose. A stack that will not
	// start is a worse outcome than a diagnostic that is missing, and the
	// missing case is already meaningful downstream.
	dir := gitRepo(t)
	for _, command := range []string{"exit 1", "no-such-command-anywhere", "echo out; exit 3"} {
		if got := serviceVersion(context.Background(), command, dir); got != "" {
			t.Errorf("%q produced %q; a failed command must not fingerprint, and must not fall back to git either", command, got)
		}
	}
}

func TestOnlyTheFirstLineOfACommandIsTheFingerprint(t *testing.T) {
	dir := t.TempDir()
	if got := serviceVersion(context.Background(), "printf 'abc123\\nsome advice\\n'", dir); got != "abc123" {
		t.Errorf("fingerprint = %q, want abc123", got)
	}
}

func TestStderrDoesNotContaminateTheFingerprint(t *testing.T) {
	// A command that warns on stderr and answers on stdout is working
	// correctly; merging the two would corrupt its answer with its own
	// diagnostics, and the corruption would differ run to run.
	dir := t.TempDir()
	if got := serviceVersion(context.Background(), "echo 'warning: deprecated' >&2; echo sha-777", dir); got != "sha-777" {
		t.Errorf("fingerprint = %q, want sha-777", got)
	}
}

func TestAFingerprintCannotRepaintTheTerminalOrGrowWithoutBound(t *testing.T) {
	// A config file naming a command does not make that command's output
	// trusted input. This value lands in a terminal report, a JSON status
	// body, and a manifest.
	dir := t.TempDir()
	got := serviceVersion(context.Background(), "printf 'a\\033[31mb\\007c'", dir)
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Errorf("fingerprint %q carries control characters", got)
	}
	if got != "a[31mbc" {
		t.Errorf("fingerprint = %q, want the printable characters kept", got)
	}

	long := serviceVersion(context.Background(), "printf 'x%.0s' $(seq 1 500)", dir)
	if len(long) > maxVersionLen {
		t.Errorf("fingerprint is %d bytes, want at most %d", len(long), maxVersionLen)
	}
}

func TestNoSeedAppliedIsReportedAsAbsenceNotAZeroRecord(t *testing.T) {
	o := &Orchestrator{}
	if got := o.LastSeed(); got != nil {
		t.Errorf("LastSeed = %+v before any seed ran, want nil", got)
	}
}

func TestTheLastAppliedSeedIsWhatIsReported(t *testing.T) {
	o := &Orchestrator{}
	first := time.Now().Add(-time.Hour)
	o.noteSeed("baseline", first)
	o.noteSeed("promo-week", first.Add(time.Minute))

	got := o.LastSeed()
	if got == nil || got.Name != "promo-week" {
		t.Fatalf("LastSeed = %+v, want promo-week", got)
	}
	// A copy, not the live record: callers hand this to a JSON encoder off
	// the orchestrator's lock, and a shared pointer would race a later seed.
	got.Name = "mutated by the caller"
	if again := o.LastSeed(); again.Name != "promo-week" {
		t.Errorf("a caller's edit reached the orchestrator's record: %q", again.Name)
	}
}

// Test: a healthy service carries its fingerprint in the state /api/status
// reports. Through Up, not serviceVersion directly — the fingerprint
// existing and the fingerprint reaching the status body are separate claims,
// and only the second one is what retrace reads.
func TestAHealthyServiceReportsItsFingerprint(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Dir: dir,
		Services: map[string]config.Service{
			"svc": {Dir: dir, Run: "sleep 30", Version: "echo build-2024"},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	st, ok := o.Service("svc")
	if !ok {
		t.Fatal("no state for svc")
	}
	if st.Version != "build-2024" {
		t.Errorf("ServiceState.Version = %q, want build-2024", st.Version)
	}
}

// Test: the fingerprint is taken AFTER the on_healthy hook, so a hook that
// deploys or migrates is reflected in what the fingerprint reports. Taking
// it earlier would describe a stack that no longer exists by the time the
// service is announced healthy.
func TestTheFingerprintIsTakenAfterTheOnHealthyHook(t *testing.T) {
	dir := t.TempDir()
	stamp := filepath.Join(dir, "deployed")
	cfg := &config.Config{
		Dir: dir,
		Services: map[string]config.Service{
			"svc": {
				Dir:       dir,
				Run:       "sleep 30",
				OnHealthy: fmt.Sprintf("echo after-hook > %s", stamp),
				Version:   fmt.Sprintf("cat %s", stamp),
			},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	st, _ := o.Service("svc")
	if st.Version != "after-hook" {
		t.Errorf("ServiceState.Version = %q, want after-hook — the fingerprint was taken before the hook ran", st.Version)
	}
}

// Test: a seed that failed partway is not recorded. Stamping a manifest with
// its name would tell retrace two runs started from the same data when one
// of them started from rubble.
func TestAFailedSeedIsNotRecorded(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Seeds: map[string]config.Seed{
			"baseline": {SQL: []config.SeedSQL{{Database: "db", File: "./x.sql"}}},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{}) // no SQLRunner wired: every SQL step fails
	if _, err := o.Seed(context.Background(), "baseline"); err == nil {
		t.Fatal("Seed succeeded with no SQL runner; this test needs it to fail")
	}
	if got := o.LastSeed(); got != nil {
		t.Errorf("LastSeed = %+v after a failed seed, want nil", got)
	}
}

func TestTwoDifferentUntrackedFilesFingerprintDifferently(t *testing.T) {
	// Stronger than "an untracked file changes the fingerprint", which the
	// mere presence of the porcelain branch satisfies. `git diff HEAD` shows
	// nothing for an untracked file, so without the porcelain listing in the
	// digest every untracked-only tree hashes identically — a service with a
	// brand new endpoint file would report the same stack as one without it.
	dir := gitRepo(t)
	write(t, filepath.Join(dir, "one.go"), "package main\n")
	first := serviceVersion(context.Background(), "", dir)

	if err := os.Remove(filepath.Join(dir, "one.go")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "two.go"), "package main\n")
	second := serviceVersion(context.Background(), "", dir)

	if first == second {
		t.Errorf("two different untracked files share a fingerprint: %q", first)
	}
}

func TestASuccessfulSeedIsRecorded(t *testing.T) {
	// The counterpart to TestAFailedSeedIsNotRecorded, which a Seed that
	// records nothing at all would also satisfy. Both directions are needed
	// or "never record anything" passes the suite.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	cfg := &config.Config{
		Dir: t.TempDir(),
		Seeds: map[string]config.Seed{
			"baseline": {HTTP: []config.SeedHTTP{{Method: http.MethodGet, URL: srv.URL + "/"}}},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{})
	before := time.Now()
	if _, err := o.Seed(context.Background(), "baseline"); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	got := o.LastSeed()
	if got == nil || got.Name != "baseline" {
		t.Fatalf("LastSeed = %+v, want baseline", got)
	}
	if got.AppliedAt.Before(before) {
		t.Errorf("appliedAt = %s, want at or after %s", got.AppliedAt, before)
	}
}
