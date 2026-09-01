Implementation order: **group 1 first** (shared schema — everything else
depends on it), then groups 2–9 are independent and parallelizable across
agents. Group 10 is the integration pass, last.

## 1. Shared schema additions (blocking; small)

- [x] 1.1 Add `BodyB64 string` and `SetCookies []string` to
      `trace.Payload`, and `Streaming bool` + `Unsupported string` to
      `trace.Hop` (`core/trace/hop.go`), all `json:"...,omitempty"`.
      Document mutual exclusion of `Body`/`BodyB64` on the struct.
- [x] 1.2 Add `DroppedHops int` to the session result type
      (`core/proxy/session.go`) and thread it into the retrace run
      manifest type.
- [x] 1.3 Fixture test: a pre-change `wire.jsonl`/`hops.jsonl` sample
      (checked in) round-trips through the NDJSON reader and
      `replay.LoadBundle` unchanged; a post-change hop with the new
      fields survives write→read.

## 2. recording-redaction (retrace + core/trace)

- [x] 2.1 Extract the secret-key list shared by query redaction
      (`core/trace/redact.go:34`) as `defaultRedactBodyKeys`; implement a
      recursive JSON body walker applying the active mode (destroy or
      encrypt) to matching keys, both payload directions.
- [x] 2.2 Wire it into `NewRedactor` defaults with a
      `redact: { body_defaults: off }` opt-out in retrace config
      (`retrace/config`); user rules still layer on top.
- [x] 2.3 Add the built-in `redacted` matcher to `rules.Classify`
      (`retrace/rules`): recorded destroy-sentinel matches any live value.
- [x] 2.4 Make `replay/match.go` use it in body-subset matching and make
      `SignificantQuery` treat sentinel-valued recorded params as equal.
- [x] 2.5 Implement the accept-time secret scan in `retrace/refs`
      (staging step in `Accept`, `refs/refs.go:395`): secret-key values
      not redacted, JWT shape (`eyJ` + two dot segments), `AKIA[0-9A-Z]{16}`,
      `(?i)bearer\s+\S{20,}` in header values. Findings → refusal listing
      field paths + suggested `retrace ref rule` commands; `--force` sets
      `acceptedWithSecrets: true` in the reference manifest.
- [x] 2.6 Surface the same scan verdict in `retrace serve` accept route +
      retrace-ui accept button (`retrace/serve/routes.go`,
      `dashboard/retrace-ui`).
- [x] 2.7 Tests: body walker (flat/nested/array/non-JSON/opt-out), matcher
      wildcard behavior in match + query, scan true/false positives,
      forced accept manifest note, old bundles still accept clean.

## 3. proxy-wiring-validation (ensemble/config + server + UI)

- [x] 3.1 Implement env-port scanning in `ensemble/config/validate.go`
      beside the port-collision check: parse loopback /
      `host.docker.internal` host:port refs out of every service/stub/
      variant `env:` value; warn when the port is another node's real
      `port:` and that node has `proxy:`. Return warnings, never errors.
- [x] 3.2 Evaluate against the *active* variant/placement; re-evaluate on
      `SetVariant`/`Flip` (orchestrator hook).
- [x] 3.3 Add `warnings` to `GET /api/status` payload and `ensemble
      status`/`--json`/`up` output.
- [x] 3.4 Dashboard Services tab: wiring-warning badge with the message as
      tooltip (`dashboard/ensemble-ui`).
- [x] 3.5 Tests: real-port hit, proxy-port clean, no-proxy target clean,
      db/stub ports clean, variant-scoped warning appears/disappears on
      switch, sample stack validates clean.

## 4. protocol-guardrails (core/proxy + retrace + UI)

- [x] 4.1 Streaming detection (`text/event-stream`, or chunked without
      `Content-Length`) + flush-through writer wrapper in
      `core/proxy/proxy.go` (`io.Copy` at :533 gains per-write flush for
      streaming responses).
- [x] 4.2 Record streaming hops at response-headers time
      (`Streaming: true`), finalize in place on close; add a
      `hop.updated` SSE event; recorder keeps slot addressable by `seq`
      (`core/proxy/recorder.go`).
