# Comparing two checkouts or repositories

The [brew comparison walkthrough](../sample/docs/comparisons.md) includes
checkout preparation, shared recording keys, stable capture settings, a
structured evidence check, and the optional Go/Java backend comparison.

Capture the same flow in each repository using its own test command. For example:

```sh
(cd /work/old-app && retrace run --flow checkout -- pnpm exec playwright test checkout.spec.ts)
(cd /work/new-app && retrace run --flow checkout -- pnpm exec playwright test checkout.spec.ts)
```

Both tests must route their traffic through the retrace proxy and use the same named screenshot checkpoints. Each repository's `retrace.yaml` still supplies its capture configuration. Maestro can use the same workflow with its own command and adapter.

Keep browser origins stable when they are meant to be equivalent. For sequential local captures, the same `proxy_host` and `proxy_port` in both configurations avoids incidental `Origin`/`Referer` changes from ephemeral ports. If different origins are intentional, review that difference and configure a narrowly scoped, explained tolerance; comparison does not silently ignore it.

From the directory containing the comparison policy you want to apply:

```sh
retrace diff --flow checkout \
  --a-root /work/old-app --a latest \
  --b-root /work/new-app --b latest \
  --json
```

Explicit roots bind each selector to its own directory, including when both checkouts use the same app name. An explicit root uses that root's configured app by default, falling back to its directory name. Override app selection with `--a-app` or `--b-app` when needed. The existing `app@selector` syntax still works; an app-qualified selector must agree with an explicit side app.

An accepted baseline can be selected with `--a reference`. To assert the exact commits recorded on each side, add:

```sh
retrace diff --flow checkout \
  --a-root /work/old-app --a latest --a-commit "$OLD_FULL_SHA" \
  --b-root /work/new-app --b latest --b-commit "$NEW_FULL_SHA" \
  --json
```

These flags assert a full SHA against the selected recording's manifest; they do not select a Git branch, check out code, or run tests. Missing or mismatching commit evidence refuses evaluation. An assertion of commit identity does not assert a clean working tree: inspect the recorded dirty flag too.

The JSON summary includes comparison provenance describing the actual selected roots/recordings and the invoking directory's effective comparison policy fingerprint. The policy fingerprint describes the configuration used **now** to compare; it is not historical capture provenance. Config values and credentials are not copied into that provenance. The two capture configurations are not silently combined into one comparison policy.

Without explicit side roots, repeated `--root` retains its existing search semantics and refuses ambiguous selectors. Existing SHA-prefix selectors also retain their behavior; use the new full-commit assertion when exact recorded commit identity matters.

Read the structured diff's verdict, capture quality, budgets, suppressions and unmeasured gates. Exit codes remain 0 for no differences, 1 for reviewable differences, 2 for hard gate failures and 3 for inability to evaluate. `--no-fail` does not turn an unevaluated comparison into success.
