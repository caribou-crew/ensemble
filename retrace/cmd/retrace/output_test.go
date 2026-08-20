package main

import "testing"

// TestExitCodeLiteralsArePinned — re-review residual on finding 8: the
// existing dispatch tests in main_test.go compare against the exitOK/
// exitUsage *constants*, never the literal numbers those constants hold.
// Renumbering exitUsage to 1, or swapping exitDiff and exitGate, keeps
// every one of those tests green while breaking every CI pipeline that
// branches on the codes — output.go:9-11 calls this "the CI contract" for
// exactly that reason. Measured: sed exitUsage from 3 to 1 and re-ran
// `go test ./retrace/...` before writing this test — it stayed green.
func TestExitCodeLiteralsArePinned(t *testing.T) {
	if exitOK != 0 {
		t.Errorf("exitOK = %d, want 0", exitOK)
	}
	if exitDiff != 1 {
		t.Errorf("exitDiff = %d, want 1", exitDiff)
	}
	if exitGate != 2 {
		t.Errorf("exitGate = %d, want 2", exitGate)
	}
	if exitUsage != 3 {
		t.Errorf("exitUsage = %d, want 3", exitUsage)
	}
}
