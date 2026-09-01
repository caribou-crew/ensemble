package replay

import (
	"encoding/json"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/rules"
)

// match_test.go pins the matcher on its own, with hand-built exchanges,
// because it is a pure function over a candidate table and every branch it
// has is reachable from that table. The WIRING — that a real bundle lowers
// into these exchanges, and that the server answers from them — is pinned
// where it belongs: bundle_test.go drives LoadBundle over a real
// wire.jsonl, and cmd_replay_test.go drives the whole thing through the
// built binary. See global-constraints.md on tests of hypotheticals.

func exch(method, path, query string, reqBody any, status int, body string, seq uint64) Exchange {
	return Exchange{
		Key:     Key{Method: method, Path: path, Query: query},
		ReqBody: reqBody,
		Status:  status,
		Body:    body,
		Seq:     seq,
	}
}

func bundleOf(exs ...Exchange) *Bundle { return &Bundle{Exchanges: exs} }

// obj decodes a JSON literal into the `any` shape LoadBundle produces for a
// recorded request body — so a fixture body is built the way production
// builds one, never as a hand-written map[string]any that json.Unmarshal
// would never emit (e.g. an int where production always has a float64).
func obj(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("obj(%q): %v", s, err)
	}
	return v
}

func fieldNamed(t *testing.T, diff []MissField, name string) MissField {
	t.Helper()
	for _, f := range diff {
		if f.Field == name {
			return f
		}
	}
	t.Fatalf("diff has no field %q: %+v", name, diff)
	return MissField{}
}

func TestMatchesOnMethodPathThenQueryThenBodySubset(t *testing.T) {
	b := bundleOf(
		exch("GET", "/cart", "", nil, 200, `{"items":[]}`, 1),
		exch("POST", "/cart", "", obj(t, `{"sku":"a"}`), 201, `{"added":"a"}`, 2),
		exch("POST", "/cart", "", obj(t, `{"sku":"b"}`), 201, `{"added":"b"}`, 3),
		exch("GET", "/items", "page=2", nil, 200, `{"page":2}`, 4),
	)

	// Method discriminates: GET /cart must not be answered by either
	// POST /cart, even though the path is identical.
	got := b.Match(Request{Method: "GET", Path: "/cart"}, Options{})
	if got.Miss || got.Hit == nil {
		t.Fatalf("GET /cart missed: %+v", got)
	}
	if got.Hit.Body != `{"items":[]}` {
		t.Fatalf("GET /cart served %q, want the GET exchange's body", got.Hit.Body)
	}

	// Body subset picks between two same-method same-path exchanges, and
	// an extra key the recording never saw does not prevent the match.
	got = b.Match(Request{Method: "POST", Path: "/cart", Body: obj(t, `{"sku":"b","qty":1}`)}, Options{})
	if got.Miss || got.Hit == nil {
		t.Fatalf("POST /cart {sku:b} missed: %+v", got)
	}
	if got.Hit.Body != `{"added":"b"}` {
		t.Fatalf("POST /cart {sku:b} served %q, want the sku=b exchange — the body decides which of two same-path exchanges answers", got.Hit.Body)
	}

	// Query discriminates: the same method+path with the recorded query.
	got = b.Match(Request{Method: "GET", Path: "/items", Query: "page=2"}, Options{})
	if got.Miss || got.Hit == nil {
		t.Fatalf("GET /items?page=2 missed: %+v", got)
	}
	if got.Hit.Status != 200 || got.Hit.Body != `{"page":2}` {
		t.Fatalf("GET /items?page=2 served %+v", got.Hit)
	}
}

