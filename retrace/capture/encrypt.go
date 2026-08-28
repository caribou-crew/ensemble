package capture

import (
	"fmt"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/reckey"
)

// resolveDataKey loads a team key and mints a fresh per-run data key when
// any rule needs mode encrypt; it returns (nil, "", "", nil) — no key,
// nothing to wrap — when no rule does, so a run with only destroy/display
// fields never touches RETRACE_RECORDING_KEY or the keyfile.
//
// Called BEFORE runs.Create in both StartStandalone and StartAttached: a
// missing team key must fail the capture before any traffic is recorded
// and before any run directory exists at all (design.md D2) — the
// alternative, silently writing plaintext or silently downgrading to
// destroy, is a surprise a developer discovers by grepping a committed
// bundle.
func resolveDataKey(dir string, rules []trace.KeyRule) (dataKey []byte, keyID, wrappedDataKey string, err error) {
	needsKey := false
	for _, r := range rules {
		if r.Mode == trace.ModeEncrypt {
			needsKey = true
			break
		}
	}
	if !needsKey {
		return nil, "", "", nil
	}
	teamKey, _, err := reckey.LoadTeamKey(dir)
	if err != nil {
		return nil, "", "", fmt.Errorf("capture: an `encrypt`-mode redact rule is configured, but no team key resolved: %w", err)
	}
	dataKey, err = reckey.GenerateDataKey()
	if err != nil {
		return nil, "", "", err
	}
	wrapped, err := reckey.WrapDataKey(dataKey, teamKey)
	if err != nil {
		return nil, "", "", err
	}
	return dataKey, reckey.KeyID(teamKey), wrapped, nil
}
