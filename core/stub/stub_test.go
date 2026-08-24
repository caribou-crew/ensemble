package stub

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/proxy"
)

func startStub(t *testing.T, rec *proxy.Recorder, routes []Route) string {
	t.Helper()
	s := New("aws-kms", routes, rec)
	addr, err := s.Serve("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return addr
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(body)
}

func TestStubServesCannedResponse(t *testing.T) {
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 8})
	addr := startStub(t, rec, []Route{{
		Match:   Match{Method: "POST", Path: "/encrypt"},
		Respond: Respond{Status: 200, Headers: map[string]string{"content-type": "application/json"}, Body: `{"ciphertext":"AAA="}`},
	}})

	resp, err := http.Post("http://"+addr+"/encrypt", "application/json", strings.NewReader(`{"plaintext":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != `{"ciphertext":"AAA="}` {
		t.Fatalf("got %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("content-type") != "application/json" {
		t.Fatalf("headers not applied: %v", resp.Header)
	}

	// The call must appear as a hop attributed to the stub.
	hops := rec.Snapshot()
	if len(hops) != 1 || hops[0].To != "aws-kms" || hops[0].Method != "POST" || hops[0].Status != 200 {
		t.Fatalf("stub hop wrong: %+v", hops)
	}
	if hops[0].Req.Body != `{"plaintext":"x"}` {
		t.Fatalf("request not captured: %+v", hops[0].Req)
	}
}

func TestStubBodyFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "resp.json")
	os.WriteFile(file, []byte(`{"fromFile":true}`), 0o644)

	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 8})
	addr := startStub(t, rec, []Route{{
		Match:   Match{Method: "GET", Path: "/f"},
		Respond: Respond{Status: 200, BodyFile: file},
	}})
	if status, body := get(t, "http://"+addr+"/f"); status != 200 || body != `{"fromFile":true}` {
		t.Fatalf("got %d %s", status, body)
	}
}

func TestStubTemplating(t *testing.T) {
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 8})
	addr := startStub(t, rec, []Route{{
		Match: Match{Method: "GET", Path: "/echo/*"},
		Respond: Respond{
			Status:   200,
			Body:     `{"path":"{{.Path}}","q":"{{.Query.Get "name"}}"}`,
			Template: true,
		},
	}})
	status, body := get(t, "http://"+addr+"/echo/hi?name=steven")
	if status != 200 || body != `{"path":"/echo/hi","q":"steven"}` {
		t.Fatalf("got %d %s", status, body)
	}
}

// TestStubRequestBodyCappedAtCaptureLimit guards final-review finding I5:
// the stub capture path did a bare io.ReadAll(r.Body) and stored the whole
// thing in the hop, with no cap — unlike core/proxy, which caps captured
// bodies at proxy.CaptureLimit (256KB) regardless of Redactor config.
// runUp disables the Redactor's own cap on the (proxy-only) assumption
// that capping already happened upstream, so a >256KB POST to a stub would
// retain the full body in the recorder ring, hops.jsonl, and every
// /api/traffic response. The stub must cap independently, at the same
// limit.
func TestStubRequestBodyCappedAtCaptureLimit(t *testing.T) {
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 8})
	addr := startStub(t, rec, []Route{{
		Match:   Match{Method: "POST", Path: "/encrypt"},
		Respond: Respond{Status: 200, Body: `{"ok":true}`},
	}})

	big := strings.Repeat("a", proxy.CaptureLimit+1024)
	resp, err := http.Post("http://"+addr+"/encrypt", "application/octet-stream", strings.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	hops := rec.Snapshot()
	if len(hops) != 1 {
		t.Fatalf("want 1 hop, got %d", len(hops))
	}
	h := hops[0]
	if !h.Req.Truncated {
		t.Fatalf("request body over CaptureLimit not marked Truncated: len=%d", len(h.Req.Body))
	}
	if len(h.Req.Body) > proxy.CaptureLimit {
		t.Fatalf("captured request body len = %d, want <= %d", len(h.Req.Body), proxy.CaptureLimit)
	}
}

// TestStubResponseBodyCappedAtCaptureLimit covers the other half of I5: a
// stub's own *rendered* response body (from a config file or template) must
// also be capped in the hop, independent of what's actually written to the
// client.
func TestStubResponseBodyCappedAtCaptureLimit(t *testing.T) {
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 8})
	bigBody := strings.Repeat("b", proxy.CaptureLimit+1024)
	addr := startStub(t, rec, []Route{{
		Match:   Match{Method: "GET", Path: "/big"},
		Respond: Respond{Status: 200, Body: bigBody},
	}})

	status, body := get(t, "http://"+addr+"/big")
	if status != 200 || len(body) != len(bigBody) {
		t.Fatalf("client response must NOT be capped: status=%d len=%d, want %d", status, len(body), len(bigBody))
	}

	hops := rec.Snapshot()
	if len(hops) != 1 {
		t.Fatalf("want 1 hop, got %d", len(hops))
	}
	h := hops[0]
	if !h.Resp.Truncated {
		t.Fatalf("response body over CaptureLimit not marked Truncated: len=%d", len(h.Resp.Body))
	}
	if len(h.Resp.Body) > proxy.CaptureLimit {
		t.Fatalf("captured response body len = %d, want <= %d", len(h.Resp.Body), proxy.CaptureLimit)
	}
}

func TestStubWildcardAndMethodMatching(t *testing.T) {
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 8})
	addr := startStub(t, rec, []Route{
		{Match: Match{Method: "GET", Path: "/v1/*"}, Respond: Respond{Status: 200, Body: "wild"}},
		{Match: Match{Path: "/any-method"}, Respond: Respond{Status: 200, Body: "any"}},
	})
	if status, body := get(t, "http://"+addr+"/v1/deep/thing"); status != 200 || body != "wild" {
		t.Fatalf("wildcard: %d %s", status, body)
	}
	resp, _ := http.Post("http://"+addr+"/any-method", "text/plain", nil)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "any" {
		t.Fatalf("empty method must match any: %s", b)
	}
	if status, _ := get(t, "http://"+addr+"/v1x"); status != 404 {
		t.Fatalf("prefix must respect the slash boundary: %d", status)
	}
}

func TestStubUnmatchedIs404AndStillRecorded(t *testing.T) {
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 8})
	addr := startStub(t, rec, []Route{{
		Match:   Match{Method: "POST", Path: "/only"},
		Respond: Respond{Status: 200, Body: "ok"},
	}})
	status, body := get(t, "http://"+addr+"/nope")
	if status != 404 || !strings.Contains(body, "no stub route") {
		t.Fatalf("got %d %s", status, body)
	}
	hops := rec.Snapshot()
	if len(hops) != 1 || hops[0].Status != 404 || hops[0].To != "aws-kms" {
		t.Fatalf("unmatched call not recorded: %+v", hops)
	}
}

// TestStubCloseReleasesPortSynchronously guards a real flake seen in
// orchestrator reconcile: Serve() hands the listener to http.Server via
// `go s.srv.Serve(ln)`, which only registers (and therefore only guarantees
// closing) the listener once that goroutine actually runs. Close() calling
// only srv.Close() can return before that goroutine is even scheduled, so
// the OS socket may still be bound when Close() returns — an immediate
// re-listen on the same port then fails with "address already in use",
// exactly what a config-reconcile restart-on-same-port does.
func TestStubCloseReleasesPortSynchronously(t *testing.T) {
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 8})
	for i := 0; i < 50; i++ {
		s := New("aws-kms", []Route{{
			Match:   Match{Method: "GET", Path: "/x"},
			Respond: Respond{Status: 200, Body: "ok"},
		}}, rec)
		addr, err := s.Serve("127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("iteration %d: port %s not free immediately after Close: %v", i, addr, err)
		}
		ln.Close()
	}
}

// TestStubTraceHeaderAdoptsCustomTraceID mirrors core/proxy's
// TestProxyTraceHeaderAdoptsCustomTraceID: a stub is as much a real hop as
// any proxied service, so a call with no traceparent but carrying the
// stack's own correlation header must land in that trace, not a fresh one.
func TestStubTraceHeaderAdoptsCustomTraceID(t *testing.T) {
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 8})
	s := New("aws-kms", []Route{{
		Match:   Match{Method: "GET", Path: "/x"},
		Respond: Respond{Status: 200, Body: "ok"},
	}}, rec)
	s.TraceHeader = "x-local-trace-id"
	addr, err := s.Serve("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	req, _ := http.NewRequest("GET", "http://"+addr+"/x", nil)
	req.Header.Set("x-local-trace-id", "company-corr-stub-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	hops := rec.Snapshot()
	if len(hops) != 1 || hops[0].TraceID != "company-corr-stub-1" {
		t.Fatalf("hop.TraceID = %q, want the custom header's value: %+v", hops[0].TraceID, hops)
	}
}
