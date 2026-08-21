// Package buildinfo derives a human-readable version string for binaries
// that goreleaser hasn't stamped via -X main.version — i.e. every local
// developer build (`go build`/`go install`/`make build` run inside a git
// checkout).
package buildinfo

import (
	"fmt"
	"runtime/debug"
)

// Resolve returns ldflagsVersion unchanged unless it's the unstamped
// default "dev", in which case it enriches "dev" with the short commit the
// binary was built from. The Go toolchain embeds that commit (as
// vcs.revision/vcs.modified build settings) automatically for any build run
// from within a git working tree, so no extra build step is required to
// get e.g. "dev+a1b2c3d" or "dev+a1b2c3d-dirty" out of a plain `go install`
// instead of a bare, indistinguishable "dev".
func Resolve(ldflagsVersion string) string {
	if ldflagsVersion != "dev" {
		return ldflagsVersion
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ldflagsVersion
	}

	var revision string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if revision == "" {
		return ldflagsVersion
	}
	if len(revision) > 7 {
		revision = revision[:7]
	}

	if dirty {
		return fmt.Sprintf("%s+%s-dirty", ldflagsVersion, revision)
	}
	return fmt.Sprintf("%s+%s", ldflagsVersion, revision)
}
