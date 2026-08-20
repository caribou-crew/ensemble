package config

import (
	"strings"
	"testing"
)

// --- Validate() table tests: each case is one rule from the task brief. ---

func TestValidateServiceMissingRunAndDocker(t *testing.T) {
	c := &Config{Services: map[string]Service{
		"bff": {},
	}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "bff") {
		t.Errorf("error does not name the service: %v", err)
	}
}

func TestValidateServiceRunWithoutPort(t *testing.T) {
	c := &Config{Services: map[string]Service{
		"bff": {Run: "node dist/main.js", Port: 0},
	}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "bff") {
		t.Errorf("error does not name the service: %v", err)
	}
}

func TestValidateDuplicateProxyPort(t *testing.T) {
	c := &Config{
		Services: map[string]Service{
			"bff":   {Run: "node dist/main.js", Port: 8003, Proxy: 7003},
			"svc-a": {Run: "node dist/main.js", Port: 8004, Proxy: 7003},
		},
		Stubs: map[string]Stub{
			"aws-kms": {Port: 7020, Routes: []StubRoute{{Match: StubMatch{Method: "GET", Path: "/x"}}}},
		},
	}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "bff") || !strings.Contains(err.Error(), "svc-a") {
		t.Errorf("error does not list both names: %v", err)
	}
}

func TestValidateDependsOnUnknownReference(t *testing.T) {
	c := &Config{Services: map[string]Service{
		"bff": {Run: "node dist/main.js", Port: 8003, DependsOn: []string{"ghost"}},
	}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error does not name the unknown dependency: %v", err)
	}
}

func TestValidateDatabaseInvalidType(t *testing.T) {
	c := &Config{Databases: map[string]Database{
		"cache": {Image: "memcached:1", Port: 11211},
	}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "cache") {
		t.Errorf("error does not name the database: %v", err)
	}
}

func TestValidateDatabaseTypeDefaultedFromImage(t *testing.T) {
	c := &Config{Databases: map[string]Database{
		"pg": {Image: "postgres:16", Port: 5432},
	}}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Databases["pg"].Type != "postgres" {
		t.Errorf("type not defaulted from image: got %q", c.Databases["pg"].Type)
	}
}

func TestValidateStubRouteEmptyPath(t *testing.T) {
	c := &Config{Stubs: map[string]Stub{
		"aws-kms": {Port: 7020, Routes: []StubRoute{{Match: StubMatch{Method: "POST", Path: ""}}}},
	}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "aws-kms") {
		t.Errorf("error does not name the stub: %v", err)
	}
}

func TestValidateSeedUnknownDatabase(t *testing.T) {
	c := &Config{Seeds: map[string]Seed{
		"baseline": {SQL: []SeedSQL{{Database: "ghost", File: "./seeds/init.sql"}}},
	}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error does not name the unknown database: %v", err)
	}
}

func TestValidateEntityEmptyBase(t *testing.T) {
	c := &Config{Entities: map[string]Entity{
		"users": {Base: "", ID: "token"},
	}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "users") {
		t.Errorf("error does not name the entity: %v", err)
	}
}

// --- Load() + ServicesForProfiles(): rule 9, the valid full config. ---

func TestLoadValidFullConfig(t *testing.T) {
	c, err := Load("testdata/valid-full.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(c.Services) != 4 {
		t.Errorf("expected 4 services, got %d", len(c.Services))
	}
	if c.Dir != "testdata" {
		t.Errorf("Dir not set from Load: got %q", c.Dir)
	}

	// Defaults applied: postgres has no explicit type and must be inferred
	// from its image; dynamo and aws set type explicitly.
	if got := c.Databases["postgres"].Type; got != "postgres" {
		t.Errorf("postgres type not defaulted: got %q", got)
	}
	if got := c.Databases["dynamo"].Type; got != "dynamodb" {
		t.Errorf("dynamo type: got %q", got)
	}
	if got := c.Databases["aws"].Type; got != "localstack" {
		t.Errorf("aws type: got %q", got)
	}

	without := c.ServicesForProfiles(nil)
	if _, ok := without["ledger"]; ok {
		t.Error("ServicesForProfiles(nil) should exclude profiled service ledger")
	}
	if _, ok := without["bff"]; !ok {
		t.Error("ServicesForProfiles(nil) should include unprofiled service bff")
	}

	with := c.ServicesForProfiles([]string{"full"})
	if _, ok := with["ledger"]; !ok {
		t.Error(`ServicesForProfiles(["full"]) should include ledger`)
	}
}
