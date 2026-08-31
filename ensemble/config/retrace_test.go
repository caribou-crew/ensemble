package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetraceAbsentByDefault(t *testing.T) {
	c := &Config{Dir: t.TempDir()}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Retrace != nil {
		t.Fatalf("Retrace = %+v, want nil when retrace: is not declared", c.Retrace)
	}
}

func TestRetraceBlockPresentValidates(t *testing.T) {
	c := &Config{Dir: t.TempDir(), Retrace: &RetraceConfig{Repo: "org/repo"}}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Retrace == nil {
		t.Fatal("Retrace = nil, want the declared block preserved")
	}
}

func TestRetraceEffectiveDirDefaultsToConfigDir(t *testing.T) {
	r := RetraceConfig{}
	configDir := "/stack"
	if got := r.EffectiveDir(configDir); got != configDir {
		t.Errorf("EffectiveDir() = %q, want %q", got, configDir)
	}
}

func TestRetraceEffectiveDirRelativeResolvesAgainstConfigDir(t *testing.T) {
	r := RetraceConfig{Dir: "subdir"}
	configDir := "/stack"
	want := filepath.Join(configDir, "subdir")
	if got := r.EffectiveDir(configDir); got != want {
		t.Errorf("EffectiveDir() = %q, want %q", got, want)
	}
}

func TestRetraceEffectiveDirAbsoluteIsUsedAsIs(t *testing.T) {
	r := RetraceConfig{Dir: "/elsewhere/.retrace"}
	if got := r.EffectiveDir("/stack"); got != "/elsewhere/.retrace" {
		t.Errorf("EffectiveDir() = %q, want /elsewhere/.retrace", got)
	}
}

func TestRetraceEffectiveSinceDefault(t *testing.T) {
	r := RetraceConfig{}
	if got := r.EffectiveSince(); got != DefaultRetraceSince {
		t.Errorf("EffectiveSince() = %q, want %q", got, DefaultRetraceSince)
	}
}

func TestRetraceEffectiveSinceOverride(t *testing.T) {
	r := RetraceConfig{Since: "24h"}
	if got := r.EffectiveSince(); got != "24h" {
		t.Errorf("EffectiveSince() = %q, want 24h", got)
	}
}

func TestRetraceInvalidSinceRejected(t *testing.T) {
	c := &Config{Dir: t.TempDir(), Retrace: &RetraceConfig{Since: "a week"}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for an unparseable since, got nil")
	}
}

func TestRetraceValidSinceUnitsAccepted(t *testing.T) {
	for _, since := range []string{"7d", "24h", "30m", "45s"} {
		c := &Config{Dir: t.TempDir(), Retrace: &RetraceConfig{Since: since}}
		if err := c.Validate(); err != nil {
			t.Errorf("since %q: unexpected error: %v", since, err)
		}
	}
}

func TestRetraceEffectiveReposDefaultsToRepo(t *testing.T) {
	r := RetraceConfig{Repo: "org/repo"}
	got := r.EffectiveRepos()
	if len(got) != 1 || got[0] != "org/repo" {
		t.Errorf("EffectiveRepos() = %v, want [org/repo]", got)
	}
}

func TestRetraceEffectiveReposPrefersRepos(t *testing.T) {
	r := RetraceConfig{Repos: []string{"org/a", "org/b"}}
	got := r.EffectiveRepos()
	if len(got) != 2 || got[0] != "org/a" || got[1] != "org/b" {
		t.Errorf("EffectiveRepos() = %v, want [org/a org/b]", got)
	}
}

func TestRetraceEffectiveReposEmptyWhenNeitherSet(t *testing.T) {
	if got := (RetraceConfig{}).EffectiveRepos(); len(got) != 0 {
		t.Errorf("EffectiveRepos() = %v, want empty", got)
	}
}

func TestRetraceBothRepoAndReposRejected(t *testing.T) {
	c := &Config{Dir: t.TempDir(), Retrace: &RetraceConfig{Repo: "org/a", Repos: []string{"org/b"}}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected an error when both repo and repos are set")
	}
}

func TestRetraceEffectiveWorkflowsDefaultsToWorkflow(t *testing.T) {
	r := RetraceConfig{Workflow: "retrace-ios"}
	got := r.EffectiveWorkflows()
	if len(got) != 1 || got[0] != "retrace-ios" {
		t.Errorf("EffectiveWorkflows() = %v, want [retrace-ios]", got)
	}
}

func TestRetraceBothWorkflowAndWorkflowsRejected(t *testing.T) {
	c := &Config{Dir: t.TempDir(), Retrace: &RetraceConfig{Workflow: "a", Workflows: []string{"b"}}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected an error when both workflow and workflows are set")
	}
}

func TestRetraceEffectiveAppDirFallsBackToEffectiveDirWhenAppIsNotMapped(t *testing.T) {
	r := RetraceConfig{}
	configDir := "/stack"
	if got := r.EffectiveAppDir(configDir, "web"); got != configDir {
		t.Errorf("EffectiveAppDir() = %q, want %q (falls back to EffectiveDir)", got, configDir)
	}
}

func TestRetraceEffectiveAppDirUsesTheAppsMapWhenPresent(t *testing.T) {
	r := RetraceConfig{Apps: map[string]string{"mobile-ios": "../mobile-app"}}
	configDir := "/stack"
	want := filepath.Join(configDir, "../mobile-app")
	if got := r.EffectiveAppDir(configDir, "mobile-ios"); got != want {
		t.Errorf("EffectiveAppDir() = %q, want %q", got, want)
	}
	// An app absent from Apps still falls back, even when Apps itself is
	// non-empty for OTHER apps.
	if got := r.EffectiveAppDir(configDir, "mobile-android"); got != configDir {
		t.Errorf("EffectiveAppDir() for an unmapped app = %q, want %q", got, configDir)
	}
}

func TestRetraceEffectiveAppDirHonoursAnAbsolutePath(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "elsewhere")
	r := RetraceConfig{Apps: map[string]string{"web": abs}}
	if got := r.EffectiveAppDir("/stack", "web"); got != abs {
		t.Errorf("EffectiveAppDir() = %q, want the absolute path %q unchanged", got, abs)
	}
}

func TestRetraceAppsEntryWithNoRetraceYamlIsRejected(t *testing.T) {
	// A typo'd or missing sibling repo resolves to a directory with no
	// retrace.yaml — retraceconfig.Discover would silently hand back a
	// bare-defaults config (no wire_ignore, no masks) for this with a nil
	// error, reproducing the false-positive-diff bug this feature exists
	// to fix. Validate must catch it instead.
	missing := t.TempDir()
	c := &Config{
		Dir:     t.TempDir(),
		Retrace: &RetraceConfig{Apps: map[string]string{"mobile-ios": missing}},
	}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected an error for an apps entry with no retrace.yaml, got nil")
	}
	if !strings.Contains(err.Error(), "mobile-ios") {
		t.Errorf("error = %q, want it to name the app key %q", err.Error(), "mobile-ios")
	}
}

func TestRetraceAppsEntryWithRetraceYamlValidates(t *testing.T) {
	present := t.TempDir()
	if err := os.WriteFile(filepath.Join(present, "retrace.yaml"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write retrace.yaml: %v", err)
	}
	c := &Config{
		Dir:     t.TempDir(),
		Retrace: &RetraceConfig{Apps: map[string]string{"web": present}},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error for an apps entry with a retrace.yaml present: %v", err)
	}
}
