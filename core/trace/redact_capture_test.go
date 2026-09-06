package trace_test

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/core/trace"
)

// This exercises capture before redaction: moving the guard after JSON
// parsing or ignoring BodyB64 would persist the synthetic password again.
func TestProxyDoesNotPersistUnsafeJSON(t *testing.T) {
	for _, side := range []string{"request", "response"} {
		for _, kind := range []string{"truncated", "gzip"} {
			t.Run(side+"/"+kind, func(t *testing.T) {
				body := []byte(`{"password":"synthetic-capture-canary","padding":"` + strings.Repeat("x", proxy.CaptureLimit) + `"}`)
				if kind == "gzip" {
					body = gzipCaptureFixture(t, []byte(`{"password":"synthetic-capture-canary"}`))
				}
				reqBody, respBody := []byte(`{"public":true}`), []byte(`{"public":true}`)
				if side == "request" {
					reqBody = body
				} else {
					respBody = body
				}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					got, err := io.ReadAll(r.Body)
					if err != nil || !bytes.Equal(got, reqBody) {
						t.Errorf("forwarded request differs: len=%d err=%v", len(got), err)
					}
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Content-Length", strconv.Itoa(len(respBody)))
					if kind == "gzip" && side == "response" {
						w.Header().Set("Content-Encoding", "gzip")
					}
					if _, err := w.Write(respBody); err != nil {
						t.Errorf("upstream write: %v", err)
					}
				}))
				defer upstream.Close()
				path := filepath.Join(t.TempDir(), "hops.jsonl")
				f, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				red, err := trace.NewRedactor(nil, 0, nil)
				if err != nil {
					t.Fatal(err)
				}
				rec := proxy.NewRecorder(proxy.RecorderOpts{Redactor: red, Writer: trace.NewWriter(f)})
				defer rec.Close()
				px := proxy.New(rec)
				defer px.Close()
				events, _, cancel := rec.Subscribe(0)
				defer cancel()
				addr, err := px.Serve(proxy.Target{Name: "fixture", Listen: "127.0.0.1:0", Upstream: upstream.URL})
				if err != nil {
					t.Fatal(err)
				}
				req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/fixture", bytes.NewReader(reqBody))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept-Encoding", "identity")
				req.Header.Set("Authorization", "synthetic-header-canary")
				if kind == "gzip" && side == "request" {
					req.Header.Set("Content-Encoding", "gzip")
				}
				tr := &http.Transport{DisableCompression: true}
				defer tr.CloseIdleConnections()
				resp, err := (&http.Client{Transport: tr}).Do(req)
				if err != nil {
					t.Fatal(err)
				}
				gotBody, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil || !bytes.Equal(gotBody, respBody) {
					t.Fatalf("forwarded response differs: len=%d err=%v", len(gotBody), err)
				}
				select {
				case <-events:
				case <-time.After(5 * time.Second):
					t.Fatal("capture timeout")
				}
				rec.Close()
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
				persisted, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				h, err := trace.NewReader(bytes.NewReader(persisted)).Next()
				if err != nil {
					t.Fatal(err)
				}
				if h.Req.Body != "" || h.Req.BodyB64 != "" || h.Resp.Body != "" || h.Resp.BodyB64 != "" {
					t.Errorf("unsafe capture persisted bodies: req=%d/%d resp=%d/%d", len(h.Req.Body), len(h.Req.BodyB64), len(h.Resp.Body), len(h.Resp.BodyB64))
				}
				if !strings.Contains(h.Err, "redaction failed: ") {
					t.Errorf("missing redaction failure: %q", h.Err)
				}
				unsafe := h.Req
				if side == "response" {
					unsafe = h.Resp
				}
				if !unsafe.Truncated {
					t.Error("discarded capture must stay visibly incomplete")
				}
				if h.Req.Headers["authorization"] != trace.Redacted {
					t.Error("degraded hop leaked authorization")
				}
			})
		}
	}
}

func gzipCaptureFixture(t *testing.T, body []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	if _, err := z.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestUnsafeJSONBodyPolicy(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(gzipCaptureFixture(t, []byte(`{"password":"synthetic-canary"}`)))
	cases := []struct {
		name    string
		payload trace.Payload
		off     bool
		rules   []trace.KeyRule
		wantErr bool
	}{
		{name: "JSON suffix with parameters", payload: trace.Payload{Headers: map[string]string{"content-type": "Application/Problem+JSON; charset=utf-8"}, Body: `{"password":"canary"`, Truncated: true}, wantErr: true},
		{name: "JSON without content type", payload: trace.Payload{Body: `  [{"password":"canary"`, Truncated: true}, wantErr: true},
		{name: "binary JSON without content encoding", payload: trace.Payload{Headers: map[string]string{"Content-Type": "application/json"}, BodyB64: encoded}, wantErr: true},
		{name: "ordinary truncated text", payload: trace.Payload{Headers: map[string]string{"Content-Type": "text/plain"}, Body: "plain text", Truncated: true}},
		{name: "complete non JSON text", payload: trace.Payload{Headers: map[string]string{"Content-Type": "text/plain"}, Body: `{not JSON`}},
		{name: "ordinary binary", payload: trace.Payload{Headers: map[string]string{"Content-Type": "image/png"}, BodyB64: encoded, Truncated: true}},
		{name: "defaults off", payload: trace.Payload{Headers: map[string]string{"Content-Type": "application/json"}, BodyB64: encoded}, off: true},
		{name: "custom destroy survives opt out", payload: trace.Payload{Headers: map[string]string{"Content-Type": "application/json"}, BodyB64: encoded}, off: true, rules: []trace.KeyRule{{Key: "private", Mode: trace.ModeDestroy}}, wantErr: true},
		{name: "custom encrypt survives opt out", payload: trace.Payload{Headers: map[string]string{"Content-Type": "application/json"}, BodyB64: encoded}, off: true, rules: []trace.KeyRule{{Key: "private", Mode: trace.ModeEncrypt}}, wantErr: true},
		{name: "display only opt out", payload: trace.Payload{Headers: map[string]string{"Content-Type": "application/json"}, BodyB64: encoded}, off: true, rules: []trace.KeyRule{{Key: "password", Mode: trace.ModeDisplay}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := trace.NewRedactor(c.rules, 0, make([]byte, 32))
			if err != nil {
				t.Fatal(err)
			}
			r.SetBodyDefaults(!c.off)
			got, err := r.Payload(c.payload)
			if (err != nil) != c.wantErr {
				t.Fatalf("error=%v, want failure=%v", err, c.wantErr)
			}
			if c.wantErr {
				if got.Body != "" || got.BodyB64 != "" || !got.Truncated {
					t.Fatalf("unsafe payload retained: %+v", got)
				}
			} else if got.Body != c.payload.Body || got.BodyB64 != c.payload.BodyB64 || got.Truncated != c.payload.Truncated {
				t.Fatalf("compatible payload altered: %+v", got)
			}
		})
	}
}
