package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unexplained is a config whose one tolerance carries no `why:`. Small on
// purpose: the assertions are about the ratchet, not about the rule.
const unexplained = "app: web\nwire_ignore:\n  - \"**.id\"\n"

func TestRequireWhyFlagRefusesDiffBeforeItComparesAnything(t *testing.T) {
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, unexplained)

	// No runs exist in this directory. Without --require-why, diff fails
	// for the ordinary reason (no reference bundle); with it, it must fail
	// for the WHY reason instead — which is what proves the check runs
	// before any comparison work, not as a footnote on a report.
	base := []string{"diff", "--flow", "checkout", "--app", "web"}
	plain := runRetrace(t, bin, cwd, "", base...)
	if strings.Contains(plain.stderr, "require_why") {
		t.Fatalf("the ratchet fired without the flag:\n%s", plain.stderr)
	}

	res := runRetrace(t, bin, cwd, "", append(base, "--require-why")...)
	if res.code == 0 {
		t.Fatalf("--require-why exited 0 on an unexplained tolerance\nstdout: %s\nstderr: %s", res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "wire_ignore[0]") {
		t.Errorf("the error must name the entry to fix, got:\n%s", res.stderr)
	}
	if strings.Contains(res.stderr, "no reference bundle") {
		t.Errorf("diff got as far as resolving sides; the check must refuse first:\n%s", res.stderr)
	}
}

func TestRequireWhyFlagRefusesRunBeforeTheSuiteStarts(t *testing.T) {
	// Checked in `run` too, so the failure lands where it is cheap to fix
	// rather than after a browser suite has spent five minutes.
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, unexplained)

	res := runRetrace(t, bin, cwd, "", "run", "--flow", "checkout", "--app", "web", "--require-why", "--upstream", "http://127.0.0.1:1", "--no-ensemble", "--", "true")
	if res.code == 0 {
		t.Fatalf("--require-why exited 0\nstdout: %s\nstderr: %s", res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "wire_ignore[0]") {
		t.Errorf("the error must name the entry to fix, got:\n%s", res.stderr)
	}
	// Nothing was recorded: a refused run must not leave a half-run behind
	// for `retrace runs` to report as abandoned.
	if entries, err := os.ReadDir(filepath.Join(cwd, ".retrace", "runs")); err == nil && len(entries) > 0 {
		t.Errorf("a refused run left %d run directory/ies behind", len(entries))
	}
}

func TestRequireWhyInTheConfigNeedsNoFlagAtAll(t *testing.T) {
	// The flag is for trying the ratchet on. `require_why: true` in the file
	// is the committed decision, and it must bind every command with no
	// opt-in from whoever is typing.
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\nrequire_why: true\nwire_ignore:\n  - \"**.id\"\n")

	res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web")
	if res.code == 0 || !strings.Contains(res.stderr, "wire_ignore[0]") {
		t.Errorf("config-set require_why did not bind: exit %d\nstderr: %s", res.code, res.stderr)
	}
}

func TestTheFlagCannotTurnAConfiguredRequireWhyOff(t *testing.T) {
	// `--require-why=false` must not be a bypass. The setting exists to
	// inconvenience the person adding an unexplained tolerance, so a flag
	// that switched it off would be a bypass handed to exactly them.
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\nrequire_why: true\nwire_ignore:\n  - \"**.id\"\n")

	res := runRetrace(t, bin, cwd, "", "diff", "--flow", "checkout", "--app", "web", "--require-why=false")
	if res.code == 0 || !strings.Contains(res.stderr, "wire_ignore[0]") {
		t.Errorf("--require-why=false bypassed the project's own setting: exit %d\nstderr: %s", res.code, res.stderr)
	}
}

func TestRefRuleWritesItsWhyIntoTheOverlay(t *testing.T) {
	// The overlay is what a reviewer reads in a pull request. A machine-
	// written rule with no stated reason is the least reviewable tolerance
	// in the product, because nobody was present when it was authored.
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")

	res := runRetrace(t, bin, cwd, "", "ref", "rule",
		"--field", "items[*].requestId", "--matcher", "ignore",
		"--why", "regenerated on every request")
	if res.code != 0 {
		t.Fatalf("ref rule: exit %d\nstderr: %s", res.code, res.stderr)
	}

	b, err := os.ReadFile(filepath.Join(cwd, ".retrace", "wire-rules.json"))
	if err != nil {
		t.Fatal(err)
	}
	var overlay []struct {
		Why string `json:"why"`
	}
	if err := json.Unmarshal(b, &overlay); err != nil {
		t.Fatalf("overlay is not a rule list: %v\n%s", err, b)
	}
	if len(overlay) != 1 || overlay[0].Why != "regenerated on every request" {
		t.Errorf("overlay = %s, want one rule carrying the why", b)
	}
}

func TestRefRuleStillWorksWithoutAWhy(t *testing.T) {
	// --why is not required by this verb even under require_why: it cannot
	// see the config it writes into, and the ratchet already catches the
	// omission at the next Discover with a message naming the entry. Two
	// checks in two places with two messages is how a check disagrees with
	// itself.
	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, "app: web\n")

	res := runRetrace(t, bin, cwd, "", "ref", "rule", "--field", "**.id", "--matcher", "ignore")
	if res.code != 0 {
		t.Fatalf("ref rule without --why: exit %d\nstderr: %s", res.code, res.stderr)
	}
}

func TestHelpNamesRequireWhyOnBothCommandsThatHaveIt(t *testing.T) {
	// The docs contract test checks that every flag the SKILL names exists
	// in --help. This is the other direction: a flag that ships without
	// appearing in --help is a flag nobody finds.
	res := runRetrace(t, buildRetrace(t), t.TempDir(), "", "--help")
	out := res.stdout + res.stderr
	for _, line := range []string{"retrace run", "retrace diff"} {
		i := strings.Index(out, line)
		if i < 0 {
			t.Fatalf("--help has no %q line:\n%s", line, out)
		}
		end := strings.IndexByte(out[i:], '\n')
		if !strings.Contains(out[i:i+end], "--require-why") {
			t.Errorf("%q does not offer --require-why:\n%s", line, out[i:i+end])
		}
	}
}
