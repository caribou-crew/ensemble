// openapi.go checks recorded calls against an OpenAPI spec.
//
// Scope ruling: this is conformance checking, not validation. No
// JSON-Schema dependency — CheckOpenAPI parses the spec with
// encoding/json into generic maps and answers five narrow questions per
// call (unknown path, unknown method, undocumented status, a missing
// top-level required field on a JSON response, or the check being unable
// to run at all). It does not validate types, formats, nested shapes, or
// anything a real JSON-Schema validator would. $refs are followed one
// level into #/components/schemas/...; anything deeper is reported as
// checked-what-we-could — the top-level required list at the one level
// we resolved — never silently treated as a full pass. "Never silently
// treated as a full pass" is enforced by a fifth ConformanceFinding.Kind,
// "unchecked": an unresolvable $ref, an unparseable body, and a
// redaction-truncated body (trace.Redactor caps bodies and sets
// Payload.Truncated) all report it explicitly rather than returning zero
// findings, which would read identically to "verified clean".
package diff

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/caribou-crew/ensemble/core/trace"
)

// ConformanceFinding is one call CheckOpenAPI could not reconcile with
// the spec.
type ConformanceFinding struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Status int    `json:"status"`
	// Kind is "unknown-path" | "unknown-method" | "undocumented-status" |
	// "missing-required-field" | "unchecked".
	//
	// "unchecked" is distinct from a pass: it means the required-field
	// check genuinely could not run (an unresolvable $ref beyond the one
	// level this file follows, a response body that fails
	// json.Unmarshal, or a body trace.Redactor truncated at capture) —
	// never that it ran and found nothing wrong. An empty finding list
	// and a list containing only "unchecked" entries both mean "nothing
	// was reported", but only the first means "everything was verified";
	// a caller that treats "unchecked" as a pass reintroduces exactly the
	// silent-success failure this Kind exists to rule out.
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

type openAPISpec struct {
	paths      map[string]any
	components map[string]any
}

func loadOpenAPISpec(specPath string) (*openAPISpec, error) {
	data, err := os.ReadFile(specPath)
	if err != nil {
		return nil, fmt.Errorf("diff: CheckOpenAPI: reading spec %s: %w", specPath, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("diff: CheckOpenAPI: parsing spec %s: %w", specPath, err)
	}
	paths, _ := raw["paths"].(map[string]any)
	components, _ := raw["components"].(map[string]any)
	return &openAPISpec{paths: paths, components: components}, nil
}

// CheckOpenAPI answers, for every hop with a status (a transport error
// carries no status to check), whether it conforms to the spec at
// specPath. A missing or unparseable spec is a returned error, never
// silent success — an implementer who typos the path should not get a
// clean plane for it.
func CheckOpenAPI(hops []trace.Hop, specPath string) ([]ConformanceFinding, error) {
	spec, err := loadOpenAPISpec(specPath)
	if err != nil {
		return nil, err
	}
	var out []ConformanceFinding
	for _, h := range hops {
		if h.Status == 0 {
			continue
		}
		out = append(out, checkHopConformance(spec, h)...)
	}
	return out, nil
}

// matchOpenAPIPath finds the paths entry matching urlPath: a literal
// (exact-string) match wins first, then the most specific templated match
// — fewest "{...}" segments, i.e. the most literal segments pinned — where
// each "{...}" pattern segment matches any one path segment.
//
// paths is a Go map, so iterating it directly is a genuine bug, not a
// style nit: whenever two templated patterns of the same segment count
// both match (e.g. "/users/{id}" and "/{entity}/{id}" both match
// "/users/42"), Go's randomized map-iteration order would return a
// different winner — and with it a different operation, responses map,
// and Detail string — on every run of the same binary against the same
// input. A CI gate that flips at random on unchanged input is worse than
// one that is simply wrong: the first flake teaches the team to ignore
// the plane. Sorting candidates and picking by specificity (ties broken
// by the sorted key order, itself deterministic) makes the result a pure
// function of (spec, hop) again, and happens to match OpenAPI's own
// concrete-before-templated resolution convention.
func matchOpenAPIPath(paths map[string]any, urlPath string) (pattern string, item map[string]any, ok bool) {
	bare := stripQueryAndFragment(urlPath)

	if raw, exists := paths[bare]; exists {
		if m, isMap := raw.(map[string]any); isMap {
			return bare, m, true
		}
	}

	segs := splitURLPath(bare)
	keys := make([]string, 0, len(paths))
	for p := range paths {
		keys = append(keys, p)
	}
	sort.Strings(keys)

	bestSpecificity := -1
	for _, p := range keys {
		m, isMap := paths[p].(map[string]any)
		if !isMap {
			continue
		}
		pSegs := splitURLPath(p)
		if !templatePathMatch(pSegs, segs) {
			continue
		}
		spec := countTemplateSegments(pSegs)
		if bestSpecificity == -1 || spec < bestSpecificity {
			pattern, item, ok = p, m, true
			bestSpecificity = spec
		}
	}
	return pattern, item, ok
}

func templatePathMatch(pattern, path []string) bool {
	if len(pattern) != len(path) {
		return false
	}
	for i, seg := range pattern {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			continue
		}
		if seg != path[i] {
			return false
		}
	}
	return true
}

func countTemplateSegments(segs []string) int {
	n := 0
	for _, s := range segs {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			n++
		}
	}
	return n
}

