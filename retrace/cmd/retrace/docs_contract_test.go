package main

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/diff"
)

// The agent recipe (AGENTS.md + the retrace-iterate skill) is documentation an
// AGENT reads and then acts on without a human checking the command first.
// That makes stale docs worse here than usual: a human who reads "pass
// --allow-degrade" tries it, sees the error and adapts, while an agent
// confidently reports that it verified something it never ran.
//
// These tests are the versioning mechanism the recipe promises. They fail the
// build when the docs name a flag the CLI does not have, or a --json field the
// Summary does not carry. The second half is not hypothetical: the recipe was
// first drafted against a `triage` field that exists only in an unshipped
// proposal, and nothing but reading the source by hand would have caught it.

// repoRoot walks up from this package (retrace/cmd/retrace) to the directory
// holding go.work. Computed rather than hardcoded as "../../.." so moving the
// package fails loudly here instead of silently skipping every assertion.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not find go.work above the test's working directory")
	return ""
}

func docFiles(t *testing.T) map[string]string {
	t.Helper()
	root := repoRoot(t)
	out := map[string]string{}
	for _, rel := range []string{
		"AGENTS.md",
		filepath.Join(".claude", "skills", "retrace-iterate", "SKILL.md"),
	} {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			// A missing recipe is a failure, not a skip: the whole point is
			// that it ships with the binary.
			t.Fatalf("agent recipe %s is missing: %v", rel, err)
		}
		out[rel] = string(b)
	}
	return out
}

// flagPattern matches a long flag as written in prose or a shell block.
var flagPattern = regexp.MustCompile(`--[a-z][a-z0-9-]*`)

// notOurFlags are long flags that legitimately appear in the recipe but belong
// to a DIFFERENT program. Enumerated one by one, with the owner named, so the
// exemption stays auditable — a blanket "ignore flags we don't recognise"
// would make this test pass by definition.
var notOurFlags = map[string]struct{ owner string }{
	"--workspaces": {"npm — quoted in AGENTS.md as the invocation that does NOT work here"},
	"--if-present": {"pnpm — part of `pnpm -r --if-present test`"},
}

func TestDocsOnlyNameFlagsTheCLIHas(t *testing.T) {
	for rel, body := range docFiles(t) {
		seen := map[string]bool{}
		for _, f := range flagPattern.FindAllString(body, -1) {
			if _, foreign := notOurFlags[f]; seen[f] || foreign {
				continue
			}
			seen[f] = true
			// usage is the string `retrace --help` prints, so this asserts
			// against exactly what a reader following the docs would see.
			if !strings.Contains(usage, f) {
				t.Errorf("%s documents %s, which does not appear in `retrace --help` — "+
					"an agent following this recipe would run a flag that does not exist", rel, f)
			}
		}
		if len(seen) == 0 && strings.Contains(body, "retrace ") {
			t.Errorf("%s mentions retrace commands but named no flags — the extractor is probably broken, "+
				"which would make this test vacuously green", rel)
		}
	}
}

// fieldsBlock is the explicitly delimited list of --json fields the recipe
// tells an agent to read. Delimited rather than scraped from the whole
// document because the doc also mentions field VALUES ("pass", "changed") and
// config keys, and a scraper that could not tell them apart would either miss
// real drift or cry wolf constantly.
var fieldsBlock = regexp.MustCompile(`(?s)<!-- retrace:fields -->(.*?)<!-- /retrace:fields -->`)

// backticked pulls `identifier` spans out of a string.
var backticked = regexp.MustCompile("`([A-Za-z][A-Za-z0-9_]*)`")