func TestAnExtraApiCallNotInTheReferenceIsAMiss(t *testing.T) {
	// The spec's "client deviation caught in CI" scenario. Absence is never
	// agreement: the call the recording never saw comes back as a miss, not
	// as the nearest plausible answer.
	b := bundleOf(
		exch("GET", "/cart", "", nil, 200, `{"items":[]}`, 1),
		exch("POST", "/checkout", "", obj(t, `{"pay":"card"}`), 200, `{"ok":true}`, 2),
	)

	got := b.Match(Request{Method: "DELETE", Path: "/admin/purge"}, Options{})
	if !got.Miss {
		t.Fatalf("DELETE /admin/purge was not a miss: %+v", got)
	}
	if got.Hit != nil {
		t.Fatalf("a miss carried a Hit (%+v) — an unmatched call must never be answered from the table", got.Hit)
	}
}

func TestAMissNamesTheNearestExchangeAndTheFieldsThatDiffered(t *testing.T) {
	// Three exchanges, and in every case below the true nearest is NOT the
	// first entry: a findNearest that always returned exchanges[0] would
	// pass a one-or-two-exchange fixture and fail here.
	b := bundleOf(
		exch("GET", "/zzz/unrelated", "", nil, 200, `{}`, 1),
		exch("GET", "/orders", "page=2", nil, 200, `{"page":2}`, 2),
		exch("POST", "/orders", "", obj(t, `{"sku":"a","qty":2}`), 201, `{"id":7}`, 3),
	)

	t.Run("no fixture for the path names the path and NOT the matching method", func(t *testing.T) {
		got := b.Match(Request{Method: "GET", Path: "/order"}, Options{})
		if !got.Miss {
			t.Fatalf("expected a miss, got %+v", got)
		}
		if got.Nearest == nil {
			t.Fatal("miss named no nearest exchange — a 501 with nothing to compare against is not debuggable")
		}
		if got.Nearest.Key.Path != "/orders" || got.Nearest.Key.Method != "GET" {
			t.Fatalf("nearest = %+v, want GET /orders (one character away), not the first entry", got.Nearest.Key)
		}
		p := fieldNamed(t, got.Diff, "path")
		if p.Expected != "/orders" || p.Actual != "/order" {
			t.Fatalf("path diff = %+v, want expected /orders actual /order", p)
		}
		// "method: expected GET, got GET" is a reported difference that is
		// not one, in the body a human reads to act on. The mirror arm
		// below — same path, different verb — is what keeps this from
		// being satisfied by never reporting the method at all.
		for _, f := range got.Diff {
			if f.Field == "method" {
				t.Fatalf("the diff reports %+v, but the method matched — a miss must name what actually differed", f)
			}
		}
	})

	t.Run("a method the recording never used names the method", func(t *testing.T) {
		// The mirror arm of the case above: same path, different verb. A
		// matcher that compared only paths would answer this from the GET.
		got := b.Match(Request{Method: "PATCH", Path: "/orders", Query: "page=2"}, Options{})
		if !got.Miss {
			t.Fatalf("PATCH /orders was answered from a GET/POST table: %+v", got)
		}
		m := fieldNamed(t, got.Diff, "method")
		if m.Actual != "PATCH" {
			t.Fatalf("method diff = %+v, want actual PATCH", m)
		}
	})

	t.Run("path matches and query differs names the query", func(t *testing.T) {
		got := b.Match(Request{Method: "GET", Path: "/orders", Query: "page=3"}, Options{})
		if !got.Miss {
			t.Fatalf("expected a miss, got %+v", got)
		}
		if got.Nearest == nil || got.Nearest.Key.Query != "page=2" {
			t.Fatalf("nearest = %+v, want the recorded page=2 exchange", got.Nearest)
		}
		q := fieldNamed(t, got.Diff, "query")
		if q.Expected != "page=2" || q.Actual != "page=3" {
			t.Fatalf("query diff = %+v, want expected page=2 actual page=3", q)
		}
		for _, f := range got.Diff {
			if f.Field == "path" || f.Field == "method" {
				t.Fatalf("query miss also reported %q — the diff must name what actually differed, not everything", f.Field)
			}
		}
	})

	t.Run("query matches and body differs is a field-level diff", func(t *testing.T) {
		got := b.Match(Request{Method: "POST", Path: "/orders", Body: obj(t, `{"sku":"a","qty":9}`)}, Options{})
		if !got.Miss {
			t.Fatalf("expected a miss, got %+v", got)
		}
		q := fieldNamed(t, got.Diff, "qty")
		if q.Expected != "2" || q.Actual != "9" {
			t.Fatalf("qty diff = %+v, want expected 2 actual 9", q)
		}
		for _, f := range got.Diff {
			if f.Field == "sku" {
				t.Fatalf("body diff named sku, which matched on both sides: %+v", got.Diff)
			}
		}
	})

	t.Run("a key the request omits entirely is named as absent", func(t *testing.T) {
		got := b.Match(Request{Method: "POST", Path: "/orders", Body: obj(t, `{"sku":"a"}`)}, Options{})
		if !got.Miss {
			t.Fatalf("a request missing a recorded key was matched: %+v", got)
		}
		q := fieldNamed(t, got.Diff, "qty")
		if q.Expected != "2" || q.Actual == "2" {
			t.Fatalf("qty diff = %+v, want the recorded 2 as expected and something else as actual", q)
		}
	})
}

