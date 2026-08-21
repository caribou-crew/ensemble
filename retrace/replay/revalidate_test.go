package replay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/retrace/rules"
)

func recordedExchange(method, path, query, reqRaw string, reqHeaders map[string]string, status int, body string, seq uint64) Exchange {
	return Exchange{
		Key:        Key{Method: method, Path: path, Query: query},
		ReqRaw:     reqRaw,
		ReqHeaders: reqHeaders,
		Status:     status,
		Body:       body,
		Seq:        seq,
	}
}

func TestRevalidateReportsAChangedResponseShapePerField(t *testing.T) {
	// The spec's "stale recording detected" scenario: the live stack still
	// answers, but the payload has moved on. The report must name the
	// field, not merely say the bodies differ.
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"total":199,"currency":"EUR"}`))
	}))
	defer live.Close()

	b := bundleOf(recordedExchange("GET", "/cart", "", "", nil, 200, `{"total":199,"currency":"USD"}`, 1))
	rep, err := Revalidate(context.Background(), b, live.URL, Options{})
	if err != nil {
		t.Fatalf("Revalidate: %v", err)
	}
	if rep.Checked != 1 {
		t.Fatalf("Checked = %d, want 1", rep.Checked)
	}
	if rep.Verdict != VerdictDrift {
		t.Fatalf("verdict = %q, want %q", rep.Verdict, VerdictDrift)
	}
	if len(rep.Drifts) != 1 {
		t.Fatalf("drifts = %+v, want exactly one", rep.Drifts)
	}
	d := rep.Drifts[0]
	if d.Method != "GET" || d.Path != "/cart" {
		t.Fatalf("drift = %+v, want it to name GET /cart", d)
	}
	if d.Status != nil {
		t.Fatalf("status drift = %+v, want nil — the status did not change", d.Status)
	}
	if len(d.Fields) != 1 {
		t.Fatalf("fields = %+v, want exactly the one field that moved", d.Fields)
	}
	if d.Fields[0].Path != "currency" || d.Fields[0].A != "USD" || d.Fields[0].B != "EUR" {
		t.Fatalf("field drift = %+v, want currency USD -> EUR", d.Fields[0])
	}
}

func TestRevalidateReportsAChangedStatusAsAHardGate(t *testing.T) {
	// The other arm of the same mechanism (see global-constraints.md on
	// mutating both arms): a status that moved, and moved to a 4xx the
	// recording never saw, is a gate rather than a field diff.
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer live.Close()

	b := bundleOf(recordedExchange("GET", "/cart", "", "", nil, 200, `{"total":199}`, 1))
	rep, err := Revalidate(context.Background(), b, live.URL, Options{})
	if err != nil {
		t.Fatalf("Revalidate: %v", err)
	}
	if rep.Verdict != VerdictFailed {
		t.Fatalf("verdict = %q, want %q — an unexpected >=400 from the live stack is a hard gate", rep.Verdict, VerdictFailed)
	}
	if len(rep.Drifts) != 1 || rep.Drifts[0].Status == nil {
		t.Fatalf("drifts = %+v, want one carrying a status drift", rep.Drifts)
	}
	if got := rep.Drifts[0].Status; got.Recorded != 200 || got.Live != 404 {
		t.Fatalf("status drift = %+v, want recorded 200 live 404", got)
	}
	if ExitCode(rep) != 2 {
		t.Fatalf("ExitCode = %d, want 2", ExitCode(rep))
	}
}

func TestRevalidateDoesNotFlagRuleMatchedVolatileFields(t *testing.T) {
	// Same rules the wire diff uses: a rule-matched volatile field is not
	// drift. Both arms — with the rule it is clean, without it the very
	// same pair of responses is drift, so this cannot pass by simply never
	// comparing bodies.
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"requestId":"11112222-3333-4444-8555-666677778888","total":199}`))
	}))
	defer live.Close()
	recorded := `{"requestId":"6f1a2b3c-4d5e-4f60-8a71-9b2c3d4e5f60","total":199}`

	rs, err := rules.Normalize([]rules.Raw{{Path: "/cart", Body: map[string]any{"requestId": "uuid"}}})
	if err != nil {
		t.Fatal(err)
	}
	b := bundleOf(recordedExchange("GET", "/cart", "", "", nil, 200, recorded, 1))
	rep, err := Revalidate(context.Background(), b, live.URL, Options{Rules: rs})
	if err != nil {
		t.Fatalf("Revalidate: %v", err)
	}
	if rep.Verdict != VerdictClean || len(rep.Drifts) != 0 {
		t.Fatalf("verdict = %q drifts = %+v, want a clean report — a uuid under a uuid rule is not drift", rep.Verdict, rep.Drifts)
	}
	if ExitCode(rep) != 0 {
		t.Fatalf("ExitCode = %d, want 0", ExitCode(rep))
	}

	b2 := bundleOf(recordedExchange("GET", "/cart", "", "", nil, 200, recorded, 1))
	rep, err = Revalidate(context.Background(), b2, live.URL, Options{})
	if err != nil {
		t.Fatalf("Revalidate: %v", err)
	}
	if rep.Verdict != VerdictDrift {
		t.Fatalf("verdict = %q without the rule, want %q — otherwise the clean arm above proves nothing", rep.Verdict, VerdictDrift)
	}
	if ExitCode(rep) != 1 {
		t.Fatalf("ExitCode = %d, want 1", ExitCode(rep))
	}
}

