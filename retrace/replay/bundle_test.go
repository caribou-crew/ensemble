package replay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// bundle_test.go drives LoadBundle over a real on-disk bundle written the
// way production writes one — runs.Create for the layout, trace.Writer for
// wire.jsonl, runs.WriteManifest for the manifest — rather than over a
// hand-populated Bundle. A matcher fed hand-built exchanges is a fine test
// of the matcher and no test at all of the lowering, which is where the
// wiring defects live.

// writeBundle lays down a bundle directory and returns it.
func writeBundle(t *testing.T, wire runs.Counts, hops []trace.Hop) string {
	t.Helper()
	root := t.TempDir()
	p, err := runs.Create(root, "web", "checkout", "20260101T000000Z-abcdef1")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(p.WirePath)
	if err != nil {
		t.Fatal(err)
	}
	w := trace.NewWriter(f)
	for _, h := range hops {
		if err := w.Write(h); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()

	m := runs.Manifest{
		App: "web", Flow: "checkout", RunID: "20260101T000000Z-abcdef1",
		Mode:      runs.ModeStandalone,
		StartedAt: time.Unix(0, 0).UTC(), FinishedAt: time.Unix(1, 0).UTC(),
		Capture: runs.CaptureTrust{Status: trace.VerdictOK, Summary: "capture looks complete"},
		Wire:    wire,
	}
	if err := runs.WriteManifest(p, &m); err != nil {
		t.Fatal(err)
	}
	return p.RunDir
}

func hop(seq uint64, method, path string, reqBody string, status int, respBody string) trace.Hop {
	return trace.Hop{
		Seq: seq, To: "client-edge", Method: method, Path: path, Status: status,
		Req:  trace.Payload{Body: reqBody},
		Resp: trace.Payload{Headers: map[string]string{"Content-Type": "application/json"}, Body: respBody},
	}
}

func TestLoadBundleLowersRecordedHopsIntoExchanges(t *testing.T) {
	dir := writeBundle(t, runs.Counts{Calls: 2, Recorded: true}, []trace.Hop{
		hop(1, "GET", "/cart?page=2&fresh=1", "", 200, `{"items":[]}`),
		hop(2, "post", "/checkout", `{"pay":"card"}`, 201, `{"ok":true}`),
	})

	b, err := LoadBundle(dir)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}
	if len(b.Exchanges) != 2 {
		t.Fatalf("exchanges = %d, want 2", len(b.Exchanges))
	}
	// Recorded ORDER is preserved — the repeat counter depends on it.
	if b.Exchanges[0].Seq != 1 || b.Exchanges[1].Seq != 2 {
		t.Fatalf("seqs = %d,%d, want 1,2 in recorded order", b.Exchanges[0].Seq, b.Exchanges[1].Seq)
	}
	// trace.Hop.Path is a RequestURI: the query belongs in Key.Query, not
	// glued onto the path, or every replayed call with a query misses.
	if got := b.Exchanges[0].Key; got.Path != "/cart" || got.Query != "page=2&fresh=1" {
		t.Fatalf("key = %+v, want path /cart and query page=2&fresh=1", got)
	}
	if b.Exchanges[0].Body != `{"items":[]}` || b.Exchanges[0].Status != 200 {
		t.Fatalf("exchange 0 = %+v, want the recorded status and body verbatim", b.Exchanges[0])
	}
	if b.Exchanges[0].Headers["Content-Type"] != "application/json" {
		t.Fatalf("headers = %v, want the recorded response headers", b.Exchanges[0].Headers)
	}
	if got := b.Exchanges[1].Key.Method; got != "POST" {
		t.Fatalf("method = %q, want it upper-cased to POST", got)
	}
	// The request body is DECODED, because matching is structural.
	body, ok := b.Exchanges[1].ReqBody.(map[string]any)
	if !ok || body["pay"] != "card" {
		t.Fatalf("reqBody = %#v, want the decoded {pay:card}", b.Exchanges[1].ReqBody)
	}
	if b.Manifest.App != "web" || b.Manifest.Flow != "checkout" {
		t.Fatalf("manifest = %+v, want the bundle's own manifest", b.Manifest)
	}
	if b.Dir != dir {
		t.Fatalf("dir = %q, want %q", b.Dir, dir)
	}
}

