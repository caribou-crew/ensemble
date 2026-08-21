package diff

import (
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/config"
)

// mkHop builds a trace.Hop carrying the topology fields (TraceID/From/To)
// that hop.mjs-style diffing needs and wire_test.go's hop() helper doesn't
// set.
func mkHop(seq uint64, traceID, from, to, method, path string, status int) trace.Hop {
	return trace.Hop{
		Seq: seq, TraceID: traceID, From: from, To: to,
		Method: method, Path: path, Status: status,
	}
}

func serviceCount(t *testing.T, counts []ServiceCount, service string) ServiceCount {
	t.Helper()
	for _, c := range counts {
		if c.Service == service {
			return c
		}
	}
	t.Fatalf("no ServiceCount for %q in %+v", service, counts)
	return ServiceCount{}
}

func hasRoute(routes []Route, to, method, path string) bool {
	for _, r := range routes {
		if r.To == to && r.Method == method && r.Path == path {
			return true
		}
	}
	return false
}

// TestDiffHopsEndToEndComposesAllFourSignals drives DiffHops itself —
// the composed entry point Task 10 calls — through a fixture that
// exercises relay folding, a new route, an error signature that's new
// only on B, and a hopRequire assertion that fails, all at once. Per-piece
// unit tests below pin each behaviour in isolation; this one pins that
// DiffHops actually wires them together, since Task 8's review found both
// its CRITICAL defects living in composition an entry-point test would
// have caught.
func TestDiffHopsEndToEndComposesAllFourSignals(t *testing.T) {
	a := []trace.Hop{
		mkHop(1, "t1", "client", "bff", "GET", "/cart", 200),
		mkHop(2, "t2", "client", "bff", "GET", "/cart/items", 200),
		mkHop(3, "t3", "client", "payments", "GET", "/payments/verify", 200),
	}
	b := []trace.Hop{
		// client -> edge -> bff, a transparent relay: must fold to one
		// logical call attributed to bff, matching side A's /cart route.
		mkHop(1, "t1", "client", "edge", "GET", "/cart", 200),
		mkHop(2, "t1", "edge", "bff", "GET", "/cart", 200),
		mkHop(3, "t2", "client", "bff", "GET", "/cart/items", 200),
		// same route as A, but the status flipped to an error: a NEW
		// error signature, not a new/gone route.
		mkHop(4, "t3", "client", "payments", "GET", "/payments/verify", 500),
		// a genuinely new downstream call.
		mkHop(5, "t4", "client", "payments", "GET", "/payments/refund", 200),
	}
	require := []config.RequiredRoute{
		{Method: "GET", Path: "/payments/refund", Status: 200}, // satisfied
		{Method: "GET", Path: "/admin/health", Status: 200},    // never called: missing
	}

	d := DiffHops(a, b, HopOptions{Require: require})

	// Relay folding: no "edge" service anywhere, and /cart is not a new
	// route.
	for _, c := range d.ServiceCounts {
		if c.Service == "edge" {
			t.Errorf("relay %q must not appear as a service after folding: %+v", "edge", d.ServiceCounts)
		}
	}
	if hasRoute(d.NewRoutes, "edge", "GET", "/cart") || hasRoute(d.NewRoutes, "bff", "GET", "/cart") {
		t.Errorf("/cart must fold and match side A's route, not appear as new: %+v", d.NewRoutes)
	}

	// New route.
	if !hasRoute(d.NewRoutes, "payments", "GET", "/payments/refund") {
		t.Errorf("expected /payments/refund as a new route, got %+v", d.NewRoutes)
	}
	if len(d.GoneRoutes) != 0 {
		t.Errorf("expected no gone routes, got %+v", d.GoneRoutes)
	}

	// New error signature.
	if len(d.NewErrors) != 1 || d.NewErrors[0].Path != "/payments/verify" || d.NewErrors[0].Status != 500 {
		t.Errorf("expected exactly one new error (payments/verify 500), got %+v", d.NewErrors)
	}
	if len(d.GoneErrors) != 0 {
		t.Errorf("expected no gone errors, got %+v", d.GoneErrors)
	}

	// hopRequire.
	if !d.HopRequireConfigured {
		t.Error("HopRequireConfigured must be true when Require is non-empty")
	}
	if len(d.RequiredRouteFailures) != 1 || d.RequiredRouteFailures[0].Path != "/admin/health" || d.RequiredRouteFailures[0].Reason != "missing" {
		t.Errorf("expected exactly one missing-route failure for /admin/health, got %+v", d.RequiredRouteFailures)
	}

	// Service counts still reflect the real call volume.
	bff := serviceCount(t, d.ServiceCounts, "bff")
	if bff.A != 2 || bff.B != 2 {
		t.Errorf("bff count = %+v, want A=2 B=2", bff)
	}
	payments := serviceCount(t, d.ServiceCounts, "payments")
	if payments.A != 1 || payments.B != 2 {
		t.Errorf("payments count = %+v, want A=1 B=2", payments)
	}
}

