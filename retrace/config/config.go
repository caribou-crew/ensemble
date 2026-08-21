// Package config parses retrace.yaml — the flows to record, the rules that
// decide what counts as a difference, and the thresholds that gate CI.
//
// Rules come from TWO places on purpose. retrace.yaml is human-owned and
// full of explanatory comments; the review queue's `rule` verb appends
// machine-written rules to .retrace/wire-rules.json instead, because
// re-emitting YAML would silently delete the human's comments. The overlay
// is loaded AFTER the yaml rules, so a hand-written rule can be overridden
// by a later reviewed one but never clobbered on disk.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/caribou-crew/ensemble/retrace/rules"
)

// overlayMu serializes every read-modify-write of the machine-owned overlay
// file across goroutines in this process. AppendWireRule additionally writes
// atomically (temp file + rename), so a concurrent reader never observes a
// partially-written file either.
var overlayMu sync.Mutex

const OverlayPath = ".retrace/wire-rules.json"

// DefaultGate and DefaultFine are the pixel thresholds every verdict is
// measured against. They are named constants because they appear in
// report copy, in `retrace diff --help`, and in the docs.
const (
	DefaultGate = 0.1
	DefaultFine = 0.05
)

// Config is the parsed contents of retrace.yaml plus the machine-owned
// overlay merged in by Discover.
type Config struct {
	App              string            `yaml:"app"`
	Flows            map[string]Flow   `yaml:"flows"`
	Entry            string            `yaml:"entry"`
	Upstream         string            `yaml:"upstream"`
	WireIgnore       []WireIgnoreEntry `yaml:"wire_ignore"`
	WireRules        []rules.Raw       `yaml:"wire_rules"`
	PathNormalize    []Normalize       `yaml:"path_normalize"`
	ExpectedStatuses []StatusRule      `yaml:"expected_statuses"`
	HopRequire       []RequiredRoute   `yaml:"hop_require"`
	Masks            map[string][]Rect `yaml:"masks"`
	Thresholds       Thresholds        `yaml:"thresholds"`
	OpenAPI          string            `yaml:"openapi"`
	Redact           []string          `yaml:"redact"`
	Deviations       string            `yaml:"deviations"`
	// Gates holds the per-plane CI budget (percent of checkpoints/calls
	// allowed to differ before the run fails). Plane keys are "pixel",
	// "wire", "hop", "perf". A plane with no entry (or an entry whose
	// BudgetPct is nil) is NOT gated — except "pixel", which applyDefaults
	// fills from Thresholds.Gate when absent, because pixel is gated today
	// at DefaultGate and must stay gated. This is a SEPARATE number from
	// Thresholds.Gate/Fine, which keep their existing meaning (the
	// tolerated-vs-failing pixel diff size); Gates["pixel"].BudgetPct is the
	// CI budget layered on top, defaulted from it but independently
	// settable.
	Gates map[string]Gate `yaml:"gates"`
	// FailOn lists the plane keys ("pixel", "wire", "hop", "perf") whose
	// gate failures should fail the run. Shape only: which planes actually
	// gate a build is the consuming task's decision, not this package's.
	FailOn []string `yaml:"fail_on"`
	// Preflight commands run once, before any flow. Per-flow Preflight (see
	// Flow.Preflight) then runs before that specific flow. Shape only: not
	// executed by this package.
	Preflight []string `yaml:"preflight"`
	// Dir is set by Load from the file's own location and is NOT a YAML
	// key. It must be tagged `yaml:"-"`, or KnownFields(true) will happily
	// accept a `dir:` key in the file and then Load will overwrite it —
	// a setting that appears to work and silently does nothing.
	Dir string `yaml:"-"`
	// Loaded reports whether a real retrace.yaml was found and read. Its
	// zero value, false, is deliberately the unsafe-to-proceed one: no
	// config means no redaction rules, so a consumer that captures traffic
	// must refuse rather than proceed when Loaded is false — writing
	// unredacted hops to disk is a leak, not a degraded mode. Discover sets
	// this true only when it actually reads a retrace.yaml off disk.
	Loaded bool `yaml:"-"`
}

type Flow struct {
	Command      string            `yaml:"command"`
	PerfBudgetMs float64           `yaml:"perf_budget_ms"`
	Masks        map[string][]Rect `yaml:"masks"`
	// Preflight commands run before THIS flow specifically, after the
	// global Config.Preflight has already run. Shape only: not executed by
	// this package.
	Preflight []string `yaml:"preflight"`
	// Setup commands run before the flow's own Command; Teardown commands
	// run after it, whether or not Command succeeded is the executing
	// task's call to make — this package only carries the shape. Not
	// executed here.
	Setup    []string `yaml:"setup"`
	Teardown []string `yaml:"teardown"`
}

