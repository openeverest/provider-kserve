# Observability

> **New here?** Read [observability-overview.md](observability-overview.md) first
> for the mental model (provider vs Prometheus vs Grafana, vLLM vs gateway).

This provider makes its LLM model workloads **discoverable** by a Prometheus
that already exists in the cluster. It ships **no** monitoring stack (no
Prometheus, no Grafana) and adds **no** metric-aggregation code — vLLM already
exposes Prometheus metrics on a single endpoint, so there is nothing to
aggregate.

> **Not qpext.** KServe's [`qpext`](https://github.com/kserve/kserve/blob/master/qpext/README.md)
> extends the Knative **queue-proxy** sidecar to merge two containers' metrics
> onto one port. It only applies to Serverless/Knative mode. This provider runs
> everything in **RawDeployment** mode — one container per model pod, one
> `/metrics` endpoint — so `qpext` is irrelevant here.

## How it works

When `metrics.podMonitor.enabled` is on (the default), the provider creates one
`PodMonitor` (`monitoring.coreos.com/v1`) per `llm` Instance, owned by that
Instance. The Prometheus Operator turns that `PodMonitor` into a scrape job; the
existing Prometheus then scrapes the vLLM `:8000/metrics` endpoint directly.

```mermaid
flowchart TB
    subgraph model_ns["Instance namespace"]
      I["Instance CR"]
      L["LLMInferenceService (KServe)"]
      POD["vLLM model pod<br/>1 container :8000/metrics"]
      PM["PodMonitor CR"]
    end
    subgraph mon_ns["monitoring namespace (pre-existing)"]
      PO["Prometheus Operator"]
      PROM["Prometheus"]
      GRAF["Grafana"]
    end
    I -->|watch| P["provider-kserve"]
    P -->|create| L
    P -->|create when enabled| PM
    L -->|reconcile| POD
    PM -.discovered by.-> PO
    PO -->|writes scrape config| PROM
    PROM -->|GET :8000/metrics every 30s| POD
    GRAF -->|PromQL| PROM
```

The provider's part is just the `PodMonitor`. Everything else (Operator,
Prometheus, Grafana) is platform infrastructure the provider does not own.

## Enabling / disabling

Metrics are **on by default** and safe to leave on: if the
`monitoring.coreos.com` CRDs are not installed, the provider **skips** emitting
the `PodMonitor` instead of erroring the reconcile. Install the Prometheus
Operator later and the next reconcile emits it automatically.

```yaml
# values.yaml
metrics:
  podMonitor:
    enabled: true     # default; set false to never emit a PodMonitor
    interval: 30s     # scrape interval
```

```bash
# turn it off for a release
helm upgrade provider-kserve ... --set metrics.podMonitor.enabled=false
```

## The one gotcha that actually bites: selector scoping

A `PodMonitor` is only honored if the Prometheus resource's
`podMonitorSelector` / `podMonitorNamespaceSelector` matches it. Many clusters
set these to "match everything"; some filter by label or namespace. If yours
filters, the `PodMonitor` is created but **silently ignored**.

**First thing to check when "no vLLM metrics show up":**

```bash
# 1. Is the PodMonitor there?
kubectl get podmonitor -n <instance-namespace>

# 2. Does the pod actually serve metrics?
kubectl port-forward -n <instance-namespace> pod/<vllm-pod> 8000:8000
curl -s localhost:8000/metrics | grep '^vllm:' | head

# 3. Does Prometheus select PodMonitors in this namespace?
kubectl get prometheus -A \
  -o jsonpath='{range .items[*]}{.metadata.name}{": podMonitorSelector="}{.spec.podMonitorSelector}{" nsSelector="}{.spec.podMonitorNamespaceSelector}{"\n"}{end}'
```

If step 3 shows a selector that excludes your namespace/labels, that is a
Prometheus configuration change (outside this provider): widen the selector or
label the namespace/`PodMonitor` to match.

Other gotchas:

- **CRD absent** → handled: the provider skips, logs at `-v1`, does not error.
- **Namespace rule** → a `PodMonitor` (default `namespaceSelector`) matches only
  pods in **its own** namespace; the provider places it alongside the workload,
  which is correct.
- **Port** → hardcoded to `8000` (the vLLM OpenAI server port, which also serves
  `/metrics`). A different runtime port is a parameter to `buildPodMonitor`.
- **Orphans on disable** → flipping the flag off does not delete existing
  `PodMonitor`s; they are harmless and garbage-collected when the Instance is
  deleted.
- **Drift** → the `PodMonitor` is not watched (owning that watch would fail the
  manager on CRD-less clusters), so a manual edit/delete is corrected on the
  next periodic resync, not instantly.

## PodMonitor vs ServiceMonitor

Both are Prometheus Operator CRs that generate scrape config; they differ in
what they target.

| | PodMonitor (used here) | ServiceMonitor |
|---|---|---|
| Targets | Pods, by pod labels | Services → the backing Endpoints |
| Needs a Service? | No | Yes |
| Port by | container port number/name | Service port **name** |
| Best when | you own the pods but not a stable Service | you have a Service with a well-named metrics port |

