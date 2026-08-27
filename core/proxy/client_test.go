package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// hdr builds a header set from k/v pairs.
func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestNoClientHeaderRecordsNoClient(t *testing.T) {
	// "" and FallbackClient are different facts and must stay that way: no
	// header at all is the overwhelming majority of traffic, and a
	// misconfigured app hidden inside it would never be found.
	p := &Proxy{}
	if got := p.clientIdentity(hdr("x-other", "web")); got != "" {
		t.Errorf("clientIdentity = %q, want \"\"", got)
	}
}

func TestTheDefaultHeadersAreCheckedInOrder(t *testing.T) {
	p := &Proxy{}
	if got := p.clientIdentity(hdr("x-source-client", "web")); got != "web" {
		t.Errorf("x-source-client: got %q, want \"web\"", got)
	}
	if got := p.clientIdentity(hdr("x-local-client", "admin")); got != "admin" {
		t.Errorf("x-local-client: got %q, want \"admin\"", got)
	}
	// Both present: the FIRST configured header wins, or the order of the
	// list is decoration.
	both := hdr("x-source-client", "web", "x-local-client", "admin")
	if got := p.clientIdentity(both); got != "web" {
		t.Errorf("both present: got %q, want \"web\" — the first entry wins", got)
	}
}

func TestAConfiguredListReplacesTheDefaultsRatherThanExtendingThem(t *testing.T) {
	// Same contract as SourceHeaders. A list that quietly kept checking the
	// built-ins underneath would make a stack with its own convention still
	// pick up a header it deliberately did not name.
	p := &Proxy{ClientHeaders: []string{"x-app"}}
	if got := p.clientIdentity(hdr("x-app", "ios")); got != "ios" {
		t.Errorf("got %q, want \"ios\"", got)
	}
	if got := p.clientIdentity(hdr("x-source-client", "web")); got != "" {
		t.Errorf("got %q, want \"\" — the default list was replaced, not extended", got)
	}
}

func TestHeaderLookupIsCaseInsensitive(t *testing.T) {
	// http.Header.Get canonicalizes, and the config documents the lookup as
	// case-insensitive. A stack sending X-Source-Client must not silently
	// record nothing.
	p := &Proxy{}
	h := http.Header{}
	h.Set("X-Source-Client", "web")
	if got := p.clientIdentity(h); got != "web" {
		t.Errorf("got %q, want \"web\"", got)
	}
}

func TestAMalformedValueIsReplacedNeverStored(t *testing.T) {
	// The whole reason Client is validated: whatever a browser puts in that
	// header would otherwise reach hops.jsonl, the traffic UI, and any
	// group-by built on it.
	bad := []string{
		"Web",                               // upper case
		"-web",                              // leading punctuation
		":web",                              // leading punctuation
		"web app",                           // space
		"web/1.0",                           // slash
		"<script>",                          // markup
		strings.Repeat("a", 33),             // one over the cap
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXV", // a token in the wrong header
	}
	for _, v := range bad {
		p := &Proxy{}
		got := p.clientIdentity(hdr("x-source-client", v))
		if got != FallbackClient {
			t.Errorf("clientIdentity(%q) = %q, want %q", v, got, FallbackClient)
		}
	}
}

func TestTheAcceptedShapeIsExactlyWhatTheDocsPromise(t *testing.T) {
	good := []string{"web", "a", "0", "web-admin", "team:web", strings.Repeat("a", 32)}
	for _, v := range good {
		p := &Proxy{}
		if got := p.clientIdentity(hdr("x-source-client", v)); got != v {
			t.Errorf("clientIdentity(%q) = %q, want it accepted unchanged", v, got)
		}
	}
}

func TestAMalformedFirstHeaderIsNotRepairedByASecondGoodOne(t *testing.T) {
	// First PRESENT wins, not first valid. Falling through would silently
	// repair a misconfigured app: the report looks clean while the value the
	// team believes they send is discarded, and nobody is ever told.
	p := &Proxy{}
	got := p.clientIdentity(hdr("x-source-client", "Web App", "x-local-client", "admin"))
	if got != FallbackClient {
		t.Errorf("got %q, want %q — the malformed first header must not fall through", got, FallbackClient)
	}
}

func TestAMalformedValueWarnsOnceForThatValue(t *testing.T) {
	var got []string
	p := &Proxy{OnWarn: func(m string) { got = append(got, m) }}
	for i := 0; i < 5; i++ {
		p.clientIdentity(hdr("x-source-client", "Web App"))
	}
	if len(got) != 1 {
		t.Fatalf("warned %d times, want 1:\n%s", len(got), strings.Join(got, "\n"))
	}
	for _, want := range []string{"x-source-client", "Web App", FallbackClient} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the warning must name %q, got:\n%s", want, got[0])
		}
	}
}