// Gate is one plane's CI budget entry under Config.Gates. BudgetPct is a
// pointer so an explicit `budget_pct: 0` (a real, meaningful setting: "any
// change at all fails") can be distinguished from the key being absent
// entirely (not gated, or — for "pixel" only — defaulted by applyDefaults).
// A bare float64 cannot make that distinction: its zero value and an
// explicit zero are the same bits.
type Gate struct {
	BudgetPct *float64 `yaml:"budget_pct"`
}

type Normalize struct {
	Pattern     string `yaml:"pattern"`
	Replacement string `yaml:"replacement"`
	re          *regexp.Regexp
}

// Apply rewrites path using this normalize rule. If the rule's pattern
// failed to compile (or Normalize was never run through Load, e.g. a
// zero-value or hand-built Normalize), Apply is a no-op — Load is what
// compiles re, and it errors out before this can be called with a broken
// pattern.
func (n Normalize) Apply(path string) string {
	if n.re == nil {
		return path
	}
	return n.re.ReplaceAllString(path, n.Replacement)
}

type StatusRule struct {
	Path   string `yaml:"path"`
	Status int    `yaml:"status"`
}

type RequiredRoute struct {
	Method string `yaml:"method"`
	Path   string `yaml:"path"`
	Status int    `yaml:"status"`
}

// Rect is the config-facing rectangle. It is deliberately NOT pixel.Rect:
// retrace/config must not import retrace/pixel (config is the leaf every
// package reads). Task 10 and Task 11 convert at the call site; the
// conversion is one function, pixel.RectsFrom, and it lives in the pixel
// package for the same reason.
type Rect struct {
	X      int `json:"x" yaml:"x"`
	Y      int `json:"y" yaml:"y"`
	Width  int `json:"width" yaml:"width"`
	Height int `json:"height" yaml:"height"`
	// Why explains what this mask is hiding and why. A mask hides pixels
	// from the diff; an un-explained mask is indistinguishable from one
	// added to silence a real regression, and masks outlive the person who
	// added them. Optional — omitted on the JSON side so existing configs
	// and reports without it keep loading — but every mask added going
	// forward should carry one.
	Why string `json:"why,omitempty" yaml:"why"`
}

// WireIgnoreEntry is one entry of Config.WireIgnore. It accepts two YAML
// shapes:
//
//	wire_ignore:
//	  - "date"                            # bare scalar: Path only, Why empty
//	  - path: "items[*].requestId"
//	    why: "regenerated on every request"
//
// The bare-scalar form must keep working: every existing config and test in
// this repo uses it. UnmarshalYAML is what makes both shapes parse into the
// same Go type instead of requiring a schema-breaking change.
//
// F13: Path is a body field-path glob (retrace/rules.MatchFieldGlob's
// dialect — "**.requestId", "items[*].sku"), NOT a URL path. An earlier
// version of this comment gave "/health" as the example, which is
// unambiguously a URL path; under the settled semantics that entry silently
// matched nothing. Load rejects any entry whose Path begins with "/" so a
// config that repeats that mistake fails loudly instead of shipping a mask
// that does nothing.
type WireIgnoreEntry struct {
	Path string `yaml:"path"`
	Why  string `yaml:"why"`
}

func (w *WireIgnoreEntry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var s string
		if err := node.Decode(&s); err != nil {
			return err
		}
		w.Path = s
		w.Why = ""
		return nil
	}
	// node.Decode(&p) below gets a FRESH yaml decoder with KnownFields off:
	// Load's dec.KnownFields(true) belongs to the outer *yaml.Decoder, and
	// that strictness does not propagate into a custom UnmarshalYAML, which
	// is handed a bare *yaml.Node instead of the decoder that produced it.
	// Left unchecked, a typo'd key here (e.g. "whyy" for "why") would
	// silently decode as if the field weren't there at all — precisely the
	// silent drop this field exists to prevent, since Why is the reason a
	// wire path is ignored. Enforce known fields by hand, matching the
	// house style used for every other unknown-key error in this package.
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			switch node.Content[i].Value {
			case "path", "why":
			default:
				return fmt.Errorf("line %d: field %s not found in type config.WireIgnoreEntry",
					node.Content[i].Line, node.Content[i].Value)
			}
		}
	}
	// Avoid infinite recursion into WireIgnoreEntry.UnmarshalYAML by
	// decoding into a distinct named type with the same fields.
	type plain WireIgnoreEntry
	var p plain
	if err := node.Decode(&p); err != nil {
		return err
	}
	*w = WireIgnoreEntry(p)
	return nil
}

