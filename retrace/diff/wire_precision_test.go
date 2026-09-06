package diff

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/rules"
)

func TestWirePreservesDistinctJSONNumbers(t *testing.T) {
	for _, tc := range []struct{ name, a, b string }{
		{"adjacent integer ids", "9007199254740992", "9007199254740993"},
		{"adjacent decimal values", "0.1", "0.10000000000000001"},
		{"underflow", "0", "1e-99999"},
	} {
		for _, scope := range []string{"req", "resp"} {
			t.Run(tc.name+"/"+scope, func(t *testing.T) {
				a := trace.Hop{Seq: 1, Method: "GET", Path: "/item", Status: 200}
				b := a
				pa, pb := trace.Payload{Body: `{"value":` + tc.a + `}`}, trace.Payload{Body: `{"value":` + tc.b + `}`}
				if scope == "req" {
					a.Req, b.Req = pa, pb
				} else {
					a.Resp, b.Resp = pa, pb
				}
				got := DiffWire([]trace.Hop{a}, []trace.Hop{b}, Options{})
				if len(got.Paired) != 1 || len(got.Paired[0].BodyDiff) != 1 || got.Paired[0].Classes[0] != "changed" {
					t.Fatalf("distinct JSON numbers disappeared: %+v", got)
				}
				field := got.Paired[0].BodyDiff[0]
				if field.Scope != scope || field.Path != "value" {
					t.Fatalf("difference lost its field/scope: %+v", field)
				}
				if tc.name == "adjacent integer ids" {
					out, err := json.Marshal(field)
					if err != nil || !strings.Contains(string(out), `"a":9007199254740992`) || !strings.Contains(string(out), `"b":9007199254740993`) {
						t.Fatalf("reported numbers must remain exact JSON numbers: %s (%v)", out, err)
					}
				}
			})
		}
	}
}

func TestWireNumericFormattingAndIntegerRulesRemainCompatible(t *testing.T) {
	for _, tc := range []struct{ a, b string }{
		{"1", "1.0"}, {"1e2", "100"}, {"-0", "0.0"},
		{"9007199254740993", "9007199254740993.0"},
	} {
		a := trace.Hop{Seq: 1, Method: "GET", Path: "/item", Resp: trace.Payload{Body: `{"value":` + tc.a + `}`}}
		b := a
		b.Resp.Body = `{"value":` + tc.b + `}`
		got := DiffWire([]trace.Hop{a}, []trace.Hop{b}, Options{})
		if got.Paired[0].Classes[0] != "identical" {
			t.Errorf("equivalent numbers %s and %s changed: %+v", tc.a, tc.b, got)
		}
	}
	var acc bodyAcc
	diffBodyScope("resp", trace.Payload{Body: `{"value":1.0}`}, trace.Payload{Body: `{"value":2e0}`}, diffCtx{
		res: rules.Resolved{Body: []rules.BodyRule{{Glob: "value", Matcher: rules.Matcher{Kind: rules.KindNamed, Name: "integer"}}}},
	}, &acc)
	if len(acc.Tolerated) != 1 || len(acc.Violations) != 0 {
		t.Fatalf("integer matcher must still accept decimal/exponent integer values: %+v", acc)
	}
}

func TestWireSimilarityAndArrayReordersPreserveLargeNumbers(t *testing.T) {
	if got := bodySimilarity(`{"id":9007199254740992}`, `{"id":9007199254740993}`); got == 1 {
		t.Fatal("pairing similarity rounded distinct ids to the same canonical body")
	}
	a := trace.Hop{Seq: 1, Method: "GET", Path: "/items", Resp: trace.Payload{Body: `[9007199254740992,9007199254740993]`}}
	b := a
	b.Resp.Body = `[9007199254740993,9007199254740992]`
	got := DiffWire([]trace.Hop{a}, []trace.Hop{b}, Options{})
	if len(got.Paired[0].OrderingChanges) != 1 || got.Paired[0].Classes[0] != "changed" {
		t.Fatalf("large-number reorder disappeared: %+v", got)
	}
}

func TestWireStillComparesMalformedJSONBodiesVerbatim(t *testing.T) {
	a := trace.Hop{Seq: 1, Method: "GET", Path: "/item", Resp: trace.Payload{Body: `{"value":1} trailing-a`}}
	b := a
	b.Resp.Body = `{"value":1} trailing-b`
	got := DiffWire([]trace.Hop{a}, []trace.Hop{b}, Options{})
	if len(got.Paired[0].BodyDiff) != 1 {
		t.Fatalf("a decoder must not silently compare only the first JSON value: %+v", got)
	}
	f := got.Paired[0].BodyDiff[0]
	if f.Path != "" || f.A != a.Resp.Body || f.B != b.Resp.Body {
		t.Fatalf("malformed JSON must retain whole-body comparison: %+v", f)
	}
}

func TestWireIntegerRulesAcceptExactJSONIntegers(t *testing.T) {
	for _, tc := range []struct {
		a, b      string
		tolerated bool
	}{
		{"90071992547409930", "90071992547409950", true},
		{"-90071992547409930", "-90071992547409950", true},
		{"9007199254740993e1", "9007199254740995e1", true},
		{"18446744073709551615", "18446744073709551614", true},
		{"-18446744073709551615", "-18446744073709551614", true},
		{"1e20", "2e20", true},
		{"1e99999", "2e99999", true},
		{"1e18446744073709551617", "2e18446744073709551617", true},
		{"90071992547409930.1", "90071992547409950.1", false},
		{"18446744073709551615.1", "18446744073709551614.1", false},
		{"1e-99999", "2e-99999", false},
	} {
		t.Run(tc.a, func(t *testing.T) {
			a := trace.Hop{Seq: 1, Method: "POST", Path: "/item", Status: 200, Resp: trace.Payload{Body: `{"value":` + tc.a + `}`}}
			b := a
			b.Resp.Body = `{"value":` + tc.b + `}`
			got := DiffWire([]trace.Hop{a}, []trace.Hop{b}, Options{Rules: []rules.Rule{{
				Method: "POST", Path: "/item",
				Body: []rules.BodyRule{{Glob: "value", Matcher: rules.Matcher{Kind: rules.KindNamed, Name: "integer"}}},
			}}})
			e := got.Paired[0]
			if tc.tolerated {
				if len(e.BodyTolerated) != 1 || len(e.BodyViolations) != 0 || e.Classes[0] != "identical" {
					t.Fatalf("integer rule rejected exact integral values %s / %s: %+v", tc.a, tc.b, e)
				}
			} else if len(e.BodyViolations) != 1 || len(e.BodyTolerated) != 0 || e.Classes[0] != "changed" {
				t.Fatalf("integer rule must reject fractional values %s / %s: %+v", tc.a, tc.b, e)
			}
		})
	}
}
