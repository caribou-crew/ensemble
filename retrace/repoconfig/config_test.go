package repoconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRepoYAML(t *testing.T, dir, contents string) string {
	t.Helper()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func TestLoadParsesAppsAndResolvesRootsRelativeToTheConfigFile(t *testing.T) {
	dir := t.TempDir()
	webRoot := dir
	mobileRoot := filepath.Join(dir, "apps", "sample", "react-native")
	if err := os.MkdirAll(mobileRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	path := writeRepoYAML(t, dir, `
repo: acme/sample-app
apps:
  uxt-web: { root: . }
  uxt-rn-ios: { root: apps/sample/react-native }
  uxt-rn-android: { root: apps/sample/react-native }
sync:
  branch: main
  since: 24h
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Repo != "acme/sample-app" {
		t.Fatalf("Repo = %q", cfg.Repo)
	}
	if root, ok := cfg.RootFor("uxt-web"); !ok || root != webRoot {
		t.Fatalf("RootFor(uxt-web) = %q, %v; want %q, true", root, ok, webRoot)
	}
	if root, ok := cfg.RootFor("uxt-rn-ios"); !ok || root != mobileRoot {
		t.Fatalf("RootFor(uxt-rn-ios) = %q, %v; want %q, true", root, ok, mobileRoot)
	}
	if cfg.Sync.Branch != "main" || cfg.Sync.Since != "24h" {
		t.Fatalf("Sync = %+v", cfg.Sync)
	}
	if cfg.Dir != dir {
		t.Fatalf("Dir = %q, want %q", cfg.Dir, dir)
	}
}

func TestLoadMultipleAppKeysCanShareOneRoot(t *testing.T) {
	dir := t.TempDir()
	mobileRoot := filepath.Join(dir, "mobile")
	if err := os.MkdirAll(mobileRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeRepoYAML(t, dir, `
apps:
  uxt-rn-ios: { root: mobile }
  uxt-rn-android: { root: mobile }
  uxt-native-ios: { root: mobile }
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	roots := cfg.Roots()
	if len(roots) != 1 || roots[0] != mobileRoot {
		t.Fatalf("Roots() = %v, want one root %q", roots, mobileRoot)
	}
	apps := cfg.AppsIn(mobileRoot)
	want := []string{"uxt-native-ios", "uxt-rn-android", "uxt-rn-ios"}
	if len(apps) != len(want) {
		t.Fatalf("AppsIn(mobileRoot) = %v, want %v", apps, want)
	}
	for i := range want {
		if apps[i] != want[i] {
			t.Fatalf("AppsIn(mobileRoot) = %v, want %v", apps, want)
		}
	}
}

func TestLoadRejectsEmptyApps(t *testing.T) {
	dir := t.TempDir()
	path := writeRepoYAML(t, dir, "repo: org/repo\napps: {}\n")
	if _, err := Load(path); err == nil {
		t.Fatal("Load with empty apps: want error, got nil")
	}
	path2 := writeRepoYAML(t, dir, "repo: org/repo\n")
	if _, err := Load(path2); err == nil {
		t.Fatal("Load with no apps key: want error, got nil")
	}
}

func TestLoadRejectsANonExistentRoot(t *testing.T) {
	dir := t.TempDir()
	path := writeRepoYAML(t, dir, `
apps:
  uxt-web: { root: does-not-exist }
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load with a missing root: want error, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "uxt-web") {
		t.Fatalf("error %q does not name the offending app key", got)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := writeRepoYAML(t, dir, "apps:\n  uxt-web: { root: ., bogus: true }\n")
	if _, err := Load(path); err == nil {
		t.Fatal("Load with an unknown field: want error, got nil")
	}
}
