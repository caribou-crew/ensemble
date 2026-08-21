# Proposal: closed-loop-round-one

Status: proposed (2026-08-20). Backlog, not scheduled. Sequenced against the
in-flight `init-ensemble-retrace` Phase 4 plan — several items are cheapest if
their config/JSON shape is fixed *before* the task that owns that surface lands.

## What

Fourteen additions to retrace/ensemble, drawn from an audit of the flowlens /
local-card-stack "closed loop" against this repo (code, specs, Phase 4 plan).
Every item is a **generic hook or a gate**, never a topology assumption: the
tool must not know what Metro, simctl, a seed user, or a card stack is. Consumer
repos encode their own traps through the hooks.

Out of scope by the author's ruling: any human-only bless/accept gate or
proposal step. The committed reference bundle + PR review remains the gate.

## Why

The audit found ~60% of the closed loop covered, ~25% absent. The absent part
is the *operational* layer that made the prototype trustworthy in practice:
refusing to capture when preconditions fail, refusing to compare garbage,
letting teams decide what "failing" means, and telling the reader *whose*
problem a red result is. Without these the four planes exist but the loop
can't run unattended, and an agent driving it will iterate on noise.

Round one is chosen by three tests: (1) removes a way the tool can lie,
(2) generic, (3) cheap now / expensive after configs proliferate.

## Ordered items (see tasks.md for the checklist)

| # | Item | Why it's round one | Owns the surface |
|---|------|--------------------|------------------|
| 1 | Configurable failing gates (`gates:` pixel %-budget per screen, `failOn` list, `--no-fail`) | Today any pixel change exits 1 — unusable as a CI gate; shape must exist before Task 10 summary hardens | Task 10 |
| 2 | Quarantine non-ok captures from diff (default on) + explicit `wire.missing/reason` in manifest | Comparing a broken capture is the "plausible lie" the verdict system exists to stop | Tasks 6, 10 |
| 3 | `why` on every tolerance (masks, wireIgnore, wireRules, expectedStatuses, deviations) surfaced in summary | One-line schema change; every config written before it lands is a debt | Task 3 (config) |
| 4 | `preflight:` + per-flow `setup:`/`teardown:` commands; non-zero preflight refuses capture naming the command | The generic slot for seed asserts, env stamps, device discovery — Task 4 is the run command, land now | Task 4 |
| 5 | Multi-flow runs: `--flows a,b`, bare `run` = all configured flows, one process, one run dir per flow; consume `Flow.Command` | `Flow.Command` is parsed and dead; each re-spawn is a full turn for a driving agent | Task 4 |
| 6 | Screen-geometry guard: adapters report geometry (sim/emulator/viewport) into manifest; flow `canonical: {width,height}` + `strict` → refuse, never scale | A geometry mismatch reports 100% on every threshold; cheaper to refuse than to explain | Tasks 1, 7, 17 |
| 7 | Triage classification in summary: planes-moved → `client / contract / stack / harness` label (table configurable) | Turns four numbers into a "whose problem" answer; trivial once summary exists | Task 10 |
| 8 | Fired-ignore report + default header shape rules (date / etag / content-length) | Small; an ignore that fires is a blind spot the reader must be told about | Tasks 2, 8 |
| 9 | Client identity: `client_identity_headers: [x-source-client, …]` read into `hop.client`; shown as entry-hop origin | Small; required to tell two apps apart on one stack | core/trace, Task 5 |
| 10 | Pluggable hop source: `hops.source: ensemble \| {arm, disarm, export} \| file` | The topology-agnostic ask: teams with their own tracing fill the hop plane without ensemble | Task 5 |
| 11 | Cross-repo diff: repeatable `--root`, selectors `app@runId \| sha \| latest` | Prerequisite for any cross-app pairing later; cheap in `runs.FindRun` | Task 1, 10 |
| 12 | Stack fingerprint: ensemble `/api/status` exposes per-service `version` (user `version:` command or git sha of `dir`) + active seed; retrace copies into manifest; diff reports "stack changed" | The only way a diff can blame the backend instead of the client | ensemble/server, Task 4/5 |
| 13 | Run supervision: `finalized` sentinel in run dir, abandoned-run detection on `retrace runs`, `retrace check` (proxy ownership / live run) | Every incident on the page's list that wasn't a verdict was one of these | Task 4 |
| 14 | Agent recipe shipped in-repo: `AGENTS.md` + `.claude/skills/retrace-iterate/` documenting capture → diff → read `--json` (never the exit code) → fix → recapture; versioned with the CLI | The product thesis is an agent driving the loop; the recipe must version with `failOn` semantics | docs |

## Deferred to round two (recorded so they are not lost)

Cross-app `pairs:` (name-based pairing across apps/roots, per-pair deviations);
full OpenAPI body validation + per-service spec map for hops; deep-linkable
review URLs + cross-link to ensemble-ui by session id + trace-window import;
per-flow flake history; `retrace ci` unattended cycle + scheduled `revalidate`
drift-bot; `retrace init` / `ensemble init`; `ensemble doctor` (identity-
stamped listeners); seed `reset` + seed assertion; multi-app config entries;
mosaic shot redaction; MCP server. Fault injection, passthrough, OTLP export,
corpus mining stay in `docs/stretch-ideas.md`.