func TestLoadBundleRefusesAWirePlaneThatWasNeverRecorded(t *testing.T) {
	// The zero-value clause, pinned. runs.Counts' zero value is
	// Recorded:false — "unknown, refuse" — and a bundle carrying it has no
	// contract in it. Serving from that bundle would 501 every call, which
	// reads in CI as "the client deviated on everything" when the truth is
	// "nobody ever recorded what the client should do".
	//
	// The hop is present on purpose: the refusal must come from the
	// manifest's claim, not from an empty exchange table, or this test
	// would be indistinguishable from the empty-bundle one below.
	dir := writeBundle(t, runs.Counts{Recorded: false, Reason: "the proxy never started"},
		[]trace.Hop{hop(1, "GET", "/cart", "", 200, `{}`)})

	_, err := LoadBundle(dir)
	if err == nil {
		t.Fatal("LoadBundle accepted a bundle whose wire plane says it was not recorded")
	}
	if !strings.Contains(err.Error(), "the proxy never started") {
		t.Fatalf("error %q does not carry the recorded reason", err)
	}
}

func TestLoadBundleRefusesACorruptWireLine(t *testing.T) {
	// runs.ReadHops is fail-open by design (a half-written record must not
	// discard its neighbours). For replay that policy would turn a dropped
	// line into an accusation against the client, so the loader refuses
	// instead. Both arms: the same bundle loads fine without the bad line.
	dir := writeBundle(t, runs.Counts{Calls: 2, Recorded: true}, []trace.Hop{
		hop(1, "GET", "/cart", "", 200, `{}`),
		hop(2, "GET", "/checkout", "", 200, `{}`),
	})
	if _, err := LoadBundle(dir); err != nil {
		t.Fatalf("the intact bundle did not load: %v", err)
	}

	wire := filepath.Join(dir, "wire.jsonl")
	f, err := os.OpenFile(wire, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("{\"schema\":\"ensemble/1\",\"method\":\"GET\"\n") // truncated, as a killed writer leaves it
	f.Close()

	_, err = LoadBundle(dir)
	if err == nil {
		t.Fatal("LoadBundle accepted a wire.jsonl with an unreadable line — every dropped line is an exchange this server would then call a client deviation")
	}
	if !strings.Contains(err.Error(), "unreadable line") {
		t.Fatalf("error %q does not name the corruption", err)
	}
}

func TestLoadBundleRefusesABundleWithNoExchanges(t *testing.T) {
	dir := writeBundle(t, runs.Counts{Calls: 0, Recorded: true}, nil)
	_, err := LoadBundle(dir)
	if err == nil {
		t.Fatal("LoadBundle accepted a bundle with no exchanges — a server that answers every call with a miss is a broken mock, not a strict one")
	}
	if !strings.Contains(err.Error(), "records no exchanges") {
		t.Fatalf("error %q does not explain the refusal", err)
	}
}

func TestLoadBundleRefusesAnUnreadableManifest(t *testing.T) {
	// A bundle DIRECTORY that exists is a committed artifact; a manifest
	// that will not read makes it corrupt, not absent — the same rule
	// refs.Resolve applies (Task 11), inherited rather than re-decided.
	dir := writeBundle(t, runs.Counts{Calls: 1, Recorded: true},
		[]trace.Hop{hop(1, "GET", "/cart", "", 200, `{}`)})
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBundle(dir); err == nil {
		t.Fatal("LoadBundle accepted a corrupt manifest")
	}

	if err := os.Remove(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBundle(dir); err == nil {
		t.Fatal("LoadBundle accepted a bundle with no manifest at all")
	}
}

func TestLoadBundleRefusesAnEmptyDirectoryArgument(t *testing.T) {
	// "" would resolve to the process working directory. A bundle is never
	// that, and the zero value of a path must not address something real.
	if _, err := LoadBundle(""); err == nil {
		t.Fatal("LoadBundle(\"\") was accepted")
	}
}

func TestLoadBundleRefusesAnExchangeRecordedWithContentEncoding(t *testing.T) {
	// The bytes in the bundle are NOT what that header describes. core/proxy
	// forwards the client's Accept-Encoding verbatim and Go's transport only
	// auto-decompresses when it added the header itself, so a browser-driven
	// capture records raw gzip bytes into a Go string — and encoding/json
	// replaces every invalid UTF-8 byte with U+FFFD when wire.jsonl is
	// written. Re-asserting `Content-Encoding: gzip` over that hands the
	// client a body it cannot decode, on a HIT, where the miss machinery
	// never sees it: a green gate over a response nobody could read.
	//
	// Refused rather than stripped. Stripping serves a mangled body as if it
	// were fine — plausible, and therefore worse (the third zero-value
	// clause). This package refuses rather than degrades.
	h := hop(1, "GET", "/cart", "", 200, "\x1f\x8b\x08garbage")
	h.Resp.Headers["Content-Encoding"] = "gzip"
	dir := writeBundle(t, runs.Counts{Calls: 1, Recorded: true}, []trace.Hop{h})

	_, err := LoadBundle(dir)
	if err == nil {
		t.Fatal("LoadBundle accepted an exchange recorded with Content-Encoding — replay would re-assert it over bytes that are no longer compressed")
	}
	if !strings.Contains(err.Error(), "Content-Encoding") || !strings.Contains(err.Error(), "/cart") {
		t.Fatalf("error %q does not name the header and the exchange", err)
	}

	// Both mirrors, so the refusal is about a CLAIMED encoding and not
	// about response headers in general: the same bundle with no
	// Content-Encoding loads, and so does one that explicitly says
	// "identity" — the header's own no-op, which is not a claim about the
	// bytes.
	clean := hop(2, "GET", "/cart", "", 200, `{"items":[]}`)
	if _, err := LoadBundle(writeBundle(t, runs.Counts{Calls: 1, Recorded: true}, []trace.Hop{clean})); err != nil {
		t.Fatalf("a bundle with no Content-Encoding did not load: %v", err)
	}
	identity := hop(3, "GET", "/cart", "", 200, `{"items":[]}`)
	identity.Resp.Headers["Content-Encoding"] = "identity"
	if _, err := LoadBundle(writeBundle(t, runs.Counts{Calls: 1, Recorded: true}, []trace.Hop{identity})); err != nil {
		t.Fatalf("a bundle recording Content-Encoding: identity did not load: %v", err)
	}
}

func TestLoadBundleRefusesATruncatedRecordedRequestBody(t *testing.T) {
	// A size-capped request body has no trustworthy tail, so there are only
	// two things a matcher could do with it and both are wrong: treat it as
	// no constraint (every client body matches — F3's wildcard) or match it
	// verbatim (every correct client is reported as a deviation). The
	// bundle is refused instead, alongside the corrupt-line refusal.
	h := hop(1, "POST", "/checkout", `{"pay":"ca`, 201, `{"ok":true}`)
	h.Req.Truncated = true
	dir := writeBundle(t, runs.Counts{Calls: 1, Recorded: true}, []trace.Hop{h})

	_, err := LoadBundle(dir)
	if err == nil {
		t.Fatal("LoadBundle accepted a truncated recorded request body")
	}
	if !strings.Contains(err.Error(), "TRUNCATED") || !strings.Contains(err.Error(), "/checkout") {
		t.Fatalf("error %q does not name the problem and the exchange", err)
	}

	// The mirror: the same body, not truncated, loads and constrains.
	whole := hop(1, "POST", "/checkout", `{"pay":"card"}`, 201, `{"ok":true}`)
	if _, err := LoadBundle(writeBundle(t, runs.Counts{Calls: 1, Recorded: true}, []trace.Hop{whole})); err != nil {
		t.Fatalf("an untruncated bundle did not load: %v", err)
	}
}