func TestRevalidateSendsTheRecordedRequestBodyAndHeaders(t *testing.T) {
	type seen struct {
		method, path, query, body, tenant, auth string
		hits                                    int
	}
	var got seen
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got = seen{
			method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, body: string(raw),
			tenant: r.Header.Get("X-Tenant"), auth: r.Header.Get("Authorization"), hits: got.hits + 1,
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer live.Close()

	b := bundleOf(recordedExchange("POST", "/orders", "dry=1", `{"sku":"a"}`, map[string]string{
		"X-Tenant":      "acme",
		"Content-Type":  "application/json",
		"Authorization": "[redacted]",
	}, 200, `{"ok":true}`, 1))

	rep, err := Revalidate(context.Background(), b, live.URL, Options{})
	if err != nil {
		t.Fatalf("Revalidate: %v", err)
	}
	if rep.Verdict != VerdictClean {
		t.Fatalf("verdict = %q, want clean: %+v", rep.Verdict, rep.Drifts)
	}
	if got.hits != 1 {
		t.Fatalf("live stack saw %d requests, want 1", got.hits)
	}
	if got.method != "POST" || got.path != "/orders" || got.query != "dry=1" {
		t.Fatalf("live stack saw %s %s?%s, want POST /orders?dry=1", got.method, got.path, got.query)
	}
	if got.body != `{"sku":"a"}` {
		t.Fatalf("live stack saw body %q, want the recorded request body — revalidating a POST with an empty body proves nothing", got.body)
	}
	if got.tenant != "acme" {
		t.Fatalf("X-Tenant = %q, want the recorded request header replayed", got.tenant)
	}
	// A redacted value is not a credential. Re-sending the literal
	// "[redacted]" would earn a 401 that revalidate would then report as
	// drift the recording never caused.
	if got.auth != "" {
		t.Fatalf("Authorization = %q, want the redacted header dropped rather than sent verbatim", got.auth)
	}
}

func TestRevalidateRefusesAnUnreachableUpstreamRatherThanReportingClean(t *testing.T) {
	// "Could not evaluate" is not "nothing differed". An upstream that
	// never answered must surface as an error, never as a clean report.
	b := bundleOf(recordedExchange("GET", "/cart", "", "", nil, 200, `{}`, 1))
	// Port 0 is never listening.
	_, err := Revalidate(context.Background(), b, "http://127.0.0.1:1", Options{})
	if err == nil {
		t.Fatal("Revalidate reported success against an upstream that is not there")
	}
}

func TestAnEmptyVerdictIsNeverExitZero(t *testing.T) {
	// The zero-value clause: a RevalReport nobody filled in must not exit
	// 0. "" is not "clean" — it is "this report was never produced".
	if got := ExitCode(RevalReport{}); got != 3 {
		t.Fatalf("ExitCode(RevalReport{}) = %d, want 3 (could not evaluate) — an unset verdict must never read as clean", got)
	}
	if got := ExitCode(RevalReport{Verdict: "banana"}); got != 3 {
		t.Fatalf("ExitCode of an unknown verdict = %d, want 3", got)
	}
}

func TestRevalidateRefusesAnEmptyUpstream(t *testing.T) {
	b := bundleOf(recordedExchange("GET", "/cart", "", "", nil, 200, `{}`, 1))
	if _, err := Revalidate(context.Background(), b, "  ", Options{}); err == nil {
		t.Fatal("Revalidate accepted an empty upstream")
	}
	if _, err := Revalidate(context.Background(), nil, "http://127.0.0.1:1", Options{}); err == nil {
		t.Fatal("Revalidate accepted a nil bundle")
	}
}

func TestRevalidateReportsEveryExchangeNotJustTheFirst(t *testing.T) {
	// Structural symmetry guard: a loop that returned after the first
	// exchange would pass every single-exchange fixture above.
	var paths []string
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/orders" {
			w.Write([]byte(`{"orders":["moved"]}`))
			return
		}
		w.Write([]byte(`{"items":[]}`))
	}))
	defer live.Close()

	b := bundleOf(
		recordedExchange("GET", "/cart", "", "", nil, 200, `{"items":[]}`, 1),
		recordedExchange("GET", "/orders", "", "", nil, 200, `{"orders":[]}`, 2),
	)
	rep, err := Revalidate(context.Background(), b, live.URL, Options{})
	if err != nil {
		t.Fatalf("Revalidate: %v", err)
	}
	if rep.Checked != 2 {
		t.Fatalf("Checked = %d, want 2", rep.Checked)
	}
	if strings.Join(paths, ",") != "/cart,/orders" {
		t.Fatalf("live stack saw %v, want both recorded calls in recorded order", paths)
	}
	if len(rep.Drifts) != 1 || rep.Drifts[0].Path != "/orders" {
		t.Fatalf("drifts = %+v, want the second exchange's drift alone", rep.Drifts)
	}
}

