package trace

// Ported from local-stack web/src/trace/export.test.ts, adapted to the
// ensemble/1 hop schema (query string lives in Path; timings carry
// first-byte; bodies are already-serialized strings).

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
)

func exportHop(over func(*Hop)) Hop {
	h := Hop{
		From: "client", To: "api-b",
		Method: "GET", Path: "/api/v1/transactions", Status: 200,
		T: Timings{
			Start:  time.Date(2026, 8, 13, 22, 22, 40, 847_000_000, time.UTC),
			DoneMs: 505,
		},
		Req:  Payload{Headers: map[string]string{"host": "localhost:4000", "user-agent": "curl/8.7.1"}},
		Resp: Payload{Headers: map[string]string{"content-type": "application/json"}},
	}
	if over != nil {
		over(&h)
	}
	return h
}

func TestCurlUsesHostHeaderAsAuthority(t *testing.T) {
	// `to` is a logical service name — not resolvable. The Host header is
	// the only thing in the record that produces a replayable URL.
	out := ToCurl(exportHop(nil))
	if !strings.Contains(out, "'http://localhost:4000/api/v1/transactions'") {
		t.Fatalf("authority wrong: %s", out)
	}
	if strings.Contains(out, "api-b") {
		t.Fatalf("service name leaked into URL: %s", out)
	}
}

func TestCurlFallsBackToServiceName(t *testing.T) {
	out := ToCurl(exportHop(func(h *Hop) { h.Req.Headers = map[string]string{} }))
	if !strings.Contains(out, "'http://api-b/api/v1/transactions'") {
		t.Fatalf("fallback wrong: %s", out)
	}
}

func TestCurlKeepsQueryInPath(t *testing.T) {
	out := ToCurl(exportHop(func(h *Hop) { h.Path = "/api/v1/tx?a=1" }))
	if got := strings.Count(out, "a=1"); got != 1 {
		t.Fatalf("query appears %d times: %s", got, out)
	}
}

func TestCurlMethodFlag(t *testing.T) {
	if strings.Contains(ToCurl(exportHop(nil)), "-X GET") {
		t.Fatal("plain GET must omit -X")
	}
	if !strings.Contains(ToCurl(exportHop(func(h *Hop) { h.Method = "POST" })), "-X POST") {
		t.Fatal("POST must include -X")
	}
}

func TestCurlShellQuoteEscaping(t *testing.T) {
	out := ToCurl(exportHop(func(h *Hop) { h.Req.Headers = map[string]string{"x-note": "it's here"} }))
	if !strings.Contains(out, `'x-note: it'\''s here'`) {
		t.Fatalf("quote escaping broken: %s", out)
	}
}

func TestCurlBody(t *testing.T) {
	withBody := ToCurl(exportHop(func(h *Hop) { h.Method = "POST"; h.Req.Body = `{"amount":100}` }))
	if !strings.Contains(withBody, `--data-raw '{"amount":100}'`) {
		t.Fatalf("body missing: %s", withBody)
	}
	if strings.Contains(ToCurl(exportHop(func(h *Hop) { h.Method = "POST" })), "--data-raw") {
		t.Fatal("bodyless request must omit --data-raw")
	}
}

func TestRawRequestShape(t *testing.T) {
	out := ToRawRequest(exportHop(func(h *Hop) { h.Method = "POST"; h.Req.Body = `{"a":1}` }))
	lines := strings.Split(out, "\n")
	if lines[0] != "POST /api/v1/transactions HTTP/1.1" {
		t.Fatalf("request line: %q", lines[0])
	}
	if !strings.Contains(out, "host: localhost:4000") {
		t.Fatal("header missing")
	}
	blank := -1
	for i, l := range lines {
		if l == "" {
			blank = i
			break
		}
	}
	if blank <= 0 || strings.Join(lines[blank+1:], "\n") != `{"a":1}` {
		t.Fatalf("body separation wrong: %q", out)
	}
}

func TestRawRequestNoBodyNoSeparator(t *testing.T) {
	if out := ToRawRequest(exportHop(nil)); strings.HasSuffix(out, "\n\n") {
		t.Fatal("bodyless request should not advertise an empty body")
	}
}

func TestRawResponseStatusLine(t *testing.T) {
	out := ToRawResponse(exportHop(func(h *Hop) { h.Status = 404; h.Resp.Body = `{"error":"nope"}` }))
	if first := strings.Split(out, "\n")[0]; first != "HTTP/1.1 404 Not Found" {
		t.Fatalf("status line: %q", first)
	}
	odd := ToRawResponse(exportHop(func(h *Hop) { h.Status = 599 }))
	if first := strings.Split(odd, "\n")[0]; first != "HTTP/1.1 599" {
		t.Fatalf("unknown status line: %q", first)
	}
}

