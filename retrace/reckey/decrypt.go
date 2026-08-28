package reckey

import "github.com/caribou-crew/ensemble/retrace/runs"

// ResolveDataKey answers "what data key would decrypt this run's
// encrypt-mode fields, if any". It is the ONE place retrace/diff,
// retrace/replay and retrace/serve resolve a run's data key, so the
// key-resolution chain (read the sidecar, load the team key, unwrap) is
// never re-derived three times.
//
// (nil, nil) means "nothing to decrypt for this run" — either it has no
// encryption.json at all (no encrypt-mode field was ever configured), or
// it does but no team key resolves from dir. Both are the same answer to
// every caller here: show markers, not values. A caller for whom the key
// is REQUIRED, not optional, should call LoadTeamKey directly instead of
// through this function, the same way capture does.
func ResolveDataKey(p runs.Paths, dir string) ([]byte, error) {
	enc, err := runs.ReadEncryption(p)
	if err != nil {
		return nil, err
	}
	if enc == nil {
		return nil, nil
	}
	teamKey, _, err := LoadTeamKey(dir)
	if err != nil {
		return nil, nil
	}
	dataKey, err := UnwrapDataKey(enc.WrappedDataKey, teamKey)
	if err != nil {
		return nil, nil
	}
	return dataKey, nil
}
