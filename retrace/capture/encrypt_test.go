package capture

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/reckey"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

func encryptEntry(field string) []config.RedactEntry {
	return []config.RedactEntry{{Field: field, Mode: "encrypt"}}
}

func TestCaptureWithEncryptModeFieldWritesMarkerAndSidecar(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"account_number":"4111111111111111"}`))
	}))
	defer upstream.Close()

	cwd := t.TempDir()
	teamKey := make([]byte, reckey.KeySize)
	for i := range teamKey {
		teamKey[i] = 'k'
	}
	t.Setenv(reckey.EnvTeamKey, hex.EncodeToString(teamKey))

	s, err := StartStandalone(Options{
		Cwd: cwd, App: "web", Flow: "checkout", Upstream: upstream.URL,
		Redact: encryptEntry("account_number"),
		Now:    func() time.Time { return time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("StartStandalone: %v", err)
	}
	resp, err := http.Get(s.ProxyURL + "/checkout")
	if err != nil {
		t.Fatalf("through proxy: %v", err)
	}
	resp.Body.Close()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	hops, skipped, err := runs.ReadHops(s.Paths.WirePath)
	if err != nil || skipped != 0 || len(hops) != 1 {
		t.Fatalf("wire.jsonl hops = %v (%v)", hops, err)
	}
	if strings.Contains(hops[0].Resp.Body, "4111111111111111") {
		t.Fatal("plaintext account number reached disk")
	}
	if !strings.Contains(hops[0].Resp.Body, trace.EncryptedPrefix) {
		t.Fatalf("no encrypted marker in body: %s", hops[0].Resp.Body)
	}

	enc, err := runs.ReadEncryption(s.Paths)
	if err != nil {
		t.Fatalf("ReadEncryption: %v", err)
	}
	if enc == nil {
		t.Fatal("expected an encryption.json sidecar for a run with an encrypt-mode field")
	}
	teamKeyOut, _, err := reckey.LoadTeamKey(cwd)
	if err != nil {
		t.Fatalf("LoadTeamKey: %v", err)
	}
	dataKey, err := reckey.UnwrapDataKey(enc.WrappedDataKey, teamKeyOut)
	if err != nil {
		t.Fatalf("UnwrapDataKey: %v", err)
	}

	var body struct {
		AccountNumber string `json:"account_number"`
	}
	if err := json.Unmarshal([]byte(hops[0].Resp.Body), &body); err != nil {
		t.Fatalf("unmarshal hop body: %v", err)
	}
	got, err := trace.DecryptField(dataKey, body.AccountNumber)
	if err != nil {
		t.Fatalf("DecryptField: %v", err)
	}
	if got != `"4111111111111111"` {
		t.Fatalf("decrypted value = %q, want the original field's JSON encoding", got)
	}
}

func TestCaptureWithEncryptModeFieldAndNoTeamKeyFailsFastWithNoRunDir(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"account_number":"4111"}`))
	}))
	defer upstream.Close()

	cwd := t.TempDir()
	_, err := StartStandalone(Options{
		Cwd: cwd, App: "web", Flow: "checkout", Upstream: upstream.URL,
		Redact: encryptEntry("account_number"),
	})
	if err == nil {
		t.Fatal("expected StartStandalone to fail with an encrypt-mode field and no team key")
	}

	entries, rerr := os.ReadDir(runs.RunsRoot(cwd))
	if rerr == nil && len(entries) != 0 {
		t.Fatalf("no run directory should be left behind on a missing-key failure, found: %v", entries)
	}
}

func TestCaptureWithOnlyDestroyAndDisplayFieldsWritesNoEncryptionSidecar(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"password":"x","display_name":"Ada"}`))
	}))
	defer upstream.Close()

	cwd := t.TempDir()
	s, err := StartStandalone(Options{
		Cwd: cwd, App: "web", Flow: "checkout", Upstream: upstream.URL,
		Redact: []config.RedactEntry{{Field: "password", Mode: "destroy"}, {Field: "display_name", Mode: "display"}},
		Now:    func() time.Time { return time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("StartStandalone: %v", err)
	}
	resp, err := http.Get(s.ProxyURL + "/checkout")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if enc, err := runs.ReadEncryption(s.Paths); err != nil || enc != nil {
		t.Fatalf("expected no encryption.json, got %+v (err %v)", enc, err)
	}
}
