package config

import (
	"path/filepath"
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
