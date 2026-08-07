# Persona 1 — Developers & Product Teams

**Who:** application developers and product teams who need a model behind their
feature. They are not ML or infra specialists.

**What they value:** deploy in minutes, call the model with a standard client,
and see that it is healthy. Simplicity is the product.

**Guiding principle:** the happy path is a handful of fields — pick a model,
pick a size, get an endpoint.

---

## 1.1 Deploy an LLM with a standard OpenAI-compatible endpoint

**Status:** Supported (`llm`)

As a developer, I want to deploy an LLM by naming a model and get an
OpenAI-compatible endpoint, so that I can use the OpenAI SDK I already know
without learning KServe.

**Acceptance criteria**
- I create an `Instance` with `topology.type: llm` and
  `components.llmEngine.parameters.modelURI`.
- The served API answers OpenAI routes (`/v1/models`, `/v1/chat/completions`) on
  port 8000.
- The request `model` field defaults to the Instance name, or I can override it
  with `modelName`.

**Provider mapping**
```yaml
topology: { type: llm }
components:
  llmEngine:
    parameters:
      modelURI: hf://Qwen/Qwen2.5-0.5B-Instruct
      modelName: qwen2.5-0.5b   # optional
```

---

## 1.2 Deploy a predictive/ML model without choosing a runtime

**Status:** Supported (`predictor`)

As a product developer, I want to deploy a classic ML model by only stating its
format and location, so that I do not have to know which serving runtime to
pick.

**Acceptance criteria**
- I set `modelFormat` and `storageURI`; KServe auto-selects a matching
  `ServingRuntime`.
- The endpoint answers the standard KServe V1/V2 inference protocol.

**Provider mapping**
```yaml
topology: { type: predictor }
components:
  predictor:
    parameters:
      modelFormat: sklearn
      storageURI: gs://kfserving-examples/models/sklearn/1.0/model
```

---

## 1.3 Get an addressable endpoint without knowing Kubernetes networking

**Status:** Supported (both)

As a developer, I want a documented way to reach my model — in-cluster or
externally — without hand-writing Services, so that I can integrate it into my
app quickly.

**Acceptance criteria**
- `ClusterIP` (default) is reachable from other pods and via `port-forward`.
- Setting `serviceType: LoadBalancer` or `NodePort` makes the provider create
  the fronting Service and publish the address in the Instance connection
  details.

**Provider mapping**
```yaml
components:
  llmEngine:
    service:
      serviceType: LoadBalancer   # or ClusterIP | NodePort
```

---

## 1.4 Pick a model from a curated catalog

**Status:** Supported (`llm`)

As a developer, I want to choose from a short list of vetted models in the UI,
so that I do not have to hunt for a correct model URI.

**Acceptance criteria**
- The **Model** dropdown is populated from the provider's model catalog.
- Gated models are visibly marked and I am told a token is required.

**Provider mapping** — catalog entries live in the chart's `.Values.models` and
render into `spec.uiSchema.llm.modelCatalog`; no field on the Instance.

---

## 1.5 See health and basic metrics for my deployment

**Status:** Partial — vLLM/KServe expose Prometheus metrics; the provider does
not yet ship a `ServiceMonitor` or dashboards.

As a developer, I want request rate, latency, and error/health signals for my
model out of the box, so that I know it is serving and can debug slow calls.

**Acceptance criteria**
- The workload exposes a Prometheus `/metrics` endpoint (vLLM: throughput,
  latency, queue; predictor: request count/latency).
- A scrape target (`ServiceMonitor`/`PodMonitor`) is created when metrics are
  enabled, so Prometheus discovers the workload without manual wiring.
- Readiness/liveness is reflected in the Instance status.

**Gap to close:** an opt-in `metrics.enabled` on the component that emits a
`ServiceMonitor` and, ideally, a starter Grafana dashboard per topology.

---

## 1.6 Serve a small model on CPU for dev/test

**Status:** Supported (`llm`)

As a developer without GPU quota, I want to run a small model on CPU, so that I
can build and test against a real endpoint cheaply.

**Acceptance criteria**
- Setting `computeProfile: cpu` runs the model on CPU-only nodes.
- I do not have to hand-tune vLLM memory flags for a small model to start.

**Provider mapping**
```yaml
components:
  llmEngine:
    parameters:
      computeProfile: cpu
```
