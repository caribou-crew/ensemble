package server

import (
	"net/http"

	"github.com/caribou-crew/ensemble/core/httpguard"
)

// guard is the shared browser-facing protection, now living in
// core/httpguard so retrace's marker door (Task 4), replay server
// (Task 12) and review server (Task 13) get the identical treatment — the
// rationale (CSRF against an unauthenticated loopback control plane; DNS
// rebinding) applies to every one of them, and a second copy would drift.
func guard(allowedHosts []string, h http.Handler) http.Handler {
	return httpguard.Handler(allowedHosts, h)
}
