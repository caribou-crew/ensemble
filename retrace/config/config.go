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
	"sort"
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

// lockOverlayFn is the cross-process lock AppendWireRule takes, behind one
// level of indirection so a test can run the function in the configuration
// the NON-UNIX build actually ships: lockOverlay is a no-op there (see
// overlaylock_other.go), which makes overlayMu the entire protection on
// windows. Without this seam the unix flock masks the mutex in every test,
// removing overlayMu turns nothing red, and the next reader deletes it as
// "redundant, the flock covers this" — green everywhere, and windows loses
// its only serialization. See TestConcurrentGoroutinesWithNoFileLockLandEveryRule.
var lockOverlayFn = lockOverlay

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
	App      string          `yaml:"app"`
	Flows    map[string]Flow `yaml:"flows"`
	Entry    string          `yaml:"entry"`
	Upstream string          `yaml:"upstream"`
	// ProxyHost is the hostname the client-edge listener binds on AND is
	// advertised as (RETRACE_PROXY_URL), in both standalone and
	// ensemble-attached capture. Empty means the built-in default,
	// "127.0.0.1" — unchanged behavior. Exists for a URL-bound auth
	// validator that compares hostnames (design.md §6.1.2): "localhost" and
	// "127.0.0.1" are different strings for the same address, so a
	// validator expecting "localhost" 401s against the default. A hostname
	// here MUST resolve loopback-only; core/proxy.ServeStoppable refuses
	// otherwise.
	ProxyHost string `yaml:"proxy_host"`
	// ProxyPort is the fixed TCP port the client-edge listener binds on
	// AND is advertised as, in both standalone and ensemble-attached
	// capture. Zero means the built-in default: an ephemeral port chosen
	// by the OS — unchanged behavior. Exists for a URL-bound auth
	// validator that does strict origin+path matching against a fixed
	// allowlist of origins (design.md §6.1.2's proxy.port addendum):
	// proxy_host alone only fixes the hostname half of that match, and
	// retrace's proxy otherwise always binds an ephemeral port. A
	// configured port already held by another process fails the run
	// immediately, naming the port — retrace never silently falls back
	// to a different one.
	ProxyPort  int               `yaml:"proxy_port"`
	WireIgnore []WireIgnoreEntry `yaml:"wire_ignore"`
	WireRules  []rules.Raw       `yaml:"wire_rules"`
	// DefaultWireRules switches the built-in header tolerances (see
	// builtins.go) off wholesale. Absent — the overwhelmingly common case —
	// means ON; see useBuiltinWireRules for why absent is the permissive
	// reading here and why this cannot be a plain bool. Individual headers
	// do not need this: a user rule for the same header simply wins, and
	// `date: exact` restores strict comparison of that one header.
	DefaultWireRules *bool `yaml:"default_wire_rules"`
	// QueryIgnore names query parameters that do not identify a call — a
	// cache-buster, a client-side timestamp — so strict replay matches on
	// what is left of the query. It is PROJECT-WIDE and top-level, a
	// sibling of wire_ignore, because the two are the same kind of thing
	// and a second scoping rule for the neighbouring key is how a config
	// grows two mental models (R-J). A per-flow need, if one appears, gets
	// a precedence rule stated in one place at that time.
	QueryIgnore      []string          `yaml:"query_ignore"`
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
	// Triage holds project-specific rows for the triage table, consulted
	// before the built-in defaults (see TriageRule). Absent for almost every
	// project: the built-in table is a total function over the five signals,
	// so a config needs an entry here only to disagree with it.
	Triage []TriageRule `yaml:"triage"`
	// FailOn lists the plane keys ("pixel", "wire", "hop", "perf") whose
	// gate failures should fail the run. Shape only: which planes actually
	// gate a build is the consuming task's decision, not this package's.
	FailOn []string `yaml:"fail_on"`
	// RequireWhy turns a tolerance with no `why:` into a config error (see
	// ValidateWhy). Off by default, and deliberately a plain bool rather
	// than a *bool: absent means "not enforced", which is what every
	// existing config means today, and there is no third state to
	// distinguish. `retrace diff --require-why` and `retrace run
	// --require-why` set it for one invocation without editing the file,
	// which is how a project tries the ratchet on before committing to it.
	RequireWhy bool `yaml:"require_why"`
	// Preflight commands run once, before any flow. Per-flow Preflight (see
	// Flow.Preflight) then runs before that specific flow. Executed by
	// `retrace run` (cmd/retrace/hooks.go), never by this package: a
	// non-zero exit refuses the capture rather than recording against a
	// stack that failed its own preconditions.
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
	// global Config.Preflight has already run.
	Preflight []string `yaml:"preflight"`
	// Setup runs before the flow's command and Teardown after it — both
	// OUTSIDE the recording window, so a seed step's own traffic is never
	// captured and diffed as though the app had made those calls. Teardown
	// runs on every exit path, including a failed flow: that is when
	// leftover state matters most, because the next run inherits it.
	//
	// Executed by `retrace run` (cmd/retrace/hooks.go), never by this
	// package.
	Setup    []string `yaml:"setup"`
	Teardown []string `yaml:"teardown"`
	// Canonical is the screen geometry this flow's shots are expected to be
	// captured on. nil — the overwhelming majority — means the flow accepts
	// whatever the adapter reports, and comparisons still refuse a pair of
	// runs whose geometries disagree with EACH OTHER (see runs.SameScreen).
	// Canonical adds the stronger claim: this flow's shots are only
	// meaningful at this one size, so a run at any other size is refused
	// against the config rather than only against its eventual partner.
	Canonical *Canonical `yaml:"canonical"`
	// Gates overrides the top-level Config.Gates for THIS flow. Resolved by
	// Config.ResolveGates, never read directly: a consumer that read this map
	// on its own would see a flow's overrides without the global budgets
	// underneath them, and report a plane as ungated because this flow did not
	// happen to mention it.
	Gates map[string]Gate `yaml:"gates"`
}

