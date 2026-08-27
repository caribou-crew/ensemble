package orchestrator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"strings"
	"time"
)

// A stack fingerprint answers one question for retrace: between these two
// runs, did the backend change? Without an answer, a diff that moved because
// a service was redeployed is indistinguishable from one that moved because
// the client regressed — and the client is what gets blamed, because the
// client is what the test touches.
//
// It is deliberately opaque. Nothing compares two fingerprints for ordering
// or parses one; they are only ever equal or not.

// versionTimeout bounds one fingerprint command. A `version:` command may
// reasonably curl the service it just gated on, and a wedged one must not
// hold a start open — the fingerprint is diagnostic, and no diagnostic is
// worth a hung `ensemble up`.
const versionTimeout = 5 * time.Second

// maxVersionLen truncates whatever a command prints. The value travels into
// a manifest, a JSON status body, and a dashboard cell; a command that dumps
// its whole build log should produce a useless fingerprint, not a useless
// manifest.
const maxVersionLen = 120

// serviceVersion resolves one service's fingerprint, preferring the
// configured command over the git default.
//
// Every failure yields "" rather than an error. A service whose fingerprint
// could not be taken is one retrace has no evidence about, and the honest
// encoding of that is absence — the same ruling the screen-geometry guard
// makes about a run with no recorded device. Failing the start instead would
// trade a missing diagnostic for a stack that will not come up at all.
func serviceVersion(ctx context.Context, command, dir string) string {
	if command != "" {
		return cleanVersion(shellOutput(ctx, command, dir))
	}
	return cleanVersion(gitFingerprint(ctx, dir))
}

// gitFingerprint is the default: the commit the service's directory is on,
// plus a digest of whatever is uncommitted.
//
// The dirty digest is not decoration. A bare HEAD sha reports two runs of
// two DIFFERENT uncommitted edits as the same stack, which is precisely the
// false negative this whole fingerprint exists to prevent — and editing
// without committing is the normal state of the machine retrace runs on.
// `git status --porcelain` alone would not do it either: it names the files
// that changed, not what they now say, so two different edits to one file
// look identical to it. The digest covers both — tracked content via
// `diff HEAD`, and the existence of untracked files via the porcelain list,
// which `diff HEAD` never shows.
func gitFingerprint(ctx context.Context, dir string) string {
	head := gitOutput(ctx, dir, "rev-parse", "HEAD")
	if head == "" {
		return "" // not a repo, or no commits yet
	}
	if len(head) > 12 {
		head = head[:12]
	}
	porcelain := gitOutput(ctx, dir, "status", "--porcelain")
	if porcelain == "" {
		return head
	}
	sum := sha256.Sum256([]byte(gitOutput(ctx, dir, "diff", "HEAD") + "\x00" + porcelain))
	return head + "+" + hex.EncodeToString(sum[:])[:8]
}

func gitOutput(ctx context.Context, dir string, args ...string) string {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(out.String())
}

// shellOutput runs a configured `version:` command the same way every other
// configured command in this file runs — /bin/sh -c, in the service's own
// directory — and takes its FIRST line. A command ending in a chatty
// trailing newline or a second line of advice still yields a fingerprint;
// requiring people to write `| head -1` themselves would make the common
// case the broken one.
//
// stderr is discarded rather than merged into stdout: a command that warns
// on stderr and prints a sha on stdout is working correctly, and merging
// would corrupt its answer with its own diagnostics.
func shellOutput(ctx context.Context, command, dir string) string {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	line, _, _ := strings.Cut(out.String(), "\n")
	return strings.TrimSpace(line)
}

// cleanVersion strips control characters and truncates. A fingerprint is
// pasted into a terminal report and a JSON body; a value carrying an escape
// sequence could repaint either, and a command's output is not a trusted
// source just because a config file named the command.
func cleanVersion(v string) string {
	v = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, v)
	if len(v) > maxVersionLen {
		v = v[:maxVersionLen]
	}
	return strings.TrimSpace(v)
}

// SeedRecord is the last seed applied to this stack — what retrace copies
// into a run's manifest so a diff can say "these two runs did not start from
// the same data" instead of reporting the difference as behaviour.
type SeedRecord struct {
	Name      string    `json:"name"`
	AppliedAt time.Time `json:"appliedAt"`
}

// noteSeed records a successful seed. Called from Seed rather than from the
// HTTP handler so the CLI and TUI paths record it too — a seed applied by a
// route that forgot to report it is worse than no record, because the
// manifest would then claim the earlier seed was still the live one.
func (o *Orchestrator) noteSeed(name string, at time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lastSeed = &SeedRecord{Name: name, AppliedAt: at}
}

// LastSeed returns a copy of the last applied seed, or nil if none has been
// applied in this stack's lifetime. A copy, not the pointer: callers hand
// this straight to a JSON encoder off the orchestrator's lock.
func (o *Orchestrator) LastSeed() *SeedRecord {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.lastSeed == nil {
		return nil
	}
	cp := *o.lastSeed
	return &cp
}
