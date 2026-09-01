# The reference-bundle lifecycle

A reference bundle is a recorded run that a person looked at and promoted:
"this is what the flow does, and future runs are judged against it." This
walkthrough follows one flow — `checkout` — through its whole life: record,
accept, commit, change, diff, review, re-accept. Every step here is a plain
CLI command; `retrace serve` and the ensemble dashboard's Retrace tab are
browsable views over the same verbs, never a second workflow.

The commands assume a `retrace.yaml` in the current directory declaring the
flow (see [`sample/retrace.yaml`](../sample/retrace.yaml) for a complete,
heavily annotated one, and [getting-started.md](getting-started.md) for how
to get to your first recording).

## 1. Record

```sh
ensemble up          # one terminal, leave it running (or `retrace run --no-ensemble`)
retrace run --flow checkout
```

`retrace run` executes the flow's test command with a recording proxy
between the app and the stack, and writes the run —
`manifest.json`, `wire.jsonl` (the exchanges), `hops.jsonl`, screenshots —
under `.retrace/runs/<app>/checkout/<run-id>/`. Runs are local working
state; `.retrace/` stays gitignored.

The first run of a flow has nothing to compare against. That is what the
next step fixes.

## 2. Accept

```sh
retrace ref accept --flow checkout
```

`accept` takes a run (`--run SELECTOR` to pick one; the latest otherwise),
distills it into a compact reference bundle, and installs it under
`.retrace-ref/<app>/checkout/`. It is a gate, not a copy — it refuses runs
whose capture verdict is degraded or broken, shots that don't decode, and:

**The secret scan.** Before promoting anything, accept scans every staged
wire exchange — headers, query strings, and bodies, *after* redaction has
already run — for likely credentials: values under secret-list keys
(`access_token`, `password`, `client_secret`, …) that weren't redacted,
JWT-shaped strings, AWS access key ids, and `Bearer`-token headers. Any
finding refuses the accept, names the exact field path, and prints the
`retrace ref rule` command that would redact it:

```
retrace: ref accept: refusing to promote — likely secret in the bundle:
  resp.body.session_key: JWT-shaped value ("eyJhbGciOi…")
  add a rule and re-record:  retrace ref rule --field session_key --matcher redacted
```

This exists because a reference bundle's whole purpose is to be committed
(step 3) — a token that survives into it lands in git history, where
deleting it later doesn't un-leak it. The right fix is always a redact
rule plus a re-record. For the genuine false positive (a JWT-shaped test
fixture, say), `--force` overrides — and records `acceptedWithSecrets:
true` in the reference manifest, so the exception is visible forever
rather than silent. The accept button in `retrace serve` surfaces the same
scan verdict; the two paths cannot disagree.

Note that most secrets never reach the scan at all: default redaction
covers the usual header, query-string, *and* JSON-body keys at capture
time. The scan is the net for what slips past — a secret under a
nonstandard key, a token in an unparsed body.

## 3. Commit

```sh
git add .retrace-ref
git commit -m "retrace: accept checkout reference"
```

`.retrace-ref/` is committed on purpose, in the same spirit as a lockfile:
it is the flow's contract at this commit, it travels with the branch, and
it is what CI replays (`retrace replay --ref checkout`, see
[retrace-ci-example.yml](retrace-ci-example.yml)) and what every future
`retrace diff` compares against. Wire exchanges are text (NDJSON), so the
PR diff of a reference change is itself reviewable — a reviewer sees
exactly which calls, fields, and screenshots the contract now includes.

## 4. Live with it

From here on, every recorded run is a comparison:

```sh
retrace run --flow checkout
retrace diff --flow checkout
```

`diff` compares the latest run against the reference on three planes —
pixel (screenshots), wire (calls, fields, ordering), hop (the chain through
the stack) — and exits `0` (no differences), `1` (differences to review),
`2` (a hard gate failed — a rule violation, a required hop missing, a
budget blown), or `3` (could not evaluate). CI wires the exit code straight
into the build; a human reads the same verdict in `retrace serve`.

## 5. Change something on purpose

Ship an intentional UI or API change and the next diff says so:

```sh
retrace run --flow checkout
retrace diff --flow checkout      # exit 1 — differences to review
```

This is the tool working, not the tool complaining. Open the review queue:

```sh
retrace serve
```

and look at what actually moved — pixel overlays per checkpoint,
field-level wire diffs per call, the hop chain. Three verbs apply, the same
three the CLI has:

- **accept** — the change is the new contract. The current run becomes the
  reference (the secret scan runs again on the way in); commit the updated
  `.retrace-ref/` alongside the code change, so the PR carries both the
  behavior and its contract in one review.
- **reject** — the change is a regression; the reference stands. Fix the
  code and re-run.
- **rule** — the difference is real but is noise (a fresh timestamp, a
  per-run id): add a tolerance with `retrace ref rule --field GLOB
  --matcher NAME --why "..."` so no future run trips on it. Every rule
  carries its `why`, and `retrace diff` prints each rule that actually
  fired, so tolerances never accumulate silently.

Then the cycle repeats from step 3.

## When a bundle refuses to load

Replay and diff fail closed: a reference exchange whose body was truncated
at the capture cap, arrived `Content-Encoding`-compressed, or is a
206/`Content-Range` partial cannot be served faithfully, so loading it is
refused rather than answered wrong. The refusal is per-exchange and the
error tells you the way out, naming the exchange and printing the exact
rule that would exclude it:

```yaml
wire_rules:
  - path: /reports/export
    exclude: true
    why: "50 MB CSV export — over the body cap; not part of the checkout contract"
```

`exclude: true` (the `why` is mandatory, like every other rule) drops that
exchange from the bundle at load: the rest of the reference serves
normally, and a live request matching the excluded route gets the standard
explained 501 miss rather than a corrupted body. A defect with no excluding
rule still refuses the whole load — silence is the one thing this system
never chooses.

## When the reference goes stale

A reference records the backend as it was. If the *backend* changes out
from under the flow (a contract change shipped by another team), replay
misses start telling you about the recording, not the client. `retrace
revalidate --ref checkout --upstream URL` replays the reference's requests
against a live stack and reports which recorded exchanges no longer hold —
the answer to "is it my client or a stale recording" before you re-record
and re-accept.