func TestAnAddedDownstreamCallIsListedAsANewRoute(t *testing.T) {
	a := []trace.Hop{
		mkHop(1, "t1", "client", "bff", "GET", "/cart", 200),
	}
	b := []trace.Hop{
		mkHop(1, "t1", "client", "bff", "GET", "/cart", 200),
		mkHop(2, "t2", "client", "payments", "POST", "/payments/charge", 200),
	}

	d := DiffHops(a, b, HopOptions{})

	if !hasRoute(d.NewRoutes, "payments", "POST", "/payments/charge") {
		t.Fatalf("expected the added payments call as a new route, got %+v", d.NewRoutes)
	}
	if len(d.NewRoutes) != 1 {
		t.Fatalf("expected exactly one new route, got %+v", d.NewRoutes)
	}
}

func TestATransparentRelayHopIsFoldedAndNotCountedAsANewRoute(t *testing.T) {
	a := []trace.Hop{
		mkHop(1, "t1", "client", "bff", "GET", "/x", 200),
	}
	b := []trace.Hop{
		mkHop(1, "t1", "client", "edge", "GET", "/x", 200),
		mkHop(2, "t1", "edge", "bff", "GET", "/x", 200),
	}

	d := DiffHops(a, b, HopOptions{})
	if len(d.NewRoutes) != 0 {
		t.Fatalf("collapsed, both runs made ONE logical call; want no new routes, got %+v", d.NewRoutes)
	}
	if !hasRoute(CollapsedRoutes(b, nil), "bff", "GET", "/x") {
		t.Fatalf("folded route must exist named after bff")
	}

	// With NoCollapse:true the same input DOES report a new route — that
	// is what proves the folding is doing the work above.
	dNoCollapse := DiffHops(a, b, HopOptions{NoCollapse: true})
	if !hasRoute(dNoCollapse.NewRoutes, "edge", "GET", "/x") {
		t.Fatalf("with NoCollapse, the edge leg must show as a new route, got %+v", dNoCollapse.NewRoutes)
	}
}

func TestAFoldedRouteIsNamedAfterItsOriginNotItsRelay(t *testing.T) {
	hops := []trace.Hop{
		mkHop(1, "t1", "client", "edge", "GET", "/x", 200),
		mkHop(2, "t1", "edge", "bff", "GET", "/x", 200),
	}

	routes := CollapsedRoutes(hops, nil)
	if len(routes) != 1 {
		t.Fatalf("want exactly one folded route, got %+v", routes)
	}
	r := routes[0]
	if r.To != "bff" {
		t.Fatalf("Route.To must come from Origin (bff), not Hop (edge): got %q", r.To)
	}
	if len(r.Via) != 1 || r.Via[0] != "edge" {
		t.Fatalf("Route.Via must list the folded relay, got %+v", r.Via)
	}
}

func TestCollapseIsAppliedToServiceCountsToo(t *testing.T) {
	a := []trace.Hop{
		mkHop(1, "t1", "client", "bff", "GET", "/x", 200),
	}
	b := []trace.Hop{
		mkHop(1, "t1", "client", "edge", "GET", "/x", 200),
		mkHop(2, "t1", "edge", "bff", "GET", "/x", 200),
	}

	d := DiffHops(a, b, HopOptions{})
	for _, c := range d.ServiceCounts {
		if c.Service == "edge" {
			t.Fatalf("edge must not appear as its own service count after folding: %+v", d.ServiceCounts)
		}
	}
	bff := serviceCount(t, d.ServiceCounts, "bff")
	if bff.A != 1 || bff.B != 1 {
		t.Fatalf("bff count = %+v, want A=1 B=1 (the folded call)", bff)
	}
}

