package rules

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// uiMatchersPath is the review UI's transcript of this package's matcher
// dialect. The path is relative to this test's own directory, so a move of
// either file breaks the test rather than silently skipping it.
const uiMatchersPath = "../../dashboard/retrace-ui/src/api/matchers.ts"

var (
	uiMatcherListRe = regexp.MustCompile(`(?s)export const MATCHER_NAMES = \[(.*?)\] as const;`)
	uiMatcherNameRe = regexp.MustCompile(`'([^']*)'`)
	uiDefaultRe     = regexp.MustCompile(`export const DEFAULT_MATCHER: MatcherName = '([^']*)';`)
)

// The matcher dialect has ONE home, and it is this package. The review UI's
// picker is a <select> over a transcript of Names(), and this test is what
// makes that a mechanically-verified copy instead of a second source of
// truth that drifts.
//
// It exists because the drift already happened, in the worst possible
// direction: the picker shipped a free-text box defaulting to "any", which
// ParseMatcher does not accept, and config.AppendWireRule validates before
// writing — so EVERY rule written without editing that box answered 400. The
// rule verb was broken on its own default path.
//
// Read this way round on purpose: Go decides, TypeScript transcribes. Adding
// a matcher here without adding it to the UI turns this red on the commit
// that adds it, which is the only moment anyone can cheaply fix it.
func TestTheReviewUIsMatcherOptionsAreExactlyTheDialect(t *testing.T) {
	b, err := os.ReadFile(filepath.FromSlash(uiMatchersPath))
	if err != nil {
		t.Fatalf("reading the review UI's matcher list at %s: %v\nThe rule picker is a <select> over this package's dialect and this test is what keeps the two from drifting; if the file moved, move this path with it rather than deleting the test.", uiMatchersPath, err)
	}
	src := string(b)

	m := uiMatcherListRe.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("no `export const MATCHER_NAMES = [...] as const;` literal in %s — the option list must stay a plain array of quoted strings so this test can read it", uiMatchersPath)
	}
	var got []string
	for _, q := range uiMatcherNameRe.FindAllStringSubmatch(m[1], -1) {
		got = append(got, q[1])
	}

	want := Names()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the review UI's matcher options have drifted from rules.Names().\n  %s: %v\n  rules.Names():   %v\nThe dialect is decided here and transcribed there; update %s.", uiMatchersPath, got, want, uiMatchersPath)
	}

	// And the default the picker opens on is a MEMBER of the dialect. This is
	// the assertion the shipped bug would have failed: "any" parses as
	// nothing, so every unedited rule 400'd.
	d := uiDefaultRe.FindStringSubmatch(src)
	if d == nil {
		t.Fatalf("no `export const DEFAULT_MATCHER: MatcherName = '...';` in %s", uiMatchersPath)
	}
	if _, err := ParseMatcher(d[1], "the review UI's default matcher"); err != nil {
		t.Fatalf("the rule picker opens on a matcher this package refuses: %v", err)
	}
}
