package replay

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/rules"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// TestABinaryBodyReplaysByteIdentical is the record→replay half of the
// binary contract: a hop captured as BodyB64 (here a PNG signature plus
// bytes that are not valid UTF-8) must load, and the replay server must
// serve the DECODED bytes — the client sees exactly what the upstream sent
// at record time, never base64 text.
func TestABinaryBodyReplaysByteIdentical(t *testing.T) {
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0xFF, 0xFE, 0x00}, []byte("pixels")...)
	h := trace.Hop{
		Seq: 1, To: "client-edge", Method: "GET", Path: "/logo.png", Status: 200,
		Resp: trace.Payload{
			Headers: map[string]string{"Content-Type": "image/png"},
			BodyB64: base64.StdEncoding.EncodeToString(png),
		},
	}
	dir := writeBundle(t, runs.Counts{Calls: 1, Recorded: true}, []trace.Hop{h})
	b, err := LoadBundle(dir, "", nil)
	if err != nil {
		t.Fatalf("LoadBundle refused a BodyB64 bundle: %v", err)
	}
	_, url := serve(t, b, Options{}, filepath.Join(t.TempDir(), "misses.jsonl"))

	resp := do(t, "GET", url+"/logo.png", "", nil)
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !bytes.Equal(got, png) {
		t.Fatalf("replay served status %d, %x — want 200 with the exact recorded bytes %x", resp.StatusCode, got, png)
	}
	if resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("Content-Type = %q, want the recorded image/png", resp.Header.Get("Content-Type"))
	}
}

// TestEverySetCookieReplaysAsItsOwnHeader: an exchange recorded with an
// ordered SetCookies list must be served as that many separate Set-Cookie
// headers — never the flattened Headers map's comma-join, which no cookie
// jar parses back apart (Expires dates contain commas).
func TestEverySetCookieReplaysAsItsOwnHeader(t *testing.T) {
	cookies := []string{
		"sid=abc; Path=/; HttpOnly",
		"pref=dark; Expires=Wed, 21 Oct 2026 07:28:00 GMT",
	}
	h := hop(1, "GET", "/login", "", 200, `{"ok":true}`)
	h.Resp.Headers["Set-Cookie"] = strings.Join(cookies, ", ")
	h.Resp.SetCookies = cookies
	dir := writeBundle(t, runs.Counts{Calls: 1, Recorded: true}, []trace.Hop{h})
	b, err := LoadBundle(dir, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, url := serve(t, b, Options{}, filepath.Join(t.TempDir(), "misses.jsonl"))

	resp := do(t, "GET", url+"/login", "", nil)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	got := resp.Header.Values("Set-Cookie")
	if len(got) != len(cookies) {
		t.Fatalf("Set-Cookie headers = %q, want the %d recorded cookies separately", got, len(cookies))
	}
	for i := range cookies {
		if got[i] != cookies[i] {
			t.Fatalf("Set-Cookie[%d] = %q, want %q (order preserved)", i, got[i], cookies[i])
		}
	}
}

// TestExcludeRuleDropsARefusableExchangeAtLoad: an exclude wire rule lets
// a bundle load past an exchange the loader would refuse outright (here a
// truncated response), and a live request for the excluded route then
// misses with the standard explained 501 — out of the contract, never
// answered wrong.
func TestExcludeRuleDropsARefusableExchangeAtLoad(t *testing.T) {
	bad := hop(1, "GET", "/metrics", "", 200, `{"cut":`)
	bad.Resp.Truncated = true
	good := hop(2, "GET", "/cart", "", 200, `{"items":[]}`)

	rs, err := rules.Normalize([]rules.Raw{{
		Method: "GET", Path: "/metrics", Exclude: true,
		Why: "prometheus scrape body blows the capture cap; not part of the contract",
	}})
	if err != nil {
		t.Fatal(err)
	}
	dir := writeBundle(t, runs.Counts{Calls: 2, Recorded: true}, []trace.Hop{bad, good})
	b, err := LoadBundle(dir, "", rs)
	if err != nil {
		t.Fatalf("LoadBundle with an exclude rule still refused: %v", err)
	}
	if len(b.Exchanges) != 1 || b.Exchanges[0].Key.Path != "/cart" {
		t.Fatalf("exchanges = %+v, want only /cart left", b.Exchanges)
	}

	_, url := serve(t, b, Options{}, filepath.Join(t.TempDir(), "misses.jsonl"))
	resp := do(t, "GET", url+"/metrics", "", nil)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("excluded route answered %d, want the standard 501 miss", resp.StatusCode)
	}
}

// TestUnexcludedRefusalPrintsTheExactExcludeRule: the refusal error names
// the exchange and carries the copy-pasteable rule that would exclude it —
// the error's own way out, with the mandatory why left to the operator.
func TestUnexcludedRefusalPrintsTheExactExcludeRule(t *testing.T) {
	bad := hop(1, "GET", "/metrics?probe=1", "", 200, `{"cut":`)
	bad.Resp.Truncated = true
	dir := writeBundle(t, runs.Counts{Calls: 1, Recorded: true}, []trace.Hop{bad})

	_, err := LoadBundle(dir, "", nil)
	if err == nil {
		t.Fatal("LoadBundle accepted a truncated exchange with no exclude rule")
	}
	msg := err.Error()
	for _, want := range []string{
		"/metrics",       // names the exchange
		"wire_rules:",    // the rule, in the config's own shape
		"method: GET",
		"path: /metrics", // path only — the query is not part of the rule
		"exclude: true",
		"why:",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error lacks %q:\n%s", want, msg)
		}
	}
}

// TestAllExchangesExcludedRefusesTheBundle: rules that exclude everything
// leave a server that can only miss — a broken mock, refused with a
// message naming the exclusion rules as the cause.
func TestAllExchangesExcludedRefusesTheBundle(t *testing.T) {
	rs, err := rules.Normalize([]rules.Raw{{Path: "/**", Exclude: true, Why: "testing the empty case"}})
	if err != nil {
		t.Fatal(err)
	}
	dir := writeBundle(t, runs.Counts{Calls: 1, Recorded: true}, []trace.Hop{
		hop(1, "GET", "/cart", "", 200, `{}`),
	})
	_, err = LoadBundle(dir, "", rs)
	if err == nil {
		t.Fatal("LoadBundle accepted a bundle with every exchange excluded")
	}
	if !strings.Contains(err.Error(), "exclusion rules") {
		t.Fatalf("error %q does not blame the exclusion rules", err)
	}
}
