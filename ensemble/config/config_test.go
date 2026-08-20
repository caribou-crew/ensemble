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

// TestValidateDuplicateServicePort guards final-review finding I11:
// validation only checked intercept-port (Service.Proxy/Stub.Port)
// collisions, so two services declaring the same real Service.Port
// validated clean — then the health gate (which polls the real port) would
// see the FIRST service's listener and report the second healthy too,
// while wireProxy silently misroutes its intercept port. Two services with
// the same real port must fail validation, naming both.
func TestValidateDuplicateServicePort(t *testing.T) {
	c := &Config{
		Services: map[string]Service{
			"svc-a": {Run: "node a.js", Port: 8080, Proxy: 7001},
			"svc-b": {Run: "node b.js", Port: 8080, Proxy: 7002},
		},
	}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "svc-a") || !strings.Contains(err.Error(), "svc-b") {
		t.Errorf("error does not list both names: %v", err)
	}
}

// TestValidateServicePortCollidesWithDatabasePort pins the other half of
// I11: Service.Port and Database.Port share one real address space
// (127.0.0.1) and must be checked against each other, not just within
// their own kind.
func TestValidateServicePortCollidesWithDatabasePort(t *testing.T) {
	c := &Config{
		Services: map[string]Service{
			"svc-a": {Run: "node a.js", Port: 5432, Proxy: 7001},
		},
		Databases: map[string]Database{
			"pg": {Image: "postgres:16", Port: 5432},
		},
	}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "svc-a") || !strings.Contains(err.Error(), "pg") {
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

// --- ServicesForProfiles: reconciling Service.Profile with top-level
// Profiles group membership (carried over from the task 2.1 review). ---

func TestServicesForProfilesNoMembershipAlwaysIncluded(t *testing.T) {
	c := &Config{Services: map[string]Service{
		"bff": {Run: "x", Port: 1},
	}}
	out := c.ServicesForProfiles(nil)
	if _, ok := out["bff"]; !ok {
		t.Error("service with no profile membership at all must always be included")
	}
}

func TestServicesForProfilesOwnProfileInactiveExcludes(t *testing.T) {
	c := &Config{Services: map[string]Service{
		"ledger": {Run: "x", Port: 1, Profile: "full"},
	}}
	out := c.ServicesForProfiles(nil)
	if _, ok := out["ledger"]; ok {
		t.Error("service whose only membership is an inactive own-Profile must be excluded")
	}
}

func TestServicesForProfilesGroupOnlyInactiveExcludes(t *testing.T) {
	c := &Config{
		Services: map[string]Service{
			"ledger": {Run: "x", Port: 1}, // no own Profile field
		},
		Profiles: map[string][]string{"full": {"ledger"}},
	}
	out := c.ServicesForProfiles(nil)
	if _, ok := out["ledger"]; ok {
		t.Error("service listed only in an inactive top-level group must be excluded")
	}
}

func TestServicesForProfilesGroupOnlyActiveIncludes(t *testing.T) {
	c := &Config{
		Services: map[string]Service{
			"ledger": {Run: "x", Port: 1}, // no own Profile field
		},
		Profiles: map[string][]string{"full": {"ledger"}},
	}
	out := c.ServicesForProfiles([]string{"full"})
	if _, ok := out["ledger"]; !ok {
		t.Error("service listed in an active top-level group must be included")
	}
}

// Union semantics: either mechanism being active is enough, even when the
// other mechanism names an inactive profile.
func TestServicesForProfilesUnionOverridesInactiveOwnProfile(t *testing.T) {
	c := &Config{
		Services: map[string]Service{
			// own Profile names an inactive profile, but the service is
			// also listed in an active top-level group.
			"ledger": {Run: "x", Port: 1, Profile: "solo-inactive"},
		},
		Profiles: map[string][]string{"full": {"ledger"}},
	}
	out := c.ServicesForProfiles([]string{"full"})
	if _, ok := out["ledger"]; !ok {
		t.Error("membership in an active group must include the service even if its own Profile is inactive")
	}
}

func TestServicesForProfilesUnionOverridesInactiveGroup(t *testing.T) {
	c := &Config{
		Services: map[string]Service{
			// listed in an inactive group, but its own Profile is active.
			"ledger": {Run: "x", Port: 1, Profile: "full"},
		},
		Profiles: map[string][]string{"other": {"ledger"}},
	}
	out := c.ServicesForProfiles([]string{"full"})
	if _, ok := out["ledger"]; !ok {
		t.Error("an active own-Profile must include the service even if it's also listed in an inactive group")
	}
}
