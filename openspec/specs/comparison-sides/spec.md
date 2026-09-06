# Explicit comparison sides

## Requirements

Extend `retrace diff` additively to compare two same-app checkouts or different apps without ambiguous root lookup. Preserve legacy `--root`, `--a`, `--b`, exit codes, accepted-reference resolution, and image/pairing behavior.

- Add `--a-root`, `--b-root` to bind each selector to one repository directory, and `--a-app`, `--b-app` to explicitly select each app. A side without its explicit root uses existing root search behavior. Conflicting app-qualified selectors and explicit app flags are errors.
- Add `--a-commit`, `--b-commit` as optional exact full commit SHA assertions against the selected recording manifest. A mismatch or missing recorded SHA refuses evaluation. Do not change legacy SHA-prefix selection and do not checkout or mutate a repository to satisfy an assertion.
- Add optional comparison provenance to the Summary: per-side resolved repository root, selector, app/run identity and recorded full git identity; plus the effective comparison policy directory and a deterministic SHA-256 fingerprint of its public effective config. Clearly label this as comparison-time configuration, not a claim about the historical capture configuration. Do not serialize config values or credentials into provenance.
- Resolve explicit roots to absolute existing directories; preserve existing cross-root ambiguity refusal where sides are not explicit. Default app for an explicit root comes from that root's retrace config (or directory name), unless the user explicitly names the app. The comparison policy remains the invoking directory's config, recorded clearly in provenance; do not silently combine incompatible policies.
- Document separate capture commands in each side's working directory and a single deterministic comparison invocation. Existing `retrace run` remains the capture executor.

## Verification

Use real temporary recording directories and CLI execution to compare same-app latest/reference in separate roots, different app defaults, explicit commit matches/mismatches/missing evidence, invalid roots, legacy ambiguity and app conflicts. Verify provenance reflects actual selected recordings and configuration changes affect the fingerprint. Verify unrelated config directory relocation does not masquerade as a policy change. Compiling mutations must kill root isolation and exact identity assertions.