// Canonical is a flow's expected screen geometry, under
// `flows.<name>.canonical`.
//
// Strict is what turns the expectation into a refusal, and it is a plain
// bool whose zero value — false — is the permissive one. That is the
// opposite of this project's usual rule, and deliberately so: the field it
// guards does not exist unless someone wrote it. A `canonical:` block with
// no `strict: true` is a project declaring the size it means to capture at
// and asking to be TOLD when a run drifts; adding `strict: true` is asking
// to be stopped. Making strictness the default would mean the act of writing
// down an expectation silently starts failing builds, which is how a useful
// annotation becomes one nobody dares add.
//
// Both readings are enforced. Non-strict warns on stderr at run time and
// carries the note into the capture-trust record, so the drift is on the
// run's own paperwork rather than only in a scrollback nobody kept.
type Canonical struct {
	Width  int  `yaml:"width"`
	Height int  `yaml:"height"`
	Strict bool `yaml:"strict"`
}

// Matches reports whether a run's device geometry is the one this flow
// expects.
//
// A nil device does NOT match: "no geometry recorded" is not evidence of the
// right geometry, and a flow that went to the trouble of declaring one is
// exactly the flow that should hear about a run which reported nothing. A
// nil Canonical matches everything — there is no expectation to violate.
func (c *Canonical) Matches(width, height int) bool {
	if c == nil {
		return true
	}
	return c.Width == width && c.Height == height
}

