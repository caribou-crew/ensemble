package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func lookupFrom(vars map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := vars[key]
		return v, ok
	}
}

func TestExpandEnvVarsUsesRealValueWhenSet(t *testing.T) {
	got, err := expandEnvVars([]byte("port: ${PORT}"), lookupFrom(map[string]string{"PORT": "8080"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "port: 8080" {
		t.Fatalf("got %q", got)
	}
}

func TestExpandEnvVarsUsesDefaultWhenUnset(t *testing.T) {
	got, err := expandEnvVars([]byte("port: ${PORT:-9000}"), lookupFrom(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "port: 9000" {
		t.Fatalf("got %q", got)
	}
}

// Bash's ":-" default operator applies when the variable is unset OR
// set-but-empty, not only when it's absent entirely.
func TestExpandEnvVarsUsesDefaultWhenSetButEmpty(t *testing.T) {
	got, err := expandEnvVars([]byte("port: ${PORT:-9000}"), lookupFrom(map[string]string{"PORT": ""}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "port: 9000" {
		t.Fatalf("got %q", got)
	}
}

func TestExpandEnvVarsErrorsOnMissingRequiredVar(t *testing.T) {
	_, err := expandEnvVars([]byte("port: ${PORT}"), lookupFrom(nil))
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "PORT") {
		t.Errorf("error does not name the variable: %v", err)
	}
}

func TestExpandEnvVarsDollarDollarEscapesToLiteralDollar(t *testing.T) {
	got, err := expandEnvVars([]byte("price: $$5"), lookupFrom(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "price: $5" {
		t.Fatalf("got %q", got)
	}
}

// TestExpandEnvVarsDefaultResolvesNestedBareVar pins the reported gap:
// expansion used to be a flat single pass, so a bare "$VAR" written inside
// a ${OUTER:-default} fallback (the natural way to spell a path default,
// e.g. "$HOME/dev/exp/local-card-stack") was copied through literally
// instead of being resolved.
func TestExpandEnvVarsDefaultResolvesNestedBareVar(t *testing.T) {
	got, err := expandEnvVars(
		[]byte("dir: ${LOCAL_CARD_STACK_DIR:-$HOME/dev/exp/local-card-stack}"),
		lookupFrom(map[string]string{"HOME": "/Users/steven"}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "dir: /Users/steven/dev/exp/local-card-stack" {
		t.Fatalf("got %q", got)
	}
}

func TestExpandEnvVarsDefaultNestedVarMissingErrors(t *testing.T) {
	_, err := expandEnvVars([]byte("dir: ${LOCAL_CARD_STACK_DIR:-$HOME/x}"), lookupFrom(nil))
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "HOME") {
		t.Errorf("error does not name the missing nested variable: %v", err)
	}
}

func TestExpandEnvVarsDefaultDollarDollarEscapesToLiteralDollar(t *testing.T) {
	got, err := expandEnvVars([]byte("price: ${PRICE:-$$5}"), lookupFrom(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "price: $5" {
		t.Fatalf("got %q", got)
	}
}

// TestExpandEnvVarsBareVarOutsideDefaultIsNotExpanded guards the scoping
// decision (see envPattern's comment): bare "$VAR" is resolved only inside
// a ${...:-default} fallback, never at the top level, so a `run:` command's
// legitimate shell-native "$JAVA_HOME" is left for the shell to expand when
// the service actually starts — including when JAVA_HOME has no value here
// at all, which top-level expansion would otherwise treat as a hard error.
func TestExpandEnvVarsBareVarOutsideDefaultIsNotExpanded(t *testing.T) {
	in := `run: "$JAVA_HOME/bin/java -jar app.jar"`
	got, err := expandEnvVars([]byte(in), lookupFrom(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != in {
		t.Fatalf("got %q, want unchanged %q", got, in)
	}
}

func TestExpandEnvVarsMultipleReferencesInOneFile(t *testing.T) {
	got, err := expandEnvVars(
		[]byte("a: ${A}\nb: ${B:-fallback}\n"),
		lookupFrom(map[string]string{"A": "1"}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "a: 1\nb: fallback\n" {
		t.Fatalf("got %q", got)
	}
}

func TestLoadDotEnvMissingFileReturnsNilNoError(t *testing.T) {
	vars, err := loadDotEnv(filepath.Join(t.TempDir(), ".env"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vars != nil {
		t.Fatalf("expected nil, got %v", vars)
	}
}

func TestLoadDotEnvParsesKeyValueSkipsCommentsAndBlanks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "# a comment\n\nAPI_KEY=secret\nPORT = 8080\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	vars, err := loadDotEnv(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vars["API_KEY"] != "secret" || vars["PORT"] != "8080" {
		t.Fatalf("got %v", vars)
	}
	if len(vars) != 2 {
		t.Fatalf("expected 2 vars (comment/blank line skipped), got %v", vars)
	}
}

func TestLoadDotEnvStripsMatchingQuotes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "DOUBLE=\"with spaces\"\nSINGLE='also spaces'\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	vars, err := loadDotEnv(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vars["DOUBLE"] != "with spaces" || vars["SINGLE"] != "also spaces" {
		t.Fatalf("got %v", vars)
	}
}

func TestLoadDotEnvRejectsLineWithoutEquals(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("not-a-key-value-line\n"), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	_, err := loadDotEnv(path)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}

// --- integration through Load() ---

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestLoadExpandsEnvVarsAndAutoLoadsDotEnv(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".env", "API_KEY=from-dotenv\n")
	writeFile(t, dir, "ensemble.yaml", `
services:
  bff:
    run: "node dist/main.js"
    port: ${PORT:-8003}
    env:
      API_KEY: ${API_KEY}
`)

	c, err := Load(filepath.Join(dir, "ensemble.yaml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Services["bff"].Port != 8003 {
		t.Errorf("port = %d, want default 8003", c.Services["bff"].Port)
	}
	if got := c.Services["bff"].Env["API_KEY"]; got != "from-dotenv" {
		t.Errorf("API_KEY = %q, want value from .env", got)
	}
}

// The real process environment must win over .env — an explicit
// `API_KEY=... ensemble up` overrides whatever's checked into .env.
func TestLoadRealEnvOverridesDotEnv(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".env", "API_KEY=from-dotenv\n")
	writeFile(t, dir, "ensemble.yaml", `
services:
  bff:
    run: "node dist/main.js"
    port: 8003
    env:
      API_KEY: ${API_KEY}
`)
	t.Setenv("API_KEY", "from-real-env")

	c, err := Load(filepath.Join(dir, "ensemble.yaml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := c.Services["bff"].Env["API_KEY"]; got != "from-real-env" {
		t.Errorf("API_KEY = %q, want the real environment's value", got)
	}
}

func TestLoadMissingRequiredEnvVarErrorsWithFilePath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "ensemble.yaml", `
services:
  bff:
    run: "node dist/main.js"
    port: ${PORT}
`)

	_, err := Load(filepath.Join(dir, "ensemble.yaml"))
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "PORT") || !strings.Contains(err.Error(), "ensemble.yaml") {
		t.Errorf("error missing variable name or file path: %v", err)
	}
}

// TestLoadDefaultResolvesRealHOME reproduces the exact reported case
// through Load(): $HOME must resolve from the real process environment
// when used inside a ${...:-...} default, not just from a test-provided
// lookup map.
func TestLoadDefaultResolvesRealHOME(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", "/Users/steven")
	writeFile(t, dir, "ensemble.yaml", `
services:
  bff:
    run: "node dist/main.js"
    port: 8003
    dir: ${LOCAL_CARD_STACK_DIR:-$HOME/dev/exp/local-card-stack}
`)

	c, err := Load(filepath.Join(dir, "ensemble.yaml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "/Users/steven/dev/exp/local-card-stack"; c.Services["bff"].Dir != want {
		t.Errorf("Dir = %q, want %q", c.Services["bff"].Dir, want)
	}
}

func TestLoadWithoutDotEnvOrReferencesIsUnaffected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "ensemble.yaml", `
services:
  bff:
    run: "node dist/main.js"
    port: 8003
`)

	c, err := Load(filepath.Join(dir, "ensemble.yaml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Services["bff"].Port != 8003 {
		t.Errorf("port = %d, want 8003", c.Services["bff"].Port)
	}
}
