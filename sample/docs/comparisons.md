# Compare the sample across commits or repositories

Use retrace 0.0.34 or newer. Run the same flow twice with the same browser,
viewport, fixture state, and public URLs. Each capture remains in its own
root; the comparison records those roots and asserts the full recorded
commit IDs. No command below checks out code on your behalf except the two
explicit Git worktree commands.

## Prepare two checkouts

From the repository root, choose the revisions to compare. These released
tags are an example; replace them with your own refs:

```sh
A_REF=v0.0.33
B_REF=v0.0.34
A_COMMIT=$(git rev-parse "${A_REF}^{commit}")
B_COMMIT=$(git rev-parse "${B_REF}^{commit}")
git worktree add --detach ../brew-before "$A_COMMIT"
git worktree add --detach ../brew-after "$B_COMMIT"
A_ROOT=$(cd ../brew-before/sample && pwd)
B_ROOT=$(cd ../brew-after/sample && pwd)
```

For two independent repositories, set `A_ROOT` and `B_ROOT` to the
respective directories containing `retrace.yaml`, and obtain each full SHA
with `git -C "$A_ROOT" rev-parse HEAD` and its B equivalent. Both configs
can use the same app name: explicit side roots remove that ambiguity.
Use `--a-app` / `--b-app` if their intended recorded app names differ.

Install dependencies and build the adapter in each checkout:

```sh
(cd "$A_ROOT/.." && pnpm install --frozen-lockfile && pnpm --filter '@caribou-crew/retrace-playwright...' run build)
(cd "$B_ROOT/.." && pnpm install --frozen-lockfile && pnpm --filter '@caribou-crew/retrace-playwright...' run build)
(cd "$A_ROOT" && pnpm -C clients/web-app exec playwright install chromium)
(cd "$B_ROOT" && pnpm -C clients/web-app exec playwright install chromium)
```

The sample includes an encrypted-field rule, so both captures and the
comparison need the same team key. If you already supply
`RETRACE_RECORDING_KEY`, keep that environment variable set for every
command. Otherwise, on these **fresh** roots, initialize it once and copy
the raw key without displaying it:

```sh
(cd "$A_ROOT" && retrace rekey --init)
mkdir -p "$B_ROOT/.retrace"
test ! -e "$B_ROOT/.retrace/recording.key" && \
  install -m 600 "$A_ROOT/.retrace/recording.key" "$B_ROOT/.retrace/recording.key"
```

Do not replace a key belonging to existing recordings. The key files and
recorded runs are ignored by Git; keep the key in your team's secret store
if you need to compare encrypted fields in CI.

## Capture A, then B

For a **frontend migration against one fixed backend**, start ensemble once
in a separate terminal and leave it running. Both clients will exercise
that same stack. For a **backend or whole-stack comparison**, start ensemble
from A before capture A, shut it down and wait for that process to exit,
then start ensemble from B before capture B. The sample uses fixed service
and database ports, so the two stacks must run sequentially. Select matching
seeds and order variants unless that is the change under comparison.

Record each side from its own root, after `ensemble ready` succeeds for the
appropriate stack:

```sh
(cd "$A_ROOT" && mkdir -p .retrace/demo && ensemble ready --timeout 60s && \
  retrace run --flow checkout --proxy-host 127.0.0.1 --proxy-port 4850 --json \
  > .retrace/demo/capture.json)

# If comparing backends: shut down A, start B in the other terminal, and
# wait for its startup before this command.
(cd "$B_ROOT" && mkdir -p .retrace/demo && ensemble ready --timeout 60s && \
  retrace run --flow checkout --proxy-host 127.0.0.1 --proxy-port 4850 --json \
  > .retrace/demo/capture.json)
```

The fixed recording port and the browser's stable `127.0.0.1:5174` origin
avoid incidental URL/header changes between sequential captures. Current
sample tests pin Playwright's viewport to 1280×720; `BREW_TEST_PORT` can
change the browser port when set identically on both sides. Keep the
browser/OS/fonts consistent too. A Maestro comparison uses the explicit
`checkout-maestro` command from the [sample README](../README.md), in both
roots, with the same Maestro version. Both checkouts must include the
Maestro wrapper and checkpoint hooks; the older example tags above predate
that sample integration. Compare each runner's recordings to
its own baseline: their screenshot geometry and checkpoint coverage differ.

