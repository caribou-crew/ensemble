---
name: retrace-iterate
description: Use when you changed a client (UI, API calls, a flow) and need to verify it against a running stack — records the flow with retrace, diffs it against the reference on pixels/wire/hops, and reads the structured verdict. Use whenever you would otherwise ask a human "does this still look right?".
---

# Verifying a change with retrace

You changed something a user can see or a server can observe. The loop that
closes is:

```
capture → diff --json → read the verdict → fix → recapture
```

It is *closed* because an **accepted** recording becomes the reference the next
run is measured against. Nothing about that loop needs a human in it, provided
you read the verdict properly — which is most of what this skill is about.

This file is portable: it describes the `retrace` CLI, not this repository. If
you copy it into another repo that uses retrace, it still applies.

## Before you start: is the tree clean?

A stale capture holding a port, or a half-written run directory, will make the
next thing you do confusing rather than wrong — which is worse.

```sh
retrace check          # 0 = every run finalized; 1 = abandoned runs found
retrace runs           # the listing, with a reason per abandoned run
```

An **abandoned** run is one whose directory has no `finalized` file and whose
owning process is gone. Its recording is partial. Never diff against it: the
wire plane stops wherever the process died, so it will report differences that
are really just truncation.

If `retrace check` reports a port still held and you do not know by what:

```sh
retrace check --url 127.0.0.1:53221    # bare host:port is fine
```

It answers with the owning pid and run id, or tells you the port is free, or
tells you something that is not retrace holds it. Exit 1 means *a retrace run
holds this address*, so `retrace check --url X && <bind X>` proceeds only when
nothing of ours is there.

## Step 1 — capture

```sh
retrace run                         # every flow in retrace.yaml, in name order
retrace run --flow checkout         # one flow
retrace run --flows checkout,browse # a named subset
```

Prefer the bare form when driving this yourself: a flow added to
`retrace.yaml` is picked up without you changing the command.

Each flow runs its own `flows.<name>.command`. A command after `--` overrides
that and is valid only for a single flow.

`--json` emits one object for `--flow` and an **array** for `--flows` or a bare
run. The shape follows the invocation, never the number of flows that happen to
be configured — so your parser does not break the day someone adds a second
flow.

### Confirm the capture actually finished

```sh
retrace runs --json --state abandoned
```

A run directory existing is not proof a run completed. `finalized` is written
last, after the manifest, and only if the manifest landed.

## Step 2 — diff

```sh
retrace diff --flow checkout --json
```

`--a` and `--b` take selectors: `reference` (the default for `--a`), `latest`,
an exact run id, or a git sha.

## Step 3 — read the verdict, not the exit code

**This is the step that matters.** The exit code is a coarse summary for CI
pipelines. It collapses situations that call for opposite responses from you.

Read these fields off `retrace diff --json`:

<!-- retrace:fields -->
- `verdict` — `pass` | `changed` | `failed` | `quarantined`
- `gates` — human-readable reasons the verdict is `failed`
- `budgets` — one row per plane your `gates:` config names: `plane`,
  `threshold`, `observed`, `failed`
- `unmeasuredGates` — planes that are gated but which this run carried no
  evidence to measure. **A gate that could not be evaluated is not a gate that
  passed.**
- `quarantined` — sides excluded because their own capture-trust verdict was
  not ok
- `capture` — the capture-trust banner for this comparison
- `counts` — per-plane tallies
- `checkpoints`, `wire`, `hops`, `unexpectedStatuses`, `perf`, `conformance` —
  the four planes in detail
<!-- /retrace:fields -->

### What each verdict means for you

| `verdict` | What happened | What you do |
|---|---|---|
| `pass` | Nothing moved beyond tolerance | Done |
| `changed` | Differences within what is allowed to change | Look, decide if intended |
| `failed` | A configured gate broke | Read `gates` — it names which |
| `quarantined` | **Nothing was compared.** One side was not trustworthy | Fix the capture, not the code |

`quarantined` is the one that gets misread. It is not "a small failure" — it
means no comparison happened at all. Treating it as a finding to fix in your
application code sends you editing code that was never measured.

### Which plane moved tells you whose problem it is

There is no single field that answers this yet, so read the planes:

- **pixel only** — a client rendering change. Nothing about the traffic moved.
- **wire moved** — the client is making different calls: different requests,
  different order, different bodies.
- **hop only, wire same** — the client behaved identically and the *stack*
  answered differently. Suspect the backend or a seed, not your change.
- **conformance fails, everything else same** — contract drift against the
  OpenAPI spec.
- **capture not ok** — a harness problem. None of the above is reliable.

