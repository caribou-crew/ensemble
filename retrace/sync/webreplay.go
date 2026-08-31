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

// webReplayBundle names one manifest-less pixel-replay run directory found
// inside a downloaded artifact — a `retrace replay` bundle uploaded by a
// browser-driven flow (Playwright), which by design has no wire plane and
// so never writes manifest.json itself.
type webReplayBundle struct {
	runDir string
	app    string
	flow   string
	runID  string
}

// findWebReplayBundles finds every shots-bearing, manifest-less run
// directory under root, at the same <app>/<flow>/<run-id> depth
// findManifests looks for manifest.json at. A directory that has BOTH a
// manifest and shots is a native run and is left to the manifest path —
// never merged twice.
func findWebReplayBundles(root string) ([]webReplayBundle, error) {
	var out []webReplayBundle
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || d.Name() != "shots" {
			return nil
		}
		runDir := filepath.Dir(p)
		if _, statErr := os.Stat(filepath.Join(runDir, "manifest.json")); statErr == nil {
			return filepath.SkipDir // native run — leave it to the manifest path
		}
		hasPNG, err := dirHasPNG(p)
		if err != nil {
			return err
		}
		if !hasPNG {
			return filepath.SkipDir // nothing to diff
		}
		flowDir := filepath.Dir(runDir)
		out = append(out, webReplayBundle{
			runDir: runDir,
			app:    filepath.Base(filepath.Dir(flowDir)),
			flow:   filepath.Base(flowDir),
			runID:  filepath.Base(runDir),
		})
		return filepath.SkipDir
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

// synthesizeReplayManifest builds the manifest a pixel-replay bundle never
// wrote itself. Groups stays nil (WriteManifest defaults it to []):
// groups.jsonl travels with the copied tree, but a pixel-only run has no
// wire diff to bucket into named sections, so there is nothing to fold it
// into here.
func synthesizeReplayManifest(b webReplayBundle) (runs.Manifest, error) {
	checkpoints, err := checkpointsFromShots(filepath.Join(b.runDir, "shots"))
	if err != nil {
		return runs.Manifest{}, err
	}
	return runs.Manifest{
		App:         b.app,
		Flow:        b.flow,
		RunID:       b.runID,
		Mode:        runs.ModePixel,
		Checkpoints: checkpoints,
		Capture:     assessPixelOnly(len(checkpoints)),
		Wire:        runs.Counts{Recorded: false, Reason: "pixel-only replay: no wire plane is captured"},
	}, nil
}

// assessPixelOnly is the pixel-only trust seam: capture.Assess is
// untouched, because ITS invariant (VerdictOK requires len(Hops) > 0) is
// about wire captures and a category error for a run with no wire plane by
// design. A shots-bearing replay assesses ok — not quarantined, diffable,
// `ref accept`-able without --force. A zero-shot bundle fails closed: there
// is nothing to diff, and reporting that as ok would be worse than the
// permissive answer this package's other zero-value rules refuse to give.
func assessPixelOnly(shots int) runs.CaptureTrust {
	if shots == 0 {
		return runs.CaptureTrust{
			Status:  trace.VerdictFailed,
			Summary: "pixel-only replay captured no shots — there is nothing to diff",
		}
	}
	return runs.CaptureTrust{
		Status: trace.VerdictOK,
		Summary: fmt.Sprintf(
			"pixel-only replay: %d shot(s) captured; trust is judged on shots, not wire (this run has no wire plane by design)",
			shots),
	}
}
