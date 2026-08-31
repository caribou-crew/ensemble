package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/caribou-crew/ensemble/retrace/sync"
)

// watchTarget is one root `retrace serve --watch` keeps synced: cwd is the
// project root sync.Options.Cwd merges into, and apps — when non-empty —
// is that root's own allowlist (sync.Options.Apps), so a repo config
// mapping several roots never lets one root's sync merge another root's
// apps (design.md D4). A single-root server (no retrace.repo.yaml found)
// has exactly one watchTarget with apps left nil — no filter, matching
// `retrace sync`'s own unfiltered default.
type watchTarget struct {
	cwd  string
	apps []string
}

// defaultWatchInterval is --watch's default re-sync cadence when
// --interval is not set — the same order of magnitude
// ensemble/config.DefaultFreshnessPollIntervalS already uses for its own
// background poller (5 minutes), not seconds: `gh run list` on a tight
// loop across every developer running `retrace serve --watch` against a
// busy repo risks GitHub API rate limits (design.md's Risks section).
const defaultWatchInterval = 5 * time.Minute

// startWatch runs one sync immediately (so a fresh `retrace serve --watch`
// shows CI data without waiting a full interval) and then one every
// interval, until ctx is done — the same context the HTTP server's own
// graceful shutdown already listens on, so Ctrl-C stops both. A sync
// error on any target, any tick, is reported to stderr and never stops
// the loop or any other target's sync — a transient `gh`/GitHub failure
// must not take down a dashboard a developer is actively looking at
// (design.md's Risks section, retrace-live-sync's own requirement).
func startWatch(ctx context.Context, stderr io.Writer, targets []watchTarget, base sync.Options, interval time.Duration) {
	tick := func() {
		for _, t := range targets {
			o := base
			o.Cwd = t.cwd
			o.Apps = t.apps
			res, err := sync.Run(o)
			if err != nil {
				fmt.Fprintf(stderr, "retrace serve --watch: sync %s: %v\n", t.cwd, err)
				continue
			}
			if len(res.Synced) > 0 {
				fmt.Fprintf(stderr, "retrace serve --watch: synced %d run(s) into %s\n", len(res.Synced), t.cwd)
			}
		}
	}

	tick()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				tick()
			}
		}
	}()
}

// pick returns flagVal when it is non-empty, else fallback — CLI flags win
// over a retrace.repo.yaml sync: default, matching every other
// flag-overrides-config precedence this command already has.
func pick(flagVal, fallback string) string {
	if flagVal != "" {
		return flagVal
	}
	return fallback
}
