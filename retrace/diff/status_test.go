package diff

import (
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/config"
)

func TestUnexpectedStatusesIgnoreTheQueryStringWhenMatchingGlobs(t *testing.T) {
	hops := []trace.Hop{
		hop(1, "GET", "/api/cards/42/eligibility?fresh=1", 404, "", ""),
	}
	expected := []config.StatusRule{{Path: "/api/cards/*/eligibility", Status: 404}}

	got := FindUnexpectedStatuses(hops, expected)

	if len(got) != 0 {
		t.Fatalf("expected the query string to be stripped before matching, excusing this status; got %+v", got)
	}
}

func TestAnyUnallowlisted4xxOr5xxIsAFinding(t *testing.T) {
	hops := []trace.Hop{
		hop(1, "GET", "/api/orders", 400, "", ""),
		hop(2, "GET", "/api/orders", 500, "", ""),
		hop(3, "GET", "/api/orders", 200, "", ""),
	}

	got := FindUnexpectedStatuses(hops, nil)

	if len(got) != 2 {
		t.Fatalf("want 2 findings (400 and 500, not the 200), got %d: %+v", len(got), got)
	}
	if got[0].Status != 400 || got[0].Seq != 1 {
		t.Errorf("finding 0 = %+v, want seq 1 status 400", got[0])
	}
	if got[1].Status != 500 || got[1].Seq != 2 {
		t.Errorf("finding 1 = %+v, want seq 2 status 500", got[1])
	}
}

func TestADoubleStarGlobSpansSegments(t *testing.T) {
	hops := []trace.Hop{
		hop(1, "GET", "/api/v1/cards/42/nested/eligibility", 404, "", ""),
	}
	expected := []config.StatusRule{{Path: "/api/**/eligibility", Status: 404}}

	got := FindUnexpectedStatuses(hops, expected)

	if len(got) != 0 {
		t.Fatalf("** should span the middle segments regardless of count, got %+v", got)
	}

	// A single '*' must NOT span segment boundaries the way '**' does —
	// this is the asymmetry check: swap the glob and the same hop must go
	// back to being a finding.
	singleStar := []config.StatusRule{{Path: "/api/*/eligibility", Status: 404}}
	got2 := FindUnexpectedStatuses(hops, singleStar)
	if len(got2) != 1 {
		t.Fatalf("a single '*' must not span multiple segments; want 1 finding, got %d: %+v", len(got2), got2)
	}
}

func TestAHopWithNoStatusIsNotAFinding(t *testing.T) {
	hops := []trace.Hop{
		{Seq: 1, Method: "GET", Path: "/api/orders", Err: "connection reset"},
	}

	got := FindUnexpectedStatuses(hops, nil)

	if len(got) != 0 {
		t.Fatalf("a transport error (status 0, Err set) must not be a finding; got %+v", got)
	}
}

func TestMatchURLGlobStripsFragmentToo(t *testing.T) {
	if !MatchURLGlob("/cart", "/cart#section") {
		t.Fatal("a fragment must be stripped before matching, same as a query string")
	}
}

func TestMatchURLGlobRequiresSameSegmentCount(t *testing.T) {
	if MatchURLGlob("/api/cards/*", "/api/cards/42/eligibility") {
		t.Fatal("a single '*' pattern with fewer segments than the path must not match")
	}
}

func TestMatchURLGlobLiteralPathsMatchExactly(t *testing.T) {
	if !MatchURLGlob("/api/cards", "/api/cards") {
		t.Fatal("identical literal paths must match")
	}
	if MatchURLGlob("/api/cards", "/api/card") {
		t.Fatal("a literal glob must not match a different literal path")
	}
}

func TestMatchURLGlobDoubleStarMatchesZeroSegments(t *testing.T) {
	if !MatchURLGlob("/api/**/eligibility", "/api/eligibility") {
		t.Fatal("'**' must be able to span zero segments, not just one-or-more")
	}
}
