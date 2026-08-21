package replay

import (
	"encoding/json"
	"testing"

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

	t.Run("no fixture for the path at all names method and path", func(t *testing.T) {
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
		m := fieldNamed(t, got.Diff, "method")
		if m.Expected != "GET" || m.Actual != "GET" {
			t.Fatalf("method diff = %+v", m)
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
