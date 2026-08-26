package orchestrator

import (
	"encoding/json"
	"fmt"

	"github.com/itchyny/gojq"
)

// evaluateBodyJQ parses body as JSON and evaluates expr (a jq-style query)
// against it, returning the first result and whether it's truthy. Ensemble
// embeds a pure-Go jq-compatible evaluator (gojq) rather than shelling out
// to a `jq` binary, so a readiness check works on any host without a new
// PATH dependency — see design.md's "body_jq uses an embedded... evaluator"
// decision.
//
// Truthiness follows the intuitive "did this assertion hold" reading, not
// jq's own convention (where only false/null are falsy): false, nil, an
// empty string, a numeric zero, and an empty array/object are all falsy —
// so `.data | length` and `.data | length > 0` both behave as an author
// would expect from an assertion.
func evaluateBodyJQ(body []byte, expr string) (truthy bool, result any, err error) {
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return false, nil, fmt.Errorf("parse response body as JSON: %w", err)
	}

	query, err := gojq.Parse(expr)
	if err != nil {
		return false, nil, fmt.Errorf("parse body_jq expression %q: %w", expr, err)
	}

	iter := query.Run(doc)
	v, ok := iter.Next()
	if !ok {
		return false, nil, nil
	}
	if e, ok := v.(error); ok {
		return false, nil, fmt.Errorf("evaluate body_jq expression %q: %w", expr, e)
	}
	return isTruthy(v), v, nil
}

// isTruthy reports whether v should be considered a passing assertion
// result — see evaluateBodyJQ's doc comment for why this diverges from
// jq's own true/false/null-only truthiness.
func isTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case int:
		return t != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	default:
		return true
	}
}