Get this backwards and you will spend an hour "fixing" a client that never
changed.

## Step 4 — fix, then recapture

Change the code. Run step 1 again. Do not edit the recording, and do not edit
tolerances to make the number go away.

## Step 5 — accept, deliberately

When the new recording is the correct one:

```sh
retrace ref accept --flow checkout
```

This promotes the run into the committed reference bundle, and every later diff
is measured against it. Accepting is how the baseline moves — so accept because
you looked and the change is right, never because the diff was red and you
wanted it green.

```sh
retrace ref list      # what each flow currently resolves to, and why
retrace ref reject    # the other direction
```

## The NEVERs

These are the ways this tool can be made to lie. Each one produces a green
result that means nothing.

**Never pass `--allow-degraded` to get a green run.** It exists to compare
against a capture that is genuinely known-degraded, as a deliberate act. Used
to clear a finding, it turns off the check that was working.

**Never let `--no-fail` stand in for a fix.** It forces 0 for `changed` or
`failed`. It deliberately does *not* zero a quarantine — 3 means nothing was
compared, which is not a finding to suppress.

**Every tolerance needs a `why`.** Masks, `wire_ignore` entries and rules all
carry a `why` field. Write a real one. An unexplained ignore is
indistinguishable a year later from a bug someone silenced, and the reader
cannot tell which.

**Never widen a threshold to pass.** `gates:` budgets describe what the team
accepts, not what today's run happens to produce. A threshold edited to fit a
result is no longer a gate.

**Never accept a reference to clear red.** See step 5.

**Never diff against an abandoned run.** See the top of this file.

**Never branch on the exit code alone.** `0` no differences, `1` differences to
review, `2` a hard gate failed, `3` could not evaluate — bad flags, unreadable
config, I/O failure, *or a quarantined comparison*. Those last two both being
`3` is exactly why the JSON exists.

## Recording checkpoints from a test

The Playwright adapter (`@caribou-crew/retrace-playwright`) extends
Playwright's `test` with a `retrace` fixture:

```js
import { test } from '@caribou-crew/retrace-playwright';

test('checkout', async ({ page, retrace }) => {
  await retrace.group('browse');
  await page.goto('/');
  await retrace.checkpoint('catalog');
  await retrace.endGroup();

  await retrace.group('pay');
  await page.getByRole('button', { name: 'checkout' }).click();
  await retrace.checkpoint('confirmation', { selector: '.confirmation' });
  await retrace.endGroup();
});
```

- `checkpoint(name, { selector, trim })` — a named screenshot. `selector`
  narrows to an element; `trim` asks for uniform-border cropping at *compare*
  time (the adapter only records the request).
- `group(name)` / `endGroup()` — name a section of the flow, so the wire diff
  reports "during pay" instead of an undifferentiated list.

Outside a run these are no-ops, so the same test still runs standalone. Set
`RETRACE_STRICT=1` in CI to make a missing handshake a loud failure instead —
otherwise a misconfigured job records nothing and reports success.

The runner learns where to write from environment variables `retrace run`
sets: `RETRACE_RUN_DIR`, `RETRACE_PROXY_URL`, `RETRACE_MARKER_URL`, and
`RETRACE_UPSTREAM_URL` when an upstream is configured. Point the app under test
at `RETRACE_PROXY_URL`.

## Config keys you will actually reach for

In `retrace.yaml` at the directory you run from (retrace does not search parent
directories, on purpose):

```yaml
app: web
entry: edge-gw            # the ensemble service to attach to; without it,
                          # only the client edge is recorded
flows:
  checkout:
    command: npm test -- checkout.spec.js
    perf_budget_ms: 4000
gates:
  pixel: { budget_pct: 2 }
fail_on: [wire, hop]      # which planes may turn the verdict to "failed"
wire_rules:
  - headers: { date: http-date }
masks:
  catalog:
    - { x: 0, y: 0, width: 320, height: 48, why: "clock in the status bar" }
```

Also available: `upstream`, `proxy_host`, `proxy_port`, `wire_ignore`,
`query_ignore`, `path_normalize`, `expected_statuses`, `hop_require`,
`thresholds`, `openapi`, `redact`, `deviations`, and `preflight` / per-flow
`setup` / `teardown` hook commands.

## When you are stuck

- `retrace diff --out DIR` writes a static report that opens with `file://`.
- `retrace serve` opens the review queue with the three planes side by side.
- `retrace export --out DIR` produces the CI artifact version of the same.
- `retrace replay --ref FLOW -- <cmd>` runs the test against strict mocks, with
  no stack at all — the fastest way to tell a client problem from a stack one.
