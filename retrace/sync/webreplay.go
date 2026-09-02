package sync

import (
	"fmt"
	"image"
	_ "image/png" // registers the PNG decoder image.DecodeConfig needs
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// webReplayBundle names one manifest-less replay run directory found inside
// a downloaded artifact — a `retrace replay` bundle. It may carry shots
// (a browser/Playwright flow, or a mobile flow whose views are
// screenshottable), wire (the observed hops replay now persists as
// wire.jsonl), or both. A mobile card flow on Android RN/native is
// wire-only: its PCI views are FLAG_SECURE, so no screenshot exists.
type webReplayBundle struct {
	runDir   string
	app      string
	flow     string
	runID    string
	hasShots bool
	hasWire  bool
}

// findWebReplayBundles finds every manifest-less replay run directory under
// root, at the <app>/<flow>/<run-id> depth findManifests looks for
// manifest.json at, that carries shots/ and/or wire.jsonl. A directory that
// already has a manifest is a native run and is left to the manifest path —
// never merged twice. Keyed off the run directory (not the shots/ dir) so a
// wire-only run with no shots/ subdir is still found.
func findWebReplayBundles(root string) ([]webReplayBundle, error) {
	var out []webReplayBundle
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		// A replay run dir is identified by carrying a shots/ dir or a
		// wire.jsonl directly. Its own name is the run-id; its parents are
		// flow and app.
		shotsDir := filepath.Join(p, "shots")
		wirePath := filepath.Join(p, "wire.jsonl")
		hasShots := false
		if info, statErr := os.Stat(shotsDir); statErr == nil && info.IsDir() {
			if ok, perr := dirHasPNG(shotsDir); perr != nil {
				return perr
			} else {
				hasShots = ok
			}
		}
		hasWire := false
		if info, statErr := os.Stat(wirePath); statErr == nil && !info.IsDir() && info.Size() > 0 {
			hasWire = true
		}
		if !hasShots && !hasWire {
			return nil
		}
		if _, statErr := os.Stat(filepath.Join(p, "manifest.json")); statErr == nil {
			return nil // native run — leave it to the manifest path
		}
		if seen[p] {
			return nil
		}
		seen[p] = true
		flowDir := filepath.Dir(p)
		out = append(out, webReplayBundle{
			runDir:   p,
			app:      filepath.Base(filepath.Dir(flowDir)),
			flow:     filepath.Base(flowDir),
			runID:    filepath.Base(p),
			hasShots: hasShots,
			hasWire:  hasWire,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("sync: walking downloaded artifact for web replays: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].runDir < out[j].runDir })
	return out, nil
}

// dirHasPNG reports whether dir directly contains at least one .png file —
// an empty shots/ directory (a replay that captured nothing) is not a
// mergeable bundle.
func dirHasPNG(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".png") {
			return true, nil
		}
	}
	return false, nil
}

// checkpointsFromShots reads shots/*.png and decodes each header for
// geometry, the same approach capture.Session.Checkpoints uses — not
// reused directly because that method hangs off a live Session, which a
// merge from a downloaded artifact never has.
func checkpointsFromShots(shotsDir string) ([]runs.Checkpoint, error) {
	entries, err := os.ReadDir(shotsDir)
	if err != nil {
		return nil, err
	}
	var out []runs.Checkpoint
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".png") {
			continue
		}
		// e.Info() rather than a second os.Stat, mirroring
		// capture.Session.Checkpoints — see runs.Checkpoint.At's doc comment.
		// Here the mtime is the artifact's extraction time, not the original
		// capture time, but it is still the best available per-checkpoint
		// ordering signal a downloaded bundle carries.
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		f, err := os.Open(filepath.Join(shotsDir, e.Name()))
		if err != nil {
			return nil, err
		}
		cfg, _, err := image.DecodeConfig(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("sync: checkpoint %s is not a readable PNG: %w", e.Name(), err)
		}
		name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		out = append(out, runs.Checkpoint{
			Name:   name,
			File:   filepath.ToSlash(filepath.Join("shots", e.Name())),
			Width:  cfg.Width,
			Height: cfg.Height,
			At:     info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// synthesizeReplayManifest builds the manifest a replay bundle never wrote
// itself. It handles three shapes: shots-only (browser/Playwright), wire-only
// (Android RN/native card flows — FLAG_SECURE, no screenshots), and both.
// Checkpoints come from shots when present; Wire.Recorded is true when a
// wire.jsonl was persisted; trust is assessed on whatever plane exists.
func synthesizeReplayManifest(b webReplayBundle) (runs.Manifest, error) {
	var checkpoints []runs.Checkpoint
	if b.hasShots {
		cps, err := checkpointsFromShots(filepath.Join(b.runDir, "shots"))
		if err != nil {
			return runs.Manifest{}, err
		}
		checkpoints = cps
	}
	wire := runs.Counts{Recorded: false, Reason: "replay: no wire plane was captured"}
	if b.hasWire {
		// Recorded=true with the count left at 0 is fine: diff reads the
		// hops from wire.jsonl directly; Counts is the recorded-vs-not flag.
		wire = runs.Counts{Recorded: true}
	}
	return runs.Manifest{
		App:         b.app,
		Flow:        b.flow,
		RunID:       b.runID,
		Mode:        runs.ModePixel,
		Checkpoints: checkpoints,
		Capture:     assessReplay(len(checkpoints), b.hasWire),
		Wire:        wire,
	}, nil
}

// assessReplay is the replay trust seam. A run with shots OR wire is
// diffable and assesses ok. A run with neither fails closed — nothing to
// compare, and reporting ok would be worse than the permissive answer this
// package's zero-value rules refuse to give.
func assessReplay(shots int, hasWire bool) runs.CaptureTrust {
	if shots == 0 && !hasWire {
		return runs.CaptureTrust{
			Status:  trace.VerdictFailed,
			Summary: "replay captured neither shots nor wire — there is nothing to diff",
		}
	}
	switch {
	case shots > 0 && hasWire:
		return runs.CaptureTrust{Status: trace.VerdictOK, Summary: fmt.Sprintf("replay: %d shot(s) + wire captured", shots)}
	case shots > 0:
		return runs.CaptureTrust{Status: trace.VerdictOK, Summary: fmt.Sprintf("pixel-only replay: %d shot(s) captured (no wire plane by design)", shots)}
	default:
		return runs.CaptureTrust{Status: trace.VerdictOK, Summary: "wire-only replay: shots are FLAG_SECURE-protected; trust is judged on the wire plane"}
	}
}