func TestQueryIgnoreMakesAVolatileParamIrrelevant(t *testing.T) {
	b := bundleOf(exch("GET", "/feed", "_=111&page=2", nil, 200, `{"feed":[]}`, 1))

	// Both arms, in the same breath (see global-constraints.md on
	// mutation-set symmetry): with the ignore it hits, without it misses.
	// One arm alone cannot tell "ignore works" from "query is never
	// compared" or from "ignore is a no-op".
	got := b.Match(Request{Method: "GET", Path: "/feed", Query: "page=2&_=999"}, Options{QueryIgnore: []string{"_"}})
	if got.Miss {
		t.Fatalf("a volatile param under queryIgnore still missed: %+v", got.Diff)
	}

	b2 := bundleOf(exch("GET", "/feed", "_=111&page=2", nil, 200, `{"feed":[]}`, 1))
	got = b2.Match(Request{Method: "GET", Path: "/feed", Query: "page=2&_=999"}, Options{})
	if !got.Miss {
		t.Fatal("without queryIgnore the differing param was matched anyway — the ignore list is doing nothing")
	}

	// And a param NOT on the ignore list is still significant, so the
	// ignore is scoped to what it names rather than disarming the query.
	b3 := bundleOf(exch("GET", "/feed", "_=111&page=2", nil, 200, `{"feed":[]}`, 1))
	got = b3.Match(Request{Method: "GET", Path: "/feed", Query: "page=5&_=999"}, Options{QueryIgnore: []string{"_"}})
	if !got.Miss {
		t.Fatal("page=5 matched a recorded page=2 under queryIgnore [_] — the ignore disarmed the whole query")
	}
}

func TestWireRulesDecideEquivalenceNotByteEquality(t *testing.T) {
	rs, err := rules.Normalize([]rules.Raw{{Path: "/orders", Body: map[string]any{"requestId": "uuid"}}})
	if err != nil {
		t.Fatal(err)
	}
	recorded := obj(t, `{"requestId":"6f1a2b3c-4d5e-4f60-8a71-9b2c3d4e5f60","sku":"a"}`)
	fresh := `{"requestId":"11112222-3333-4444-8555-666677778888","sku":"a"}`

	b := bundleOf(exch("POST", "/orders", "", recorded, 201, `{"id":7}`, 1))
	got := b.Match(Request{Method: "POST", Path: "/orders", Body: obj(t, fresh)}, Options{Rules: rs})
	if got.Miss {
		t.Fatalf("a fresh uuid under a uuid rule missed: %+v", got.Diff)
	}

	// Without the rule the same pair of bodies is a miss — otherwise this
	// test would pass just as well against a matcher that never compared
	// request bodies at all.
	b2 := bundleOf(exch("POST", "/orders", "", recorded, 201, `{"id":7}`, 1))
	got = b2.Match(Request{Method: "POST", Path: "/orders", Body: obj(t, fresh)}, Options{})
	if !got.Miss {
		t.Fatal("two different uuids matched with no rule in play — the body is not being compared")
	}

	// And the rule excuses the field it names, not the call: a real change
	// beside a rule-matched uuid is still a miss.
	b3 := bundleOf(exch("POST", "/orders", "", recorded, 201, `{"id":7}`, 1))
	got = b3.Match(Request{Method: "POST", Path: "/orders",
		Body: obj(t, `{"requestId":"11112222-3333-4444-8555-666677778888","sku":"CHANGED"}`)}, Options{Rules: rs})
	if !got.Miss {
		t.Fatal("a changed sku beside a rule-matched requestId was tolerated — the rule excused the whole body")
	}
	if f := fieldNamed(t, got.Diff, "sku"); f.Expected != `"a"` {
		t.Fatalf("sku diff = %+v", f)
	}
}

