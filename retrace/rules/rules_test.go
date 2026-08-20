package rules

import (
	"strings"
	"testing"
)

func TestPathGlobsScopeARuleStarOneSegmentDoubleStarAnySpan(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"/experience/*", "/experience/home", true},
		{"/experience/*", "/experience/home/v2", false},
		{"/experience/*.json", "/experience/home.json", true},
		{"/api/**", "/api/v1/cart/items", true},
		{"/api/**/items", "/api/v1/cart/items", true},
		{"", "/anything", true}, // an unset path scopes to everything
	}
	for _, c := range cases {
		if got := MatchPathGlob(c.glob, c.path); got != c.want {
			t.Errorf("MatchPathGlob(%q, %q) = %v, want %v", c.glob, c.path, got, c.want)
		}
	}
}

func TestALaterMoreSpecificRuleOverridesAnEarlierGlobalOnePerKey(t *testing.T) {
	rs, err := Normalize([]Raw{
		{Headers: map[string]any{"x-request-id": "uuid", "date": "http-date"}},
		{Path: "/cart", Headers: map[string]any{"x-request-id": "ignore"}},
	})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	res := Resolve(rs, "GET", "/cart")
	if got := res.ForHeader("X-Request-Id").Kind; got != KindIgnore {
		t.Errorf("specific rule must win for its key: %v", got)
	}
	if got := res.ForHeader("date").Name; got != "http-date" {
		t.Errorf("untouched keys keep the global rule: %v", got)
	}
}

func TestMethodScopesARuleAndIsCaseInsensitive(t *testing.T) {
	rs, err := Normalize([]Raw{
		{Method: "post", Path: "/api/v1/auth/login", Headers: map[string]any{"x-request-id": "uuid"}},
	})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got := Resolve(rs, "POST", "/api/v1/auth/login").ForHeader("x-request-id"); got.Zero() {
		t.Error("POST should match a rule whose method was stored lower-cased as \"post\"")
	}
	if got := Resolve(rs, "GET", "/api/v1/auth/login").ForHeader("x-request-id"); !got.Zero() {
		t.Error("GET must not match a POST-scoped rule")
	}
}

func TestTheLastMatchingBodyGlobWins(t *testing.T) {
	rs, _ := Normalize([]Raw{{Body: map[string]any{"**.id": "uuid"}}, {Body: map[string]any{"order.id": "integer"}}})
	res := Resolve(rs, "POST", "/orders")
	if got := res.ForField("order.id").Name; got != "integer" {
		t.Errorf("ForField = %q, want integer", got)
	}
	if got := res.ForField("user.id").Name; got != "uuid" {
		t.Errorf("ForField = %q, want uuid", got)
	}
}

func TestHeaderLookupIsCaseInsensitiveOnTheName(t *testing.T) {
	rs, err := Normalize([]Raw{{Headers: map[string]any{"X-Request-Id": "uuid"}}})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	res := Resolve(rs, "GET", "/anything")
	if got := res.ForHeader("x-request-id"); got.Zero() {
		t.Error("a lower-case lookup should find a header stored under a different case")
	}
	if got := res.ForHeader("X-REQUEST-ID"); got.Zero() {
		t.Error("an upper-case lookup should find a header stored under a different case")
	}
}

func TestNormalizeRejectsAnUnknownMatcherNamingTheRuleIndex(t *testing.T) {
	_, err := Normalize([]Raw{
		{},
		{Headers: map[string]any{"x-request-id": "uuidv4"}},
	})
	if err == nil {
		t.Fatal("want an error naming the offending rule")
	}
	if !strings.Contains(err.Error(), "wireRules[1]") {
		t.Errorf("error should name the rule index: %v", err)
	}
}
