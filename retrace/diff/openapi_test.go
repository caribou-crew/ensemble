package diff

import (
	"os"
	"slices"
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
		if !slices.Contains(kinds, k) {
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

// TestPathMatchingIsDeterministicAcrossRepeatedCalls pins C1: two
// templated paths of the same segment count ("/users/{id}" and
// "/{entity}/{id}") both match "/users/55", and Go's randomized map
// iteration order previously made matchOpenAPIPath return whichever one
// the runtime handed it first — a CI gate that flips between "conformant"
// and "undocumented-status" on unchanged input. "/users/{id}" is strictly
// more specific (one template segment vs two) and must win every time.
// It documents only a 2XX range, not 500, so a hop returning 500 must
// deterministically report undocumented-status against "/users/{id}" —
// if the less-specific "/{entity}/{id}" (which DOES document 500) ever
// won instead, this hop would silently report zero findings.
func TestPathMatchingIsDeterministicAcrossRepeatedCalls(t *testing.T) {
	hops := []trace.Hop{apiHop(1, "GET", "/users/55", 500, `{}`)}

	for i := range 200 {
		findings, err := CheckOpenAPI(hops, openAPIFixture)
		if err != nil {
			t.Fatalf("iteration %d: CheckOpenAPI: %v", i, err)
		}
		if len(findings) != 1 || findings[0].Kind != "undocumented-status" {
			t.Fatalf("iteration %d: want exactly one undocumented-status finding (via the more specific /users/{id}), got %+v", i, findings)
		}
		if !strings.Contains(findings[0].Detail, "/users/{id}") {
			t.Fatalf("iteration %d: Detail must name the more specific pattern /users/{id}, got %q", i, findings[0].Detail)
		}
		if strings.Contains(findings[0].Detail, "/{entity}/{id}") {
			t.Fatalf("iteration %d: Detail must not name the less specific pattern /{entity}/{id}, got %q", i, findings[0].Detail)
		}
	}
}

func TestALiteralPathWinsOverATemplateThatAlsoMatches(t *testing.T) {
	// /things/42 (literal) and /things/{id} (template) both match
	// "/things/42"; the literal must win. Proven by content, not just by
	// which Kind comes back: the literal's schema requires "literalField",
	// the template's requires "templateField" — a body carrying only
	// literalField must produce no finding only if the literal path item
	// was actually selected.
	findings, err := CheckOpenAPI([]trace.Hop{apiHop(1, "GET", "/things/42", 200, `{"literalField":1}`)}, "testdata/openapi-priority.json")
	if err != nil {
		t.Fatalf("CheckOpenAPI: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("the literal /things/42 path item must win over the template; got %+v", findings)
	}
}

// TestLeftmostLiteralSegmentWinsOverATemplateAtTheSamePosition pins R1
// (fix round 2): two templated patterns tie on specificity (one template
// segment each) — "/{tenant}/user" and "/~user/{id}" both match
// "/~user/user". Sorted-key order ALONE would pick "/{tenant}/user": '{' is
// 0x7B, which sorts below '~' (0x7E), so ascending sort.Strings puts the
// template-first pattern first. That is the wrong answer — the byte-sort
// coincidence the doc comment on pathPatternIsMoreSpecific describes. The
// correct rule (leftmost literal wins) picks "/~user/{id}", whose segment 0
// is the literal "~user" where "/{tenant}/user"'s segment 0 is a template.
// Proven by content, the same way TestALiteralPathWinsOverATemplateThatAlsoMatches
// is: the two patterns document different required fields, so only the
// actually-selected pattern's requirement is enforced.
func TestLeftmostLiteralSegmentWinsOverATemplateAtTheSamePosition(t *testing.T) {
	specPath := writeTempSpec(t, `{
		"paths": {
			"/{tenant}/user": {
				"get": {
					"responses": {
						"200": {
							"content": {
								"application/json": {
									"schema": { "required": ["tenantField"] }
								}
							}
						}
					}
				}
			},
			"/~user/{id}": {
				"get": {
					"responses": {
						"200": {
							"content": {
								"application/json": {
									"schema": { "required": ["idField"] }
								}
							}
						}
					}
				}
			}
		},
		"components": { "schemas": {} }
	}`)

	findings, err := CheckOpenAPI([]trace.Hop{apiHop(1, "GET", "/~user/user", 200, `{"idField":1}`)}, specPath)
	if err != nil {
		t.Fatalf("CheckOpenAPI: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("the leftmost-literal pattern /~user/{id} must win over /{tenant}/user; got %+v", findings)
	}
}

// TestAllTemplateTieFallsBackToSortedKeyOrder pins key 3 of
// pathPatternIsMoreSpecific: "/{a}/orders" and "/{b}/orders" both match
// "/x/orders", tie on specificity (one template segment each) AND on
// leftmost-literal (both patterns are template-at-every-position, so key 2
// never finds a differing literal/template position). This is a live
// branch — not the "should not occur for well-formed specs" case it might
// look like — and the ruling is that key 3 (sorted-key ascending order)
// decides it, arbitrarily but stably: "/{a}/orders" sorts first.
func TestAllTemplateTieFallsBackToSortedKeyOrder(t *testing.T) {
	specPath := writeTempSpec(t, `{
		"paths": {
			"/{a}/orders": {
				"get": {
					"responses": {
						"200": {
							"content": {
								"application/json": {
									"schema": { "required": ["aField"] }
								}
							}
						}
					}
				}
			},
			"/{b}/orders": {
				"get": {
					"responses": {
						"200": {
							"content": {
								"application/json": {
									"schema": { "required": ["bField"] }
								}
							}
						}
					}
				}
			}
		},
		"components": { "schemas": {} }
	}`)

	findings, err := CheckOpenAPI([]trace.Hop{apiHop(1, "GET", "/x/orders", 200, `{"aField":1}`)}, specPath)
	if err != nil {
		t.Fatalf("CheckOpenAPI: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("an all-template tie must fall back to sorted-key order, picking /{a}/orders (sorts before /{b}/orders); got %+v", findings)
	}
}

func TestDefaultResponseCoversAnUndocumentedStatus(t *testing.T) {
	findings, err := CheckOpenAPI([]trace.Hop{apiHop(1, "GET", "/orders", 503, `{"orderId":"o1"}`)}, openAPIFixture)
	if err != nil {
		t.Fatalf("CheckOpenAPI: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("503 is not documented literally or as a range on /orders, but IS covered by \"default\"; want no findings, got %+v", findings)
	}
}

func TestCheckOpenAPIStripsTheQueryStringBeforeMatchingThePath(t *testing.T) {
	findings, err := CheckOpenAPI([]trace.Hop{apiHop(1, "GET", "/cart?ref=abc123", 200, `{"items":[]}`)}, openAPIFixture)
	if err != nil {
		t.Fatalf("CheckOpenAPI: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("a query string must be stripped before path matching (every real capture carries one); got %+v", findings)
	}
}

func TestAnUnresolvableRefIsUncheckedNotAPass(t *testing.T) {
	specPath := writeTempSpec(t, `{
		"paths": {
			"/widgets": {
				"get": {
					"responses": {
						"200": {
							"content": {
								"application/json": {
									"schema": { "$ref": "#/components/schemas/DoesNotExist" }
								}
							}
						}
					}
				}
			}
		},
		"components": { "schemas": {} }
	}`)

	findings, err := CheckOpenAPI([]trace.Hop{apiHop(1, "GET", "/widgets", 200, `{}`)}, specPath)
	if err != nil {
		t.Fatalf("CheckOpenAPI: %v", err)
	}
	if len(findings) != 1 || findings[0].Kind != "unchecked" {
		t.Fatalf("an unresolvable $ref must report \"unchecked\", not a silent pass; got %+v", findings)
	}
}

func TestATruncatedBodyIsUncheckedNotAPass(t *testing.T) {
	truncatedHop := apiHop(1, "GET", "/cart", 200, `{}`) // missing "items" -- would normally be a finding
	truncatedHop.Resp.Truncated = true

	findings, err := CheckOpenAPI([]trace.Hop{truncatedHop}, openAPIFixture)
	if err != nil {
		t.Fatalf("CheckOpenAPI: %v", err)
	}
	if len(findings) != 1 || findings[0].Kind != "unchecked" {
		t.Fatalf("a redaction-truncated body must report \"unchecked\", not a silent pass (and not the missing-field finding a lucky parse might produce); got %+v", findings)
	}
}

func TestAnUnparseableBodyIsUncheckedNotAPass(t *testing.T) {
	findings, err := CheckOpenAPI([]trace.Hop{apiHop(1, "GET", "/cart", 200, `not json at all`)}, openAPIFixture)
	if err != nil {
		t.Fatalf("CheckOpenAPI: %v", err)
	}
	if len(findings) != 1 || findings[0].Kind != "unchecked" {
		t.Fatalf("a body that fails json.Unmarshal must report \"unchecked\", not a silent pass; got %+v", findings)
	}
}

// TestALiteralPathIsMatchedByExactStringNotJustBySegments pins the part of
// MUT-36 (removing matchOpenAPIPath's exact-match-first branch) that
// TestALiteralPathWinsOverATemplateThatAlsoMatches does NOT reach: two
// literal (template-free) keys, "/dup" and "/dup/", split into the SAME
// segments (["dup"]) — a trailing slash contributes no segment. With the
// exact-match branch, requesting "/dup/" finds the "/dup/" entry directly
// by string. Without it, both candidates tie at specificity 0 and the
// general loop's sorted-key tie-break ("/dup" < "/dup/") silently picks the
// wrong one — a real behavior change the specificity ranking alone cannot
// prevent, since it only orders by TEMPLATE-segment count, not by which
// string was actually requested.
func TestALiteralPathIsMatchedByExactStringNotJustBySegments(t *testing.T) {
	specPath := writeTempSpec(t, `{
		"paths": {
			"/dup/": {
				"get": {
					"responses": {
						"200": {
							"content": {
								"application/json": {
									"schema": { "required": ["trailingSlashField"] }
								}
							}
						}
					}
				}
			},
			"/dup": {
				"get": {
					"responses": {
						"200": {
							"content": {
								"application/json": {
									"schema": { "required": ["bareField"] }
								}
							}
						}
					}
				}
			}
		},
		"components": { "schemas": {} }
	}`)

	findings, err := CheckOpenAPI([]trace.Hop{apiHop(1, "GET", "/dup/", 200, `{}`)}, specPath)
	if err != nil {
		t.Fatalf("CheckOpenAPI: %v", err)
	}
	if len(findings) != 1 || findings[0].Kind != "missing-required-field" {
		t.Fatalf("expected one missing-required-field finding, got %+v", findings)
	}
	if !strings.Contains(findings[0].Detail, "trailingSlashField") {
		t.Fatalf("requesting the exact string \"/dup/\" must match the \"/dup/\" entry (missing trailingSlashField), not the segment-equivalent \"/dup\" entry; got %q", findings[0].Detail)
	}
}

func writeTempSpec(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	p := dir + "/spec.json"
	if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing temp spec: %v", err)
	}
	return p
}
