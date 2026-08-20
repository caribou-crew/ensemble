package server

import "testing"

// White-box test for joinEntityURL: lives in package server (not
// server_test) because this is the actual security boundary for entity
// passthrough (see entities.go's doc comment) and is worth pinning
// directly, independent of net/http.ServeMux's own path-cleaning/redirect
// behavior for the incoming request (which a black-box HTTP test would
// otherwise be at the mercy of — a ".." in the request line gets resolved,
// and possibly redirected, before ever reaching PathValue("path")).
func TestJoinEntityURLNeverEscapesBase(t *testing.T) {
	cases := []struct {
		name    string
		base    string
		subPath string
		want    string
	}{
		{"simple append", "http://127.0.0.1:9000/v1", "users/1", "http://127.0.0.1:9000/v1/users/1"},
		{"empty subpath", "http://127.0.0.1:9000/v1", "", "http://127.0.0.1:9000/v1"},
		{"traversal within bounds collapses harmlessly", "http://127.0.0.1:9000/v1", "a/../b", "http://127.0.0.1:9000/v1/b"},
		{"traversal attempting to climb above base is rooted at base, not the filesystem", "http://127.0.0.1:9000/v1", "../../../../etc/passwd", "http://127.0.0.1:9000/v1/etc/passwd"},
		{"traversal attempting to climb above base's own path segment", "http://127.0.0.1:9000/v1", "../secret", "http://127.0.0.1:9000/v1/secret"},
		{"base with no path", "http://127.0.0.1:9000", "../../x", "http://127.0.0.1:9000/x"},
		{"base with trailing slash", "http://127.0.0.1:9000/v1/", "x", "http://127.0.0.1:9000/v1/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := joinEntityURL(tc.base, tc.subPath, "")
			if err != nil {
				t.Fatalf("joinEntityURL(%q, %q): %v", tc.base, tc.subPath, err)
			}
			if got != tc.want {
				t.Errorf("joinEntityURL(%q, %q) = %q, want %q", tc.base, tc.subPath, got, tc.want)
			}
		})
	}
}

// TestJoinEntityURLForwardsQuery confirms rawQuery passes through verbatim
// and isn't itself treated as part of the path to clean.
func TestJoinEntityURLForwardsQuery(t *testing.T) {
	got, err := joinEntityURL("http://127.0.0.1:9000/v1", "users", "active=true&limit=5")
	if err != nil {
		t.Fatalf("joinEntityURL: %v", err)
	}
	want := "http://127.0.0.1:9000/v1/users?active=true&limit=5"
	if got != want {
		t.Errorf("joinEntityURL = %q, want %q", got, want)
	}
}

// TestJoinEntityURLRejectsInvalidBase guards against a malformed
// cfg.Entities[name].Base (relative, or otherwise not an absolute http(s)
// URL) silently producing a nonsensical target.
func TestJoinEntityURLRejectsInvalidBase(t *testing.T) {
	for _, base := range []string{"", "not a url", "/just/a/path", "ftp://127.0.0.1"} {
		if _, err := joinEntityURL(base, "x", ""); err == nil {
			t.Errorf("joinEntityURL(%q, ...): want error, got nil", base)
		}
	}
}
