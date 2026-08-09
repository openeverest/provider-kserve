# Envoy AI Gateway metrics

> **Context:** See [observability-overview.md](observability-overview.md) for how
> gateway metrics relate to vLLM metrics and Grafana.

The Envoy AI Gateway (enabled with `aiGateway.enabled`) exposes GenAI metrics
for every request that enters the system: per-model token usage (cost basis),
request duration, TTFT, and TPOT. This is the front-door view; vLLM pod metrics
are the engine view. See [observability.md](observability.md) for the shared
PodMonitor / Prometheus setup.

## How it is scraped

The chart renders one `PodMonitor` when both `aiGateway.enabled` and
`metrics.podMonitor.enabled` are true and the `monitoring.coreos.com/v1` PodMonitor CRD is present (Prometheus Operator installed):

```
charts/provider-kserve/templates/ai-gateway-podmonitor.yaml
```

It targets Envoy proxy pods (`app.kubernetes.io/name=envoy`,
`app.kubernetes.io/component=proxy`) on port `aigw-admin` at `/metrics`, with
`namespaceSelector: {any: true}` because those pods live in Envoy Gateway's
namespace (usually `envoy-gateway-system`), not the release namespace.

This monitor is a chart template, not provider Go. The gateway is a singleton;
`syncLLM` runs per Instance, so emitting the monitor there would create
duplicates.

## Metrics

| Metric | What it tells you |
|---|---|
| `gen_ai_client_token_usage` | Tokens processed; `gen_ai_token_type` is `input` / `output` / `total` |
| `gen_ai_server_request_duration` | Full request duration at the gateway |
| `gen_ai_server_time_to_first_token` | TTFT |
| `gen_ai_server_time_per_output_token` | TPOT / inter-token latency |

Common labels: `gen_ai_request_model`, `gen_ai_operation_name`,
`gen_ai_provider_name`.

```promql
sum by (gen_ai_request_model, gen_ai_token_type) (
  rate(gen_ai_client_token_usage_sum{gen_ai_token_type!="total"}[5m]))

sum by (gen_ai_request_model) (
  increase(gen_ai_client_token_usage_sum{gen_ai_token_type="total"}[1h]))
```

## Gotchas

1. **Prometheus 3 name format.** The gateway emits OTel dotted names. The
   PodMonitor sets `scrapeProtocols: [PrometheusText0.0.4]` so names land as
   underscores (`gen_ai_client_token_usage`). See
   [envoyproxy/ai-gateway#1051](https://github.com/envoyproxy/ai-gateway/issues/1051).
2. **Port name may change.** Confirm against a live proxy pod if scrapes are
   empty:

   ```bash
   kubectl get pod -n envoy-gateway-system -l app.kubernetes.io/component=proxy \
     -o jsonpath='{.items[0].spec.containers[*].ports[*].name}'
   ```

## Dashboard

[`docs/dashboards/gateway.json`](dashboards/gateway.json) — token rate by model
and type, request rate, TTFT, TPOT, request duration, total tokens per model.

**Helm:** with `metrics.grafanaDashboards.enabled=true` and `aiGateway.enabled`,
the chart creates a labeled ConfigMap in `metrics.grafanaDashboards.namespace`
for the Grafana dashboard sidecar. See [observability.md](observability.md).

**Manual:** Grafana → Import → upload `gateway.json`, or use the kubectl
ConfigMap one-liner in [observability.md](observability.md).

## References

- [Envoy AI Gateway metrics](https://aigateway.envoyproxy.io/docs/capabilities/observability/metrics/)
- [Upstream scrape example](https://raw.githubusercontent.com/envoyproxy/ai-gateway/main/examples/monitoring/monitoring.yaml)
