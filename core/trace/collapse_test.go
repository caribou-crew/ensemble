package trace

// Test cases ported from local-stack web/src/trace/collapse.test.ts —
// behavior pinned by the prototype's real-capture-derived scenarios.

import (
	"reflect"
	"testing"
	"time"
)

var collapseClock int

func chop(from, to string, over func(*Hop)) Hop {
	collapseClock++
	h := Hop{
		From: from, To: to,
		Method: "GET", Path: "/api/v1/profile", Status: 200,
		TraceID: "t1",
		T: Timings{
			Start:  time.Date(2026, 8, 13, 0, 0, collapseClock, 0, time.UTC),
			DoneMs: 10,
		},
	}
	if over != nil {
		over(&h)
	}
	return h
}

func viaOf(l LogicalHop) []string {
	if l.Via == nil {
		return []string{}
	}
	return l.Via
}

func TestPassThroughRelayFolds(t *testing.T) {
	hops := []Hop{chop("legacy:primary", "edge-service", nil), chop("edge-service", "bff2", nil)}
	rows := CollapseRelays(hops, true)
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	l := rows[0]
	if l.Hop.From != "legacy:primary" || l.Origin.To != "bff2" || !reflect.DeepEqual(l.Via, []string{"edge-service"}) {
		t.Fatalf("bad fold: %+v", l)
	}
}

func TestChainOfRelaysFoldsRecursively(t *testing.T) {
	hops := []Hop{chop("client", "edge", nil), chop("edge", "client-gateway", nil), chop("client-gateway", "bff2", nil)}
	rows := CollapseRelays(hops, true)
	if !reflect.DeepEqual(rows[0].Via, []string{"edge", "client-gateway"}) || rows[0].Origin.To != "bff2" {
		t.Fatalf("bad chain fold: %+v", rows[0])
	}
}

func TestStatusMismatchNotFoldedAndFlagged(t *testing.T) {
	hops := []Hop{
		chop("client", "edge", func(h *Hop) { h.Status = 504 }),
		chop("edge", "bff2", func(h *Hop) { h.Status = 200 }),
	}
	rows := CollapseRelays(hops, true)
	if len(rows) != 2 {
		t.Fatalf("both legs must stay visible, got %d rows", len(rows))
	}
	if !rows[0].StatusMismatch || len(viaOf(rows[0])) != 0 {
		t.Fatalf("mismatch not flagged: %+v", rows[0])
	}
}

func TestDifferentTracesNeverFold(t *testing.T) {
	hops := []Hop{
		chop("client", "edge", func(h *Hop) { h.TraceID = "a" }),
		chop("edge", "bff2", func(h *Hop) { h.TraceID = "b" }),
	}
	if got := len(CollapseRelays(hops, true)); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

func TestNoTraceIDNeverFolds(t *testing.T) {
	hops := []Hop{
		chop("client", "edge", func(h *Hop) { h.TraceID = "" }),
		chop("edge", "bff2", func(h *Hop) { h.TraceID = "" }),
	}
	if got := len(CollapseRelays(hops, true)); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

func TestQueryStringFoldedIntoPathStillPairs(t *testing.T) {
	hops := []Hop{
		chop("client", "edge-service", func(h *Hop) { h.Path = "/api/v1/account" }),
		chop("edge-service", "bff2", func(h *Hop) { h.Path = "/api/v1/account?account_token=019fed39" }),
	}
	rows := CollapseRelays(hops, true)
	if len(rows) != 1 || !reflect.DeepEqual(rows[0].Via, []string{"edge-service"}) {
		t.Fatalf("query-suffixed path did not pair: %+v", rows)
	}
}

func TestDifferentPathOrMethodNotFolded(t *testing.T) {
	byPath := []Hop{chop("client", "edge", nil), chop("edge", "bff2", func(h *Hop) { h.Path = "/other" })}
	byMethod := []Hop{chop("client", "edge", nil), chop("edge", "bff2", func(h *Hop) { h.Method = "POST" })}
	if len(CollapseRelays(byPath, true)) != 2 || len(CollapseRelays(byMethod, true)) != 2 {
		t.Fatal("folded legs that differ by path or method")
	}
}

func TestDisabledPassesThroughUntouched(t *testing.T) {
	hops := []Hop{chop("legacy:primary", "edge-service", nil), chop("edge-service", "bff2", nil)}
	rows := CollapseRelays(hops, false)
	if len(rows) != 2 {
		t.Fatalf("want 2, got %d", len(rows))
	}
	for i, r := range rows {
		if r.Index != i || len(viaOf(r)) != 0 || r.Origin != r.Hop {
			t.Fatalf("row %d altered: %+v", i, r)
		}
	}
}

func TestIndexPointsAtOriginalArray(t *testing.T) {
	hops := []Hop{
		chop("bff1", "core", func(h *Hop) { h.TraceID = "other" }),
		chop("legacy:primary", "edge-service", nil),
		chop("edge-service", "bff2", nil),
	}
	rows := CollapseRelays(hops, true)
	got := []int{rows[0].Index, rows[1].Index}
	if !reflect.DeepEqual(got, []int{0, 1}) {
		t.Fatalf("indices %v", got)
	}
}

func TestMergeForDetailGraftsRequestOntoOriginResponse(t *testing.T) {
	first := chop("legacy:primary", "edge-service", func(h *Hop) {
		h.Req = Payload{Headers: map[string]string{"x-source-client": "legacy:primary"}}
		h.T.DoneMs = 463
	})
	second := chop("edge-service", "bff2", func(h *Hop) {
		h.Resp = Payload{Headers: map[string]string{"content-type": "application/json"}}
		h.T.DoneMs = 455
	})
	rows := CollapseRelays([]Hop{first, second}, true)
	detail := MergeForDetail(rows[0])
	if detail.From != "legacy:primary" || detail.To != "bff2" {
		t.Fatalf("ends wrong: %+v", detail)
	}
	if detail.Req.Headers["x-source-client"] != "legacy:primary" {
		t.Fatal("request side must come from the first leg")
	}
	if detail.Resp.Headers["content-type"] != "application/json" {
		t.Fatal("response side must come from the origin")
	}
	if detail.T.DoneMs != 463 {
		t.Fatalf("wall-clock must be the outer leg's: %v", detail.T.DoneMs)
	}
}

func TestMergeForDetailIdentityWhenNothingFolded(t *testing.T) {
	only := chop("bff2", "bff1", nil)
	rows := CollapseRelays([]Hop{only}, true)
	detail := MergeForDetail(rows[0])
	if detail.To != "bff1" || detail.From != "bff2" {
		t.Fatalf("unexpected detail: %+v", detail)
	}
}
