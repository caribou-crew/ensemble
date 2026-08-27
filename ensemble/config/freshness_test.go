package config

import "testing"

func TestFreshnessAbsentByDefault(t *testing.T) {
	c := &Config{Dir: t.TempDir()}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Freshness != nil {
		t.Fatalf("Freshness = %+v, want nil when freshness: is not declared", c.Freshness)
	}
}

func TestFreshnessEffectiveDefaults(t *testing.T) {
	f := FreshnessConfig{}
	if got := f.EffectiveDefaultBranch(); got != DefaultFreshnessDefaultBranch {
		t.Errorf("EffectiveDefaultBranch() = %q, want %q", got, DefaultFreshnessDefaultBranch)
	}
	if got := f.EffectivePollIntervalS(); got != DefaultFreshnessPollIntervalS {
		t.Errorf("EffectivePollIntervalS() = %d, want %d", got, DefaultFreshnessPollIntervalS)
	}
}

func TestFreshnessEffectiveOverrides(t *testing.T) {
	f := FreshnessConfig{DefaultBranch: "develop", PollIntervalS: 60}
	if got := f.EffectiveDefaultBranch(); got != "develop" {
		t.Errorf("EffectiveDefaultBranch() = %q, want develop", got)
	}
	if got := f.EffectivePollIntervalS(); got != 60 {
		t.Errorf("EffectivePollIntervalS() = %d, want 60", got)
	}
}

func TestFreshnessBlockPresentValidates(t *testing.T) {
	c := &Config{Dir: t.TempDir(), Freshness: &FreshnessConfig{}}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Freshness == nil {
		t.Fatal("Freshness = nil, want the declared (empty) block preserved")
	}
}

func TestFreshnessNegativePollIntervalRejected(t *testing.T) {
	c := &Config{Dir: t.TempDir(), Freshness: &FreshnessConfig{PollIntervalS: -1}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error for negative poll_interval_s, got nil")
	}
}
