// Package server exposes ensemble's control plane as REST + SSE: every
// capability the dashboard/TUI needs (status, topology, traffic, latency,
// sessions, seed, restart, placement) is reachable over this JSON surface
// first — the dashboard is just another client of it. Built on net/http's
// method+wildcard ServeMux (Go 1.22+); no router dependency.
package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/ensemble/config"
	"github.com/caribou-crew/ensemble/ensemble/inspector"
	"github.com/caribou-crew/ensemble/ensemble/orchestrator"
	"github.com/caribou-crew/ensemble/ensemble/server/ui"
)

// Deps wires the server to the live orchestrator and proxy machinery it
// fronts. Every field is a shared, already-constructed instance — the
// server never owns their lifecycle.
type Deps struct {
	Cfg      *config.Config
	Orch     *orchestrator.Orchestrator
	Rec      *proxy.Recorder
	Lat      *proxy.LatencyStore
	Sessions *proxy.SessionManager
	Version  string
	// Insp, when set, backs the GET /api/databases*, GET
	// /api/databases/{name}/schema|rows, and GET /api/inspector/stream
	// endpoints. Nil disables them (501) — entity passthrough (GET
	// /api/entities, ANY /api/entities/{name}/{path...}) does not depend on
	// it and keeps working regardless.
	Insp *inspector.Inspector
	// InspectPollInterval overrides how often the inspector change-stream
	// poller (GET /api/inspector/stream) checks every registered driver.
	// Zero uses the production default (2s) — exposed mainly so tests don't
	// have to wait out that real interval to observe an event.
	InspectPollInterval time.Duration
	// Shutdown, when set, is invoked (in a new goroutine, after the
	// response is written) by POST /api/shutdown — typically the
	// context.CancelFunc a caller (e.g. cmd/ensemble's `up`) uses to stop
	// Serve. Nil disables the endpoint (501); the endpoint is further
	// guarded to loopback callers regardless.
	Shutdown func()
	// AllowedHosts extends the Host/Origin allow-list the browser guard
	// enforces (see guard.go). Loopback literals and "localhost" are
	// always allowed, so the zero value is the right one for the default
	// loopback bind; a caller that binds elsewhere must name that host
	// (or pass the single entry "*" to turn host/origin matching off for
	// a bind whose reachable names can't be enumerated).
	AllowedHosts []string
}

// server holds Deps plus the inspector change-stream fan-out hub (see
// inspect.go) — handlers are methods on it so they can share Deps without a
// global.
type server struct {
	Deps
	hub *inspectHub
}

// New builds ensemble's single-origin HTTP surface: the /api control plane
// plus the embedded dashboard, mounted at "/" behind every more specific
// "/api/..." pattern — Go 1.22's ServeMux prefers the more specific match,
// so the dashboard's SPA fallback never shadows an API route.
//
// Everything is wrapped in guard, which rejects cross-origin browser calls
// and Host headers we don't serve — the control plane is unauthenticated,
// so that guard is the only thing standing between a random web page the
// developer has open and this stack's control surface. The dashboard is
// mounted inside that wrapper, not outside it, so its routes get the same
// CSRF/DNS-rebinding protection as /api/*.
func New(d Deps) http.Handler {
	s := &server{Deps: d}
	if d.Insp != nil {
		interval := d.InspectPollInterval
		if interval <= 0 {
			interval = inspectPollInterval
		}
		s.hub = newInspectHub(d.Insp, interval)
	}
	mux := http.NewServeMux()
	s.routes(mux)
	mux.Handle("GET /", ui.Handler())
	return guard(newHostSet(d.AllowedHosts), mux)
}

// shutdownGrace bounds how long Serve waits for in-flight requests (notably
// long-lived SSE/NDJSON streams) to drain once ctx is canceled.
const shutdownGrace = 5 * time.Second

// Serve runs h on addr until ctx is canceled, then shuts down gracefully.
// Returns nil on a clean shutdown; any other bind/serve error is returned
// as-is.
func Serve(ctx context.Context, addr string, h http.Handler) error {
	// BaseContext ties every accepted connection's (and therefore every
	// request's) context to ctx, so a handler blocked on r.Context().Done()
	// — e.g. handleTrafficStream's SSE loop — unblocks the instant ctx is
	// canceled, rather than only when the client disconnects. Without this,
	// Shutdown below waits out the full shutdownGrace for such handlers and
	// returns context.DeadlineExceeded.
	srv := &http.Server{
		Addr:    addr,
		Handler: h,
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		// Drain the ListenAndServe goroutine so it doesn't leak/log after
		// this function returns.
		<-errCh
		return nil
	}
}
