// Package server exposes ensemble's control plane as REST + SSE: every
// capability the dashboard/TUI needs (status, topology, traffic, latency,
// sessions, seed, restart, placement) is reachable over this JSON surface
// first — the dashboard is just another client of it. Built on net/http's
// method+wildcard ServeMux (Go 1.22+); no router dependency.
package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/ensemble/config"
	"github.com/caribou-crew/ensemble/ensemble/orchestrator"
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
}

// server holds Deps plus nothing else — handlers are methods on it so they
// can share Deps without a global.
type server struct {
	Deps
}

// New builds the /api control-plane surface as an http.Handler. A later
// task embeds the dashboard's static assets at "/"; for now unmatched paths
// 404 via the mux's default behavior.
func New(d Deps) http.Handler {
	s := &server{Deps: d}
	mux := http.NewServeMux()
	s.routes(mux)
	return mux
}

// shutdownGrace bounds how long Serve waits for in-flight requests (notably
// long-lived SSE/NDJSON streams) to drain once ctx is canceled.
const shutdownGrace = 5 * time.Second

// Serve runs h on addr until ctx is canceled, then shuts down gracefully.
// Returns nil on a clean shutdown; any other bind/serve error is returned
// as-is.
func Serve(ctx context.Context, addr string, h http.Handler) error {
	srv := &http.Server{Addr: addr, Handler: h}

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
