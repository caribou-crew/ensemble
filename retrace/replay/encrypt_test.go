package replay

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/reckey"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

func bytes32(t *testing.T, fill byte) []byte {
	t.Helper()
	b := make([]byte, 32)
	for i := range b {
		b[i] = fill
	}
	return b
}

func mustEncryptField(t *testing.T, key []byte, plaintext string) string {
	t.Helper()
	enc, err := trace.EncryptField(key, plaintext)
	if err != nil {
		t.Fatalf("EncryptField: %v", err)
	}
	return enc
}

// TestServerDecryptsAnEncryptedFieldWhenTheTeamKeyResolves is the scenario
// CI actually needs: a client asserting against the real account number in
// a strict-mock replay must see the real number, not the ciphertext
// marker.
func TestServerDecryptsAnEncryptedFieldWhenTheTeamKeyResolves(t *testing.T) {
	key := bytes32(t, 'a')
	marker := mustEncryptField(t, key, `"4111111111111111"`)
	b := &Bundle{
		Exchanges: []Exchange{{
			Key:     Key{Method: "GET", Path: "/checkout"},
			Status:  200,
			Headers: map[string]string{"X-Account": mustEncryptField(t, key, "acct-secret")},
			Body:    `{"account_number":"` + marker + `"}`,
			Seq:     1,
		}},
		dataKey: key,
	}
	s, url := serve(t, b, Options{}, "")

	resp := do(t, "GET", url+"/checkout", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Account"); got != "acct-secret" {
		t.Fatalf("X-Account = %q, want the decrypted header value", got)
	}
	if got := readBody(t, resp); got != `{"account_number":"4111111111111111"}` {
		t.Fatalf("body = %q, want the decrypted account number", got)
	}
	if s.MissCount() != 0 {
		t.Fatalf("MissCount = %d, want 0 for a served response", s.MissCount())
	}
	if s.ServedCount() != 1 {
		t.Fatalf("ServedCount = %d, want 1", s.ServedCount())
	}
}