// WireIgnorePaths returns just the paths from Config.WireIgnore, for the
// diff engine, which has no use for Why — the same way pixel.RectsFrom
// converts config.Rect at one seam. An entry that parsed to Path == "" is
// dropped rather than passed down: an empty path reaching the diff engine
// as an ignore rule would match every path, which is the most permissive
// value the type has, so the zero-value clause applies to this method too.
func (c *Config) WireIgnorePaths() []string {
	paths := make([]string, 0, len(c.WireIgnore))
	for _, e := range c.WireIgnore {
		if e.Path == "" {
			continue
		}
		paths = append(paths, e.Path)
	}
	return paths
}

// Thresholds gates how big a pixel diff must be before it's a Fine
// (tolerated) verdict versus a Gate (CI-failing) one. Zero on either field
// means "unset", not "gate/tolerate at literally 0" — applyDefaults is the
// only place that substitutes DefaultGate/DefaultFine, so an omitted
// thresholds: block in retrace.yaml behaves exactly like an explicit
// thresholds: { gate: 0.1, fine: 0.05 }, never like a hair-trigger gate at
// zero.
type Thresholds struct {
	Gate float64 `yaml:"gate"`
	Fine float64 `yaml:"fine"`
}

// validPlanes is the fixed set of gate/fail_on plane names. It is fixed by
// the brief, not open-ended, so validating it at Load is shape work: any
// other name is a typo, and gates/fail_on both escape KnownFields on their
// own — Gates is a map (its keys are values, invisible to KnownFields) and
// FailOn is a []string (there is no field-name concept to check at all).
var validPlanes = map[string]bool{"pixel": true, "wire": true, "hop": true, "perf": true}

