package buildinfo

import (
	"regexp"
	"testing"
)

func TestResolveLeavesAStampedVersionAlone(t *testing.T) {
	got := Resolve("v1.2.3")
	if got != "v1.2.3" {
		t.Fatalf("Resolve(%q) = %q, want unchanged", "v1.2.3", got)
	}
}

// go test builds its binary from local sources the same way `go build`
// does, so the Go toolchain stamps this test binary with the same
// vcs.revision/vcs.modified settings a developer's `go install` would get
// — letting this assert Resolve's real enrichment behavior end to end
// rather than a fake BuildInfo.
func TestResolveDevEnrichesWithTheBuildingCommit(t *testing.T) {
	got := Resolve("dev")

	want := regexp.MustCompile(`^dev(\+[0-9a-f]{7}(-dirty)?)?$`)
	if !want.MatchString(got) {
		t.Fatalf("Resolve(%q) = %q, want a match for %s", "dev", got, want)
	}
}