func TestADifferentMalformedValueWarnsAgain(t *testing.T) {
	// The amendment to "a one-time warning". `ensemble up` runs for hours: a
	// second app's mistake, or the same app after a failed fix, must not be
	// silently swallowed because an unrelated value was warned about first.
	var got []string
	p := &Proxy{OnWarn: func(m string) { got = append(got, m) }}
	p.clientIdentity(hdr("x-source-client", "Web App"))
	p.clientIdentity(hdr("x-source-client", "iOS App"))
	p.clientIdentity(hdr("x-local-client", "Web App")) // same value, other header
	if len(got) != 3 {
		t.Fatalf("warned %d times, want 3:\n%s", len(got), strings.Join(got, "\n"))
	}
}

func TestWarningsStopAtTheCapRatherThanGrowingWithoutBound(t *testing.T) {
	// A request id sent in the wrong header varies per request. Without the
	// cap that is both a log flood and a map that grows for the life of the
	// process, keyed on attacker-controlled input.
	var n int
	p := &Proxy{OnWarn: func(string) { n++ }}
	for i := 0; i < badClientWarnCap*3; i++ {
		p.clientIdentity(hdr("x-source-client", strings.Repeat("A", i+1)))
	}
	if n != badClientWarnCap {
		t.Errorf("warned %d times, want the cap of %d", n, badClientWarnCap)
	}
	if len(p.warnedClients) > badClientWarnCap {
		t.Errorf("the warned set grew to %d, past its own cap", len(p.warnedClients))
	}
}

func TestTheWarningTruncatesTheOffendingValue(t *testing.T) {
	// A header this is the wrong place for often holds a token. Echoing one
	// in full would move a secret into a log file; 32 bytes is the length a
	// VALID identity could have had, so nothing legitimate is ever cut.
	secret := strings.Repeat("s", 200)
	var got string
	p := &Proxy{OnWarn: func(m string) { got = m }}
	p.clientIdentity(hdr("x-source-client", secret))
	if strings.Contains(got, secret) {
		t.Error("the warning echoed the whole value")
	}
	if !strings.Contains(got, "…") {
		t.Errorf("a truncated value must say so, got:\n%s", got)
	}
}

func TestTheWarningStripsControlCharacters(t *testing.T) {
	// The value is attacker-controlled and the sink is a terminal. An escape
	// sequence here would let a header rewrite what an operator sees.
	var got string
	p := &Proxy{OnWarn: func(m string) { got = m }}
	p.clientIdentity(hdr("x-source-client", "we\x1b[2Jb"))
	if strings.Contains(got, "\x1b") {
		t.Errorf("the warning passed an escape sequence through:\n%q", got)
	}
}

func TestNoWarnSinkIsNotACrash(t *testing.T) {
	// OnWarn nil is the default for every embedder that has no logger.
	p := &Proxy{}
	if got := p.clientIdentity(hdr("x-source-client", "Web App")); got != FallbackClient {
		t.Errorf("got %q, want %q", got, FallbackClient)
	}
}

func TestAValidClientNeverWarns(t *testing.T) {
	var n int
	p := &Proxy{OnWarn: func(string) { n++ }}
	p.clientIdentity(hdr("x-source-client", "web"))
	p.clientIdentity(hdr("x-other", "anything at all"))
	if n != 0 {
		t.Errorf("warned %d times on well-formed traffic", n)
	}
}

// TestAClientIdentityLandsOnTheRecordedHop is the one that says the field is
// actually wired rather than merely resolvable. Every test above calls
// clientIdentity directly; if the call site were deleted from the hop
// construction they would all still pass.
func TestAClientIdentityLandsOnTheRecordedHop(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	defer p.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	addr, err := p.Serve(Target{Name: "svc", Listen: "127.0.0.1:0", Upstream: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", "http://"+addr+"/x", nil)
	req.Header.Set("x-source-client", "web")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	hops := rec.Snapshot()
	if len(hops) != 1 {
		t.Fatalf("recorded %d hops, want 1", len(hops))
	}
	if hops[0].Client != "web" {
		t.Errorf("hop.Client = %q, want \"web\"", hops[0].Client)
	}
}

// TestClientAndFromStayIndependent pins the reason these are two fields.
// SourceHeaders answers "which service called this hop" and is a FALLBACK
// consulted only when trace context cannot say; ClientHeaders answers "which
// front-end started this" and is read unconditionally. A request carrying
// real trace context must still record its client — collapsing the two would
// lose exactly that case, which is the common one in a traced stack.
func TestClientAndFromStayIndependent(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	p.SourceHeaders = []string{"x-ensemble-caller"}
	defer p.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	addr, err := p.Serve(Target{Name: "svc", Listen: "127.0.0.1:0", Upstream: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", "http://"+addr+"/x", nil)
	req.Header.Set("x-ensemble-caller", "some-tool")
	req.Header.Set("x-source-client", "web")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	h := rec.Snapshot()[0]
	if h.Client != "web" {
		t.Errorf("hop.Client = %q, want \"web\"", h.Client)
	}
	if h.From != "some-tool" {
		t.Errorf("hop.From = %q, want \"some-tool\" — the two fields must not overwrite each other", h.From)
	}
}
