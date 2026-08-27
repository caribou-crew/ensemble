package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// The three hop-source kinds. They are the values that appear in a run's
// capture-trust record as `hop-source: <kind>`, so they are constants rather
// than string literals scattered across the packages that report them.
const (
	HopSourceEnsemble = "ensemble"
	HopSourceCommand  = "command"
	HopSourceFile     = "file"
)

// Hops configures where a run's hop chain comes from. It exists because the
// chain — who called whom, in what order, how long each leg took — is the one
// plane retrace cannot observe from the client edge. Ensemble knows it because
// ensemble proxies every service. A stack ensemble does not run knows it too,
// in its own tracing backend, and until now there was no way to hand that
// knowledge to retrace.
type Hops struct {
	Source HopSource `yaml:"source"`
}

// HopSource is the `hops.source:` value. It accepts three shapes:
//
//	hops:
//	  source: ensemble                      # the default; drain the control plane
//
//	hops:
//	  source:
//	    arm: ./scripts/trace-on.sh          # optional
//	    disarm: ./scripts/trace-off.sh      # optional; stdout: {"windowId": "..."}
//	    export: ./scripts/trace-export.sh   # required; stdout: hops NDJSON
//
//	hops:
//	  source:
//	    file: ./fixtures/hops.jsonl
//
// One field, three shapes, rather than three sibling keys with a precedence
// rule: a config that names two hop sources at once is not a thing anyone
// means, and a shape that cannot express it needs no rule for resolving it.
//
// An absent `hops:` block leaves Kind empty, which every consumer reads as
// HopSourceEnsemble — see Kind(). The zero value is the behaviour every
// existing config already has.
type HopSource struct {
	// Kind is one of the HopSource* constants, or "" for an absent block.
	// Derived by UnmarshalYAML from which shape was written; never a YAML key
	// of its own, or a config could claim one kind while carrying another.
	Kind string `yaml:"-"`

	// Arm runs before the test command, Disarm after it. Both optional: a
	// backend that is always tracing needs neither, and one that needs to be
	// told when to start needs both.
	Arm    string `yaml:"arm"`
	Disarm string `yaml:"disarm"`
	// Export writes the hop chain as NDJSON on stdout, in core/trace's Hop
	// schema — the same shape hops.jsonl holds. Required for the command form:
	// arm and disarm only bound a window, and a window nobody exports is a
	// hop plane that never reaches disk.
	Export string `yaml:"export"`

	// File is a path to hops NDJSON that already exists, resolved relative to
	// the directory holding retrace.yaml. It is the whole fixture story: a
	// recording someone else produced, replayed into a run without a live
	// backend anywhere.
	File string `yaml:"file"`
}

// Resolved reports the source kind, reading the zero value as "ensemble".
// Every consumer goes through this rather than switching on the raw field, so
// an absent `hops:` block cannot be mistaken for a fourth, unnamed kind.
func (h HopSource) Resolved() string {
	if h.Kind == "" {
		return HopSourceEnsemble
	}
	return h.Kind
}

// External reports whether the hop chain comes from somewhere other than
// ensemble's control plane. It is the one question every caller actually
// asks, so it is answered in one place.
func (h HopSource) External() bool { return h.Resolved() != HopSourceEnsemble }

func (h *HopSource) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var s string
		if err := node.Decode(&s); err != nil {
			return err
		}
		// Only "ensemble" is expressible as a scalar. Anything else is a
		// misspelling of it or an attempt at a kind that does not exist, and
		// both must name the three shapes rather than silently defaulting —
		// a run that quietly drained ensemble because `hops.source: datadog`
		// meant nothing would report a hop plane from the wrong stack.
		if strings.TrimSpace(s) != HopSourceEnsemble {
			return fmt.Errorf("line %d: hops.source: %q is not a hop source — write `ensemble`, or a mapping with `export:` (and optionally `arm:`/`disarm:`), or a mapping with `file:`",
				node.Line, s)
		}
		*h = HopSource{Kind: HopSourceEnsemble}
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: hops.source must be `ensemble` or a mapping", node.Line)
	}
	// KnownFields on the outer decoder does not reach a custom UnmarshalYAML
	// (it is handed a bare node, not the decoder), so unknown keys are
	// rejected by hand — the same reason WireIgnoreEntry does it. A typo'd
	// `exprot:` would otherwise decode as an empty command form and fail with
	// "export is required", pointing at the wrong thing.
	for i := 0; i+1 < len(node.Content); i += 2 {
		switch node.Content[i].Value {
		case "arm", "disarm", "export", "file":
		default:
			return fmt.Errorf("line %d: field %s not found in type config.HopSource",
				node.Content[i].Line, node.Content[i].Value)
		}
	}
	type plain HopSource
	var p plain
	if err := node.Decode(&p); err != nil {
		return err
	}
	got := HopSource{
		Arm:    strings.TrimSpace(p.Arm),
		Disarm: strings.TrimSpace(p.Disarm),
		Export: strings.TrimSpace(p.Export),
		File:   strings.TrimSpace(p.File),
	}
	hasCmd := got.Arm != "" || got.Disarm != "" || got.Export != ""
	switch {
	case got.File != "" && hasCmd:
		return fmt.Errorf("line %d: hops.source names both a file and commands — a run has one hop source, not two", node.Line)
	case got.File != "":
		got.Kind = HopSourceFile
	case hasCmd && got.Export == "":
		// arm/disarm without export is the shape that looks like it works: the
		// window opens, the window closes, and nothing ever reads it.
		return fmt.Errorf("line %d: hops.source needs `export:` — arm and disarm only bound a window, and a window nobody exports records no hops", node.Line)
	case hasCmd:
		got.Kind = HopSourceCommand
	default:
		return fmt.Errorf("line %d: hops.source is empty — write `ensemble`, or a mapping with `export:`, or a mapping with `file:`", node.Line)
	}
	*h = got
	return nil
}