func TestARecordedFourHundredThatIsStillAFourHundredIsNotAGate(t *testing.T) {
	// The mirror of the hard-gate arm (global-constraints.md: mutate both
	// arms in the same breath). "Unexpected >=400" means the RECORDING did
	// not already see one — a recorded 404 that is still a 404 is the
	// contract holding, not a failure. A gate written as `status >= 400`
	// alone would pass every fixture in the other direction and fail here.
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"no such cart"}`))
	}))
	defer live.Close()

	b := bundleOf(recordedExchange("GET", "/cart/999", "", "", nil, 404, `{"error":"no such cart"}`, 1))
	rep, err := Revalidate(context.Background(), b, live.URL, Options{})
	if err != nil {
		t.Fatalf("Revalidate: %v", err)
	}
	if rep.Verdict != VerdictClean {
		t.Fatalf("verdict = %q, want clean — a recorded 404 that is still a 404 is the recording holding: %+v", rep.Verdict, rep.Drifts)
	}
	if ExitCode(rep) != 0 {
		t.Fatalf("ExitCode = %d, want 0", ExitCode(rep))
	}
}

func TestRevalidateReportsAFieldThatBrokeItsOwnRuleAsDrift(t *testing.T) {
	// A rule declared over a field is a claim about its SHAPE, and the
	// live stack breaking that shape is drift, not tolerance. DiffWire
	// files it under BodyViolations rather than BodyDiff, so a report that
	// only carried BodyDiff would call this clean — a rule the operator
	// wrote to excuse a volatile value would then also excuse the value
	// ceasing to be that kind of value at all.
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"requestId":"not-a-uuid-any-more","total":199}`))
	}))
	defer live.Close()

	rs, err := rules.Normalize([]rules.Raw{{Path: "/cart", Body: map[string]any{"requestId": "uuid"}}})
	if err != nil {
		t.Fatal(err)
	}
	b := bundleOf(recordedExchange("GET", "/cart", "", "", nil, 200,
		`{"requestId":"6f1a2b3c-4d5e-4f60-8a71-9b2c3d4e5f60","total":199}`, 1))

	rep, err := Revalidate(context.Background(), b, live.URL, Options{Rules: rs})
	if err != nil {
		t.Fatalf("Revalidate: %v", err)
	}
	if rep.Verdict != VerdictDrift {
		t.Fatalf("verdict = %q, want %q — a field that broke its own rule is drift", rep.Verdict, VerdictDrift)
	}
	if len(rep.Drifts) != 1 || len(rep.Drifts[0].Fields) != 1 || rep.Drifts[0].Fields[0].Path != "requestId" {
		t.Fatalf("drifts = %+v, want the requestId violation reported", rep.Drifts)
	}
}

