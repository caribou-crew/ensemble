package capture

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/reckey"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

func unsafeChainHop() trace.Hop {
	h := hop(1, "backend")
	h.From = "api"
	h.Resp = trace.Payload{Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"password":"synthetic-chain-canary","tail":"`, Truncated: true}
	return h
}

// External evidence may disprove capture trust, but never prove local
// reachability or supply timestamps to local gap detection.
func TestExternalRedactionFailureDegradesObservedWire(t *testing.T) {
	cwd := t.TempDir()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"ok":true}`) }))
	defer upstream.Close()
	s, err := StartStandalone(Options{Cwd: cwd, App: "web", Flow: "checkout", Upstream: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var source bytes.Buffer
	w := trace.NewWriter(&source)
	if err := w.Write(unsafeChainHop()); err != nil {
		t.Fatal(err)
	}
	later := hop(2, "other")
	later.T.Start = later.T.Start.Add(time.Hour)
	if err := w.Write(later); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(cwd, "source.jsonl")
	if err := os.WriteFile(sourcePath, source.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	src := HopSource{Kind: config.HopSourceFile, Dir: cwd, File: "source.jsonl"}
	exported, err := src.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordExternalHops(exported); err != nil {
		t.Fatal(err)
	}
	untouched, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(untouched, source.Bytes()) {
		t.Error("recording modified the source oracle")
	}
	chain := s.RecordedChain()
	if len(chain) != 2 {
		t.Fatalf("recorded chain count=%d, want 2", len(chain))
	}
	if chain[0].Resp.Body != "" || chain[0].Resp.BodyB64 != "" || !trace.HasRedactionFailure(chain[0]) {
		t.Error("RecordedChain exposes the unredacted external hop")
	}
	disk, skipped, err := runs.ReadHops(s.Paths.HopsPath)
	if err != nil || skipped != 0 || !reflect.DeepEqual(chain, disk) {
		t.Errorf("RecordedChain disagrees with persisted hops: skipped=%d err=%v", skipped, err)
	}
	before := Assess(AssessInput{Hops: s.Hops(), RequestsSeen: s.RequestsSeen(), Notes: s.TrustNotes()})
	if before.Status != trace.VerdictBroken {
		t.Errorf("external chain vouched for an unreached local proxy: %s", before.Status)
	}
	resp, err := http.Get(s.ProxyURL + "/observed")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	observed := s.Hops()
	if len(observed) != 1 || observed[0].Path != "/observed" {
		t.Fatalf("external chain replaced local observations: %+v", observed)
	}
	got := Assess(AssessInput{Hops: observed, RequestsSeen: s.RequestsSeen(), Notes: s.TrustNotes()})
	if got.Status != trace.VerdictDegraded {
		t.Errorf("lost external capture assessed as %s, want degraded", got.Status)
	}
	if len(got.Gaps) != 0 {
		t.Errorf("external timestamps polluted local gap evidence: %+v", got.Gaps)
	}
}

func TestAttachedStoresSanitizedChainWithoutDoubleEncryptingWire(t *testing.T) {
	t.Setenv(reckey.EnvTeamKey, hex.EncodeToString(bytes.Repeat([]byte{'k'}, reckey.KeySize)))
	edge := hop(1, "edge")
	edge.Resp.Body = `{"account_number":"synthetic-account"}`
	unsafe := unsafeChainHop()
	unsafe.Seq = 2
	f := &fakeEnsemble{hops: []trace.Hop{edge, unsafe}}
	s, err := StartAttached(Options{Cwd: t.TempDir(), App: "web", Flow: "checkout", Redact: encryptEntry("account_number")}, f, "edge")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	disk, skipped, err := runs.ReadHops(s.Paths.HopsPath)
	if err != nil || skipped != 0 || len(disk) != 2 {
		t.Fatalf("full chain: len=%d skipped=%d err=%v", len(disk), skipped, err)
	}
	if !reflect.DeepEqual(s.RecordedChain(), disk) {
		t.Error("attached session retained original unsanitized hops")
	}
	wire, skipped, err := runs.ReadHops(s.Paths.WirePath)
	if err != nil || skipped != 0 || len(wire) != 1 {
		t.Fatalf("wire subset: len=%d skipped=%d err=%v", len(wire), skipped, err)
	}
	for _, h := range []trace.Hop{disk[0], wire[0]} {
		clear, ok := trace.DecryptBody(h.Resp.Body, s.dataKey)
		if !ok || clear != `{"account_number":"synthetic-account"}` {
			t.Errorf("field was not encrypted exactly once: %q, ok=%v", clear, ok)
		}
	}
	got := Assess(AssessInput{Hops: s.Hops(), RequestsSeen: -1, Notes: s.TrustNotes()})
	if got.Status != trace.VerdictDegraded {
		t.Errorf("attached redaction failure assessed as %s", got.Status)
	}
}

func TestAttachedWriteFailureRetainsOnlySanitizedWrittenChain(t *testing.T) {
	f := &fakeEnsemble{hops: []trace.Hop{unsafeChainHop()}}
	s := attachedSessionFor(t, f)
	if err := s.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The full chain writes successfully, then the second output fails.
	if err := os.Mkdir(s.Paths.WirePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err == nil {
		t.Fatal("unwritable wire path was reported as success")
	}
	chain := s.RecordedChain()
	if len(chain) != 1 || chain[0].Resp.Body != "" || !trace.HasRedactionFailure(chain[0]) {
		t.Error("write failure exposed raw chain instead of persisted sanitized hops")
	}
	if !f.endCalled {
		t.Error("write failure skipped session teardown")
	}
}