// Gate is one plane's CI budget entry under Config.Gates. BudgetPct is a
// pointer so an explicit `budget_pct: 0` (a real, meaningful setting: "any
// change at all fails") can be distinguished from the key being absent
// entirely (not gated, or — for "pixel" only — defaulted by applyDefaults).
// A bare float64 cannot make that distinction: its zero value and an
// explicit zero are the same bits.
type Gate struct {
	BudgetPct *float64 `yaml:"budget_pct"`
	// Checkpoints overrides BudgetPct for individually named checkpoints —
	// `gates: {pixel: {budget_pct: 1.5, checkpoints: {cart: 8}}}` means "1.5%
	// everywhere, except the cart screen, which is allowed 8%". Only the
	// pixel plane has checkpoints; the other three planes have no per-item
	// unit for this to key on, and ValidateGateCheckpoints rejects the key
	// there rather than letting it load clean and do nothing.
	//
	// A plain float64, not a *float64, unlike BudgetPct: a map key's PRESENCE
	// already draws the absent-vs-explicit-zero distinction that BudgetPct
	// needs a pointer for, and `checkpoints: {cart: 0}` is a real setting
	// ("this screen must not move at all") that a present key expresses
	// perfectly well.
	Checkpoints map[string]float64 `yaml:"checkpoints"`
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
	// Why explains why this path is allowed to answer with this 4xx/5xx.
	// An expected status is a tolerance like any other: it stops a real
	// error status from being reported, so an un-explained one is
	// indistinguishable from an entry added to quiet a genuine break.
	// Optional by default; `require_why: true` makes it mandatory.
	Why string `yaml:"why"`
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

// QueryIgnoreKeys returns the query parameters to drop before matching,
// with blank entries removed. An empty key reaching the matcher would be
// `url.Values.Del("")` — harmless today, and exactly the kind of
// "permissive value derived from an unset one" the zero-value constraint
// is about — so it is dropped at this seam, the same way WireIgnorePaths
// drops an empty path.
func (c *Config) QueryIgnoreKeys() []string {
	out := make([]string, 0, len(c.QueryIgnore))
	for _, k := range c.QueryIgnore {
		if strings.TrimSpace(k) == "" {
			continue
		}
		out = append(out, k)
	}
	return out
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

// validateCanonical refuses a `canonical:` block that does not describe a
// real screen.
//
// The trap this closes is `canonical: {strict: true}` with no dimensions —
// which reads, to anyone skimming the file, as the strongest possible
// assertion, and means 0×0. Every run would fail it, forever, for a reason
// the message would never explain. Half a block (a width and no height) is
// the same mistake with one line of evidence instead of none.
//
// Flows are walked in name order so a config with two bad blocks reports the
// same one on every run; a user fixing the error they were shown must not
// see it move.
func validateCanonical(c *Config) error {
	for _, name := range sortedFlowNames(c.Flows) {
		cn := c.Flows[name].Canonical
		if cn == nil {
			continue
		}
		if cn.Width <= 0 || cn.Height <= 0 {
			return fmt.Errorf("flows.%s.canonical: %dx%d is not a screen — `canonical:` needs a positive width and height (a block with `strict: true` and no dimensions fails every run at 0x0)", name, cn.Width, cn.Height)
		}
	}
	return nil
}

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
	if err := validateGateCheckpoints("gates", c.Gates); err != nil {
		return err
	}
	// Per-flow gates get the SAME two checks, or a typo is caught at the top
	// level and waved through one level down — where it is harder to spot,
	// because the plane name it shadows is spelled correctly right above it.
	for _, flow := range sortedFlowNames(c.Flows) {
		for _, plane := range sortedPlanes(c.Flows[flow].Gates) {
			if !validPlanes[plane] {
				return fmt.Errorf("flows.%s.gates: unknown plane %q, want one of pixel, wire, hop, perf", flow, plane)
			}
		}
		if err := validateGateCheckpoints("flows."+flow+".gates", c.Flows[flow].Gates); err != nil {
			return err
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
	if err := validateThresholds(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
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
	if err := validateCanonical(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	// validateTriage also FILLS each unnamed rule's Name from its index, so it
	// must run on the value that is returned, not on a copy.
	if err := validateTriage(&c); err != nil {
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

// validateThresholds rejects thresholds.gate or thresholds.fine outside the
// open interval (0, 1) (F6). thresholds.gate is overloaded across two unit
// systems: retrace/diff/pixel.Match uses it as a per-pixel YIQ
// colour-distance threshold (maxDelta = maxYIQDelta * gate * gate), while
// retrace/diff/summary.go compares the same configured value against DiffPct
// as a percent of pixels. The two coincide near the 0.1 default and diverge
// completely at gate >= 1: at any threshold >= 1, maxDelta >= maxYIQDelta, so
// |delta| > maxDelta is unsatisfiable and every checkpoint reports 0.00%
// forever — the pixel plane is permanently green. A user writing `gate: 5`
// meaning "5% of pixels may differ" gets exactly that, silently, with no
// error and a clean-looking report.
//
// This runs AFTER applyDefaults, so the 0 it might see has already been
// substituted with DefaultGate/DefaultFine — 0 continues to mean "unset,
// use the default" and is never rejected here, only the effective value is
// checked. Splitting the overloaded key into two distinct settings is the
// real fix and belongs to Phase 4b; this guard only stops the silent pass
// shipping in the meantime.
func validateThresholds(c *Config) error {
	check := func(key string, v float64) error {
		if v > 0 && v < 1 {
			return nil
		}
		return fmt.Errorf("thresholds.%s: %v is outside the valid range (0, 1) — thresholds.%s doubles as a per-pixel colour-distance threshold (pixel.Match) and as the fraction of pixels allowed to differ (summary), so %v cannot mean %q; use a fraction like 0.1, not a percentage",
			key, v, key, v, fmt.Sprintf("%v%%", v))
	}
	if err := check("gate", c.Thresholds.Gate); err != nil {
		return err
	}
	return check("fine", c.Thresholds.Fine)
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
	// AFTER the overlay merge, never inside Load: a machine-written rule is
	// a tolerance like any other, and a ratchet that exempted the writer
	// nobody reviews would be aimed at the wrong half of the list. This is
	// also why `retrace ref rule` and POST /api/rule both take a why.
	if c.RequireWhy {
		if err := c.ValidateWhy(); err != nil {
			return nil, err
		}
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
//
// The built-in header rules are PREPENDED, never stored: rules.Resolve
// collapses the list with last-write-wins per key, so anything the user
// wrote for the same header beats the built-in, and so does a reviewed
// overlay rule (Discover appends those to WireRules, after the yaml ones).
// Prepending here rather than mutating WireRules also keeps this function
// idempotent — Load calls it to validate, Discover calls it again after
// merging the overlay, and a version that appended to the field would grow
// three built-ins per call.
// The two lists are normalized SEPARATELY rather than concatenated first,
// because rules.Normalize labels its errors by index into the slice it was
// handed. Prepending three built-ins to the user's list renumbered every
// one of their rules, so a bad matcher in the user's first rule reported as
// `wireRules[3]` — an index pointing at a rule they never wrote. The
// user's own list is normalized first for the same reason: their error is
// the one worth reaching, and it should not queue behind ours.
func (c *Config) Rules() ([]rules.Rule, error) {
	user, err := rules.Normalize(c.WireRules)
	if err != nil {
		return nil, err
	}
	if !c.useBuiltinWireRules() {
		return user, nil
	}
	builtin, err := rules.Normalize(BuiltinWireRules())
	if err != nil {
		// Only reachable if builtinHeaderMatchers itself names a matcher
		// that does not exist — a programming error, not a config error, so
		// say so rather than letting it read as the user's fault.
		return nil, fmt.Errorf("built-in wire rules are malformed (this is a bug in retrace, not in your config): %w", err)
	}
	// builtin is a fresh slice from a fresh BuiltinWireRules, so appending
	// onto it cannot write into WireRules' backing array.
	return append(builtin, user...), nil
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

// FlowMaskEntryCheckpoints names every checkpoint a mask entry is written
// for in THIS FLOW's own map (`flows.<flow>.masks`), sorted, with "*"
// excluded. A flow-scoped entry can only ever apply to this flow, so one
// that matches no checkpoint in the run being promoted protects nothing,
// anywhere, ever — `refs.Accept` refuses it.
//
// This is the enumeration MasksFor cannot give: a lookup keyed on a
// checkpoint name returns nil for a name it does not hold, so a misspelt
// entry is indistinguishable from a screen that needs no mask, and
// `retrace ref accept` would promote the pixels the entry was written to
// hide.
//
// The "*" wildcard is excluded here and below: it names no checkpoint, so
// it can never be a typo.
func (c *Config) FlowMaskEntryCheckpoints(flow string) []string {
	return maskEntryNames(c.Flows[flow].Masks)
}

// ProjectMaskEntryCheckpoints names every checkpoint the PROJECT-WIDE
// top-level `masks:` map declares an entry for, sorted, without "*".
//
// It is deliberately separate from FlowMaskEntryCheckpoints and carries a
// weaker verdict, because the two have different meanings. Top-level means
// "every flow", so an entry matching nothing in the checkout run has an
// obvious innocent reading: it is doing its job in the login flow.
// Refusing it would reject a correct configuration.
//
// The principled check — evaluate a top-level entry against EVERY flow's
// checkpoints, since one matching nothing project-wide really is a typo —
// is not computable at accept time: checkpoints are discovered from run
// manifests, not declared in config, so a flow that has never been run has
// no known checkpoints and the verdict would depend on what happens to sit
// in the gitignored .retrace/runs/. So Accept REPORTS these instead of
// refusing them.
//
// That is not the "a warning is not a gate" rule being broken. That rule
// bites when the condition is unambiguously a defect (`gate: 5`, a fatal
// capture verdict, a flag the dialect cannot honour) — one reading each, so
// warning is a machine declining to act on what it already knows. When the
// condition is genuinely AMBIGUOUS, a warning is the correct instrument and
// refusing is the error: the alternative is not a safer failure, it is a
// louder one landing on people whose config is fine.
func (c *Config) ProjectMaskEntryCheckpoints() []string {
	return maskEntryNames(c.Masks)
}

func maskEntryNames(m map[string][]Rect) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		if name == "*" {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
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
// The read-modify-write is serialized by overlayMu WITHIN this process and
// by an exclusive flock(2) on a sidecar file ACROSS processes (see
// lockOverlay in overlaylock_unix.go), and the write itself is atomic (temp
// file in the same directory, then os.Rename over the target). So on unix
// concurrent appends never lose a rule — whether they come from two
// goroutines or two separate `retrace` processes — and a concurrent reader,
// in any process, never observes a partially-written file.
//
// The cross-process half was measured before it existed and after. Task 3
// measured 3 processes x 12 appends landing 12, 12 and 14 of 36, every lost
// call returning a nil error; Task 11's own regression test
// (TestNSeparateProcessesAppendingConcurrentlyLandNRules, 4 processes x 25
// appends) landed 34 of 100 against the unlocked function and 100 of 100
// with the lock. Silent loss behind a nil error is the same failure shape
// the atomicity fix was written to eliminate; it simply moved up a level
// once a second writer process appeared. Deleting the lock re-opens it, and
// that test is what says so.
//
// The wait for the lock is BOUNDED (overlayLockWait): a command a developer
// typed reports which file it is waiting on rather than hanging forever.
//
// NON-UNIX platforms have no lock — the Go standard library exposes no
// portable file lock, and the portable alternative was ruled out for
// wedging on a crash. There, this function keeps exactly its pre-Task-11
// guarantee: safe within one process, and two writer processes can still
// lose an append. See overlaylock_other.go. There overlayMu is the ONLY
// serialization left, so it has a test of its own —
// TestConcurrentGoroutinesWithNoFileLockLandEveryRule runs this function
// with lockOverlayFn stubbed to a no-op, which is that build exactly. On
// unix the flock alone is enough, which is why deleting overlayMu leaves
// the multi-process test green: an intended survivor, and the control
// proving that test measures the flock rather than the mutex. The two
// tests prove different things and must not be merged into one.
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

	// overlayMu first, then the file lock: goroutines in this process queue
	// on the cheap mutex instead of each opening a descriptor and contending
	// on flock. The file lock is what the OTHER processes queue on, and it
	// is held across the read, the merge and the rename — a lock released
	// before the rename would leave exactly the read-modify-write window it
	// exists to close.
	overlayMu.Lock()
	defer overlayMu.Unlock()

	// The overlay's directory must exist before the sidecar lock can be
	// created in it; this used to happen just before the write.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	unlock, err := lockOverlayFn(path)
	if err != nil {
		return err
	}
	defer unlock()

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
