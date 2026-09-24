package config

import (
	"strings"
	"testing"
)

func TestMaxExtraParsesAnIntegerAndTheWord(t *testing.T) {
	dir := writeYAML(t, `app: web
wire_repeats:
  - method: POST
    path: /oauth/cardholder-token
    max_extra: 1
    why: retried once on cold start
  - method: GET
    path: /session
    max_extra: any
    why: polled until ready
`)
	c, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(c.WireRepeats) != 2 {
		t.Fatalf("WireRepeats = %+v, want 2 entries", c.WireRepeats)
	}
	if c.WireRepeats[0].MaxExtra.Any || c.WireRepeats[0].MaxExtra.N != 1 {
		t.Errorf("entry 0 MaxExtra = %+v, want {N:1}", c.WireRepeats[0].MaxExtra)
	}
	if !c.WireRepeats[1].MaxExtra.Any {
		t.Errorf("entry 1 MaxExtra = %+v, want Any", c.WireRepeats[1].MaxExtra)
	}
}

func TestMaxExtraRejectsAnythingElse(t *testing.T) {
	for _, body := range []string{
		"app: web\nwire_repeats:\n  - method: POST\n    path: /x\n    max_extra: -1\n    why: x\n",
		"app: web\nwire_repeats:\n  - method: POST\n    path: /x\n    max_extra: nope\n    why: x\n",
	} {
		if _, err := Discover(writeYAML(t, body)); err == nil {
			t.Errorf("Discover(%q) = nil, want an error rejecting the bad max_extra", body)
		}
	}
}

func TestMaxExtraAllows(t *testing.T) {
	cases := []struct {
		name string
		m    MaxExtra
		n    int
		want bool
	}{
		{"under budget", MaxExtra{N: 1}, 0, true},
		{"at budget", MaxExtra{N: 1}, 1, true},
		{"over budget", MaxExtra{N: 1}, 2, false},
		{"any", MaxExtra{Any: true}, 1000, true},
	}
	for _, c := range cases {
		if got := c.m.Allows(c.n); got != c.want {
			t.Errorf("%s: Allows(%d) = %v, want %v", c.name, c.n, got, c.want)
		}
	}
}

func TestValidateWireRepeatsRequiresMethodAndPath(t *testing.T) {
	for _, body := range []string{
		"app: web\nwire_repeats:\n  - path: /x\n    max_extra: 1\n    why: x\n",
		"app: web\nwire_repeats:\n  - method: POST\n    max_extra: 1\n    why: x\n",
	} {
		_, err := Discover(writeYAML(t, body))
		if err == nil {
			t.Fatalf("Discover(%q) = nil, want an error", body)
		}
		if !strings.Contains(err.Error(), "wire_repeats[0]") {
			t.Errorf("error does not name wire_repeats[0]:\n%v", err)
		}
	}
}

func TestRequireWhyCatchesAnUnexplainedWireRepeat(t *testing.T) {
	dir := writeYAML(t, "app: web\nrequire_why: true\nwire_repeats:\n  - method: POST\n    path: /oauth/cardholder-token\n    max_extra: 1\n")
	_, err := Discover(dir)
	if err == nil {
		t.Fatal("Discover = nil, want a config error")
	}
	if !strings.Contains(err.Error(), "wire_repeats[0]") {
		t.Errorf("error must name the entry, got: %v", err)
	}
}