func TestRepeatedIdenticalCallsAreServedInRecordedOrder(t *testing.T) {
	// Two recorded GET /cart responses: empty, then one item. A poll-until-
	// ready flow that got the same first response forever would hang.
	b := bundleOf(
		exch("GET", "/cart", "", nil, 200, `{"items":[]}`, 1),
		exch("GET", "/cart", "", nil, 200, `{"items":[{"sku":"a"}]}`, 2),
	)
	first := b.Match(Request{Method: "GET", Path: "/cart"}, Options{})
	second := b.Match(Request{Method: "GET", Path: "/cart"}, Options{})
	if first.Miss || second.Miss {
		t.Fatalf("a repeated recorded call missed: %+v %+v", first, second)
	}
	if first.Hit.Body != `{"items":[]}` {
		t.Fatalf("first call served %q, want the first recorded response", first.Hit.Body)
	}
	if second.Hit.Body != `{"items":[{"sku":"a"}]}` {
		t.Fatalf("second call served %q, want the SECOND recorded response — serving the first twice hangs a poll-until-ready flow", second.Hit.Body)
	}
}

func TestWhenRepeatsAreExhaustedTheLastRecordedResponseRepeats(t *testing.T) {
	// A retry loop's extra attempt is not a client deviation, so the third
	// call is served rather than missed — and it is served the LAST
	// recorded response, not the first (which would be the hang again).
	b := bundleOf(
		exch("GET", "/cart", "", nil, 200, `{"items":[]}`, 1),
		exch("GET", "/cart", "", nil, 200, `{"items":[{"sku":"a"}]}`, 2),
	)
	b.Match(Request{Method: "GET", Path: "/cart"}, Options{})
	b.Match(Request{Method: "GET", Path: "/cart"}, Options{})
	third := b.Match(Request{Method: "GET", Path: "/cart"}, Options{})
	if third.Miss {
		t.Fatalf("a third identical call was reported as a client deviation: %+v", third.Diff)
	}
	if third.Hit.Body != `{"items":[{"sku":"a"}]}` {
		t.Fatalf("third call served %q, want the last recorded response", third.Hit.Body)
	}
}

