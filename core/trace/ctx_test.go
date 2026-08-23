package trace

import (
	"regexp"
	"testing"
)

var traceparentRe = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`)

func TestParseCtxValidTraceparent(t *testing.T) {
	ctx := ParseCtx("00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01", "")
	if ctx.TraceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Fatalf("trace id: %q", ctx.TraceID)
	}
	if ctx.SpanID != "b7ad6b7169203331" {
		t.Fatalf("span id: %q", ctx.SpanID)
	}
}

func TestParseCtxInvalidGeneratesFresh(t *testing.T) {
	for _, bad := range []string{"", "garbage", "00-zzz-yyy-01", "00-00000000000000000000000000000000-0000000000000000-00"} {
		ctx := ParseCtx(bad, "")
		if !traceparentRe.MatchString(ctx.Traceparent()) {
			t.Fatalf("input %q: invalid traceparent %q", bad, ctx.Traceparent())
		}
		if ctx.TraceID == "00000000000000000000000000000000" || ctx.SpanID == "0000000000000000" {
			t.Fatalf("input %q: zero ids not regenerated", bad)
		}
	}
}

func TestChildKeepsTraceNewSpan(t *testing.T) {
	parent := ParseCtx("00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01", "")
	child := parent.Child()
	if child.TraceID != parent.TraceID {
		t.Fatalf("trace id changed")
	}
	if child.SpanID == parent.SpanID {
		t.Fatalf("span id not regenerated")
	}
	if child.ParentSpanID != parent.SpanID {
		t.Fatalf("parent linkage missing: %q", child.ParentSpanID)
	}
}

func TestBaggageRoundTripAndWellKnownKeys(t *testing.T) {
	ctx := ParseCtx("", "correlationId=corr-9,retrace-run=run-7,other=x%20y")
	if got := ctx.Baggage["correlationId"]; got != "corr-9" {
		t.Fatalf("correlationId: %q", got)
	}
	if got := ctx.CorrelationID(); got != "corr-9" {
		t.Fatalf("CorrelationID(): %q", got)
	}
	if got := ctx.Session(); got != "run-7" {
		t.Fatalf("Session(): %q", got)
	}
	if got := ctx.Baggage["other"]; got != "x y" {
		t.Fatalf("percent-decoding: %q", got)
	}

	hdr := ctx.BaggageHeader()
	re := ParseCtx("", hdr)
	if re.Baggage["other"] != "x y" || re.Session() != "run-7" {
		t.Fatalf("baggage header round-trip failed: %q -> %+v", hdr, re.Baggage)
	}
}

func TestEnsureCorrelationIDStableAndAdditive(t *testing.T) {
	ctx := ParseCtx("", "")
	id1 := ctx.EnsureCorrelationID()
	if id1 == "" {
		t.Fatal("empty correlation id")
	}
	if id2 := ctx.EnsureCorrelationID(); id2 != id1 {
		t.Fatalf("not stable: %q vs %q", id1, id2)
	}
	ctx.Baggage["retrace-run"] = "run-1"
	if ctx.CorrelationID() != id1 {
		t.Fatal("adding baggage clobbered correlation id")
	}
}

func TestResolveInboundPrefersRealTraceparent(t *testing.T) {
	ctx, spanID := ResolveInbound("00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01", "", "some-custom-value")
	if ctx.TraceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Fatalf("trace id = %q, want the real traceparent's, not the custom header", ctx.TraceID)
	}
	if spanID != "b7ad6b7169203331" {
		t.Fatalf("incoming span id = %q, want the real traceparent's", spanID)
	}
}

func TestResolveInboundFallsBackToCustomHeader(t *testing.T) {
	ctx, spanID := ResolveInbound("", "", "company-correlation-id-123")
	if ctx.TraceID != "company-correlation-id-123" {
		t.Fatalf("trace id = %q, want the custom header's raw value", ctx.TraceID)
	}
	// A flat correlation header carries no span structure of its own — no
	// span id to look an owner up by, unlike a real traceparent.
	if spanID != "" {
		t.Fatalf("incoming span id = %q, want empty (no real span to attribute)", spanID)
	}
}

func TestResolveInboundMintsFreshWhenNeitherPresent(t *testing.T) {
	ctx, spanID := ResolveInbound("", "", "")
	if !traceparentRe.MatchString(ctx.Traceparent()) {
		t.Fatalf("expected a freshly minted, well-formed trace id, got %q", ctx.Traceparent())
	}
	if spanID != "" {
		t.Fatalf("incoming span id = %q, want empty", spanID)
	}
}
