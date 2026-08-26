package orchestrator

import "testing"

func TestEvaluateBodyJQTruthy(t *testing.T) {
	cases := []struct {
		name string
		body string
		expr string
	}{
		{"length greater than zero", `{"data": [1,2,3]}`, `.data | length > 0`},
		{"field equals", `{"status": "UP"}`, `.status == "UP"`},
		{"nonzero length as bare value", `{"data": [1]}`, `.data | length`},
		{"nonempty string", `{"name": "x"}`, `.name`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			truthy, _, err := evaluateBodyJQ([]byte(tc.body), tc.expr)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !truthy {
				t.Errorf("expected truthy result for %q against %s", tc.expr, tc.body)
			}
		})
	}
}

func TestEvaluateBodyJQFalsy(t *testing.T) {
	cases := []struct {
		name string
		body string
		expr string
	}{
		{"empty array length", `{"data": []}`, `.data | length > 0`},
		{"field mismatch", `{"status": "DOWN"}`, `.status == "UP"`},
		{"zero length as bare value", `{"data": []}`, `.data | length`},
		{"explicit false", `{"ok": false}`, `.ok`},
		{"null field", `{"ok": null}`, `.ok`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			truthy, _, err := evaluateBodyJQ([]byte(tc.body), tc.expr)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if truthy {
				t.Errorf("expected falsy result for %q against %s", tc.expr, tc.body)
			}
		})
	}
}

func TestEvaluateBodyJQInvalidJSON(t *testing.T) {
	_, _, err := evaluateBodyJQ([]byte("not json"), ".status")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestEvaluateBodyJQInvalidExpression(t *testing.T) {
	_, _, err := evaluateBodyJQ([]byte(`{"a":1}`), "not a valid jq expr !!!")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
