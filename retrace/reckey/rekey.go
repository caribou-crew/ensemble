package reckey

import (
	"fmt"
	"path/filepath"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

// RekeyEntry is what Rekey did to, or decided about, one encryption.json.
type RekeyEntry struct {
	// Path is relative to Cwd, for a report a human can read without every
	// line repeating the project root.
	Path string
	// Action is "rewrapped", "already-current", or "skipped".
	Action string
	// Reason is set for every Action except "rewrapped" — there is nothing
	// to explain about the ordinary case.
	Reason string
}

const (
	RekeyRewrapped      = "rewrapped"
	RekeyAlreadyCurrent = "already-current"
	RekeySkipped        = "skipped"
)

// RekeyOptions names the two keys a rotation moves every data key's wrap
// between. Both are raw key bytes — resolved from --old/--new flags by the
// caller, not read from RETRACE_RECORDING_KEY, so a rotation can run with
// both keys in hand without ever exporting either.
type RekeyOptions struct {
	Cwd string
	Old []byte
	New []byte
}

// RekeyResult tallies one pass over every encryption.json under Cwd.
type RekeyResult struct {
	Entries []RekeyEntry
}

// Rewrapped is every entry Rekey actually rewrapped.
func (r RekeyResult) Rewrapped() []RekeyEntry { return r.entries(RekeyRewrapped) }

// Skipped is every entry Rekey left untouched, with a reason each — a file
// already on --new (a resumed or re-run rotation) and a file wrapped under
// neither key both land here, distinguished by Reason.
func (r RekeyResult) Skipped() []RekeyEntry { return r.entries(RekeySkipped, RekeyAlreadyCurrent) }

// NeedsAttention is every skip that is NOT "already on --new" — the ordinary
// no-op case for a resumed rotation is not something an operator needs to
// see flagged, but a read failure or a third, unrelated key is.
func (r RekeyResult) NeedsAttention() []RekeyEntry {
	var out []RekeyEntry
	for _, e := range r.Entries {
		if e.Action == RekeySkipped {
			out = append(out, e)
		}
	}
	return out
}

func (r RekeyResult) entries(actions ...string) []RekeyEntry {
	var out []RekeyEntry
	for _, e := range r.Entries {
		for _, a := range actions {
			if e.Action == a {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// Rekey walks every encryption.json under .retrace/runs and .retrace-ref
// beneath Cwd and rewraps each run's data key from Old to New.
//
// A file already wrapped under New is a no-op, reported as
// "already-current" rather than silently skipped: the ordinary shape of a
// resumed or re-run rotation, not an error. A file wrapped under neither
// Old nor New is left untouched and reported "skipped" — Rekey never
// guesses which key an unrelated file belongs to, and a partial migration
// across two unrelated rotations must never merge into one.
//
// A crash partway through leaves every file independently valid: each
// directory's rewrap is read-old, wrap-new, write — one atomic write per
// file (via runs.WriteEncryption's own temp-file-and-rename), so no run is
// ever left wrapped under neither key.
func Rekey(o RekeyOptions) (RekeyResult, error) {
	oldID, newID := KeyID(o.Old), KeyID(o.New)
	var res RekeyResult
	dirs, err := encryptionDirs(o.Cwd)
	if err != nil {
		return RekeyResult{}, err
	}
	for _, dir := range dirs {
		rel, err := filepath.Rel(o.Cwd, dir)
		if err != nil {
			rel = dir
		}
		p := runs.Paths{RunDir: dir}
		e, err := runs.ReadEncryption(p)
		if err != nil {
			res.Entries = append(res.Entries, RekeyEntry{Path: rel, Action: RekeySkipped, Reason: fmt.Sprintf("cannot read encryption.json: %v", err)})
			continue
		}
		if e == nil {
			continue // no sidecar here at all — nothing for this command to do
		}
		switch e.KeyID {
		case newID:
			res.Entries = append(res.Entries, RekeyEntry{Path: rel, Action: RekeyAlreadyCurrent, Reason: "already wrapped under --new"})
		case oldID:
			dataKey, err := UnwrapDataKey(e.WrappedDataKey, o.Old)
			if err != nil {
				res.Entries = append(res.Entries, RekeyEntry{Path: rel, Action: RekeySkipped, Reason: fmt.Sprintf("failed to unwrap under --old: %v", err)})
				continue
			}
			wrapped, err := WrapDataKey(dataKey, o.New)
			if err != nil {
				return RekeyResult{}, fmt.Errorf("reckey: rewrapping %s: %w", rel, err)
			}
			if err := runs.WriteEncryption(p, runs.Encryption{KeyID: newID, WrappedDataKey: wrapped}); err != nil {
				return RekeyResult{}, fmt.Errorf("reckey: writing %s: %w", rel, err)
			}
			res.Entries = append(res.Entries, RekeyEntry{Path: rel, Action: RekeyRewrapped})
		default:
			res.Entries = append(res.Entries, RekeyEntry{Path: rel, Action: RekeySkipped,
				Reason: fmt.Sprintf("wrapped under an unrelated key (keyId %s matches neither --old's %s nor --new's %s)", e.KeyID, oldID, newID)})
		}
	}
	return res, nil
}

// encryptionDirs lists every run and reference-bundle directory that might
// hold an encryption.json, via retrace/runs' own directory-listing helpers
// — never a hand-rolled filepath.Walk, so a rekey pass sees exactly the
// same app/flow/run tree every other command in this repo does.
func encryptionDirs(cwd string) ([]string, error) {
	var dirs []string

	root := runs.RunsRoot(cwd)
	apps, err := runs.ListAppsErr(root)
	if err != nil {
		return nil, fmt.Errorf("reckey: listing %s: %w", root, err)
	}
	for _, app := range apps {
		flows, err := runs.ListFlowsErr(root, app)
		if err != nil {
			return nil, fmt.Errorf("reckey: listing %s/%s: %w", root, app, err)
		}
		for _, flow := range flows {
			ids, err := runs.ListRunsErr(root, app, flow)
			if err != nil {
				return nil, fmt.Errorf("reckey: listing %s/%s/%s: %w", root, app, flow, err)
			}
			for _, id := range ids {
				p, err := runs.PathsFor(root, app, flow, id)
				if err != nil {
					continue // not a run directory shape retrace itself would have made
				}
				dirs = append(dirs, p.RunDir)
			}
		}
	}

	refsRoot := runs.RefsRoot(cwd)
	refApps, err := runs.ListAppsErr(refsRoot)
	if err != nil {
		return nil, fmt.Errorf("reckey: listing %s: %w", refsRoot, err)
	}
	for _, app := range refApps {
		flows, err := runs.ListFlowsErr(refsRoot, app)
		if err != nil {
			return nil, fmt.Errorf("reckey: listing %s/%s: %w", refsRoot, app, err)
		}
		for _, flow := range flows {
			dirs = append(dirs, filepath.Join(refsRoot, app, flow, runs.RefRunID))
		}
	}

	return dirs, nil
}
