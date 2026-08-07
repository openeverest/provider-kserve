# provider-kserve — User Stories

This folder captures the user stories that shape `provider-kserve`. They are
grouped by the four personas the provider serves. Each story is written in the
form **"As a … I want … so that …"** with concrete acceptance criteria and a
mapping to the provider surface (`Instance` fields, topologies, components).

The provider exposes two topologies, and most personas care about both. Both
are implemented end-to-end today (the controller syncs, statuses, and cleans up
both KServe resources):

| Topology    | KServe resource        | Use case |
|-------------|------------------------|----------|
| `llm`       | `LLMInferenceService` (`v1alpha2`) | LLMs served with vLLM (OpenAI-compatible). |
| `predictor` | `InferenceService` (`v1beta1`)     | Predictive/ML models (sklearn, pytorch, triton, huggingface, …). |

> **Note on topology names (open decision).** The current names are `llm` and
> `predictor`. `predictor` is borrowed from KServe, but there it names a
> *component inside* an `InferenceService` (`spec.predictor`, alongside
> `transformer`/`explainer`) — not the whole resource — so using it for the
> topology can be slightly misleading. A clearer scheme is to mirror the CR
> kinds directly: `inference` (`InferenceService`) and `llminference`
> (`LLMInferenceService`). That reads unambiguously for anyone fluent in KServe.
> These docs keep `llm`/`predictor` because that is what the code uses today; if
> we rename, the mapping is `llm → llminference` and `predictor → inference`.

## Personas

| # | Persona | Core need | File |
|---|---------|-----------|------|
| 1 | Developers & product teams | Deploy a model, reach it over a standard API, see metrics. Simplicity first. | [01-developers-product-teams.md](01-developers-product-teams.md) |
| 2 | ML engineers | Point at the right model/adapter, cache and pre-pull artifacts, serve fine-tunes. | [02-ml-engineers.md](02-ml-engineers.md) |
| 3 | MLOps engineers | Scale across nodes, tune parallelism, expose multiple protocols. | [03-mlops-engineers.md](03-mlops-engineers.md) |
| 4 | Security & compliance engineers | Force traffic through an LLM gateway, apply guardrails, rate-limit. | [04-security-compliance.md](04-security-compliance.md) |

## Status legend

Stories are labelled with an implementation status so the docs double as a
backlog:

- **Supported** — exposed today through documented `Instance` fields.
- **Partial** — reachable via an escape hatch (`baseRefs`, inline `config`) but
  not a first-class field.
- **Gap** — a real need the provider does not cover yet; tracked here so it is
  not lost.
