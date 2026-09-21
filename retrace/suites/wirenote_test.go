package suites

import (
	"strings"
	"testing"
)

func TestWireNoteIsCarriedAndBounded(t *testing.T) {
	inv := testInventory()
	a := fullAttempt("ios", "ios")
	a.Results[0].Planes.Wire = "incomplete"
	a.Results[0].WireNote = "No reference wire exists for iOS; only the replay gateway was checked."
	if err := ValidateAttempt(inv, a); err != nil {
		t.Fatal(err)
	}
	cell := aggregate(t, inv, a)[0].Builds[0].Features[0].Flows[0].Platforms[1]
	if cell.Latest == nil || cell.Latest.WireNote != a.Results[0].WireNote || cell.Latest.Planes.Wire != "incomplete" {
		t.Fatalf("note not carried or plane altered: %+v", cell.Latest)
	}
	for name, note := range map[string]string{"padded": " note ", "too long": strings.Repeat("x", maxWireNote+1), "whitespace only": "   "} {
		bad := fullAttempt("bad", "ios")
		bad.Results[0].WireNote = note
		if err := ValidateAttempt(inv, bad); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