// documentedFields reads the field NAMES out of the delimited block. Each
// bullet is `- ` + one or more backticked field names + an em dash + prose.
// Only the part LEFT of the em dash is field names; the prose to its right
// legitimately quotes field VALUES ("pass", "changed") and nested struct
// members ("plane", "threshold"), none of which are keys on Summary. An
// earlier cut scanned the whole block and reported all six of those as drift.
func documentedFields(block string) []string {
	var out []string
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue // a wrapped continuation line, not a new field
		}
		names := line
		if i := strings.Index(line, "—"); i >= 0 {
			names = line[:i]
		}
		for _, m := range backticked.FindAllStringSubmatch(names, -1) {
			out = append(out, m[1])
		}
	}
	return out
}

func TestDocsOnlyNameSummaryFieldsThatExist(t *testing.T) {
	docs := docFiles(t)
	skill := docs[filepath.Join(".claude", "skills", "retrace-iterate", "SKILL.md")]

	m := fieldsBlock.FindStringSubmatch(skill)
	if m == nil {
		t.Fatal("the skill has no <!-- retrace:fields --> block — without it nothing pins the documented " +
			"--json fields to the Summary struct, and this test is decoration")
	}

	// The real field set, by reflection over the type `diff --json` marshals.
	// Derived, never a second hand-maintained list, or the list itself becomes
	// the thing that drifts.
	real := map[string]bool{}
	st := reflect.TypeOf(diff.Summary{})
	for i := 0; i < st.NumField(); i++ {
		tag := st.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		real[strings.Split(tag, ",")[0]] = true
	}
	if len(real) == 0 {
		t.Fatal("reflected zero json fields off diff.Summary — the extractor is broken")
	}

	documented := map[string]bool{}
	for _, f := range documentedFields(m[1]) {
		documented[f] = true
	}
	// A floor, not just non-empty: the block lists well over five fields, so
	// an extractor that silently degraded to one or two would still be
	// "non-empty" while checking almost nothing.
	if len(documented) < 5 {
		t.Fatalf("the retrace:fields block yielded only %d field names (%v) — the extractor is broken, "+
			"and a broken extractor here is a test that passes by checking nothing", len(documented), documented)
	}

	var missing []string
	for f := range documented {
		if !real[f] {
			missing = append(missing, f)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the skill tells agents to read %v off `retrace diff --json`, but diff.Summary has no such "+
			"field — this is how a recipe comes to describe an unshipped proposal", missing)
	}
}

// TestRecipeNamesEveryTriageLabel guards the SECOND table an agent branches
// on. `triage.label` is the field that tells an agent which repository to go
// edit, so a label the CLI can emit and the recipe does not explain is a
// label an agent meets in production and guesses at — and guessing wrong here
// costs an hour spent "fixing" a client that never changed.
//
// diff.TriageLabels() is derived from the built-in table itself, so this
// checks the docs against the code and not against a second hand-kept list.
// Project labels from `triage:` in retrace.yaml are deliberately NOT covered:
// they are configuration, and no recipe can document a stranger's config.
func TestRecipeNamesEveryTriageLabel(t *testing.T) {
	docs := docFiles(t)
	skill := docs[filepath.Join(".claude", "skills", "retrace-iterate", "SKILL.md")]
	labels := diff.TriageLabels()
	if len(labels) < 5 {
		t.Fatalf("diff.TriageLabels() returned %d labels (%v) — the built-in table is smaller than the brief's five defaults, so this test is checking almost nothing", len(labels), labels)
	}
	for _, l := range labels {
		if !strings.Contains(skill, "`"+l+"`") {
			t.Errorf("the CLI can classify a run as %q and the skill never explains that label", l)
		}
	}
}

// TestRecipeNamesEveryVerdict guards the one table an agent branches on. A
// verdict the CLI can emit and the recipe does not explain is a verdict an
// agent will meet for the first time in production and guess at.
func TestRecipeNamesEveryVerdict(t *testing.T) {
	docs := docFiles(t)
	skill := docs[filepath.Join(".claude", "skills", "retrace-iterate", "SKILL.md")]
	for _, v := range []string{"pass", "changed", "failed", "quarantined"} {
		if !strings.Contains(skill, "`"+v+"`") {
			t.Errorf("the skill never explains the %q verdict", v)
		}
	}
}