func TestAnUnparseableRecordedRequestBodyMatchesOnlyItsOwnBytes(t *testing.T) {
	// F3. `nil recorded body == no constraint` made a recorded POST with a
	// form-encoded, XML, plain-text or otherwise non-JSON body match ANY
	// body on the same method+path+query — the wildcard this package
	// exists to refuse, arrived at by an unparseable value degrading into
	// a permissive one instead of a refusing one.
	//
	// The rule is byte-exact rather than "refuse the bundle" because a
	// non-JSON body is a perfectly legitimate recording (a form post, a
	// protobuf) and its bytes ARE a contract; there is simply no structure
	// in it to subset-match.
	for _, c := range []struct{ name, recorded string }{
		{"form encoded", "user=ada&pass=hunter2"},
		{"xml", "<order><sku>a</sku></order>"},
		{"plain text", "ping"},
		{"truncated json", `{"sku":"a","qt`},
		{"literal null", "null"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := bundleOf(Exchange{
				Key:    Key{Method: "POST", Path: "/login"},
				ReqRaw: c.recorded, ReqBody: decodeBody(payloadOf(c.recorded)),
				Status: 200, Body: `{"ok":true}`, Seq: 1,
			})
			// Something else entirely, and something one byte away: a
			// prefix rule would let the second one through.
			for _, sent := range []string{`{"anything":true}`, c.recorded + "x", ""} {
				got := b.Match(Request{Method: "POST", Path: "/login", Raw: sent, Body: decodeJSON([]byte(sent))}, Options{})
				if !got.Miss {
					t.Fatalf("a recorded %q body was matched by %q — an unparseable recording must not be a wildcard", c.recorded, sent)
				}
				if len(got.Diff) == 0 {
					t.Fatalf("the miss for %q carries no diff to read", sent)
				}
			}
			// The mirror: the recorded bytes still match themselves, so
			// the refusal above is not "nothing ever matches".
			got := b.Match(Request{Method: "POST", Path: "/login", Raw: c.recorded, Body: decodeJSON([]byte(c.recorded))}, Options{})
			if got.Miss || got.Hit == nil {
				t.Fatalf("the recorded body did not match itself: %+v", got)
			}
		})
	}
}

// payloadOf builds the trace.Payload lower() would hand decodeBody, so the
// fixtures above go through the SAME decode production uses rather than a
// hand-chosen ReqBody.
func payloadOf(body string) trace.Payload { return trace.Payload{Body: body} }

func TestARecordingWithNoRequestBodyAtAllStillConstrainsNothing(t *testing.T) {
	// The other arm of F3's rule, so the fix cannot be "every request body
	// must now be byte-identical". A GET the recording made with no body
	// is answered whatever the client sends, exactly as before.
	b := bundleOf(exch("GET", "/cart", "", nil, 200, `{"items":[]}`, 1))
	got := b.Match(Request{Method: "GET", Path: "/cart"}, Options{})
	if got.Miss || got.Hit == nil {
		t.Fatalf("a recording with no request body did not match a bodyless request: %+v", got)
	}
}

func TestAMalformedQueryIsNeverCanonicalisedIntoSomethingElse(t *testing.T) {
	// SignificantQuery returns an unparseable query VERBATIM. Collapsing
	// it to "" would make every malformed query match a recording with NO
	// query, and make two different malformed queries match each other —
	// two calls the operator can see are different, matched by a
	// canonicalisation nobody asked for.
	const bad = "a=%zz"
	const otherBad = "b=%zz"
	if got := SignificantQuery(bad, nil); got != bad {
		t.Fatalf("SignificantQuery(%q) = %q, want it returned verbatim", bad, got)
	}
	if SignificantQuery(bad, nil) == SignificantQuery(otherBad, nil) {
		t.Fatalf("two different malformed queries canonicalise to the same value (%q)", SignificantQuery(bad, nil))
	}
	if SignificantQuery(bad, nil) == SignificantQuery("", nil) {
		t.Fatal("a malformed query canonicalises to the empty query — it would match a recording that carried no query at all")
	}

	b := bundleOf(
		exch("GET", "/cart", "", nil, 200, `{"none":true}`, 1),
		exch("GET", "/cart", bad, nil, 200, `{"bad":true}`, 2),
	)
	// The malformed query matches only its own recording...
	got := b.Match(Request{Method: "GET", Path: "/cart", Query: bad}, Options{})
	if got.Miss || got.Hit == nil || got.Hit.Body != `{"bad":true}` {
		t.Fatalf("a malformed query did not match the exchange recorded with it: %+v", got)
	}
	// ...and a DIFFERENT malformed query matches neither.
	got = b.Match(Request{Method: "GET", Path: "/cart", Query: otherBad}, Options{})
	if !got.Miss {
		t.Fatalf("%q matched a table recorded with %q and with no query: %+v", otherBad, bad, got)
	}
}