- [x] 4.3 Dashboard + TUI upsert hops by `seq` on `hop.updated` (verify
      dashboard already does; fix TUI if append-only).
- [x] 4.4 WebSocket/gRPC refusal: detect `Upgrade`/`Connection: Upgrade`
      and `Content-Type: application/grpc*` before forwarding; respond
      501 with an explanatory JSON body; record hop with
      `Unsupported: "websocket"|"grpc"`.
- [x] 4.5 Degraded capture-verdict note when a session contains
      `Unsupported` hops (`core/proxy/session.go` verdict assembly);
      distinct badge in the Traffic view.
- [x] 4.6 Binary body capture: invalid-UTF-8 or known-binary content type
      → `BodyB64` (same 256 KB cap) in the tee capture path; replay
      serves decoded bytes; wire diff compares `BodyB64` payloads as
      opaque hashes; `LoadBundle` accepts them.
- [x] 4.7 Tests: SSE flush timing (httptest upstream with paced events),
      streaming hop record-then-finalize + updated event, ws/grpc 501 +
      flagged hop + degraded verdict, PNG byte-identical through
      record→replay, old string-body bundles unaffected.

## 5. service-supervision (orchestrator + server + UIs)

- [x] 5.1 Reaper goroutine in `startNativeProcess`
      (`ensemble/orchestrator/proc.go:93`) captures exit code/signal +
      time; new statuses `exited`/`crashed` (distinct from operator
      `stopped`); `lastErr` gets the log tail via the existing
      `tailBuffer` mechanism; SSE status event emitted. Guard against
      racing operator-initiated Stop/Restart (don't mark `crashed` when
      the orchestrator killed it).
- [x] 5.2 Docker placement: health poll marks container-gone as
      `crashed`/`exited` equivalently.
- [x] 5.3 `ensemble ready` fails fast on `crashed`; `ensemble status` and
      TUI/dashboard render the new states distinctly.
- [x] 5.4 Log endpoints in `ensemble/server`: `GET
      /api/services/{name}/logs?tail=N` (default 200, cap 5000) and
      `.../logs/stream` (SSE follow) over `.ensemble/run/<name>.log`;
      404 unknown service, empty (not error) when no file.
- [x] 5.5 Dashboard: log pane on the service panel (tail + follow). TUI:
      `l` opens log tail on the selected service.
- [x] 5.6 Tests: crash detection (process killed externally), clean exit
      vs crash vs operator stop, ready-fails-fast, log tail/stream
      endpoints, no-file empty case, restart clears exit state.

## 6. traffic-history (server + UI + CLI)

- [x] 6.1 `GET /api/traffic/history` reading `.ensemble/hops.jsonl`
      newest-first with `before=<seq>`+`limit` and the existing traffic
      filters; corrupt lines skipped + counted. Reuse the NDJSON reader
      (16 MB line cap).
- [x] 6.2 Traffic view "load earlier" paging, merged by `seq` with the
      live stream.
- [x] 6.3 `GET /api/sessions/{id}/export?format=har` across ring +
      history, reusing `core/trace/export.go`; CLI `ensemble traffic
      --session <id> --export har`.
- [x] 6.4 Tests: paging across ring/disk boundary, filters on history,
      empty/no-file, corrupt-line skip count, whole-session HAR matches
      per-trace HAR union.

## 7. capture-robustness (core)

- [x] 7.1 Replace `trace/redact.go:141,273,277` panics with error
      returns; `Recorder.Record` degrades the hop (bodies dropped, `Err`
      set) on redaction failure.
- [x] 7.2 Recorder write pipeline: ordered bounded queue + writer
      goroutine (`core/proxy/recorder.go:79-104`); overflow drops+counts,
      write errors count (`recorder.go:88`); counters in `/api/status`;
      flush-and-close on shutdown so short-lived retrace captures lose
      nothing.
- [x] 7.3 Ring byte budget (default 256 MB, configurable) alongside count
      cap, applied to ensemble (1024) and retrace (8192,
      `retrace/capture/capture.go:212`) rings.
