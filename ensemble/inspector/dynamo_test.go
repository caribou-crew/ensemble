package inspector

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

// Test: unwrapAttributeValue converts every DynamoDB scalar/collection
// AttributeValue shape into a plain Go value — unit-testable with no
// network access at all.
func TestUnwrapAttributeValue(t *testing.T) {
	cases := []struct {
		name string
		av   map[string]any
		want any
	}{
		{"string", map[string]any{"S": "hello"}, "hello"},
		{"number", map[string]any{"N": "42.5"}, 42.5},
		{"number unparseable falls back to string", map[string]any{"N": "not-a-number"}, "not-a-number"},
		{"bool", map[string]any{"BOOL": true}, true},
		{"null", map[string]any{"NULL": true}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := unwrapAttributeValue(tc.av)
			if got != tc.want {
				t.Fatalf("unwrapAttributeValue(%v) = %v (%T), want %v (%T)", tc.av, got, got, tc.want, tc.want)
			}
		})
	}

	t.Run("nested map", func(t *testing.T) {
		av := map[string]any{"M": map[string]any{
			"inner": map[string]any{"S": "v"},
		}}
		got := unwrapAttributeValue(av)
		m, ok := got.(map[string]any)
		if !ok || m["inner"] != "v" {
			t.Fatalf("unwrapAttributeValue(M) = %#v, want map[inner:v]", got)
		}
	})

	t.Run("list", func(t *testing.T) {
		av := map[string]any{"L": []any{
			map[string]any{"S": "a"},
			map[string]any{"N": "1"},
		}}
		got := unwrapAttributeValue(av)
		l, ok := got.([]any)
		if !ok || len(l) != 2 || l[0] != "a" || l[1] != float64(1) {
			t.Fatalf("unwrapAttributeValue(L) = %#v, want [a 1]", got)
		}
	})
}

// Test: unwrapItem applies unwrapAttributeValue to every attribute of an
// Item.
func TestUnwrapItem(t *testing.T) {
	item := map[string]any{
		"id":   map[string]any{"S": "abc"},
		"qty":  map[string]any{"N": "3"},
		"note": map[string]any{"NULL": true},
	}
	got := unwrapItem(item)
	want := map[string]any{"id": "abc", "qty": float64(3), "note": nil}
	if len(got) != len(want) {
		t.Fatalf("unwrapItem = %#v, want %#v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("unwrapItem[%q] = %#v, want %#v", k, got[k], v)
		}
	}
}

// Test: signSigV4 produces a well-formed Authorization header (unit-level
// sanity — full correctness is exercised by the live integration test
// actually succeeding against DynamoDB Local, which validates request
// shape/headers even though it ignores the signature's cryptographic
// correctness).
func TestSignSigV4SetsHeaders(t *testing.T) {
	body := []byte(`{}`)
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:8000/", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "DynamoDB_20120810.ListTables")

	fixedTime := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := signSigV4(req, body, "AKIDEXAMPLE", "secret", "us-east-1", "dynamodb", fixedTime); err != nil {
		t.Fatalf("signSigV4: %v", err)
	}
	if req.Header.Get("X-Amz-Date") == "" {
		t.Fatal("X-Amz-Date not set")
	}
	auth := req.Header.Get("Authorization")
	if auth == "" {
		t.Fatal("Authorization not set")
	}
	wantPrefix := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/"
	if len(auth) < len(wantPrefix) || auth[:len(wantPrefix)] != wantPrefix {
		t.Fatalf("Authorization = %q, want prefix %q", auth, wantPrefix)
	}
}

// Test: against a live DynamoDB Local, ListTables/DescribeTable/Scan work
// through the raw HTTP+SigV4 client: Tables sees a created table's key
// schema, Rows sees inserted items, and Fingerprint changes on insert.
// Skipped unless ENSEMBLE_TEST_DYNAMO_ENDPOINT is set (e.g.
// "http://127.0.0.1:18000" pointed at a throwaway
// `amazon/dynamodb-local` container).
func TestDynamoDriverIntegration(t *testing.T) {
	endpoint := os.Getenv("ENSEMBLE_TEST_DYNAMO_ENDPOINT")
	if endpoint == "" {
		t.Skip("ENSEMBLE_TEST_DYNAMO_ENDPOINT not set; skipping live DynamoDB Local integration test")
	}

	drv := NewDynamoDriver(endpoint)
	ctx := context.Background()
	const table = "inspector_test_widgets"

	// Best-effort cleanup from a previous failed run.
	drv.request(ctx, "DeleteTable", map[string]any{"TableName": table})

	_, err := drv.request(ctx, "CreateTable", map[string]any{
		"TableName": table,
		"AttributeDefinitions": []map[string]any{
			{"AttributeName": "id", "AttributeType": "S"},
		},
		"KeySchema": []map[string]any{
			{"AttributeName": "id", "KeyType": "HASH"},
		},
		"BillingMode": "PAY_PER_REQUEST",
	})
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	t.Cleanup(func() {
		drv.request(context.Background(), "DeleteTable", map[string]any{"TableName": table})
	})

	for _, id := range []string{"a", "b"} {
		_, err := drv.request(ctx, "PutItem", map[string]any{
			"TableName": table,
			"Item": map[string]any{
				"id":   map[string]any{"S": id},
				"name": map[string]any{"S": fmt.Sprintf("widget-%s", id)},
			},
		})
		if err != nil {
			t.Fatalf("PutItem %s: %v", id, err)
		}
	}

	tables, err := drv.Tables(ctx)
	if err != nil {
		t.Fatalf("Tables: %v", err)
	}
	var widgets *Table
	for i := range tables {
		if tables[i].Name == table {
			widgets = &tables[i]
		}
	}
	if widgets == nil {
		t.Fatalf("Tables did not include %s: %+v", table, tables)
	}
	if len(widgets.Columns) != 1 || widgets.Columns[0].Name != "id" || widgets.Columns[0].Nullable {
		t.Fatalf("columns = %+v, want one non-nullable key column %q", widgets.Columns, "id")
	}

	rows, err := drv.Rows(ctx, table, 10, 0)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("Rows = %d, want 2: %+v", len(rows), rows)
	}

	fp1, err := drv.Fingerprint(ctx, table)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}

	if _, err := drv.request(ctx, "PutItem", map[string]any{
		"TableName": table,
		"Item": map[string]any{
			"id":   map[string]any{"S": "c"},
			"name": map[string]any{"S": "widget-c"},
		},
	}); err != nil {
		t.Fatalf("PutItem c: %v", err)
	}
	fp2, err := drv.Fingerprint(ctx, table)
	if err != nil {
		t.Fatalf("Fingerprint after insert: %v", err)
	}
	if fp1 == fp2 {
		t.Fatalf("Fingerprint did not change after insert: both %q", fp1)
	}
}