func TestRevalidateReportsARedirectAsDriftInsteadOfFollowingIt(t *testing.T) {
	// A recorded call the live stack has since MOVED is drift — the most
	// interesting kind, because the recording is now describing a route
	// that no longer answers. http.DefaultClient would follow the 302 and
	// compare the recording against the redirect TARGET's response,
	// reporting "no drift" about a call that is gone; it also downgrades
	// POST to GET on 301/302/303, so a write endpoint would never be
	// exercised at all.
	var sawMovedPath, sawTarget atomic.Bool
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cart" {
			sawMovedPath.Store(true)
			http.Redirect(w, r, "/v2/cart", http.StatusFound)
			return
		}
		sawTarget.Store(true)
		w.Write([]byte(`{"items":[]}`))
	}))
	defer live.Close()

	b := bundleOf(exch("GET", "/cart", "", nil, 200, `{"items":[]}`, 1))
	rep, err := Revalidate(context.Background(), b, live.URL, Options{})
	if err != nil {
		t.Fatalf("Revalidate: %v", err)
	}
	if !sawMovedPath.Load() {
		t.Fatal("the recorded path was never requested")
	}
	if sawTarget.Load() {
		t.Fatal("revalidate followed the redirect — it compared the recording against the redirect target, not against what the recorded call now does")
	}
	if rep.Verdict != VerdictDrift {
		t.Fatalf("verdict = %q, want %q — a 302 is a finding, not a step", rep.Verdict, VerdictDrift)
	}
	if len(rep.Drifts) != 1 || rep.Drifts[0].Status == nil || rep.Drifts[0].Status.Live != http.StatusFound {
		t.Fatalf("drifts = %+v, want the 302 reported as a status change", rep.Drifts)
	}
}

func TestRevalidateGivesUpOnALiveStackThatNeverAnswers(t *testing.T) {
	// The one outcome a 0/1/2/3 contract cannot express is "still running".
	// A stack that accepts the connection and never answers would hang
	// `retrace revalidate` forever, and a CI job cannot tell that from a
	// slow build. Whatever comes back must be an ERROR — never a report,
	// and above all never a clean one.
	block := make(chan struct{})
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	// LIFO: the handler is released BEFORE Close, which waits for every
	// outstanding request to finish.
	defer live.Close()
	defer close(block)

	old := liveCallTimeout
	liveCallTimeout = 50 * time.Millisecond
	defer func() { liveCallTimeout = old }()

	b := bundleOf(exch("GET", "/cart", "", nil, 200, `{"items":[]}`, 1))
	done := make(chan struct{})
	var rep RevalReport
	var err error
	go func() {
		defer close(done)
		rep, err = Revalidate(context.Background(), b, live.URL, Options{})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Revalidate never returned against a stack that never answers — there is no deadline at any layer")
	}
	if err == nil {
		t.Fatalf("Revalidate returned a report (%+v) for a stack that never answered — could-not-evaluate must be an error", rep)
	}
	if rep.Verdict == VerdictClean {
		t.Fatalf("verdict = %q for a stack that never answered", rep.Verdict)
	}
}
