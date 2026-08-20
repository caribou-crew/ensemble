package stub

import (
	"io"
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
