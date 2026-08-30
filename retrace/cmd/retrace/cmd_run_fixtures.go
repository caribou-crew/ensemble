package main

// cmd_run_fixtures.go implements `retrace run --fixtures`: a flow's own
// accepted reference bundle serves as the upstream while `run` records,
// instead of a live one — see docs/superpowers/specs/
// 2026-08-30-retrace-run-fixtures-design.md. capture.StartStandalone's own
// recording proxy sits in front exactly as it always does; a
// fixtureUpstream is only ever wired in as one listener's Upstream, and
// everything downstream (wire.jsonl, redaction, RETRACE_PROXY_URL...) is
// unchanged.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/refs"
	"github.com/caribou-crew/ensemble/retrace/replay"
)

// errFixtureMiss marks a run whose fixture-serving upstream could not
// answer at least one call the app made. Exactly parallel to
// errCanonicalRefused (cmd_run.go): the manifest and finalized sentinel
// are still written before this is returned — the recording is real and
// worth keeping — and the caller maps it to exit 2 with the manifest
// intact, never exit 3.
var errFixtureMiss = fmt.Errorf("fixture miss")

// fixtureUpstream is one listener's local, loopback-only fixture server —
// a *replay.Server bound to an ephemeral port, standing in for that
// listener's live Upstream for the lifetime of one flow's capture.
type fixtureUpstream struct {
	ln  net.Listener
	srv *http.Server
	rs  *replay.Server
}

// addr is the "http://host:port" a listener's Upstream field should point
// at to reach this fixture server.
func (f *fixtureUpstream) addr() string { return "http://" + f.ln.Addr().String() }

// startFixtureUpstreams starts one fixture server per entry in listeners,
// each answering ONLY from ref's bundle. Misses are recorded in memory
// only (missesPath ""): the run directory that will hold misses.jsonl
// does not exist yet at this point — capture.StartStandalone creates it —
// so the caller flushes the aggregated misses once Paths.MissesPath is
// known (see flushMisses).
//
// TargetFilter mirrors bindReplayListeners' own rule (cmd_replay.go):
// unfiltered — every exchange eligible regardless of Target — whenever
// there is at most one listener, since nothing could conflict; filtered to
// each listener's own name only when there is more than one, so a
// multi-backend flow's fixture servers never answer for each other's
// traffic.
//
// Every listener loads its OWN Bundle, for the same reason
// bindReplayListeners does: replay.Bundle.Match mutates the bundle's
// `used` counters and is serialised only by the ONE Server that owns it,
// so two listeners sharing a Bundle under concurrent traffic — exactly
// what an app calling two backends in parallel produces — would race on
// that counter.
//
// On any failure, every server already started is closed before
// returning, so a caller never has to reason about a partially started
// set of fixture upstreams.
func startFixtureUpstreams(r refs.Reference, cfg *config.Config, listeners []config.ListenerEntry) ([]*fixtureUpstream, error) {
	opts, err := replayOptions(cfg)
	if err != nil {
		return nil, err
	}
	unfiltered := len(listeners) <= 1
	out := make([]*fixtureUpstream, 0, len(listeners))
	closeAll := func() {
		for _, f := range out {
			f.srv.Close()
		}
	}
	for _, l := range listeners {
		bundle, err := replay.LoadBundle(r.Dir, cfg.Dir)
		if err != nil {
			closeAll()
			return nil, err
		}
		o := opts
		if !unfiltered {
			o.TargetFilter = l.Name
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			closeAll()
			return nil, fmt.Errorf("cannot start a fixture server for listener %q: %w", l.Name, err)
		}
		rs := replay.NewServer(bundle, o, "")
		srv := &http.Server{Handler: rs}
		go srv.Serve(ln)
		out = append(out, &fixtureUpstream{ln: ln, srv: srv, rs: rs})
	}
	return out, nil
}

// shutdownFixtureUpstreams gracefully closes every fixture server, using
// the same bound cmd_replay.go's own listeners shut down under
// (replayShutdownTimeout) — the test command is already gone by the time
// this runs, so anything still in flight is a leaked connection, not work.
func shutdownFixtureUpstreams(ups []*fixtureUpstream) {
	ctx, cancel := context.WithTimeout(context.Background(), replayShutdownTimeout)
	defer cancel()
	for _, f := range ups {
		_ = f.srv.Shutdown(ctx)
	}
}

// overrideUpstreams copies base and replaces each entry's Upstream with
// its corresponding fixture server's address, in order. Never mutates
// base: it is flowRunParams.listeners, shared across every flow in a
// --flows invocation, and each flow's fixture servers are its own.
func overrideUpstreams(base []config.ListenerEntry, ups []*fixtureUpstream) []config.ListenerEntry {
	out := make([]config.ListenerEntry, len(base))
	copy(out, base)
	for i := range out {
		out[i].Upstream = ups[i].addr()
	}
	return out
}

// aggregateFixtureStats sums every fixture server's served/unused/miss
// counts and concatenates their misses, for Manifest.Fixtures and the
// exit-2 gate.
func aggregateFixtureStats(ups []*fixtureUpstream) (served, unusedCount, missCount int, misses []replay.Miss) {
	for _, f := range ups {
		served += f.rs.ServedCount()
		unusedCount += len(f.rs.UnusedExchanges())
		m := f.rs.Misses()
		missCount += len(m)
		misses = append(misses, m...)
	}
	return served, unusedCount, missCount, misses
}

// flushMisses appends every miss to path as NDJSON, in the exact format
// replay.Server writes internally (server.go's appendMissLocked) — a
// reader of misses.jsonl must see an identical shape regardless of
// whether it came from `retrace replay` or `retrace run --fixtures`.
// A no-op for zero misses: an ordinary clean --fixtures run must never
// leave an empty misses.jsonl behind where none existed before.
func flushMisses(path string, misses []replay.Miss) error {
	if path == "" || len(misses) == 0 {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, m := range misses {
		line, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			return err
		}
	}
	return nil
}
