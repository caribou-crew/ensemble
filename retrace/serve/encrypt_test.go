package serve

import (
	"encoding/hex"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/reckey"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

func bytes32(fill byte) []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = fill
	}
	return b
}

func mustEncrypt(t *testing.T, key []byte, plaintextJSON string) string {
	t.Helper()
	marker, err := trace.EncryptField(key, plaintextJSON)
	if err != nil {
		t.Fatalf("EncryptField: %v", err)
	}
	return marker
}

// writeEncryptionSidecar wraps dataKey under teamKey and writes the run's
// encryption.json, mirroring what capture does at session close.
func writeEncryptionSidecar(t *testing.T, runDir string, dataKey, teamKey []byte) {
	t.Helper()
	wrapped, err := reckey.WrapDataKey(dataKey, teamKey)
	if err != nil {
		t.Fatalf("WrapDataKey: %v", err)
	}
	if err := runs.WriteEncryption(runs.Paths{RunDir: runDir}, runs.Encryption{
		KeyID: reckey.KeyID(teamKey), WrappedDataKey: wrapped,
	}); err != nil {
		t.Fatalf("WriteEncryption: %v", err)
	}
}

// TestItemDecryptsAnEncryptedFieldWhenTheServersOwnKeyResolves is D6's
// retrace/serve scenario: the review queue's item detail decrypts an
// encrypt-mode field when the SERVING PROCESS's own env resolves the team
// key — automatic here because SummaryFor calls diff.Build with the
// server's own Deps.Cfg, the exact path task 6.2 wired ResolveDataKey into.
func TestItemDecryptsAnEncryptedFieldWhenTheServersOwnKeyResolves(t *testing.T) {
	teamKey := bytes32('e')
	t.Setenv(reckey.EnvTeamKey, hex.EncodeToString(teamKey))

	cwd := t.TempDir()
	dataKeyA, err := reckey.GenerateDataKey()
	if err != nil {
		t.Fatal(err)
	}
	dataKeyB, err := reckey.GenerateDataKey()
	if err != nil {
		t.Fatal(err)
	}
	markerA := mustEncrypt(t, dataKeyA, `"1111111111111111"`)
	markerB := mustEncrypt(t, dataKeyB, `"2222222222222222"`)

	pA := recordRun(t, cwd, "web", "checkout", runA, nil,
		[]trace.Hop{hop(1, "GET", "/checkout", 200, `{"account_number":"`+markerA+`"}`)})
	writeEncryptionSidecar(t, pA.RunDir, dataKeyA, teamKey)
	acceptRef(t, cwd, "web", "checkout", runA)

	pB := recordRun(t, cwd, "web", "checkout", runB, nil,
		[]trace.Hop{hop(1, "GET", "/checkout", 200, `{"account_number":"`+markerB+`"}`)})
	writeEncryptionSidecar(t, pB.RunDir, dataKeyB, teamKey)

	ts := newServer(t, cwd)
	resp := do(t, ts, "GET", "/api/queue/web/checkout", "", nil)
	if resp.status != 200 {
		t.Fatalf("status = %d, want 200: %s", resp.status, resp.body)
	}
	body := resp.json(t)
	summary := body["summary"].(map[string]any)
	wire := summary["wire"].(map[string]any)
	paired := wire["paired"].([]any)
	if len(paired) != 1 {
		t.Fatalf("expected one paired entry, got %d: %s", len(paired), resp.body)
	}
	entry := paired[0].(map[string]any)
	bodyDiff, _ := entry["bodyDiff"].([]any)
	if len(bodyDiff) == 0 {
		t.Fatalf("expected a body diff for the decrypted account_number field: %s", resp.body)
	}
	fd := bodyDiff[0].(map[string]any)
	if fd["a"] != "1111111111111111" || fd["b"] != "2222222222222222" {
		t.Fatalf("bodyDiff[0] = %+v, want the decrypted account numbers on both sides", fd)
	}
}

// TestItemLeavesTheMarkerWhenTheServersOwnKeyDoesNotResolve is the other
// half of D6: with no RETRACE_RECORDING_KEY and no keyfile, the API
// response must carry the marker unresolved rather than erroring the whole
// item.
func TestItemLeavesTheMarkerWhenTheServersOwnKeyDoesNotResolve(t *testing.T) {
	cwd := t.TempDir()
	dataKey, err := reckey.GenerateDataKey()
	if err != nil {
		t.Fatal(err)
	}
	// Same marker on both sides: with no key, comparison is byte-literal,
	// so an identical marker must NOT be reported as a diff.
	marker := mustEncrypt(t, dataKey, `"unchanged"`)

	recordRun(t, cwd, "web", "checkout", runA, nil,
		[]trace.Hop{hop(1, "GET", "/checkout", 200, `{"account_number":"`+marker+`"}`)})
	acceptRef(t, cwd, "web", "checkout", runA)
	recordRun(t, cwd, "web", "checkout", runB, nil,
		[]trace.Hop{hop(1, "GET", "/checkout", 200, `{"account_number":"`+marker+`"}`)})

	ts := newServer(t, cwd)
	resp := do(t, ts, "GET", "/api/queue/web/checkout", "", nil)
	if resp.status != 200 {
		t.Fatalf("status = %d, want 200: %s", resp.status, resp.body)
	}
	body := resp.json(t)
	summary := body["summary"].(map[string]any)
	wire := summary["wire"].(map[string]any)
	paired := wire["paired"].([]any)
	entry := paired[0].(map[string]any)
	bodyDiff, _ := entry["bodyDiff"].([]any)
	if len(bodyDiff) != 0 {
		t.Fatalf("byte-identical markers with no key must not report a diff: %+v", bodyDiff)
	}
}
