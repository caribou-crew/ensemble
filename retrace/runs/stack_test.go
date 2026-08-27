package runs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func stackOf(pairs ...string) *Stack {
	s := &Stack{Services: map[string]string{}}
	for i := 0; i+1 < len(pairs); i += 2 {
		s.Services[pairs[i]] = pairs[i+1]
	}
	return s
}

func TestAServiceWhoseFingerprintMovedIsNamed(t *testing.T) {
	changed, seed := SameStack(
		stackOf("api", "abc123", "web", "def456"),
		stackOf("api", "abc123", "web", "999999"),
	)
	if len(changed) != 1 || changed[0] != "web" {
		t.Errorf("changed = %v, want [web]", changed)
	}
	if seed {
		t.Error("seedMoved is true with no seed on either side")
	}
}

func TestAServiceOnlyOneSideFingerprintedIsNotAChange(t *testing.T) {
	// The upgrade path and the not-a-repo path, which are the same shape. A
	// signal that fires on missing data fires on every run recorded before
	// this field existed and on every service whose dir is not a repository —
	// and it would fire as "the backend changed", the most alarming reading
	// available.
	for _, tc := range []struct {
		name string
		a, b *Stack
	}{
		{"absent from b", stackOf("api", "abc", "web", "def"), stackOf("api", "abc")},
		{"absent from a", stackOf("api", "abc"), stackOf("api", "abc", "web", "def")},
		{"no stack on b", stackOf("api", "abc"), nil},
		{"no stack on either", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if changed, _ := SameStack(tc.a, tc.b); len(changed) != 0 {
				t.Errorf("changed = %v, want none — absence of a fingerprint is not evidence of a change", changed)
			}
		})
	}
}

func TestEveryChangedServiceIsNamedInTheSameOrderEveryTime(t *testing.T) {
	// Map iteration order is randomized. A set of offenders that reshuffles
	// between two runs of the same comparison makes a stable report look
	// unstable, and a CI diff of two reports show changes nobody made.
	a := stackOf("web", "1", "api", "1", "billing", "1", "search", "1")
	b := stackOf("web", "2", "api", "2", "billing", "2", "search", "2")
	first, _ := SameStack(a, b)
	if len(first) != 4 {
		t.Fatalf("changed = %v, want all four", first)
	}
	for i := 0; i < 20; i++ {
		again, _ := SameStack(a, b)
		if strings.Join(again, ",") != strings.Join(first, ",") {
			t.Fatalf("offender order is not stable: %v then %v", first, again)
		}
	}
	if strings.Join(first, ",") != "api,billing,search,web" {
		t.Errorf("changed = %v, want sorted", first)
	}
}

func TestTheSameSeedAppliedAgainIsADifference(t *testing.T) {
	// Whatever the first run left behind is gone. The name alone cannot say
	// so, which is why the timestamp is part of the comparison.
	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	a := &Stack{Seed: &SeedRef{Name: "baseline", AppliedAt: at}}
	b := &Stack{Seed: &SeedRef{Name: "baseline", AppliedAt: at.Add(time.Hour)}}
	if _, seedMoved := SameStack(a, b); !seedMoved {
		t.Error("a re-applied seed reported as the same data")
	}
	same := &Stack{Seed: &SeedRef{Name: "baseline", AppliedAt: at}}
	if _, seedMoved := SameStack(a, same); seedMoved {
		t.Error("the identical seed record reported as a difference")
	}
}

func TestASeedOnlyOneSideRecordedIsNotADifference(t *testing.T) {
	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	a := &Stack{Seed: &SeedRef{Name: "baseline", AppliedAt: at}}
	if _, seedMoved := SameStack(a, &Stack{}); seedMoved {
		t.Error("a run that recorded no seed reported as seeded differently")
	}
}

func TestAnEmptyFingerprintIsRefusedRatherThanRecorded(t *testing.T) {
	// A blank on one side against a real value on the other has no honest
	// reading: SameStack skips it, so recording it would be storing a value
	// that means the same as omitting it — and the day someone compares
	// differently, it would mean "changed".
	err := validateStack(&Stack{Services: map[string]string{"api": ""}})
	if err == nil {
		t.Fatal("an empty fingerprint was accepted")
	}
	if !strings.Contains(err.Error(), "api") {
		t.Errorf("the error does not name the service: %v", err)
	}
	if err := validateStack(&Stack{Services: map[string]string{"": "abc"}}); err == nil {
		t.Error("a nameless service was accepted")
	}
	if err := validateStack(&Stack{Seed: &SeedRef{}}); err == nil {
		t.Error("a nameless seed was accepted — omitting it is how \"never seeded\" is spelled")
	}
	if err := validateStack(nil); err != nil {
		t.Errorf("a nil stack is the normal standalone case, not an error: %v", err)
	}
}

func TestAHandEditedManifestCannotSmuggleAnEmptyFingerprintIn(t *testing.T) {
	// ReadManifest validates too, not only WriteManifest. A manifest written
	// by an older build, or edited by hand, must fail the same way one
	// written wrongly does — otherwise the rule holds only for files this
	// binary produced, which is not where bad ones come from.
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	body := `{"schema":"` + Schema + `","capture":{"status":"ok"},"wire":{"recorded":true},` +
		`"stack":{"services":{"api":""}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(path); err == nil {
		t.Fatal("ReadManifest accepted a stack carrying an empty fingerprint")
	}
}
