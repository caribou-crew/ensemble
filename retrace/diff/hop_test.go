package diff

import (
	"reflect"
	"strings"
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
	// Pins MUT-08: the deduped finding must carry a REAL hop's Seq (the
	// first occurrence's, per the dedup's first-wins map insert), not a
	// placeholder zero.
	if d.NewErrors[0].Seq != 1 {
		t.Fatalf("deduped finding Seq = %d, want 1 (the first occurrence's), got %+v", d.NewErrors[0].Seq, d.NewErrors[0])
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
	// Pins MUT-05: a "missing" failure must carry ActualStatus 0, not the
	// expected status — there is no actual status to report because no
	// hop was ever seen.
	if failures[0].ExpectedStatus != 200 || failures[0].ActualStatus != 0 {
		t.Fatalf("missing failure = %+v, want ExpectedStatus=200 ActualStatus=0", failures[0])
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

// --- Fix round 1: ports of the JS reference's missing tests, plus the
// mutation-survivor-targeted tests the review called for. ---

// C2 — hopExpect, the implementer's own novel wiring, ported from
// flowlens/test/hop-diff.test.mjs.

func TestHopExpectExcusesAKnownErrorFromBothSidesEntirely(t *testing.T) {
	a := []trace.Hop{
		mkHop(1, "ta1", "client", "bff1", "GET", "/v1/cards", 200),
		mkHop(2, "ta2", "client", "bff1", "GET", "/v1/internal/digitalwalletprovisionrequests/eligibility", 400),
	}
	b := []trace.Hop{
		mkHop(1, "tb1", "client", "bff1", "GET", "/v1/cards", 200),
		mkHop(2, "tb2", "client", "bff1", "GET", "/v1/internal/digitalwalletprovisionrequests/eligibility", 400),
	}
	expected := []config.StatusRule{{Path: "/v1/internal/digitalwalletprovisionrequests/eligibility", Status: 400}}

	d := DiffHops(a, b, HopOptions{Expected: expected})
	if len(d.NewErrors) != 0 || len(d.GoneErrors) != 0 {
		t.Fatalf("an excused error present on both sides must never surface as new or gone: new=%+v gone=%+v", d.NewErrors, d.GoneErrors)
	}
}

func TestAnExcusedErrorOnOnlyOneSideStillNeverSurfacesAsNewOrGone(t *testing.T) {
	a := []trace.Hop{mkHop(1, "ta1", "client", "bff1", "GET", "/v1/cards", 200)}
	b := []trace.Hop{
		mkHop(1, "tb1", "client", "bff1", "GET", "/v1/cards", 200),
		mkHop(2, "tb2", "client", "bff1", "GET", "/v1/internal/digitalwalletprovisionrequests/eligibility", 400),
	}
	expected := []config.StatusRule{{Path: "/v1/internal/digitalwalletprovisionrequests/eligibility", Status: 400}}

	d := DiffHops(a, b, HopOptions{Expected: expected})
	if len(d.NewErrors) != 0 {
		t.Fatalf("an excused error appearing on only one side must not surface as new: %+v", d.NewErrors)
	}
}

// I2 — the "gone" half of every signal, ported/added: nothing in the
// suite before this asserted a non-empty GoneRoutes or GoneErrors.

func TestARouteOnTheReferenceButAbsentFromTheLatestRunVanished(t *testing.T) {
	a := []trace.Hop{
		mkHop(1, "ta1", "client", "bff1", "GET", "/v1/cards", 200),
		mkHop(2, "ta2", "client", "bff1", "GET", "/v2/wallet", 200),
	}
	b := []trace.Hop{
		mkHop(1, "tb1", "client", "bff1", "GET", "/v1/cards", 200),
	}

	d := DiffHops(a, b, HopOptions{})
	if len(d.NewRoutes) != 0 {
		t.Fatalf("expected no new routes, got %+v", d.NewRoutes)
	}
	if !hasRoute(d.GoneRoutes, "bff1", "GET", "/v2/wallet") {
		t.Fatalf("expected /v2/wallet as a gone route, got %+v", d.GoneRoutes)
	}
	if len(d.GoneRoutes) != 1 {
		t.Fatalf("expected exactly one gone route, got %+v", d.GoneRoutes)
	}
}

func TestAnErrorStatusOnlyOnTheReferenceVanished(t *testing.T) {
	a := []trace.Hop{
		mkHop(1, "ta1", "client", "bff1", "GET", "/v1/cards", 200),
		mkHop(2, "ta2", "client", "bff1", "GET", "/v1/risk", 500),
	}
	b := []trace.Hop{
		mkHop(1, "tb1", "client", "bff1", "GET", "/v1/cards", 200),
	}

	d := DiffHops(a, b, HopOptions{})
	if len(d.NewErrors) != 0 {
		t.Fatalf("expected no new errors, got %+v", d.NewErrors)
	}
	if len(d.GoneErrors) != 1 || d.GoneErrors[0].Path != "/v1/risk" || d.GoneErrors[0].Status != 500 {
		t.Fatalf("expected /v1/risk 500 as a gone error, got %+v", d.GoneErrors)
	}
}

// I3 — Normalize, applied to routes and to error signatures, driven
// through DiffHops itself (not CollapsedRoutes, a different entry point).

func normalizeCartIDForHopTests(p string) string {
	if strings.HasPrefix(p, "/cart/") {
		return "/cart/{id}"
	}
	return p
}

func TestNormalizeIsAppliedToBothSidesRoutesThroughDiffHops(t *testing.T) {
	a := []trace.Hop{mkHop(1, "ta1", "client", "bff", "GET", "/cart/1", 200)}
	b := []trace.Hop{mkHop(1, "tb1", "client", "bff", "GET", "/cart/2", 200)}

	d := DiffHops(a, b, HopOptions{Normalize: normalizeCartIDForHopTests})
	if len(d.NewRoutes) != 0 || len(d.GoneRoutes) != 0 {
		t.Fatalf("normalized, /cart/1 and /cart/2 are the same route; want no new/gone routes, got new=%+v gone=%+v", d.NewRoutes, d.GoneRoutes)
	}
}

func TestNormalizeIsAppliedToErrorSignaturesThroughDiffHops(t *testing.T) {
	a := []trace.Hop{mkHop(1, "ta1", "client", "bff", "GET", "/cart/1", 500)}
	b := []trace.Hop{mkHop(1, "tb1", "client", "bff", "GET", "/cart/2", 500)}

	d := DiffHops(a, b, HopOptions{Normalize: normalizeCartIDForHopTests})
	if len(d.NewErrors) != 0 || len(d.GoneErrors) != 0 {
		t.Fatalf("normalized, /cart/1 500 and /cart/2 500 are the same error signature; want no new/gone errors, got new=%+v gone=%+v", d.NewErrors, d.GoneErrors)
	}
}

// I4 — hopRequire's glob matching (the implementer's own deviation #2)
// and its method comparison, ported from the H17 tests plus a
// method-mismatch case neither the JS source nor the original Go tests
// covered.

func TestHopRequireWildcardSegmentMatchesARealPathParam(t *testing.T) {
	b := []trace.Hop{mkHop(1, "t1", "client", "svc", "GET", "/v1/internal/users/98765/profile", 200)}
	require := []config.RequiredRoute{{Method: "GET", Path: "/v1/internal/users/*/profile", Status: 200}}

	failures := RequiredRouteFailures(b, require)
	if len(failures) != 0 {
		t.Fatalf("a wildcard segment must match a hop carrying a real path param, got %+v", failures)
	}
}

func TestHopRequireLiteralIdDoesNotMatchADifferentId(t *testing.T) {
	b := []trace.Hop{mkHop(1, "t1", "client", "svc", "GET", "/v1/internal/users/98765/profile", 200)}
	require := []config.RequiredRoute{{Method: "GET", Path: "/v1/internal/users/11111/profile", Status: 200}}

	failures := RequiredRouteFailures(b, require)
	if len(failures) != 1 || failures[0].Reason != "missing" {
		t.Fatalf("a literal id must not match a hop with a different id at that segment, got %+v", failures)
	}
}

func TestHopRequireMethodMustMatchExactly(t *testing.T) {
	b := []trace.Hop{mkHop(1, "t1", "client", "svc", "POST", "/x", 200)}
	require := []config.RequiredRoute{{Method: "GET", Path: "/x", Status: 200}}

	failures := RequiredRouteFailures(b, require)
	if len(failures) != 1 || failures[0].Reason != "missing" {
		t.Fatalf("a required GET must not be satisfied by a POST to the same path, got %+v", failures)
	}
}

// I6 — the fixture-symmetry gap the sweep missed: every relay test above
// puts the relay on side B. These mirror them onto side A, plus the JS's
// recurrence test, which — done as a straight port — doesn't actually
// exercise dedup (see the comment on the second test below).

func TestRelayFoldingAppliesToSideATooNotJustSideB(t *testing.T) {
	// Side A recorded through a relay; side B calls bff directly. Folded,
	// both sides made the SAME single logical call. A same-side-only fold
	// bug (attributing side A's count to the relay, or skipping collapse
	// on side A only) would have hidden behind every other test in this
	// file, which all put the relay on side B.
	a := []trace.Hop{
		mkHop(1, "t1", "client", "edge", "GET", "/x", 200),
		mkHop(2, "t1", "edge", "bff", "GET", "/x", 200),
	}
	b := []trace.Hop{
		mkHop(1, "t2", "client", "bff", "GET", "/x", 200),
	}

	d := DiffHops(a, b, HopOptions{})
	if len(d.NewRoutes) != 0 || len(d.GoneRoutes) != 0 {
		t.Fatalf("both sides made one logical bff call; want no new/gone routes, got new=%+v gone=%+v", d.NewRoutes, d.GoneRoutes)
	}
	for _, c := range d.ServiceCounts {
		if c.Service == "edge" {
			t.Fatalf("edge must not appear as a service when the relay is on side A: %+v", d.ServiceCounts)
		}
	}
	bff := serviceCount(t, d.ServiceCounts, "bff")
	if bff.A != 1 || bff.B != 1 {
		t.Fatalf("bff count = %+v, want A=1 B=1 (folded via Origin on side A too)", bff)
	}
}

func TestARouteOnBothSidesIsNeitherNewNorVanishedRegardlessOfRecurrence(t *testing.T) {
	a := []trace.Hop{
		mkHop(1, "ta1", "client", "bff1", "GET", "/v1/cards", 200),
		mkHop(2, "ta2", "client", "bff1", "GET", "/v1/cards", 200),
		mkHop(3, "ta3", "client", "bff1", "GET", "/v1/cards", 200),
	}
	b := []trace.Hop{
		mkHop(1, "tb1", "client", "bff1", "GET", "/v1/cards", 200),
	}

	d := DiffHops(a, b, HopOptions{})
	if len(d.NewRoutes) != 0 || len(d.GoneRoutes) != 0 {
		t.Fatalf("the same route recurring 3x on one side and once on the other is present on both; want no new/gone routes, got new=%+v gone=%+v", d.NewRoutes, d.GoneRoutes)
	}
}

// TestANewRouteIsListedOnceRegardlessOfHowManyTimesItRecursOnB is the
// mutation-killer for routesFromLogicalHops' dedup (MUT-24): unlike the
// test above (where the recurring route is present on both sides and so
// membership-check diffing hides a missing dedup either way), a route
// that recurs on the side where it's genuinely NEW must still be listed
// exactly once, not once per occurrence.
func TestANewRouteIsListedOnceRegardlessOfHowManyTimesItRecursOnB(t *testing.T) {
	a := []trace.Hop{mkHop(1, "ta1", "client", "bff1", "GET", "/v1/cards", 200)}
	b := []trace.Hop{
		mkHop(1, "tb1", "client", "bff1", "GET", "/v1/cards", 200),
		mkHop(2, "tb2", "client", "payments", "GET", "/v1/wallet", 200),
		mkHop(3, "tb3", "client", "payments", "GET", "/v1/wallet", 200),
		mkHop(4, "tb4", "client", "payments", "GET", "/v1/wallet", 200),
	}

	d := DiffHops(a, b, HopOptions{})
	if len(d.NewRoutes) != 1 {
		t.Fatalf("a new route recurring 3x on B must be listed once, not %d times: %+v", len(d.NewRoutes), d.NewRoutes)
	}
}

// m3 — countDeviates' boundary: JS uses a strict '>', so a ratio exactly
// at the tolerance must NOT deviate.

func TestServiceCountDriftExactlyAtToleranceIsNotFlagged(t *testing.T) {
	a := repeatHops(2, "ta", "client", "svc", "GET", "/x", 200) // |2-4|/4 == 0.5, exactly DefaultCountTolerance
	b := repeatHops(4, "tb", "client", "svc", "GET", "/x", 200)

	d := DiffHops(a, b, HopOptions{})
	svc := serviceCount(t, d.ServiceCounts, "svc")
	if svc.Deviates {
		t.Fatalf("a ratio exactly at the tolerance must not be flagged (strict '>', not '>='), got %+v", svc)
	}
}

// m4 — errorSignatures must treat 4xx as an error signature too, not
// just 5xx (every other error-signature test in this file uses 500).

func TestErrorSignatureDiffCoversFourXXNotJustFiveXX(t *testing.T) {
	a := []trace.Hop{}
	b := []trace.Hop{mkHop(1, "t1", "client", "svc", "GET", "/x", 400)}

	d := DiffHops(a, b, HopOptions{})
	if len(d.NewErrors) != 1 || d.NewErrors[0].Status != 400 {
		t.Fatalf("a 4xx must be treated as an error signature too, not just 5xx: %+v", d.NewErrors)
	}
}

// m9 — ServiceCounts' sort order, load-bearing for summary.json stability.

func TestServiceCountsAreSortedByServiceName(t *testing.T) {
	a := []trace.Hop{
		mkHop(1, "t1", "client", "zeta", "GET", "/z", 200),
		mkHop(2, "t2", "client", "alpha", "GET", "/a", 200),
		mkHop(3, "t3", "client", "mid", "GET", "/m", 200),
	}

	d := DiffHops(a, a, HopOptions{})
	var names []string
	for _, c := range d.ServiceCounts {
		names = append(names, c.Service)
	}
	want := []string{"alpha", "mid", "zeta"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("ServiceCounts order = %v, want %v (sorted)", names, want)
	}
}

// m2/m8 — the JS source's "empty run on both sides" test, ported as an
// exact-shape comparison: NewRoutes/GoneRoutes/ServiceCounts must all be
// non-nil empty slices (marshal as `[]`, not `null`), while
// NewErrors/GoneErrors/RequiredRouteFailures (all `omitempty`) stay nil.

func TestAnEmptyRunOnBothSidesReportsEmptyEverything(t *testing.T) {
	got := DiffHops(nil, nil, HopOptions{})
	want := HopDiff{
		ServiceCounts:        []ServiceCount{},
		NewRoutes:            []Route{},
		GoneRoutes:           []Route{},
		HopRequireConfigured: false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiffHops(nil, nil, HopOptions{}) = %+v, want %+v", got, want)
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