func TestHarLogShape(t *testing.T) {
	har := ToHar([]Hop{exportHop(nil), exportHop(func(h *Hop) { h.Method = "POST"; h.Status = 201 })})
	if har.Log.Version != "1.2" || har.Log.Creator.Name != "ensemble" || len(har.Log.Entries) != 2 {
		t.Fatalf("log shape: %+v", har.Log)
	}
	empty := ToHar(nil)
	if empty.Log.Version != "1.2" || len(empty.Log.Entries) != 0 {
		t.Fatal("empty list must still be a valid log")
	}
	// Entries must marshal with a JSON array even when empty.
	b, _ := json.Marshal(empty)
	if !strings.Contains(string(b), `"entries":[]`) {
		t.Fatalf("entries not an array: %s", b)
	}
}

func TestHarEntryRequiredFields(t *testing.T) {
	entry := ToHar([]Hop{exportHop(nil)}).Log.Entries[0]
	if entry.StartedDateTime != "2026-08-13T22:22:40.847Z" {
		t.Fatalf("startedDateTime: %q", entry.StartedDateTime)
	}
	if entry.Time != 505 || entry.Request.Method != "GET" ||
		entry.Request.URL != "http://localhost:4000/api/v1/transactions" ||
		entry.Response.Status != 200 {
		t.Fatalf("entry: %+v", entry)
	}
	// No first-byte measured: total goes to wait, unmeasured legs are the
	// spec's -1 ("not applicable"), never 0 (a lie).
	if entry.Timings.Send != -1 || entry.Timings.Wait != 505 || entry.Timings.Receive != -1 {
		t.Fatalf("timings: %+v", entry.Timings)
	}
}

func TestHarTimingsSplitWhenFirstByteKnown(t *testing.T) {
	entry := ToHar([]Hop{exportHop(func(h *Hop) { h.T.FirstByteMs = 100 })}).Log.Entries[0]
	if entry.Timings.Wait != 100 || entry.Timings.Receive != 405 {
		t.Fatalf("timings: %+v", entry.Timings)
	}
}

func TestHarQueryPairs(t *testing.T) {
	entry := ToHar([]Hop{exportHop(func(h *Hop) { h.Path = "/api/v1/tx?account_token=019fed39&limit=10" })}).Log.Entries[0]
	want := []HarNameValue{{"account_token", "019fed39"}, {"limit", "10"}}
	if len(entry.Request.QueryString) != 2 || entry.Request.QueryString[0] != want[0] || entry.Request.QueryString[1] != want[1] {
		t.Fatalf("queryString: %+v", entry.Request.QueryString)
	}
}

func TestHarHeadersAndBodies(t *testing.T) {
	entry := ToHar([]Hop{exportHop(func(h *Hop) {
		h.Method = "POST"
		h.Req.Body = `{"a":1}`
		h.Resp.Body = `{"b":2}`
	})}).Log.Entries[0]
	// Headers are sorted by name for deterministic output.
	if len(entry.Request.Headers) != 2 || entry.Request.Headers[0] != (HarNameValue{"host", "localhost:4000"}) ||
		entry.Request.Headers[1] != (HarNameValue{"user-agent", "curl/8.7.1"}) {
		t.Fatalf("headers: %+v", entry.Request.Headers)
	}
	if entry.Request.PostData == nil || entry.Request.PostData.Text != `{"a":1}` ||
		entry.Request.PostData.MimeType != "application/json" {
		t.Fatalf("postData: %+v", entry.Request.PostData)
	}
	if entry.Response.Content.Text != `{"b":2}` || entry.Response.Content.Size != 7 {
		t.Fatalf("content: %+v", entry.Response.Content)
	}
}

func TestHarOmitsPostDataWhenNoBody(t *testing.T) {
	entry := ToHar([]Hop{exportHop(nil)}).Log.Entries[0]
	if entry.Request.PostData != nil {
		t.Fatalf("postData should be nil: %+v", entry.Request.PostData)
	}
	b, _ := json.Marshal(entry)
	if strings.Contains(string(b), "postData") {
		t.Fatalf("postData serialized: %s", b)
	}
}

func TestHarContentMimeTypeFollowsHeader(t *testing.T) {
	entry := ToHar([]Hop{exportHop(func(h *Hop) {
		h.Resp.Headers = map[string]string{"content-type": "text/plain"}
		h.Resp.Body = "hi"
	})}).Log.Entries[0]
	if entry.Response.Content.MimeType != "text/plain" {
		t.Fatalf("mimeType: %q", entry.Response.Content.MimeType)
	}
}

var curlStartRe = regexp.MustCompile(`^curl( -X [A-Z]+)? '`)

func TestCurlAlwaysStartsSane(t *testing.T) {
	if !curlStartRe.MatchString(ToCurl(exportHop(nil))) {
		t.Fatal("curl output malformed")
	}
}
