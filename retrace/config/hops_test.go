package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadHops(t *testing.T, body string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "retrace.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

// TestAConfigThatNeverMentionsHopsMeansEnsemble is the compatibility floor:
// every retrace.yaml that exists today omits the block, and all of them must
// keep draining the control plane. The zero value has to BE that answer, not
// merely be corrected into it by whichever caller remembers to.
func TestAConfigThatNeverMentionsHopsMeansEnsemble(t *testing.T) {
	cfg, err := loadHops(t, "app: web\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Hops.Source.Resolved(); got != HopSourceEnsemble {
		t.Errorf("resolved kind = %q, want ensemble", got)
	}
	if cfg.Hops.Source.External() {
		t.Error("an absent hops block reads as an external source")
	}
	// And the same for a bare HopSource nobody parsed at all — the value a
	// caller constructs, or a struct field left at its default.
	var zero HopSource
	if zero.External() || zero.Resolved() != HopSourceEnsemble {
		t.Errorf("the zero HopSource resolves to %q, external=%v", zero.Resolved(), zero.External())
	}
}

func TestTheThreeHopSourceShapesParse(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want HopSource
	}{
		{"the scalar", "app: web\nhops:\n  source: ensemble\n",
			HopSource{Kind: HopSourceEnsemble}},
		{"commands", "app: web\nhops:\n  source:\n    arm: ./on.sh\n    disarm: ./off.sh\n    export: ./dump.sh\n",
			HopSource{Kind: HopSourceCommand, Arm: "./on.sh", Disarm: "./off.sh", Export: "./dump.sh"}},
		{"export alone", "app: web\nhops:\n  source:\n    export: ./dump.sh\n",
			HopSource{Kind: HopSourceCommand, Export: "./dump.sh"}},
		{"a file", "app: web\nhops:\n  source:\n    file: ./fixtures/hops.jsonl\n",
			HopSource{Kind: HopSourceFile, File: "./fixtures/hops.jsonl"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadHops(t, tc.yaml)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Hops.Source != tc.want {
				t.Errorf("source = %+v, want %+v", cfg.Hops.Source, tc.want)
			}
			if tc.want.Kind != HopSourceEnsemble && !cfg.Hops.Source.External() {
				t.Error("a configured source does not read as external")
			}
		})
	}
}

// TestAHopSourceThatNamesTwoSourcesIsRefused: a file and a command are two
// answers to one question. There is no precedence rule to apply because
// nobody means both — so the shape refuses rather than silently picking.
func TestAHopSourceThatNamesTwoSourcesIsRefused(t *testing.T) {
	_, err := loadHops(t, "app: web\nhops:\n  source:\n    file: ./h.jsonl\n    export: ./dump.sh\n")
	if err == nil {
		t.Fatal("a source naming both a file and a command was accepted")
	}
	if !strings.Contains(err.Error(), "one hop source") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// TestAWindowNobodyExportsIsRefused. arm and disarm without export is the
// shape that looks like it works: the window opens, the window closes, and
// nothing ever reads it — a run with a hop plane the config believes in and
// disk has never seen.
func TestAWindowNobodyExportsIsRefused(t *testing.T) {
	_, err := loadHops(t, "app: web\nhops:\n  source:\n    arm: ./on.sh\n    disarm: ./off.sh\n")
	if err == nil {
		t.Fatal("arm/disarm with no export was accepted")
	}
	if !strings.Contains(err.Error(), "export") {
		t.Errorf("the refusal does not name the missing key: %v", err)
	}
}

// TestAnUnrecognizedHopSourceIsRefusedRatherThanDefaulted: `source: datadog`
// must not quietly mean "ensemble". A run that drained the control plane
// because a scalar meant nothing would report a hop plane from a stack the
// config was trying to point away from.
func TestAnUnrecognizedHopSourceIsRefusedRatherThanDefaulted(t *testing.T) {
	_, err := loadHops(t, "app: web\nhops:\n  source: datadog\n")
	if err == nil {
		t.Fatal("`source: datadog` was accepted")
	}
	if !strings.Contains(err.Error(), "datadog") || !strings.Contains(err.Error(), "export") {
		t.Errorf("the refusal names neither what was written nor what is allowed: %v", err)
	}
}

// TestATypoInAHopSourceKeyIsRefused. KnownFields on the outer decoder does
// not reach a custom UnmarshalYAML, so without the hand-rolled check
// `exprot:` decodes as an empty command form and fails with a message about
// the wrong key entirely.
func TestATypoInAHopSourceKeyIsRefused(t *testing.T) {
	_, err := loadHops(t, "app: web\nhops:\n  source:\n    exprot: ./dump.sh\n")
	if err == nil {
		t.Fatal("a misspelled key was accepted")
	}
	if !strings.Contains(err.Error(), "exprot") {
		t.Errorf("the refusal does not name the key that was wrong: %v", err)
	}
}

func TestAnEmptyHopSourceIsRefused(t *testing.T) {
	// Whitespace, not absence: an absent block is the default, but a block
	// someone wrote and left blank is an unfinished edit.
	_, err := loadHops(t, "app: web\nhops:\n  source:\n    export: \"   \"\n")
	if err == nil {
		t.Fatal("an all-whitespace source was accepted")
	}
}
