package replay

import (
	"net/http"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/rules"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

func TestStrictReplayPreservesJSONNumberIdentity(t *testing.T) {
	for _, tc := range []struct{ recorded, different, equivalent string }{
		{"9007199254740992", "9007199254740993", "9007199254740992.0"},
		{"9007199254740993", "9007199254740992", "9007199254740993.0"},
		{"0.1", "0.10000000000000001", "0.10"},
	} {
		t.Run(tc.recorded, func(t *testing.T) {
			dir := writeBundle(t, runs.Counts{Recorded: true, Calls: 1}, []trace.Hop{
				hop(1, "POST", "/item", `{"id":`+tc.recorded+`}`, 201, `{"ok":true}`),
			})
			for _, request := range []struct {
				value  string
				status int
			}{
				{tc.different, http.StatusNotImplemented}, {tc.recorded, http.StatusCreated}, {tc.equivalent, http.StatusCreated},
			} {
				t.Run(request.value, func(t *testing.T) {
					b, err := LoadBundle(dir, "", nil)
					if err != nil {
						t.Fatal(err)
					}
					s, url := serve(t, b, Options{}, "")
					resp := do(t, "POST", url+"/item", `{"id":`+request.value+`}`, map[string]string{"Content-Type": "application/json"})
					body := readBody(t, resp)
					if resp.StatusCode != request.status {
						t.Fatalf("recorded %s, received %s: status=%d want %d, body=%s", tc.recorded, request.value, resp.StatusCode, request.status, body)
					}
					if request.status == http.StatusNotImplemented && (s.MissCount() != 1 || !strings.Contains(body, "id")) {
						t.Fatalf("wrong numeric id must be an explained, counted miss: count=%d body=%s", s.MissCount(), body)
					}
				})
			}
		})
	}
}

func TestReplayIntegerRulesAcceptExactJSONIntegers(t *testing.T) {
	for _, tc := range []struct {
		recorded, request string
		status            int
	}{
		{"90071992547409930", "90071992547409950", http.StatusCreated},
		{"-90071992547409930", "-90071992547409950", http.StatusCreated},
		{"9007199254740993e1", "9007199254740995e1", http.StatusCreated},
		{"18446744073709551615", "18446744073709551614", http.StatusCreated},
		{"-18446744073709551615", "-18446744073709551614", http.StatusCreated},
		{"1e20", "2e20", http.StatusCreated},
		{"1e99999", "2e99999", http.StatusCreated},
		{"1e18446744073709551617", "2e18446744073709551617", http.StatusCreated},
		{"90071992547409930.1", "90071992547409950.1", http.StatusNotImplemented},
		{"18446744073709551615.1", "18446744073709551614.1", http.StatusNotImplemented},
		{"1e-99999", "2e-99999", http.StatusNotImplemented},
	} {
		t.Run(tc.recorded, func(t *testing.T) {
			dir := writeBundle(t, runs.Counts{Recorded: true, Calls: 1}, []trace.Hop{
				hop(1, "POST", "/item", `{"id":`+tc.recorded+`}`, 201, `{"ok":true}`),
			})
			b, err := LoadBundle(dir, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			s, url := serve(t, b, Options{Rules: []rules.Rule{{
				Method: "POST", Path: "/item",
				Body: []rules.BodyRule{{Glob: "id", Matcher: rules.Matcher{Kind: rules.KindNamed, Name: "integer"}}},
			}}}, "")
			resp := do(t, "POST", url+"/item", `{"id":`+tc.request+`}`, map[string]string{"Content-Type": "application/json"})
			body := readBody(t, resp)
			if resp.StatusCode != tc.status {
				t.Fatalf("integer rule: recorded %s, received %s: status=%d want %d, body=%s", tc.recorded, tc.request, resp.StatusCode, tc.status, body)
			}
			wantMisses := 0
			if tc.status == http.StatusNotImplemented {
				wantMisses = 1
			}
			if s.MissCount() != wantMisses {
				t.Fatalf("miss count=%d want %d", s.MissCount(), wantMisses)
			}
		})
	}
}