- [x] 7.4 Session fixes: duplicate-id check before edge-listener bind
      (`core/proxy/session.go:234-252`); post-`End` hops counted into
      `DroppedHops` (`session.go:157`), degrading the verdict note when
      non-zero (closes roadmap F.3 — check its box with a pointer here).
- [x] 7.5 Stub hardening: `http.MaxBytesReader` at the body cap → 413
      (`core/stub/stub.go:114`); loopback-only bind enforcement
      (`stub.go:76`); `ReadHeaderTimeout` on stub and proxy servers
      (`stub.go:80`, `core/proxy/proxy.go:307`).
- [x] 7.6 `SetCookies` capture (every `Set-Cookie`, ordered) and replay
      emission as separate headers (`retrace/replay/server.go:447` note
      resolved); joined `headers` form retained.
- [x] 7.7 Per-exchange refusal exclusion: wire rule `exclude: true` (+
      mandatory `why`) drops the exchange at `LoadBundle`
      (`retrace/replay/bundle.go`); unexcluded refusal error names the
      exchange and prints the exact rule command; excluded route misses
      with the standard explained 501.
- [x] 7.8 Tests: no-panic under corrupted encrypt state, request latency
      unaffected by stalled writer (fake slow io.Writer), byte-budget
      eviction, dup-session race, droppedHops in manifest, 413, loopback
      refusal, multi-cookie round-trip, exclusion rule load/serve/miss.

## 8. Docs

- [x] 8.1 Add a `retrace replay --ref` job to `docs/retrace-ci-example.yml`
      (checkout ref bundle → replay serving mocks → app e2e against
      `--listen` → exit-code gate), with the same annotation density as
      the existing jobs.
- [x] 8.2 Write `docs/reference-lifecycle.md`: record → `ref accept` →
      commit `.retrace-ref` → PR review → intentional change → diff exit
      1 → re-accept, including the secret-scan and exclusion-rule flows
      added by this change.
- [x] 8.3 Write `docs/getting-started.md`: bring-your-own-stack walkthrough
      (minimal `ensemble.yaml` → up → dashboard → wire a client through
      the proxy ports → first retrace flow), linking the sample for the
      full-featured version.
- [x] 8.4 Cleanup: delete or move `docs/phase-3-porting-inventory.md` to
      an archive; fix `sample/README.md:15` (rn-app was dropped, not
      pending); README: document the new warning/exit states, log +
      history endpoints, body-redaction defaults, and the protocol
      limitations now that they're loudly enforced.
- [x] 8.5 Sync `openspec/changes/init-ensemble-retrace/tasks.md`: check
      stale boxes 4.1–4.8/6.0/6.2 with "shipped — see git history",
      leaving 4.9, 5.4, 6.1, 6.3, F.1, F.2 (F.3 closed here) accurate.

## 9. CI / packaging hygiene

- [x] 9.1 Add a lint job to `.github/workflows/ci.yml` (golangci-lint for
      Go, the workspace's existing lint script for TS if present); fix
      or explicitly configure-away findings so the job lands green.
- [x] 9.2 Windows npm packaging: stage the already-built windows binaries
      in `scripts/prepare-npm-binary.mjs` and add `win32` to the npm
      packages' `os` field; verify install-path logic handles `.exe`.

## 10. Integration verification (after all groups)

- [x] 10.1 Full-stack pass on `sample/`: `ensemble up` (expect zero wiring
      warnings), record the checkout flow, `ref accept` (expect clean
      secret scan), `retrace replay` of the committed reference with the
      Playwright suite, `retrace diff` — all green; kill a service
      mid-run and verify `crashed` surfaces.
- [x] 10.2 `go test -race ./...` across the workspace + `pnpm -r test`
      green; `goreleaser check` still passes.
- [x] 10.3 Re-run the audit's sharp probes: SSE through a proxied port
      streams live; a WebSocket attempt yields the flagged 501; a PNG
      survives record→replay byte-identical; a post-End hop shows in
      `droppedHops`.