**Why PodMonitor here:** the KServe workload `Service` is KServe-owned and its
port name is not guaranteed stable across versions, and it is `ClusterIP`-only
(the provider's external Service exists only for `LoadBalancer`/`NodePort`). A
`PodMonitor` selects the workload pods directly by labels the provider already
stamps, targeting port `8000` by **number** — so nothing depends on Service or
container-port naming.

## What metrics a vLLM pod gives you

vLLM exposes Prometheus metrics at `:8000/metrics`, all prefixed `vllm:`. These
are LLM-specific signals you cannot get from generic pod metrics.

| Metric | Type | What it tells you |
|---|---|---|
| `vllm:num_requests_running` | gauge | Requests actively decoding right now |
| `vllm:num_requests_waiting` | gauge | Queued requests (backlog) — scale-up signal |
| `vllm:gpu_cache_usage_perc` | gauge | KV-cache utilization (0–1); ~1.0 = memory-bound |
| `vllm:cpu_cache_usage_perc` | gauge | CPU KV-cache utilization (CPU serving) |
| `vllm:prefix_cache_hits_total` / `_queries_total` | counter | Prefix-cache effectiveness (matters for gateway/EPP routing) |
| `vllm:prompt_tokens_total` | counter | Input tokens processed — throughput & cost |
| `vllm:generation_tokens_total` | counter | Output tokens generated — throughput & cost |
| `vllm:time_to_first_token_seconds` | histogram | **TTFT** — perceived responsiveness |
| `vllm:time_per_output_token_seconds` | histogram | **TPOT / inter-token latency** — streaming smoothness |
| `vllm:e2e_request_latency_seconds` | histogram | Full request latency |
| `vllm:request_queue_time_seconds` | histogram | Time spent waiting before running |
| `vllm:request_prefill_time_seconds` / `_decode_time_seconds` | histogram | Prefill vs decode split (llm-d disaggregated path) |
| `vllm:request_success_total` | counter | Completions, labeled by `finished_reason` (stop/length) |
| `vllm:num_preemptions_total` | counter | Requests preempted under memory pressure (bad sign) |
| `vllm:iteration_tokens_total` | histogram | Tokens processed per engine step (batch efficiency) |
| `process_*`, `python_*` | — | Standard `prometheus_client` process metrics |

> Metric names track upstream vLLM and can shift slightly between versions.
> Confirm against a live pod with `curl localhost:8000/metrics | grep '^vllm:'`.

### Handy PromQL

```promql
# Output throughput (tokens/s)
sum(rate(vllm:generation_tokens_total[1m]))

# p95 time-to-first-token
histogram_quantile(0.95, sum(rate(vllm:time_to_first_token_seconds_bucket[5m])) by (le))

# Queue pressure
vllm:num_requests_waiting

# KV-cache saturation
vllm:gpu_cache_usage_perc
```

## Grafana dashboard

A ready-to-import dashboard lives at
[`docs/dashboards/vllm.json`](dashboards/vllm.json). Panels: token throughput,
running vs waiting requests, TTFT, TPOT, KV-cache usage, end-to-end latency, and
success/preemptions. It has `Namespace` and `Model` template variables (built
from the `namespace` and `model_name` labels).

**Import:** Grafana → Dashboards → New → Import → upload `vllm.json` → pick your
Prometheus data source.

**Provision via Helm (recommended for prod with kube-prometheus-stack):** enable
labeled dashboard ConfigMaps on the provider chart. The Grafana dashboard
sidecar (`grafana-sc-dashboard`) watches ConfigMaps with label
`grafana_dashboard=1` and auto-imports them:

```bash
helm upgrade --install provider-kserve ./charts/provider-kserve \
  --set metrics.grafanaDashboards.enabled=true \
  --set metrics.grafanaDashboards.namespace=monitoring
```

With AI Gateway, also set `aiGateway.enabled=true` (gateway dashboard is included
when `metrics.grafanaDashboards.gateway` is true, the default). Adjust
`metrics.grafanaDashboards.namespace` and label fields if your platform uses
different conventions.

**Provision manually** (same sidecar mechanism, without Helm):

```bash
kubectl create configmap vllm-dashboard \
  --from-file=vllm.json=docs/dashboards/vllm.json \
  -n monitoring \
  --dry-run=client -o yaml \
  | kubectl label -f - --local -o yaml grafana_dashboard=1 \
  | kubectl apply -f -
```

## Gateway (token / cost) metrics

When `aiGateway.enabled` is on, the chart also emits a singleton PodMonitor for
the Envoy AI Gateway's `gen_ai.*` metrics (per-model token/cost, TTFT, TPOT).
Details, PromQL, and the Grafana dashboard are in
[observability-gateway.md](observability-gateway.md).

## Whole architecture (data path)

```mermaid
flowchart LR
    U["client"] -->|/v1/chat/completions| POD["vLLM pod :8000"]
    POD -->|response| U
    POD -->|/metrics| PROM["Prometheus"]
    PROM --> GRAF["Grafana dashboard"]
    subgraph provider["control plane"]
      P["provider-kserve"] -->|PodMonitor| PROM
    end
```

The serving path (left) and the metrics path (right) are independent: metrics
scraping never sits in the request path, so it cannot add serving latency.