// validatePlanes rejects a typo'd plane name in gates or fail_on, naming the
// offender. Left unchecked, `gates: {pixle: ...}` loads clean and silently
// ungates "pixle" while leaving "pixel" at its untouched default — the user
// believes they configured a plane and got nothing, with no error at all.
func validatePlanes(c *Config) error {
	for name := range c.Gates {
		if !validPlanes[name] {
			return fmt.Errorf("gates: unknown plane %q, want one of pixel, wire, hop, perf", name)
		}
	}
	for _, name := range c.FailOn {
		if !validPlanes[name] {
			return fmt.Errorf("fail_on: unknown plane %q, want one of pixel, wire, hop, perf", name)
		}
	}
	return nil
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true) // a typo'd key is an error, not a silently ignored setting
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	// A `---` second document is not merged, appended, or silently kept —
	// yaml.v3's Decoder only ever fills c from the first one, so a second
	// document would otherwise vanish without a word. Decode into a
	// throwaway value just to detect its presence; io.EOF means there is no
	// second document.
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, fmt.Errorf("%s: multiple YAML documents are not supported", path)
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.Dir = filepath.Dir(path)
	c.Loaded = true
	applyDefaults(&c)
	for i := range c.PathNormalize {
		re, err := regexp.Compile(c.PathNormalize[i].Pattern)
		if err != nil {
			return nil, fmt.Errorf("%s: path_normalize[%d]: %w", path, i, err)
		}
		c.PathNormalize[i].re = re
	}
	// Fail at load, not at first diff: an unknown matcher name in config is
	// a typo the user wants to hear about now.
	if _, err := c.Rules(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := validatePlanes(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := validateWireIgnore(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// validateWireIgnore rejects a wire_ignore entry that looks like a URL path
// (leading '/') rather than the body field-path glob the settled semantics
// require (F13). Left unchecked, a user following this file's own former
// doc-comment example ("path: /health") gets a mask that cannot match
// anything and no error — as misleading as an empty mask, per the
// zero-value rule's spirit.
func validateWireIgnore(c *Config) error {
	for i, e := range c.WireIgnore {
		if strings.HasPrefix(e.Path, "/") {
			return fmt.Errorf("wire_ignore[%d]: %q looks like a URL path, not a body field-path glob (e.g. %q) — wire_ignore matches JSON body fields, not request paths",
				i, e.Path, "items[*].requestId")
		}
	}
	return nil
}

// Discover loads <cwd>/retrace.yaml plus the machine-owned overlay. A
// missing retrace.yaml is not an error — an app with no config still records
// and diffs, it just has no rules.
func Discover(cwd string) (*Config, error) {
	// Defaults have ONE source: applyDefaults, which Load also calls. The
	// earlier draft re-hardcoded 0.1/0.05 here, which is two places to
	// change a number that appears in every pixel verdict.
	c := &Config{Dir: cwd}
	applyDefaults(c)
	if _, err := os.Stat(filepath.Join(cwd, "retrace.yaml")); err == nil {
		var err error
		c, err = Load(filepath.Join(cwd, "retrace.yaml"))
		if err != nil {
			return nil, err
		}
	}
	overlay, err := readOverlay(filepath.Join(cwd, OverlayPath))
	if err != nil {
		return nil, err
	}
	c.WireRules = append(c.WireRules, overlay...)
	if _, err := c.Rules(); err != nil {
		return nil, fmt.Errorf("%s: %w", OverlayPath, err)
	}
	return c, nil
}

func applyDefaults(c *Config) {
	if c.Thresholds.Gate == 0 {
		c.Thresholds.Gate = DefaultGate
	}
	if c.Thresholds.Fine == 0 {
		c.Thresholds.Fine = DefaultFine
	}
	// Pixel is the one plane gated by default (at Thresholds.Gate, already
	// defaulted above). Wire, hop and perf have no default today and stay
	// ungated when their gates entry is absent — do not extend this to
	// every plane, or pixel's "gated at 0.1 unless configured otherwise"
	// behavior silently disappears alongside them.
	if c.Gates == nil {
		c.Gates = map[string]Gate{}
	}
	if g, ok := c.Gates["pixel"]; !ok || g.BudgetPct == nil {
		gate := c.Thresholds.Gate
		c.Gates["pixel"] = Gate{BudgetPct: &gate}
	}
}

// Rules normalizes WireRules into evaluable rules.Rule values, surfacing
// any unknown matcher (or other malformed rule) as an error naming the
// offending rule — see rules.Normalize.
func (c *Config) Rules() ([]rules.Rule, error) {
	return rules.Normalize(c.WireRules)
}

// MasksFor resolves the masks for one checkpoint: the flow's own map wins,
// then the top-level map, and within each the named checkpoint wins over
// the "*" wildcard. First non-empty list wins; an explicit empty list at a
// more specific level does NOT mask anything (use it to opt a checkpoint
// out of a wildcard).
func (c *Config) MasksFor(flow, checkpoint string) []Rect {
	for _, m := range []map[string][]Rect{c.Flows[flow].Masks, c.Masks} {
		if m == nil {
			continue
		}
		if r, ok := m[checkpoint]; ok {
			return r
		}
		if r, ok := m["*"]; ok {
			return r
		}
	}
	return nil
}

// NormalizePath applies PathNormalize in order, each rewrite feeding the
// next.
func (c *Config) NormalizePath(path string) string {
	for _, n := range c.PathNormalize {
		path = n.Apply(path)
	}
	return path
}

// AppendWireRule adds one rule to the overlay, idempotently: the review
// queue's `rule` verb can be pressed twice on the same field (or by a human
// and an agent at once) and must not grow the file each time.
//
// The rule is validated with rules.Normalize before anything is written —
// an unknown matcher name must fail the append, not brick every later
// Discover call in this project with no way to repair it through the API.
//
// The read-modify-write is serialized by overlayMu and the write itself is
// atomic (temp file in the same directory, then os.Rename over the target),
// so within a single process concurrent appends never lose a rule, and a
// concurrent reader — even one in another process — never observes a
// partially-written file.
//
// overlayMu is an in-process mutex, though: it does not serialize appends
// made by two separate OS processes, and this function does not implement
// cross-process locking. If the future `retrace ref rule` command and the
// review server both end up calling AppendWireRule concurrently as separate
// processes, one of their appends can still be silently lost — that case is
// out of scope here and belongs to whichever task first makes the review
// server a second live writer.
//
// dir is a working-directory ROOT, exactly like runs.PathsFor's root — it
// is intentionally not validated as a path component; only a
// caller-supplied NAME joined into a path gets that treatment, and there
// is none here.
func AppendWireRule(dir string, r rules.Raw) error {
	if _, err := rules.Normalize([]rules.Raw{r}); err != nil {
		return err
	}
	path := filepath.Join(dir, OverlayPath)

	overlayMu.Lock()
	defer overlayMu.Unlock()

	existing, err := readOverlay(path)
	if err != nil {
		return err
	}
	want, _ := json.Marshal(r)
	for _, e := range existing {
		if got, _ := json.Marshal(e); string(got) == string(want) {
			return nil
		}
	}
	existing = append(existing, r)
	b, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return err
	}
	dir2 := filepath.Dir(path)
	if err := os.MkdirAll(dir2, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir2, ".wire-rules-*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return err
	}
	// Same-directory matters: os.Rename is only atomic within a filesystem,
	// so a reader either sees the old file or the new one, never a tear.
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// readOverlay reads the machine-owned wire-rule overlay. A missing overlay
// is not an error — no rule has been reviewed yet. DisallowUnknownFields
// matches the strictness of the YAML side's KnownFields(true): a mis-shaped
// rule (a typo'd key from a serialization bug) must fail loudly rather than
// decode as an empty, match-everything rules.Raw.
func readOverlay(path string) ([]rules.Raw, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []rules.Raw
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}