// lookupResponse finds the responses entry for status: exact status code
// string first, then its "NXX" range form, then "default".
func lookupResponse(responses map[string]any, status int) (map[string]any, bool) {
	if responses == nil {
		return nil, false
	}
	if raw, ok := responses[strconv.Itoa(status)]; ok {
		m, _ := raw.(map[string]any)
		return m, true
	}
	if raw, ok := responses[fmt.Sprintf("%dXX", status/100)]; ok {
		m, _ := raw.(map[string]any)
		return m, true
	}
	if raw, ok := responses["default"]; ok {
		m, _ := raw.(map[string]any)
		return m, true
	}
	return nil, false
}

// resolveSchemaRef follows a "$ref" value one level into
// #/components/schemas/<name>. Returns nil if it can't be followed — a
// $ref outside that prefix, or a name not present in components.schemas.
func resolveSchemaRef(spec *openAPISpec, ref string) map[string]any {
	const prefix = "#/components/schemas/"
	if !strings.HasPrefix(ref, prefix) {
		return nil
	}
	name := strings.TrimPrefix(ref, prefix)
	schemas, _ := spec.components["schemas"].(map[string]any)
	if schemas == nil {
		return nil
	}
	resolved, _ := schemas[name].(map[string]any)
	return resolved
}

// unchecked builds a single "unchecked" finding: the required-field check
// could not run, which must never be indistinguishable from it running
// and finding nothing wrong (see ConformanceFinding.Kind's doc comment).
func unchecked(h trace.Hop, detail string) []ConformanceFinding {
	return []ConformanceFinding{{
		Method: h.Method, Path: h.Path, Status: h.Status,
		Kind: "unchecked", Detail: detail,
	}}
}

// requiredFieldFindings checks a hop's JSON response body against the
// documented response's inline content["application/json"].schema.required
// list (following one $ref level). A response with no such schema
// documented at all is checked-what-we-could with nothing to check
// against: no finding either way. But once a schema IS documented, every
// reason the check can't actually run — an unresolvable $ref, a
// truncated body, a body that fails json.Unmarshal — reports "unchecked"
// rather than silently returning zero findings, which would be
// indistinguishable from "checked and clean".
func requiredFieldFindings(spec *openAPISpec, respItem map[string]any, h trace.Hop) []ConformanceFinding {
	content, _ := respItem["content"].(map[string]any)
	if content == nil {
		return nil
	}
	appJSON, _ := content["application/json"].(map[string]any)
	if appJSON == nil {
		return nil
	}
	schemaRaw, _ := appJSON["schema"].(map[string]any)
	if schemaRaw == nil {
		return nil
	}

	schema := schemaRaw
	if refRaw, isRef := schemaRaw["$ref"]; isRef {
		ref, isString := refRaw.(string)
		var resolved map[string]any
		if isString {
			resolved = resolveSchemaRef(spec, ref)
		}
		// Checked structurally, not just "resolveSchemaRef returned nil":
		// a resolved schema that STILL carries an unresolved "$ref" (e.g.
		// a $ref this function fell back to returning unchanged) is just
		// as unchecked as a nil one — the required list a caller would
		// read off it doesn't exist either way.
		if _, stillRef := resolved["$ref"]; resolved == nil || stillRef {
			return unchecked(h, fmt.Sprintf("$ref %v could not be resolved beyond one level", refRaw))
		}
		schema = resolved
	}

	reqList, _ := schema["required"].([]any)
	if len(reqList) == 0 {
		return nil
	}

	if h.Resp.Truncated {
		return unchecked(h, "response body was truncated at capture; required-field check skipped")
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(h.Resp.Body), &body); err != nil {
		return unchecked(h, fmt.Sprintf("response body is not valid JSON: %v", err))
	}

	var out []ConformanceFinding
	for _, r := range reqList {
		name, _ := r.(string)
		if name == "" {
			continue
		}
		if _, present := body[name]; present {
			continue
		}
		out = append(out, ConformanceFinding{
			Method: h.Method, Path: h.Path, Status: h.Status,
			Kind:   "missing-required-field",
			Detail: fmt.Sprintf("required field %q missing from response", name),
		})
	}
	return out
}

func checkHopConformance(spec *openAPISpec, h trace.Hop) []ConformanceFinding {
	pattern, pathItem, ok := matchOpenAPIPath(spec.paths, h.Path)
	if !ok {
		return []ConformanceFinding{{
			Method: h.Method, Path: h.Path, Status: h.Status,
			Kind: "unknown-path", Detail: fmt.Sprintf("no paths entry matches %s", h.Path),
		}}
	}

	opRaw, ok := pathItem[strings.ToLower(h.Method)]
	if !ok {
		return []ConformanceFinding{{
			Method: h.Method, Path: h.Path, Status: h.Status,
			Kind: "unknown-method", Detail: fmt.Sprintf("%s not documented for %s", h.Method, pattern),
		}}
	}
	op, _ := opRaw.(map[string]any)

	responses, _ := op["responses"].(map[string]any)
	respItem, found := lookupResponse(responses, h.Status)
	if !found {
		return []ConformanceFinding{{
			Method: h.Method, Path: h.Path, Status: h.Status,
			Kind: "undocumented-status", Detail: fmt.Sprintf("status %d not documented for %s %s", h.Status, h.Method, pattern),
		}}
	}

	return requiredFieldFindings(spec, respItem, h)
}
