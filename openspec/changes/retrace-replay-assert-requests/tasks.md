## 1. `replay` package: observe requests without changing serving behavior

- [x] 1.1 Add `Options.AssertRequests bool` to `retrace/replay/match.go`'s
      `Options` (zero value `false` — matches the "every field's zero value
      is the strict/off one" convention `Options`'s doc comment already
      states).
- [x] 1.2 Add an `observed []trace.Hop` field (mutex-protected, alongside
      `misses`/`served`) to `replay.Server`, and an `ObservedHops() []trace.Hop`
      accessor mirroring `Misses()`'s copy-out pattern.
- [x] 1.3 In `Server.serve` (server.go), when `s.opts.AssertRequests` is true:
      on a **hit**, append an observed `trace.Hop` built from the real
      incoming method/path/query/headers/body (request side) and the
      already-computed `decrypted` exchange's Status/Headers/Body (response
      side, mirrored verbatim — see design Decision 3). On a **miss**
      (`MissUnmatched` or `MissKeyUnavailable`), append nothing.
- [x] 1.4 Unit tests in `replay/server_test.go`: a hit is observed with the
      client's real headers/body (not the recorded `ReqHeaders`); a miss
      contributes no observed hop; `AssertRequests: false` (the default)
      records nothing and leaves `ObservedHops()` empty, proving zero
      overhead/behavior change when the flag is off.

## 2. Reference-side hops for the comparison

- [x] 2.1 In `cmd_replay.go`, after `replay.LoadBundle` succeeds, read the
      bundle directory's `wire.jsonl` a second time via `runs.ReadHops`
      (same file `LoadBundle` already reads) to get `[]trace.Hop` in the
      shape `diff.DiffWire` expects — `Bundle` has no accessor back to the
      original hops (see design Decision 2 for why re-reading is preferred
      over adding one).
- [x] 2.2 When a listener's `TargetFilter` is set (multi-listener replay),
      filter the reference hops to `h.To == TargetFilter` before diffing —
      mirror `UnusedExchanges`' own filtering so `--assert-requests` never
      compares one listener's observed traffic against another listener's
      recorded traffic.

## 3. Wire the comparison into `cmdReplay`

- [x] 3.1 Add the `--assert-requests` flag to `cmdReplay`'s flag set
      (cmd_replay.go), default `false`.
- [x] 3.2 Thread `AssertRequests` through `replayOptions`/`bindReplayListeners`
      into each `replay.Server`'s `Options`.
- [x] 3.3 After the test command exits and the existing `len(misses) > 0`
      gate and `served == 0` ("nothing was compared") checks have both
      passed (design Decision 3: a miss already gates the run; the
      request-assertion gate only ever needs to run on a clean-match run),
      and only when `--assert-requests` was passed: for each listener,
      collect `rl.srv.ObservedHops()` and its filtered reference hops (task
      2), and call `diff.DiffWire(refHops, observedHops, opts)` with
      `opts` built the same way `replayOptions` already builds
      `replay.Options` from `cfg` (Rules, WireIgnorePaths, NormalizePath) —
      no `GroupsA/B`, no `Deviations` (replay has neither).
- [x] 3.4 Compute the wire-budget percentage using the same formula
      `diff/summary.go`'s `observedFor(s, "wire")` uses (changed paired ÷
      total paired × 100), reading `gates.wire.budget_pct` via
      `cfg.ResolveGates(flow)["wire"]` — nil budget means "any deviation at
      all fails" (design Decision 4). An `Extra` entry (call-count drift or
      a brand-new endpoint) fails unconditionally, independent of the
      percentage.
- [x] 3.5 On a failing gate, mark the run for `exitGate` (2) — same code
      path/precedence as an unmatched miss, applied after the miss and
      "nothing compared" checks so neither message is ever shadowed.

## 4. Report shape

- [x] 4.1 Add `Extra []diff.Call` and `RequestDiff *replayRequestDiff` to
      `replayReport` (cmd_replay.go) — both `omitempty` so a plain
      `retrace replay` (flag absent) emits byte-identical JSON to today.
      `replayRequestDiff` carries `Changed`, `Paired`, `BudgetPct int/float64`,
      `Threshold *float64`, and `Entries []diff.Entry` (only the paired
      entries that actually carry a request-side finding — response-side
      arrays are empty by construction per design Decision 3, so filtering
      to non-empty entries keeps the report from restating N no-op rows).
- [x] 4.2 `renderReplay`'s text output: when `--assert-requests` was passed,
      print the surplus/extra calls and any request-side deviation in the
      same style the existing miss section uses (nearest-call framing where
      useful), and print "every request matched exactly" when clean —
      mirroring the existing "every call matched the recording" sentence's
      pattern of never printing the same verdict for two different worlds.
- [x] 4.3 Unit/integration tests in `cmd_replay_test.go`: call-count drift
      (reference records 1, client calls 5 against the same exchange) is
      reported in `extra` and exits non-zero; a new request header on an
      otherwise-matching call is reported in `requestDiff` and exits
      non-zero; a clean run (`--assert-requests`, no drift) exits 0 with
      `extra: []` and `requestDiff.changed == 0`; a run WITHOUT the flag
      is unaffected (existing tests continue to pass unmodified).

## 5. Multi-listener and encryption interaction

- [x] 5.1 Test against `cmd_replay_multilistener_test.go`'s fixtures: two
      listeners each get their own `--assert-requests` comparison, scoped
      by `TargetFilter` (task 2.2) — traffic on listener A never counts as
      a deviation against listener B's recording.
- [x] 5.2 Test against an encrypted-field bundle (`replay/encrypt_test.go`'s
      fixtures): the observed hop's mirrored response uses the same
      `decryptExchange` output `writeHit` already computes, so a matched
      encrypted field does not read as a spurious `changed` under
      `--assert-requests` (design's Risk on this exact failure mode).

## 6. Documentation

- [x] 6.1 Update `cmdReplay`'s flag help text and any `retrace replay -h`
      usage strings to describe `--assert-requests`, its exit behavior, and
      that it is config-gated via `gates.wire.budget_pct` (no dedicated CLI
      flag for the threshold — see design Decision 6).
