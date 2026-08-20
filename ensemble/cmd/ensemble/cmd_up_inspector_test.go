package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/caribou-crew/ensemble/ensemble/config"
	"github.com/caribou-crew/ensemble/ensemble/server"
)

// TestBuildInspectorRegistersKnownTypesOnly guards buildInspector's
// contract: postgres/mysql/dynamodb databases get a registered Driver;
// types the inspector package has no Driver for (redis, localstack) are
// provisioned by the orchestrator but left unregistered, matching GET
// /api/databases' "cfg.Databases ∩ registered drivers" behavior.
//
// This deliberately does NOT drive a full runUp: cfg.Databases entries are
// always started as real docker containers by orchestrator.Up (see
// startDatabase/dockerRunDatabase — there's no config knob to skip that),
// so a full end-to-end runUp test with a database entry would need a live
// docker daemon and network access to pull images, which conflicts with
// "DB integration tests must remain skippable without a live database".
// buildInspector's own drivers connect lazily (see its doc comment), so
// it's fully unit-testable on its own without either docker or a live DB —
// this test constructs it directly and, for the dynamodb case, proves the
// registered driver actually round-trips against a fake HTTP endpoint.
func TestBuildInspectorRegistersKnownTypesOnly(t *testing.T) {
	dynamoFake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		_ = json.NewEncoder(w).Encode(map[string]any{"TableNames": []string{}})
	}))
	t.Cleanup(dynamoFake.Close)
	dynamoURL, err := url.Parse(dynamoFake.URL)
	if err != nil {
		t.Fatalf("parse dynamo fake url: %v", err)
	}
	dynamoPort, err := strconv.Atoi(dynamoURL.Port())
	if err != nil {
		t.Fatalf("dynamo fake port: %v", err)
	}

	cfg := &config.Config{
		Databases: map[string]config.Database{
			"pg":      {Type: "postgres", Port: 15432},
			"mysqldb": {Type: "mysql", Port: 13306},
			"ddb":     {Type: "dynamodb", Port: dynamoPort},
			"cache":   {Type: "redis", Port: 16379},
		},
	}

	var logs []string
	logf := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }

	insp := buildInspector(cfg, logf)

	for _, name := range []string{"pg", "mysqldb", "ddb"} {
		if !insp.Has(name) {
			t.Errorf("buildInspector: %q not registered, want registered (logs: %v)", name, logs)
		}
	}
	if insp.Has("cache") {
		t.Error(`buildInspector: "cache" (redis) registered, want unregistered — no inspector.Driver for redis`)
	}

	// Wire it into a real server.New handler and hit GET /api/databases,
	// the same way cmd_up.go's runUp does — proving the dynamodb driver
	// this produced actually round-trips against a live endpoint, and that
	// only the three inspectable databases are listed.
	handler := server.New(server.Deps{Cfg: cfg, Version: "test", Insp: insp})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/databases")
	if err != nil {
		t.Fatalf("GET /api/databases: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got struct {
		Databases []struct{ Name, Type string } `json:"databases"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Databases) != 3 {
		t.Fatalf("databases = %+v, want 3 (pg, mysqldb, ddb)", got.Databases)
	}

	// And the dynamodb driver specifically works end-to-end against the
	// fake: GET .../ddb/schema should reach the fake's ListTables and come
	// back with zero tables (not an error).
	resp2, err := http.Get(ts.URL + "/api/databases/ddb/schema")
	if err != nil {
		t.Fatalf("GET /api/databases/ddb/schema: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("schema status = %d", resp2.StatusCode)
	}
	var schema struct {
		Tables []map[string]any `json:"tables"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	if len(schema.Tables) != 0 {
		t.Fatalf("tables = %+v, want empty (fake ListTables returns none)", schema.Tables)
	}
}
