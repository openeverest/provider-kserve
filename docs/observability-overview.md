# Observability overview 

## Two metric streams, one Prometheus

Both vLLM and AI Gateway use the **same** Prometheus and Grafana. They differ in
**who emits the PodMonitor** and **what question the metrics answer**.

```
                    ┌─────────────────────────────────────┐
                    │  Grafana :3000                      │
                    │  • vLLM dashboard (engine view)     │
                    │  • Gateway dashboard (front door)   │
                    └──────────────▲──────────────────────┘
                                   │ PromQL queries
                    ┌──────────────┴──────────────────────┐
                    │  Prometheus (scrapes & stores)      │
                    └─▲───────────────────────────────▲──┘
                      │                               │
         PodMonitor   │                               │  PodMonitor
         (1 per       │                               │  (1 per cluster,
          Instance)   │                               │   Helm chart)
                      │                               │
              vLLM pod :8000/metrics          Envoy proxy :aigw-admin/metrics
              "how is the engine?"            "how many tokens passed the gate?"
```

| | **vLLM metrics** | **AI Gateway metrics** |
|---|------------------|------------------------|
| **Question** | Is the model healthy? Queue depth? KV cache? | How many tokens/requests per model at the **entry**? |
| **Emitted by** | Provider Go (one PodMonitor **per Instance**) | Provider Helm chart (one PodMonitor **for the whole gateway**) |
| **Toggle (Instance UI)** | **Yes** — Observability → vLLM Prometheus metrics | **No** — cluster-wide; see chart flags below |
| **Toggle (chart)** | `metrics.podMonitor.enabled` | `aiGateway.enabled` **and** `metrics.podMonitor.enabled` |
| **Dashboard** | `docs/dashboards/vllm.json` | `docs/dashboards/gateway.json` |

You can deploy three models behind **one** shared AI Gateway. You get **three**
vLLM PodMonitors (one per model) and **one** gateway PodMonitor (shared front
door).

---

## Three knobs (do not mix them up)

### 1. Chart: `metrics.podMonitor.enabled` (provider Helm)

Master switch for **emitting PodMonitor CRs** at all.

- `false` → no vLLM PodMonitors, no gateway PodMonitor
- `true` → provider *may* create PodMonitors (subject to other rules)

Default: `true`. Safe on clusters without Prometheus Operator (provider skips
if CRD missing).

### 2. Instance UI: **vLLM Prometheus metrics** (Observability section)

Per-model opt-in/out for the **vLLM** PodMonitor only.

- Disabled → provider **deletes** that Instance's PodMonitor → no engine
  metrics for that model in Grafana
- Does **not** affect gateway metrics

### 3. Chart: `metrics.grafanaDashboards.enabled` (optional dashboards)

Creates labeled **ConfigMaps** in your Grafana namespace for the kube-prometheus
stack dashboard sidecar. **Off by default** — enable when platform Grafana uses
the standard `grafana_dashboard=1` sidecar watcher.

```bash
helm upgrade ... --set metrics.grafanaDashboards.enabled=true \
  --set metrics.grafanaDashboards.namespace=monitoring
```

Does **not** install Prometheus or Grafana. Does **not** affect scraping.

### 4. Dev Tilt: `ENABLE_METRICS` + `ENABLE_AI_GATEWAY`

**Local dev only.** Installs kube-prometheus-stack and sets
`metrics.grafanaDashboards.enabled=true` on the provider chart (same ConfigMap
mechanism as prod).

| `ENABLE_METRICS` | `ENABLE_AI_GATEWAY` | What you get locally |
|------------------|---------------------|----------------------|
| `false` | any | No Prometheus/Grafana from Tilt; PodMonitors still created if chart allows |
| `true` | `false` | Prometheus + Grafana + **vLLM** dashboard ConfigMap |
| `true` | `true` | Prometheus + Grafana + **both** dashboard ConfigMaps |

Open Grafana at http://localhost:3000 (`admin` / `prom-operator`).

In **production**, your platform team runs Prometheus/Grafana; enable
`metrics.grafanaDashboards` when using kube-prometheus-stack, or import JSON
manually — see [observability.md](observability.md).

---

## Why there is no per-instance “AI Gateway metrics” toggle

The Envoy AI Gateway is **shared infrastructure** — one gateway, many models.
Metrics are emitted by **Envoy proxy pods**, not by each Instance. A single
PodMonitor scrapes all gateway traffic; labels like `gen_ai_request_model`
separate models in Grafana.

A per-instance “disable gateway metrics” toggle would lie: disabling it on
Instance A would not stop scraping traffic for Instance B.

**Design choice:** gateway scrape is **cluster-wide** (chart flags). Instance UI
only controls **vLLM engine** scrape. See
[adr/0001-cluster-wide-gateway-metrics.md](adr/0001-cluster-wide-gateway-metrics.md).

---

## End-to-end: local dev with metrics + AI Gateway

```bash
cp dev/.env.example dev/.env
# set ENABLE_METRICS=true and ENABLE_AI_GATEWAY=true
make dev-up
```

1. Tilt installs OpenEverest + provider (with AI Gateway subcharts)
2. Chart creates gateway PodMonitor (if flags on)
3. Tilt installs kube-prometheus-stack
4. Provider chart creates dashboard ConfigMaps (vLLM + gateway when AI GW on)
5. Create an LLM Instance in Everest UI (:8080)
6. Provider creates vLLM PodMonitor for that Instance (unless disabled in UI)
7. Prometheus scrapes both endpoints → Grafana shows both dashboards

---

## Where to go next

- vLLM scrape details, PromQL, gotchas → [observability.md](observability.md)
- Gateway `gen_ai.*` metrics → [observability-gateway.md](observability-gateway.md)
- Domain terms → [CONTEXT.md](../CONTEXT.md)
