package diff

import (
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
)

const openAPIFixture = "testdata/openapi.json"

func apiHop(seq uint64, method, path string, status int, respBody string) trace.Hop {
	return trace.Hop{Seq: seq, Method: method, Path: path, Status: status, Resp: trace.Payload{Body: respBody}}
}

func findingKinds(t *testing.T, findings []ConformanceFinding) []string {
	t.Helper()
	out := make([]string, len(findings))
	for i, f := range findings {
		out[i] = f.Kind
	}
	return out
}

// TestCheckOpenAPIEndToEndAcrossAKindOfEachFinding drives CheckOpenAPI —
// the sole entry point — across one batch of hops that together exercise
// every finding Kind plus the two "no finding" cases (a clean call, and a
// transport error with no status to check). Per-behaviour tests below
// pin each piece individually.
func TestCheckOpenAPIEndToEndAcrossAKindOfEachFinding(t *testing.T) {
	hops := []trace.Hop{
		apiHop(1, "GET", "/cart", 200, `{"items":[]}`),       // clean: ref-followed schema satisfied
		apiHop(2, "GET", "/cart", 200, `{}`),                 // missing-required-field: "items"
		apiHop(3, "GET", "/does-not-exist", 200, `{}`),       // unknown-path
		apiHop(4, "DELETE", "/cart", 200, `{}`),              // unknown-method
		apiHop(5, "GET", "/cart", 404, `{}`),                 // undocumented-status
		apiHop(6, "GET", "/users/42", 201, `{"id":"42"}`),    // clean via the 2XX range
		{Seq: 7, Method: "GET", Path: "/cart", Err: "reset"}, // transport error: no status, skipped entirely
	}

	findings, err := CheckOpenAPI(hops, openAPIFixture)
	if err != nil {
		t.Fatalf("CheckOpenAPI: %v", err)
	}

	kinds := findingKinds(t, findings)
	want := []string{"missing-required-field", "unknown-path", "unknown-method", "undocumented-status"}
	if len(kinds) != len(want) {
		t.Fatalf("findings = %+v, want kinds %v (one per non-clean hop, none for the two clean hops or the transport error)", findings, want)
	}
	for _, k := range want {
		found := false
		for _, got := range kinds {
			if got == k {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected a %q finding among %v", k, kinds)
		}
	}
}

func TestAnUndocumentedPathIsAFinding(t *testing.T) {
	findings, err := CheckOpenAPI([]trace.Hop{apiHop(1, "GET", "/nonexistent", 200, `{}`)}, openAPIFixture)
	if err != nil {
		t.Fatalf("CheckOpenAPI: %v", err)
	}
	if len(findings) != 1 || findings[0].Kind != "unknown-path" {
		t.Fatalf("expected one unknown-path finding, got %+v", findings)
	}
}

func TestATemplatedPathMatchesASegment(t *testing.T) {
	findings, err := CheckOpenAPI([]trace.Hop{apiHop(1, "GET", "/users/42", 200, `{"id":"42"}`)}, openAPIFixture)
	if err != nil {
		t.Fatalf("CheckOpenAPI: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("/users/{id} must match /users/42 as one segment; got findings %+v", findings)
	}

	// Asymmetry check: a template segment matches exactly ONE path
	// segment, not a whole sub-tree — /users/42/extra must NOT match.
	extra, err := CheckOpenAPI([]trace.Hop{apiHop(1, "GET", "/users/42/extra", 200, `{}`)}, openAPIFixture)
	if err != nil {
		t.Fatalf("CheckOpenAPI: %v", err)
	}
	if len(extra) != 1 || extra[0].Kind != "unknown-path" {
		t.Fatalf("/users/42/extra has one extra segment beyond the template; want unknown-path, got %+v", extra)
	}
}

func TestAnUndocumentedStatusIsAFindingAndARangeCounts(t *testing.T) {
	// 201 is not documented literally on /users/{id}, only via "2XX" —
	// the range form must count.
	ranged, err := CheckOpenAPI([]trace.Hop{apiHop(1, "GET", "/users/7", 201, `{"id":"7"}`)}, openAPIFixture)
	if err != nil {
		t.Fatalf("CheckOpenAPI: %v", err)
	}
	if len(ranged) != 0 {
		t.Fatalf("201 must be covered by the documented 2XX range; got findings %+v", ranged)
	}

	// /cart only documents 200 — 404 is genuinely undocumented.
	undoc, err := CheckOpenAPI([]trace.Hop{apiHop(1, "GET", "/cart", 404, `{}`)}, openAPIFixture)
	if err != nil {
		t.Fatalf("CheckOpenAPI: %v", err)
	}
	if len(undoc) != 1 || undoc[0].Kind != "undocumented-status" {
		t.Fatalf("expected one undocumented-status finding for /cart 404, got %+v", undoc)
	}
}

func TestAMissingRequiredFieldIsAFinding(t *testing.T) {
	findings, err := CheckOpenAPI([]trace.Hop{apiHop(1, "GET", "/cart", 200, `{}`)}, openAPIFixture)
	if err != nil {
		t.Fatalf("CheckOpenAPI: %v", err)
	}
	if len(findings) != 1 || findings[0].Kind != "missing-required-field" {
		t.Fatalf("expected one missing-required-field finding, got %+v", findings)
	}
	if !strings.Contains(findings[0].Detail, "items") {
		t.Fatalf("Detail should name the missing field %q, got %q", "items", findings[0].Detail)
	}
}

func TestARefIsFollowedOneLevel(t *testing.T) {
	// /cart's 200 response schema is a bare {"$ref": "#/components/schemas/Cart"}.
	// A body satisfying Cart's required list must produce no finding —
	// this only holds if the $ref was actually resolved (paired with
	// TestAMissingRequiredFieldIsAFinding, which shows the required list
	// IS enforced when it's missing; together they pin that the list
	// comes from the resolved ref, not from nowhere).
	findings, err := CheckOpenAPI([]trace.Hop{apiHop(1, "GET", "/cart", 200, `{"items":[]}`)}, openAPIFixture)
	if err != nil {
		t.Fatalf("CheckOpenAPI: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("a body satisfying the $ref-resolved schema must produce no finding, got %+v", findings)
	}
}

func TestAMissingSpecFileIsAnErrorNotSilentSuccess(t *testing.T) {
	findings, err := CheckOpenAPI([]trace.Hop{apiHop(1, "GET", "/cart", 200, `{}`)}, "testdata/does-not-exist.json")
	if err == nil {
		t.Fatal("a missing spec file must return an error, not silently report zero findings")
	}
	if findings != nil {
		t.Fatalf("findings must be nil on error, got %+v", findings)
	}
}

func TestConformanceFindingJSONKeysMatchContract(t *testing.T) {
	assertJSONKeys(t, ConformanceFinding{Method: "GET", Path: "/x", Status: 200, Kind: "unknown-path", Detail: "d"},
		[]string{"method", "path", "status", "kind", "detail"})
}
