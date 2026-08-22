# Phase 4 — retrace

Phase 4 built **retrace**: recording test runs, diffing them (pixel, wire, hop),
serving recordings as strict CI mocks, and the adapters that plug it into test
runners. 18 tasks, ~187 commits, complete and merged to `main`.

These four documents are the durable record. They were produced in a
**git-ignored** scratch workspace (`.superpowers/sdd/…`, which `git clean -fdx`
would destroy) and are copied here so they survive on `main`. The scratch
workspace also holds ~80 per-task briefs, reports and reviews with the full
mutation transcripts; those are process scaffolding and deliberately not copied.

| File | What it is | Read it when |
| --- | --- | --- |
| [HANDOFF.md](HANDOFF.md) | **Start here.** Roadmap ticks, spec notes, and the items needing a human decision. | You want to know what to do next. |
| [final-review.md](final-review.md) | The whole-phase review: verdict, six findings, and a section of negative results. | You want an outside assessment of the phase. |
| [global-constraints.md](global-constraints.md) | The constraints every task was held to, and the process rules learned by breaking them. | You are starting Phase 4b, or running agents on this repo. |
| [decision-log.md](decision-log.md) | The full ledger: every ruling with its reasoning and its stated cost-if-wrong. Large. | You want to know **why** something is the way it is. |

## The short version

**Verdict: coherent and shippable.** Wire contracts agree across subsystems,
dependency direction is structurally enforced, the Go↔TS mirror has zero drift
(checked mechanically), and the Zero-Value Constraint holds at every seam probed
but two — both since fixed.

**23 follow-ups are parked** (F.1–F.23 in the decision log, summarised in the
hand-off). Each is a decision with reasoning, not a loose end. F.23 is the one
to look at first, and its obvious fix is the wrong one — the hand-off explains
why.

## The lesson worth carrying to Phase 4b

The phase began with a rule: *a guard that cannot be shown to fail is not a
guard.* That turned out to be **half** of it.

Seven times, across two languages and four subsystems, **the mechanism that
existed to prevent a failure was itself the failure**:

1. a socket guard that threw where nothing could observe it;
2. its own fix's `beforeEach` reset, which wiped the evidence the assertion read;
3. a probe suite that asserted a child process failed but never checked *why*;
4. a docker skip-guard with no timeout, which hangs the package it exists to let pass;
5. a configured gate that could not be evaluated and reported `pass` with exit 0;
6. a zero timestamp that became a quiet interval and disabled gap detection for a whole run;
7. a `t.Skip` in a parent's range expression that silently removed an assertion its own comment called "not optional".

Three of those were in a single task, each introduced by the fix for the one
before it.

The half that actually kept catching things is **shown to fail for the RIGHT
REASON**. Ask the two questions separately, because they are different: *can it
fail?* and *can it fail for the wrong reason and be read as a pass?* A plain
reading of the code answers neither — only a fixture does.
