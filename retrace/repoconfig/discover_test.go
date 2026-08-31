package repoconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverFindsConfigInTheStartingDirectory(t *testing.T) {
	dir := t.TempDir()
	writeRepoYAML(t, dir, "apps:\n  uxt-web: { root: . }\n")

	cfg, foundDir, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cfg == nil {
		t.Fatal("Discover: cfg is nil, want a loaded config")
	}
	if foundDir != dir {
		t.Fatalf("foundDir = %q, want %q", foundDir, dir)
	}
}

func TestDiscoverSearchesUpwardFromANestedDirectory(t *testing.T) {
	dir := t.TempDir()
	writeRepoYAML(t, dir, "apps:\n  uxt-web: { root: . }\n")
	nested := filepath.Join(dir, "apps", "sample", "react-native")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg, foundDir, err := Discover(nested)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cfg == nil {
		t.Fatal("Discover: cfg is nil, want a loaded config found by walking upward")
	}
	if foundDir != dir {
		t.Fatalf("foundDir = %q, want %q", foundDir, dir)
	}
}

func TestDiscoverReturnsNilWhenNoConfigExistsAboveTheStartingDirectory(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	// A .git directory bounds the search so it never escapes this
	// temp-dir tree into the real filesystem (which may itself sit under
	// a repo with an unrelated retrace.repo.yaml).
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg, foundDir, err := Discover(nested)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cfg != nil {
		t.Fatalf("Discover: cfg = %+v, want nil (no retrace.repo.yaml anywhere above)", cfg)
	}
	if foundDir != "" {
		t.Fatalf("foundDir = %q, want empty", foundDir)
	}
}

func TestDiscoverStopsAtADotGitDirectory(t *testing.T) {
	// Two sibling "repos" under one temp dir: an outer retrace.repo.yaml
	// must NOT be found by a search that starts inside the inner one,
	// because the inner .git marks its own boundary.
	root := t.TempDir()
	writeRepoYAML(t, root, "apps:\n  outer: { root: . }\n")

	inner := filepath.Join(root, "inner")
	if err := os.MkdirAll(filepath.Join(inner, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(inner, "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg, foundDir, err := Discover(nested)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cfg != nil {
		t.Fatalf("Discover: cfg = %+v, want nil (search should stop at inner's .git)", cfg)
	}
	if foundDir != "" {
		t.Fatalf("foundDir = %q, want empty", foundDir)
	}
}

func TestDiscoverFindsConfigBesideDotGit(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRepoYAML(t, dir, "apps:\n  uxt-web: { root: . }\n")

	cfg, foundDir, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cfg == nil {
		t.Fatal("Discover: cfg is nil, want a loaded config found beside .git")
	}
	if foundDir != dir {
		t.Fatalf("foundDir = %q, want %q", foundDir, dir)
	}
}
