package rules

import "testing"

func TestNamedMatchersAcceptTheirFormatAndRejectOthers(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  bool
	}{
		{"uuid", "3f2504e0-4f89-11d3-9a0c-0305e82c3301", true},
		{"uuid", "not-a-uuid", false},
		{"iso8601", "2026-08-21T10:15:00.123Z", true},
		{"iso8601", "Wed", false}, // stricter than time.Parse-anything on purpose
		{"http-date", "Wed, 21 Aug 2026 10:15:00 GMT", true},
		{"http-date", "2026-08-21T10:15:00Z", false},
		{"etag", `W/"abc"`, true},
		{"etag", "abc", false},
		{"integer", 1760.0, true}, // a JSON number decodes as float64
		{"integer", "1760", true}, // a header carries the string form
		{"integer", 17.6, false},
		{"semver", "1.2.3-rc.1", true},
		{"semver", "1.2", false},
	}
	for _, c := range cases {
		m, err := ParseMatcher(c.name, "test")
		if err != nil {
			t.Fatalf("ParseMatcher(%q): %v", c.name, err)
		}
		got := Classify(m, c.value, c.value, true) == Tolerated
		if got != c.want {
			t.Errorf("%s(%v) satisfied = %v, want %v", c.name, c.value, got, c.want)
		}
	}
}

func TestMatcherToleratesValueChangeButCatchesShapeChange(t *testing.T) {
	m, _ := ParseMatcher("uuid", "test")
	if got := Classify(m, "3f2504e0-4f89-11d3-9a0c-0305e82c3301", "6ec0bd7f-11c0-43da-975e-2a8ad9ebae0b", true); got != Tolerated {
		t.Errorf("two uuids = %v, want tolerated", got)
	}
	if got := Classify(m, "3f2504e0-4f89-11d3-9a0c-0305e82c3301", 42.0, true); got != Violation {
		t.Errorf("uuid vs number = %v, want violation", got)
	}
}

func TestUnknownMatcherNameIsAnErrorNotSilentTolerance(t *testing.T) {
	if _, err := ParseMatcher("uuidv4", "wireRules[0].body.id"); err == nil {
		t.Fatal("want an error naming the location and the valid names")
	}
}

func TestCustomPatternRequiresBothSidesToMatch(t *testing.T) {
	m, err := ParseMatcher(map[string]any{"pattern": `^v\d+$`}, "test")
	if err != nil {
		t.Fatalf("ParseMatcher: %v", err)
	}
	if got := Classify(m, "v1", "v2", true); got != Tolerated {
		t.Errorf("both match = %v, want tolerated", got)
	}
	if got := Classify(m, "v1", "x", true); got != Violation {
		t.Errorf("one side fails = %v, want violation", got)
	}
	if m.Label() != `/^v\d+$/` {
		t.Errorf("Label = %q", m.Label())
	}
}

func TestAValueMatcherNeverExcusesAnAppearingOrDisappearingField(t *testing.T) {
	uuid, _ := ParseMatcher("uuid", "test")
	if got := Classify(uuid, "3f2504e0-4f89-11d3-9a0c-0305e82c3301", nil, false); got != Changed {
		t.Errorf("one-sided value under a matcher = %v, want changed", got)
	}
	ign, _ := ParseMatcher("ignore", "test")
	if got := Classify(ign, "x", nil, false); got != Ignored {
		t.Errorf("ignore must silence a one-sided value, got %v", got)
	}
}