func TestHopRequireAndErrorSignaturesRunAgainstRawHops(t *testing.T) {
	a := []trace.Hop{}
	b := []trace.Hop{
		// the relay itself returns an error: this must surface as an
		// error signature attributed to the raw leg, not be absorbed or
		// hidden by folding (there is nothing to fold here — the second
		// leg never happened because the relay itself failed).
		mkHop(1, "t1", "client", "edge", "GET", "/x", 500),
	}

	d := DiffHops(a, b, HopOptions{})
	if len(d.NewErrors) != 1 || d.NewErrors[0].Status != 500 || d.NewErrors[0].Path != "/x" {
		t.Fatalf("expected the relay's own 500 as a new error signature, got %+v", d.NewErrors)
	}

	// hopRequire runs against raw hops directly: a route satisfied only
	// by a raw leg (never mind what any folded view of it would look
	// like) still passes.
	failures := RequiredRouteFailures(b, []config.RequiredRoute{{Method: "GET", Path: "/x", Status: 500}})
	if len(failures) != 0 {
		t.Fatalf("the raw leg matches Method+Path+Status; expected no failure, got %+v", failures)
	}

	wrongStatus := RequiredRouteFailures(b, []config.RequiredRoute{{Method: "GET", Path: "/x", Status: 200}})
	if len(wrongStatus) != 1 || wrongStatus[0].Reason != "wrong-status" || wrongStatus[0].ActualStatus != 500 {
		t.Fatalf("expected a wrong-status failure carrying the actual status 500, got %+v", wrongStatus)
	}
}

func TestServiceCountDriftUnderToleranceIsNotFlagged(t *testing.T) {
	a := repeatHops(4, "t", "client", "svc", "GET", "/x", 200)
	b := repeatHops(5, "t", "client", "svc", "GET", "/x", 200)

	d := DiffHops(a, b, HopOptions{})
	svc := serviceCount(t, d.ServiceCounts, "svc")
	if svc.Deviates {
		t.Fatalf("4 vs 5 calls is within the default tolerance; want Deviates=false, got %+v", svc)
	}
}

func TestServiceCountDriftOverToleranceIsFlagged(t *testing.T) {
	a := repeatHops(2, "t", "client", "svc", "GET", "/x", 200)
	b := repeatHops(8, "t", "client", "svc", "GET", "/x", 200)

	d := DiffHops(a, b, HopOptions{})
	svc := serviceCount(t, d.ServiceCounts, "svc")
	if !svc.Deviates {
		t.Fatalf("2 vs 8 calls exceeds the default tolerance; want Deviates=true, got %+v", svc)
	}
}

func TestCountToleranceZeroFallsBackToDefault(t *testing.T) {
	// 4 vs 5 only clears DefaultCountTolerance (0.5); mutate the zero ->
	// default substitution away and this must fail, since a truly-zero
	// tolerance would flag any nonzero drift.
	a := repeatHops(4, "t", "client", "svc", "GET", "/x", 200)
	b := repeatHops(5, "t", "client", "svc", "GET", "/x", 200)

	d := DiffHops(a, b, HopOptions{CountTolerance: 0})
	svc := serviceCount(t, d.ServiceCounts, "svc")
	if svc.Deviates {
		t.Fatalf("CountTolerance:0 must fall back to DefaultCountTolerance; got Deviates=true for 4 vs 5")
	}
}

func TestCountToleranceExplicitValueIsHonoredNotOverridden(t *testing.T) {
	// 4 vs 5 (ratio 0.2) is within the 0.5 default but NOT within an
	// explicit, tighter 0.1 — if DiffHops silently substituted the
	// default over any nonzero value, this would wrongly pass.
	a := repeatHops(4, "t", "client", "svc", "GET", "/x", 200)
	b := repeatHops(5, "t", "client", "svc", "GET", "/x", 200)

	d := DiffHops(a, b, HopOptions{CountTolerance: 0.1})
	svc := serviceCount(t, d.ServiceCounts, "svc")
	if !svc.Deviates {
		t.Fatalf("an explicit CountTolerance:0.1 must be honored; want Deviates=true for 4 vs 5, got %+v", svc)
	}
}

func TestNegativeCountToleranceMeansNoTolerance(t *testing.T) {
	// 4 vs 5 differs at all, so a caller explicitly asking for NO
	// tolerance (negative) must flag it, even though it's inside the
	// default tolerance band.
	a := repeatHops(4, "t", "client", "svc", "GET", "/x", 200)
	b := repeatHops(5, "t", "client", "svc", "GET", "/x", 200)

	d := DiffHops(a, b, HopOptions{CountTolerance: -1})
	svc := serviceCount(t, d.ServiceCounts, "svc")
	if !svc.Deviates {
		t.Fatalf("a negative CountTolerance means NO tolerance; want Deviates=true for 4 vs 5, got %+v", svc)
	}
}