Read each `capture.json`: both test exit codes must be zero, capture status
must be `ok`, and screenshot/wire/hop evidence must be present. Full SHA
assertions below verify the recording's Git identity, not a clean working
tree or the identity of an independently running backend. Inspect
`git.dirty` and `stack.services` as well; a shared backend intentionally
keeps its own version evidence while the client commits differ.

## Compare and read the verdict

Choose the directory whose **comparison policy** should apply. This example
uses B's config and key; capture configurations are not merged:

```sh
(cd "$B_ROOT" && retrace diff --flow checkout \
  --a-root "$A_ROOT" --a latest --a-commit "$A_COMMIT" \
  --b-root "$B_ROOT" --b latest --b-commit "$B_COMMIT" \
  --json > .retrace/demo/comparison.json)
```

A wrong full SHA refuses evaluation with exit 3. Exit 1 can mean harmless
concurrent call reordering, so inspect the JSON rather than branching on
that number alone. For deterministic sample captures, this optional check
requires actual evidence on all three planes, zero differing measured
pixels, and no changed/missing/extra calls or hop failures. It allows
reordering and stack/version/seed metadata changes:

```sh
jq -e -s '
  length == 1 and (.[0] |
    .schema == "retrace-diff/1" and
    (.verdict == "pass" or .verdict == "changed") and
    .capture.a.status == "ok" and .capture.b.status == "ok" and
    .gates == [] and .unmeasuredGates == [] and .quarantined == [] and
    ([.a.manifest, .b.manifest] | all(
      .wire.recorded == true and .wire.calls > 0 and
      .hops.recorded == true and .hops.calls > 0 and
      (.checkpoints | length) > 0)) and
    (.checkpoints | length) > 0 and
    (.checkpoints | all(.verdict == "ok" and .numDiff == 0)) and
    (.budgets | all(.failed == false)) and
    ([.counts.pixelChanged, .counts.wireChanged, .counts.wireMissing,
      .counts.wireExtra, .counts.violations, .counts.hopNew, .counts.hopGone,
      .counts.unexpectedStatuses, .counts.conformance] | all(. == 0))
  )
' "$B_ROOT/.retrace/demo/comparison.json"
```

An additional browser `OPTIONS` preflight, observed in a slower Maestro
run, makes this strict check fail too. Inspect the differing calls before
deciding what changed; a matching screenshot alone does not explain them.

This check still respects configured redaction, masks, and wire tolerances.
Review `suppressions`, `provenance`, `stack`, and the per-call/checkpoint
results before accepting equivalence under that policy. It does not prove
that ignored values or unexercised paths are equivalent. The sample's
normal `fail_on` list gates wire and hops; the explicit check above also
requires zero measured pixel differences.

## Try the existing Go and Java order implementations

The sample already has two implementations behind the logical `order`
service. With a JDK 17 available to the process that starts ensemble (set
`JAVA_HOME` if its launcher cannot find Java), record the same client flow
against each. From `sample/`, with the stack up:

```sh
mkdir -p .retrace/demo
ensemble variant order stub
retrace run --flow checkout --app brew-variants --proxy-host 127.0.0.1 --proxy-port 4850 --json > .retrace/demo/go.json
ensemble variant order real
retrace run --flow checkout --app brew-variants --proxy-host 127.0.0.1 --proxy-port 4850 --json > .retrace/demo/java.json
ensemble variant order stub

retrace diff --flow checkout --app brew-variants \
  --a "$(jq -r .runId .retrace/demo/go.json)" \
  --b "$(jq -r .runId .retrace/demo/java.json)" \
  --json > .retrace/demo/variants.json
```

A failed build, test, or capture needs fixing before comparison; restore
`order stub` if the optional Java run stops early. Read the actual diff;
the two implementations sharing an API shape is a hypothesis to check,
not grounds for automatically accepting a reference or adding tolerances.

The Java sample explicitly uses HTTP/1.1 for its downstream REST calls.
Java's default HTTP client tries an h2c upgrade, which the recording proxy
refuses as an unsupported protocol. With HTTP/1.1, this walkthrough's
checkout and unknown-user flows produced complete captures for both
variants: four matching screenshots, 32 paired calls without content
changes under the existing tolerances, and no hop violations. Call ordering
and stack/seed metadata can still differ.