// TestAssertRequestsObservesTheDecryptedResponseNotTheCiphertextMarker
// proves the design doc's Risk mitigation for encrypted fields: the
// observed hop's mirrored response must come from the same decrypted
// Exchange writeHit already computed, not the raw Exchange straight off
// the bundle — otherwise every encrypted field would read as a spurious
// "changed" the moment --assert-requests diffed it against itself.
func TestAssertRequestsObservesTheDecryptedResponseNotTheCiphertextMarker(t *testing.T) {
	key := bytes32(t, 'a')
	marker := mustEncryptField(t, key, `"4111111111111111"`)
	b := &Bundle{
		Exchanges: []Exchange{{
			Key:     Key{Method: "GET", Path: "/checkout"},
			Status:  200,
			Headers: map[string]string{"X-Account": mustEncryptField(t, key, "acct-secret")},
			Body:    `{"account_number":"` + marker + `"}`,
			Seq:     1,
		}},
		dataKey: key,
	}
	s, url := serve(t, b, Options{AssertRequests: true}, "")

	resp := do(t, "GET", url+"/checkout", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	hops := s.ObservedHops()
	if len(hops) != 1 {
		t.Fatalf("ObservedHops() = %+v, want exactly one", hops)
	}
	h := hops[0]
	if strings.Contains(h.Resp.Body, trace.EncryptedPrefix) {
		t.Fatalf("observed hop response body still carries the ciphertext marker: %s", h.Resp.Body)
	}
	if h.Resp.Body != `{"account_number":"4111111111111111"}` {
		t.Fatalf("observed hop response body = %q, want the decrypted account number", h.Resp.Body)
	}
	if got := h.Resp.Headers["X-Account"]; got != "acct-secret" {
		t.Fatalf("observed hop response header X-Account = %q, want the decrypted value", got)
	}
}

// TestServerFailsTheMatchWhenNoTeamKeyResolves is "never leak the marker
// as data": a matched exchange with an encrypt-mode field and no
// resolvable key must not serve the client the literal ciphertext marker
// as though it were the real value.
func TestServerFailsTheMatchWhenNoTeamKeyResolves(t *testing.T) {
	key := bytes32(t, 'b')
	marker := mustEncryptField(t, key, `"4111111111111111"`)
	b := &Bundle{
		Exchanges: []Exchange{{
			Key:    Key{Method: "GET", Path: "/checkout"},
			Status: 200,
			Body:   `{"account_number":"` + marker + `"}`,
			Seq:    1,
		}},
		// No dataKey: nothing resolved.
	}
	s, url := serve(t, b, Options{}, "")

	resp := do(t, "GET", url+"/checkout", "", nil)
	if resp.StatusCode != 500 {
		t.Fatalf("status = %d, want 500 for an undecryptable matched exchange", resp.StatusCode)
	}
	body := readBody(t, resp)
	if strings.Contains(body, trace.EncryptedPrefix) {
		t.Fatalf("marker leaked into the response body: %s", body)
	}
	if s.ServedCount() != 0 {
		t.Fatalf("ServedCount = %d, want 0 — a key-unavailable response was never actually served", s.ServedCount())
	}
	misses := s.Misses()
	if len(misses) != 1 {
		t.Fatalf("expected one miss, got %d", len(misses))
	}
	if misses[0].Kind != MissKeyUnavailable {
		t.Fatalf("miss kind = %q, want %q", misses[0].Kind, MissKeyUnavailable)
	}
}

// TestLoadBundleResolvesTheDataKeyFromTheEncryptionSidecar is the
// integration-level version of the two Server tests above: a real bundle
// directory on disk, with a real encryption.json sidecar, loaded exactly
// the way `retrace replay` loads one — LoadBundle resolving
// RETRACE_RECORDING_KEY itself, not a hand-built Bundle carrying dataKey.
func TestLoadBundleResolvesTheDataKeyFromTheEncryptionSidecar(t *testing.T) {
	teamKey := bytes32(t, 'c')
	t.Setenv(reckey.EnvTeamKey, hex.EncodeToString(teamKey))

	dataKey, err := reckey.GenerateDataKey()
	if err != nil {
		t.Fatal(err)
	}
	marker := mustEncryptField(t, dataKey, `"4111111111111111"`)
	dir := writeBundle(t, runs.Counts{Calls: 1, Recorded: true}, []trace.Hop{
		hop(1, "GET", "/checkout", "", 200, `{"account_number":"`+marker+`"}`),
	})
	wrapped, err := reckey.WrapDataKey(dataKey, teamKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := runs.WriteEncryption(runs.Paths{RunDir: dir}, runs.Encryption{
		KeyID: reckey.KeyID(teamKey), WrappedDataKey: wrapped,
	}); err != nil {
		t.Fatal(err)
	}

	b, err := LoadBundle(dir, "", nil)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}
	s, url := serve(t, b, Options{}, "")

	resp := do(t, "GET", url+"/checkout", "", nil)
	if got := readBody(t, resp); got != `{"account_number":"4111111111111111"}` {
		t.Fatalf("body = %q, want the decrypted account number", got)
	}
	if s.MissCount() != 0 {
		t.Fatalf("MissCount = %d, want 0", s.MissCount())
	}
}

// TestServerLeavesAnUnencryptedResponseAloneWithNoDataKey guards against a
// regression where decryptExchange's "ok" check fires on ordinary
// responses that were never encrypted in the first place.
func TestServerLeavesAnUnencryptedResponseAloneWithNoDataKey(t *testing.T) {
	b := bundleOf(exch("GET", "/cart", "", nil, 200, `{"items":[]}`, 1))
	s, url := serve(t, b, Options{}, "")

	resp := do(t, "GET", url+"/cart", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := readBody(t, resp); got != `{"items":[]}` {
		t.Fatalf("body = %q, want unchanged", got)
	}
	if s.MissCount() != 0 {
		t.Fatalf("MissCount = %d, want 0", s.MissCount())
	}
}
