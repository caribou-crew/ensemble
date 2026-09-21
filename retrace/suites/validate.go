package suites

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

var shaPattern = regexp.MustCompile(`^(?:[a-fA-F0-9]{40}|[a-fA-F0-9]{64})$`)
var platforms = []string{"web", "ios", "android"}
var planeNames = []string{"functional", "wire", "visual"}
var states = []string{"pass", "failed", "incomplete", "not-run", "not-applicable"}

func nonempty(v string) bool { return strings.TrimSpace(v) != "" }
func uniqueAllowed(values, allowed []string, label string) error {
	if len(values) == 0 {
		return fmt.Errorf("suites: %s must not be empty", label)
	}
	seen := map[string]bool{}
	for _, v := range values {
		if !slices.Contains(allowed, v) || seen[v] {
			return fmt.Errorf("suites: invalid or duplicate %s %q", label, v)
		}
		seen[v] = true
	}
	return nil
}
func ValidateInventory(inv Inventory) error {
	if inv.Schema != InventorySchema {
		return fmt.Errorf("suites: unsupported inventory schema %q", inv.Schema)
	}
	if inv.Suites == nil {
		return fmt.Errorf("suites: suites array is required (use [] for an empty inventory)")
	}
	ids := map[string]bool{}
	for _, s := range inv.Suites {
		if err := runs.ValidateComponents(s.ID); err != nil {
			return fmt.Errorf("suites: suite id: %w", err)
		}
		if ids[s.ID] {
			return fmt.Errorf("suites: duplicate suite id %q", s.ID)
		}
		ids[s.ID] = true
		if !nonempty(s.Title) || !nonempty(s.Version) {
			return fmt.Errorf("suites: suite %q requires title and version", s.ID)
		}
		if err := uniqueAllowed(s.Platforms, platforms, "suite platform"); err != nil {
			return err
		}
		if len(s.Features) == 0 {
			return fmt.Errorf("suites: suite %q requires features", s.ID)
		}
		features, flows := map[string]bool{}, map[string]bool{}
		for _, f := range s.Features {
			if err := runs.ValidateComponents(f.ID); err != nil {
				return fmt.Errorf("suites: feature id: %w", err)
			}
			if features[f.ID] {
				return fmt.Errorf("suites: duplicate feature id %q", f.ID)
			}
			features[f.ID] = true
			if !nonempty(f.Title) || len(f.Flows) == 0 {
				return fmt.Errorf("suites: feature %q requires title and flows", f.ID)
			}
			for _, flow := range f.Flows {
				if err := runs.ValidateComponents(flow.ID); err != nil {
					return fmt.Errorf("suites: flow id: %w", err)
				}
				if flows[flow.ID] {
					return fmt.Errorf("suites: duplicate flow id %q", flow.ID)
				}
				flows[flow.ID] = true
				if !nonempty(flow.Title) {
					return fmt.Errorf("suites: flow %q requires title", flow.ID)
				}
				if flow.Platforms != nil {
					if err := uniqueAllowed(flow.Platforms, s.Platforms, "flow platform"); err != nil {
						return err
					}
				}
				if err := uniqueAllowed(flow.RequiredPlanes, planeNames, "required plane"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func flowPlatforms(s Suite, f Flow) []string {
	if f.Platforms != nil {
		return f.Platforms
	}
	return s.Platforms
}

// ValidateAttempt checks assertions against exactly the configured inventory
// revision. An old revision is an actionable error, never a silently lost run.
func ValidateAttempt(inv Inventory, a Attempt) error {
	if err := ValidateInventory(inv); err != nil {
		return err
	}
	return validateAttempt(inv, a)
}
func validateAttempt(inv Inventory, a Attempt) error {
	if a.Schema != AttemptSchema {
		return fmt.Errorf("suites: unsupported attempt schema %q", a.Schema)
	}
	if err := runs.ValidateComponents(a.SuiteID, a.AttemptID); err != nil {
		return fmt.Errorf("suites: attempt identity: %w", err)
	}
	var suite *Suite
	for i := range inv.Suites {
		if inv.Suites[i].ID == a.SuiteID {
			suite = &inv.Suites[i]
			break
		}
	}
	if suite == nil {
		return fmt.Errorf("suites: attempt %q names unconfigured suite %q", a.AttemptID, a.SuiteID)
	}
	if a.SuiteVersion != suite.Version {
		return fmt.Errorf("suites: attempt %q inventory version %q does not match configured %q; use the matching inventory revision in a separate evidence root", a.AttemptID, a.SuiteVersion, suite.Version)
	}
	if !slices.Contains(suite.Platforms, a.Platform) {
		return fmt.Errorf("suites: attempt %q names unconfigured platform %q", a.AttemptID, a.Platform)
	}
	if !shaPattern.MatchString(a.Git.SHA) {
		return fmt.Errorf("suites: attempt %q requires a full 40 or 64 hex git sha", a.AttemptID)
	}
	if a.Git.Dirty && !nonempty(a.WorkspaceID) {
		return fmt.Errorf("suites: dirty attempt %q requires workspaceId", a.AttemptID)
	}
	if !nonempty(a.BaselineID) || !nonempty(a.PolicyID) {
		return fmt.Errorf("suites: attempt %q requires baselineId and policyId", a.AttemptID)
	}
	start, err := time.Parse(time.RFC3339, a.StartedAt)
	if err != nil {
		return fmt.Errorf("suites: invalid startedAt: %w", err)
	}
	finish, err := time.Parse(time.RFC3339, a.FinishedAt)
	if err != nil {
		return fmt.Errorf("suites: invalid finishedAt: %w", err)
	}
	if finish.Before(start) {
		return fmt.Errorf("suites: finishedAt precedes startedAt")
	}
	if a.Results == nil {
		return fmt.Errorf("suites: results array is required")
	}
	flows := map[string]Flow{}
	for _, f := range suite.Features {
		for _, flow := range f.Flows {
			flows[flow.ID] = flow
		}
	}
	seen := map[string]bool{}
	for _, r := range a.Results {
		flow, ok := flows[r.FlowID]
		if !ok || !slices.Contains(flowPlatforms(*suite, flow), a.Platform) {
			return fmt.Errorf("suites: result names unconfigured flow/platform %q/%q", r.FlowID, a.Platform)
		}
		if seen[r.FlowID] {
			return fmt.Errorf("suites: duplicate result flow %q", r.FlowID)
		}
		seen[r.FlowID] = true
		for _, p := range planeNames {
			state := r.Planes.State(p)
			if !slices.Contains(states, state) {
				return fmt.Errorf("suites: result %q has missing or invalid %s state %q", r.FlowID, p, state)
			}
			if state == "not-applicable" && slices.Contains(flow.RequiredPlanes, p) {
				return fmt.Errorf("suites: result %q required plane %s cannot be not-applicable", r.FlowID, p)
			}
		}
		if err := validateScreens(r.FlowID, r.Screens); err != nil {
			return err
		}
		if n := strings.TrimSpace(r.WireNote); r.WireNote != "" && (n != r.WireNote || len(n) > maxWireNote) {
			return fmt.Errorf("suites: result %q wireNote must be 1-%d characters without surrounding space", r.FlowID, maxWireNote)
		}
		if r.Evidence != nil {
			e := r.Evidence
			if e.RunID == "latest" || e.RunID == "reference" {
				return fmt.Errorf("suites: evidence requires a concrete run ID, not %q", e.RunID)
			}
			if err := runs.ValidateComponents(e.App, e.Flow, e.RunID); err != nil {
				return fmt.Errorf("suites: evidence: %w", err)
			}
			if e.PairID != "" {
				if err := runs.ValidateComponents(e.PairID); err != nil {
					return fmt.Errorf("suites: evidence pair: %w", err)
				}
			}
		}
	}
	return nil
}

// strictJSON rejects duplicate keys, nulls, unknown fields and trailing JSON.
// encoding/json otherwise silently accepts duplicate keys and null scalar values,
// either of which can turn incomplete runner metadata into a plausible report.
func strictJSON(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	if err := checkValue(d); err != nil {
		return fmt.Errorf("suites: invalid JSON: %w", err)
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("suites: trailing JSON data")
	}
	if err := checkFieldNames(data, reflect.TypeOf(target).Elem()); err != nil {
		return fmt.Errorf("suites: invalid JSON: %w", err)
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return fmt.Errorf("suites: invalid JSON: %w", err)
	}
	return nil
}

// encoding/json matches struct names case-insensitively, which would allow
// "dirty" and "Dirty" to assign the same field despite duplicate-key checks.
// Restrict object keys to their exact schema spelling before typed decoding.
func checkFieldNames(data json.RawMessage, typ reflect.Type) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return err
		}
		schema := map[string]reflect.Type{}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				schema[name] = field.Type
			}
		}
		for name, value := range fields {
			fieldType, ok := schema[name]
			if !ok {
				return fmt.Errorf("unknown field %q", name)
			}
			if err := checkFieldNames(value, fieldType); err != nil {
				return err
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		for _, value := range values {
			if err := checkFieldNames(value, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
func checkValue(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	if t == nil {
		return fmt.Errorf("null is not an asserted value")
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return fmt.Errorf("invalid object key")
			}
			if seen[name] {
				return fmt.Errorf("duplicate key %q", name)
			}
			seen[name] = true
			if err := checkValue(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := checkValue(d); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected delimiter %q", delim)
	}
	_, err = d.Token()
	return err
}
func DecodeInventory(data []byte) (Inventory, error) {
	var inv Inventory
	if err := strictJSON(data, &inv); err != nil {
		return inv, err
	}
	return inv, ValidateInventory(inv)
}
func DecodeAttempt(data []byte, inv Inventory) (Attempt, error) {
	var a Attempt
	if err := strictJSON(data, &a); err != nil {
		return a, err
	}
	// A missing dirty boolean must not be interpreted as a clean checkout.
	var fields struct {
		Git map[string]json.RawMessage `json:"git"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return a, err
	}
	if _, ok := fields.Git["dirty"]; !ok {
		return a, fmt.Errorf("suites: git.dirty must be explicitly reported")
	}
	if _, ok := fields.Git["branch"]; !ok {
		return a, fmt.Errorf("suites: git.branch must be explicitly reported (empty for detached HEAD)")
	}
	return a, ValidateAttempt(inv, a)
}