func TestErrorSignaturesAreDedupedToOnePerRouteAndStatus(t *testing.T) {
	a := []trace.Hop{}
	b := []trace.Hop{
		mkHop(1, "t1", "client", "svc", "GET", "/x", 500),
		mkHop(2, "t2", "client", "svc", "GET", "/x", 500),
		mkHop(3, "t3", "client", "svc", "GET", "/x", 500),
	}

	d := DiffHops(a, b, HopOptions{})
	if len(d.NewErrors) != 1 {
		t.Fatalf("three hops sharing method+path+status must dedup to one finding, got %+v", d.NewErrors)
	}
}

func TestHopRequireMissingRouteIsAFailure(t *testing.T) {
	b := []trace.Hop{
		mkHop(1, "t1", "client", "svc", "GET", "/x", 200),
	}
	failures := RequiredRouteFailures(b, []config.RequiredRoute{{Method: "GET", Path: "/never-called", Status: 200}})
	if len(failures) != 1 || failures[0].Reason != "missing" {
		t.Fatalf("expected one missing failure, got %+v", failures)
	}
}

func TestHopRequireWrongStatusReportsTheActualStatus(t *testing.T) {
	b := []trace.Hop{
		mkHop(1, "t1", "client", "svc", "GET", "/x", 503),
	}
	failures := RequiredRouteFailures(b, []config.RequiredRoute{{Method: "GET", Path: "/x", Status: 200}})
	if len(failures) != 1 || failures[0].Reason != "wrong-status" || failures[0].ActualStatus != 503 || failures[0].ExpectedStatus != 200 {
		t.Fatalf("expected wrong-status carrying actual=503 expected=200, got %+v", failures)
	}
}

func TestHopRequireConfiguredDistinguishesNoAssertionsFromAllPassing(t *testing.T) {
	b := []trace.Hop{
		mkHop(1, "t1", "client", "svc", "GET", "/x", 200),
	}

	noAssertions := DiffHops(nil, b, HopOptions{})
	if noAssertions.HopRequireConfigured {
		t.Fatalf("no Require entries: HopRequireConfigured must be false")
	}
	if len(noAssertions.RequiredRouteFailures) != 0 {
		t.Fatalf("no Require entries: RequiredRouteFailures must be empty, got %+v", noAssertions.RequiredRouteFailures)
	}

	allPassing := DiffHops(nil, b, HopOptions{Require: []config.RequiredRoute{{Method: "GET", Path: "/x", Status: 200}}})
	if !allPassing.HopRequireConfigured {
		t.Fatalf("Require entries present: HopRequireConfigured must be true even though everything passed")
	}
	if len(allPassing.RequiredRouteFailures) != 0 {
		t.Fatalf("all Require entries satisfied: RequiredRouteFailures must be empty, got %+v", allPassing.RequiredRouteFailures)
	}
}

// repeatHops builds n hops sharing everything but Seq and TraceID (each
// call is its own logical exchange, so each needs a distinct TraceID or
// CollapseRelays' successor lookup could chain them together).
func repeatHops(n int, tracePrefix, from, to, method, path string, status int) []trace.Hop {
	out := make([]trace.Hop, n)
	for i := range out {
		out[i] = mkHop(uint64(i+1), tracePrefix+string(rune('a'+i)), from, to, method, path, status)
	}
	return out
}

func TestRoutesFromLogicalHopsAppliesNormalize(t *testing.T) {
	hops := []trace.Hop{
		mkHop(1, "t1", "client", "svc", "GET", "/cart/42", 200),
	}
	normalize := func(p string) string {
		if p == "/cart/42" {
			return "/cart/:id"
		}
		return p
	}
	routes := CollapsedRoutes(hops, normalize)
	if len(routes) != 1 || routes[0].Path != "/cart/:id" {
		t.Fatalf("expected the normalized path, got %+v", routes)
	}
}

func TestDiffHopsResultIsJSONSerializableWithContractTags(t *testing.T) {
	d := HopDiff{
		ServiceCounts:         []ServiceCount{{Service: "svc", A: 1, B: 2, Deviates: true}},
		NewErrors:             []StatusFinding{{Seq: 1, Method: "GET", Path: "/x", Status: 500}},
		GoneErrors:            nil,
		NewRoutes:             []Route{{To: "svc", Method: "GET", Path: "/x"}},
		GoneRoutes:            []Route{},
		RequiredRouteFailures: []RouteFailure{{Method: "GET", Path: "/x", ExpectedStatus: 200, ActualStatus: 500, Reason: "wrong-status"}},
		HopRequireConfigured:  true,
	}
	assertJSONKeys(t, d, []string{"serviceCounts", "newErrors", "newRoutes", "goneRoutes", "requiredFailures", "hopRequireConfigured"})
}
