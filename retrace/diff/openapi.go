// openapi.go checks recorded calls against an OpenAPI spec.
//
// Scope ruling: this is conformance checking, not validation. No
// JSON-Schema dependency — CheckOpenAPI parses the spec with
// encoding/json into generic maps and answers four narrow questions per
// call (unknown path, unknown method, undocumented status, a missing
// top-level required field on a JSON response). It does not validate
// types, formats, nested shapes, or anything a real JSON-Schema validator
// would. $refs are followed one level into #/components/schemas/...;
// anything deeper is reported as checked-what-we-could — the top-level
// required list at the one level we resolved — never silently treated as
// a full pass.
package diff

import (
	"encoding/json"
	"fmt"
	"os"
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
	// "missing-required-field".
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
// (exact-string) match wins first, then a templated match where each
// "{...}" pattern segment matches any one path segment.
func matchOpenAPIPath(paths map[string]any, urlPath string) (pattern string, item map[string]any, ok bool) {
	bare := stripQueryAndFragment(urlPath)

	if raw, exists := paths[bare]; exists {
		if m, isMap := raw.(map[string]any); isMap {
			return bare, m, true
		}
	}

	segs := splitURLPath(bare)
	for p, raw := range paths {
		m, isMap := raw.(map[string]any)
		if !isMap {
			continue
		}
		if templatePathMatch(splitURLPath(p), segs) {
			return p, m, true
		}
	}
	return "", nil, false
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

// resolveSchema follows schema["$ref"] one level into
// #/components/schemas/<name> when present; otherwise returns schema
// unchanged. A $ref this doesn't recognize (not a components/schemas
// reference) resolves to nil — checked-what-we-could, never a pass.
func resolveSchema(spec *openAPISpec, schema map[string]any) map[string]any {
	ref, isRef := schema["$ref"].(string)
	if !isRef {
		return schema
	}
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

// requiredFieldFindings checks a hop's JSON response body against the
// documented response's inline content["application/json"].schema.required
// list (following one $ref level). A response with no such schema, or a
// body that doesn't parse as a JSON object, is checked-what-we-could: no
// finding either way, since there's nothing to check against.
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
	schema := resolveSchema(spec, schemaRaw)
	if schema == nil {
		return nil
	}
	reqList, _ := schema["required"].([]any)
	if len(reqList) == 0 {
		return nil
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(h.Resp.Body), &body); err != nil {
		return nil
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
