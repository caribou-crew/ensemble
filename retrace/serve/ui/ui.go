// Package ui embeds the built review UI (dashboard/retrace-ui, via Vite's
// build.outDir) and serves it as a single-page app: real files are served as
// themselves, everything else falls back to index.html so client-side routes
// survive a hard refresh or a deep link.
//
// This is deliberately the same body as ensemble/server/ui, not a second
// answer to the same question: the two dashboards are two bundles, and the
// serving contract they share — SPA fallback everywhere, 404 under /assets/ —
// is one contract. The duplication is one file with one test each rather than
// a shared package, because a shared package would have to be parameterised
// by an embed.FS, and //go:embed cannot be parameterised: the directive is
// resolved at compile time against the SOURCE directory it sits in.
package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist holds the review UI's built output. Only the placeholder
// dist/index.html is committed; `pnpm -r build` overwrites it with the real
// bundle, which is never itself committed (see .gitignore).
//
//go:embed all:dist
var dist embed.FS

// Handler serves the embedded dist/ tree at whatever prefix it's mounted
// under. It never sees /api/* — retrace/serve's New dispatches those to the
// API mux before this handler is reached, which is what keeps a wrong-method
// API call answering 405 instead of 200-with-the-app-shell.
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
			// client-side route (e.g. /?app=web&flow=checkout) — serve the
			// app shell and let the router in the browser sort it out.
			fallback := r.Clone(r.Context())
			fallback.URL.Path = "/"
			fileServer.ServeHTTP(w, fallback)
			return
		}

		fileServer.ServeHTTP(w, r)
	})
}
