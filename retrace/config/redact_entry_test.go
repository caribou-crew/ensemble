package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
)

func loadRedact(t *testing.T, yaml string) []RedactEntry {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "retrace.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg.Redact
}

func TestRedactBareScalarListParsesAsDestroyMode(t *testing.T) {
	entries := loadRedact(t, "app: web\nredact: [password, card_number]\n")
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	for i, want := range []string{"password", "card_number"} {
		if entries[i].Field != want {
			t.Errorf("entry %d field = %q, want %q", i, entries[i].Field, want)
		}
		if entries[i].Mode != "destroy" {
			t.Errorf("entry %d mode = %q, want destroy", i, entries[i].Mode)
		}
	}
}

func TestRedactMappingFormWithEncryptAndDisplayModes(t *testing.T) {
	entries := loadRedact(t, `
app: web
redact:
  - field: account_number
    mode: encrypt
    why: "checkout total needs the real account id to assert against"
  - field: display_name
    mode: display
`)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Field != "account_number" || entries[0].Mode != "encrypt" || entries[0].Why == "" {
		t.Errorf("entry 0 = %+v", entries[0])
	}
	if entries[1].Field != "display_name" || entries[1].Mode != "display" {
		t.Errorf("entry 1 = %+v", entries[1])
	}
}

func TestRedactMixedBareAndMappingFormsInOneList(t *testing.T) {
	entries := loadRedact(t, `
app: web
redact:
  - password
  - field: account_number
    mode: encrypt
`)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Field != "password" || entries[0].Mode != "destroy" {
		t.Errorf("entry 0 = %+v", entries[0])
	}
	if entries[1].Field != "account_number" || entries[1].Mode != "encrypt" {
		t.Errorf("entry 1 = %+v", entries[1])
	}
}

func TestRedactUnknownModeIsALoadError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retrace.yaml")
	yaml := "app: web\nredact:\n  - field: x\n    mode: encypt\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("a typo'd mode value should fail to load, not silently become destroy")
	}
}

func TestRedactKeyRulesConvertsEntriesToTraceKeyRules(t *testing.T) {
	entries := []RedactEntry{
		{Field: "password", Mode: "destroy"},
		{Field: "account_number", Mode: "encrypt"},
		{Field: "", Mode: "destroy"}, // dropped, matching WireIgnorePaths' zero-value rule
	}
	rules := RedactKeyRules(entries)
	if len(rules) != 2 {
		t.Fatalf("got %d rules, want 2 (empty field dropped)", len(rules))
	}
	if rules[0].Key != "password" || rules[0].Mode != trace.ModeDestroy {
		t.Errorf("rule 0 = %+v", rules[0])
	}
	if rules[1].Key != "account_number" || rules[1].Mode != trace.ModeEncrypt {
		t.Errorf("rule 1 = %+v", rules[1])
	}
}
