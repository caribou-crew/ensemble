package capture

import "github.com/caribou-crew/ensemble/retrace/config"

// destroyEntries builds a plain destroy-mode redact list — the shape every
// test in this package used before per-key modes existed.
func destroyEntries(keys ...string) []config.RedactEntry {
	out := make([]config.RedactEntry, len(keys))
	for i, k := range keys {
		out[i] = config.RedactEntry{Field: k, Mode: "destroy"}
	}
	return out
}