func TestTrailingSlashAndEmptyPathEquivalenceHoldsInBothDirections(t *testing.T) {
	// NormalizePath is one line and every one of its behaviours was
	// unpinned: reduced to the identity, nothing in the package noticed.
	// Both directions, because a future change that WIDENED it (stripping
	// every trailing slash, so "/a//" == "/a") is as invisible as one that
	// dropped it.
	for _, c := range []struct{ in, want string }{
		{"", "/"},
		{"/", "/"},
		{"/cart/", "/cart"},
		{"/cart", "/cart"},
		{"/cart//", "/cart/"}, // ONE trailing slash, not every one
		{"/a/b/", "/a/b"},
	} {
		if got := NormalizePath(c.in); got != c.want {
			t.Fatalf("NormalizePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	b := bundleOf(exch("GET", "/cart", "", nil, 200, `{"items":[]}`, 1))
	if got := b.Match(Request{Method: "GET", Path: "/cart/"}, Options{}); got.Miss {
		t.Fatalf("a trailing slash was a miss against a recording without one: %+v", got)
	}
	// And the mirror: a genuinely different path is still a miss, so the
	// equivalence above is not "paths barely matter".
	if got := b.Match(Request{Method: "GET", Path: "/cart/items"}, Options{}); !got.Miss {
		t.Fatalf("/cart/items matched a recording of /cart: %+v", got)
	}
}

func TestTheNearestExchangePrefersTheMatchingMethodOverACloserPath(t *testing.T) {
	// nearest's method penalty decides which exchange a human is shown on
	// a 501. Dropped, nothing in the package noticed, so no fixture
	// distinguished the right nearest rule from a wrong one — the "rule
	// symmetry" costume: a fixture where both rules agree pins neither.
	//
	// Here they disagree by construction: /cart is one edit away from the
	// request path but recorded under the WRONG verb, while /orders is
	// four edits away under the right one.
	b := bundleOf(
		exch("POST", "/cart", "", nil, 201, `{}`, 1),
		exch("GET", "/orders", "", nil, 200, `{}`, 2),
	)
	got := b.Match(Request{Method: "GET", Path: "/carts"}, Options{})
	if !got.Miss || got.Nearest == nil {
		t.Fatalf("expected a miss naming a nearest: %+v", got)
	}
	if got.Nearest.Key.Method != "GET" || got.Nearest.Key.Path != "/orders" {
		t.Fatalf("nearest = %+v, want GET /orders — an exchange under a different verb is not a near miss, it is a different call",
			got.Nearest.Key)
	}
	// The mirror: with the same verb available, the closest path wins.
	b = bundleOf(
		exch("GET", "/cart", "", nil, 200, `{}`, 1),
		exch("GET", "/orders", "", nil, 200, `{}`, 2),
	)
	got = b.Match(Request{Method: "GET", Path: "/carts"}, Options{})
	if got.Nearest == nil || got.Nearest.Key.Path != "/cart" {
		t.Fatalf("nearest = %+v, want GET /cart", got.Nearest)
	}
}

func twoTargetBundle() *Bundle {
	edge := exch("GET", "/cart", "", nil, 200, `{"from":"edge"}`, 1)
	edge.Target = "edge"
	auth := exch("GET", "/cart", "", nil, 200, `{"from":"auth"}`, 2)
	auth.Target = "auth"
	return bundleOf(edge, auth)
}

func TestATargetFilterOnlyMatchesItsOwnListenersExchanges(t *testing.T) {
	b := twoTargetBundle()
	got := b.Match(Request{Method: "GET", Path: "/cart"}, Options{TargetFilter: "auth"})
	if got.Miss || got.Hit == nil {
		t.Fatalf("expected a hit filtered to target auth: %+v", got)
	}
	if got.Hit.Body != `{"from":"auth"}` {
		t.Fatalf("body = %q, want the auth-target exchange's body", got.Hit.Body)
	}
}

func TestATargetFilterMissesARequestOnlyTheOtherTargetRecorded(t *testing.T) {
	b := bundleOf(
		func() Exchange { e := exch("GET", "/only-on-auth", "", nil, 200, `{}`, 1); e.Target = "auth"; return e }(),
	)
	got := b.Match(Request{Method: "GET", Path: "/only-on-auth"}, Options{TargetFilter: "edge"})
	if !got.Miss {
		t.Fatalf("expected a miss: a request matching another target's exchange must never be served, got %+v", got)
	}
}

func TestNoTargetFilterMatchesEitherTargetLikeBeforeMultiListener(t *testing.T) {
	b := twoTargetBundle()
	got := b.Match(Request{Method: "GET", Path: "/cart"}, Options{})
	if got.Miss || got.Hit == nil {
		t.Fatalf("expected an unfiltered hit: %+v", got)
	}
}

// TestRedactedRequestFieldMatchesAnyLiveValue pins the spec's
// "Redacting a request field SHALL NOT by itself cause a replay miss": a
// recorded body field carrying the destroy-mode sentinel is a built-in
// wildcard — no rule required — while an ABSENT live field, or a live
// sentinel against a recorded plaintext, stays a real difference.
func TestRedactedRequestFieldMatchesAnyLiveValue(t *testing.T) {
	b := bundleOf(
		exch("POST", "/login", "", obj(t, `{"user":"a","password":"`+trace.Redacted+`"}`), 200, `{"ok":true}`, 1),
	)
	got := b.Match(Request{Method: "POST", Path: "/login", Body: obj(t, `{"user":"a","password":"hunter2"}`)}, Options{})
	if got.Miss || got.Hit == nil {
		t.Fatalf("redacted password should match any live value, got %+v", got.Diff)
	}

	// Nested, and a non-string live value: still a wildcard.
	b = bundleOf(
		exch("POST", "/pay", "", obj(t, `{"card":{"token":"`+trace.Redacted+`"}}`), 200, `{}`, 1),
	)
	got = b.Match(Request{Method: "POST", Path: "/pay", Body: obj(t, `{"card":{"token":12345}}`)}, Options{})
	if got.Miss {
		t.Fatalf("nested redacted field should match a non-string live value, got %+v", got.Diff)
	}

	// The key must still be present: a client that never sends the field is
	// not sending "any value".
	got = b.Match(Request{Method: "POST", Path: "/pay", Body: obj(t, `{"card":{}}`)}, Options{})
	if !got.Miss {
		t.Fatal("an absent field must stay a miss even when the recording redacted it")
	}

	// The wildcard is one-directional: a recorded plaintext value is not
	// excused by the LIVE side sending the sentinel.
	b = bundleOf(
		exch("POST", "/login", "", obj(t, `{"password":"real"}`), 200, `{}`, 1),
	)
	got = b.Match(Request{Method: "POST", Path: "/login", Body: obj(t, `{"password":"`+trace.Redacted+`"}`)}, Options{})
	if !got.Miss {
		t.Fatal("live sentinel against recorded plaintext must miss")
	}
}

// TestRedactedQueryParamMatchesAnyLiveValue is the query half of the same
// spec requirement: `token=[redacted]` recorded admits `token=abc` live —
// and only that key; a differing unredacted param still discriminates, and
// an absent live param is still a miss.
func TestRedactedQueryParamMatchesAnyLiveValue(t *testing.T) {
	b := bundleOf(
		exch("GET", "/me", "token="+trace.Redacted+"&page=1", nil, 200, `{"me":true}`, 1),
	)
	got := b.Match(Request{Method: "GET", Path: "/me", Query: "token=abc&page=1"}, Options{})
	if got.Miss || got.Hit == nil {
		t.Fatalf("redacted query param should match any live value: %+v", got.Diff)
	}
	got = b.Match(Request{Method: "GET", Path: "/me", Query: "token=abc&page=2"}, Options{})
	if !got.Miss {
		t.Fatal("an unredacted param that differs must still miss")
	}
	got = b.Match(Request{Method: "GET", Path: "/me", Query: "page=1"}, Options{})
	if !got.Miss {
		t.Fatal("a live request missing the redacted param entirely must still miss")
	}
}
