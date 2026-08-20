// Package ui embeds the built dashboard (dashboard/ensemble-ui, via Vite's
// build.outDir) and serves it as a single-page app: real files are served
// as themselves, everything else falls back to index.html so client-side
// routes survive a hard refresh or a deep link.
package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist holds the dashboard's built output. Task 3.1 commits only a
// placeholder dist/index.html (see this directory's README-less
// convention, documented in the Phase 3 plan); `pnpm -r build` overwrites
// it with the real bundle, which is never itself committed.
//
//go:embed all:dist
var dist embed.FS

// Handler serves the embedded dist/ tree at whatever prefix it's mounted
// under. It never needs to see /api/* — the caller (server.New) is
// responsible for keeping that precedence, typically by registering this
// at the mux's catch-all "/" pattern alongside more specific "/api/..."
// patterns, which Go 1.22's ServeMux prefers automatically.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// Unreachable outside a broken build: "dist" is the embed
		// directive's own root, so fs.Sub can only fail here if the
		// embedded tree is malformed at compile time.
		panic("ui: embedded dist is unusable: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := path.Clean(r.URL.Path)
		name := strings.TrimPrefix(clean, "/")
		if name == "" || name == "." {
			name = "index.html"
		}

		if _, err := fs.Stat(sub, name); err != nil {
			// /assets/* is where the hashed build output lives; a miss
			// there is a genuinely broken reference (or a probe), not a
			// client-side route, so it must 404 rather than quietly
			// return the app shell.
			if strings.HasPrefix(clean, "/assets/") {
				http.NotFound(w, r)
				return
			}
			// SPA fallback: any other unknown path is assumed to be a
			// client-side route (e.g. ?view=/deep link) — serve the app
			// shell and let the router in the browser sort it out.
			fallback := r.Clone(r.Context())
			fallback.URL.Path = "/"
			fileServer.ServeHTTP(w, fallback)
			return
		}

		fileServer.ServeHTTP(w, r)
	})
}
