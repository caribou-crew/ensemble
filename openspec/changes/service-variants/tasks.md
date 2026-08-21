# Tasks: service-variants

## 1. Config

- [x] 1.1 `Variant` type; `Service.Variants map[string]Variant`,
      `Service.Default`; `Service.VariantNames()`, `Service.DefaultVariant()`;
      `Config.ResolveService(name, variant) (Service, error)` overlaying
      backing fields. Tests: overlay, single-variant default, unknown.
- [x] 1.2 Validation rules (D2) with tests per rejection + clean config.

## 2. Orchestrator

- [x] 2.1 `variant map[string]string`, `Opts.Variants`, `currentVariant(name)`;
      `Up`/`Restart`/`Flip` resolve through it; per-variant build stamps.
- [x] 2.2 `stopCurrent(name)` extracted from Flip; `SetVariant` per D3.
      Tests: up starts default; switch kills + starts other (stamp per
      variant); switch while stopped records only; restart keeps variant;
      unknown variant errors without stopping.

## 3. Server + CLI

- [x] 3.1 `POST /api/services/{name}/variant`; `ServiceState.Variant`;
      `TopologyNode.Variant/Variants`. Tests: 200 path, 404, 400.
- [x] 3.2 `ensemble variant <svc> <v>`, `up --variant`, status VARIANT column,
      usage text; client method.

## 4. Dashboard + docs

- [x] 4.1 Variant `<select>` in the service panel (only when `variants`),
      `api.setVariant`, types; vitest/tsc green.
- [x] 4.2 README "Variants" section; full test sweep.
