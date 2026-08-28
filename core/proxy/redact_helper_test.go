package proxy

import (
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
)

// mustRedactor builds a destroy-only Redactor for tests that only care
// about wiring a Recorder up with SOME redactor, not about per-key modes.
func mustRedactor(t *testing.T, keys []string, maxBody int) *trace.Redactor {
	t.Helper()
	r, err := trace.NewRedactor(trace.DestroyKeys(keys), maxBody, nil)
	if err != nil {
		t.Fatalf("trace.NewRedactor: %v", err)
	}
	return r
}
