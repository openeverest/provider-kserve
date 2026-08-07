# Persona 3 — MLOps Engineers

**Who:** MLOps engineers responsible for running inference at scale and
integrating it with the rest of the platform.

**What they value:** horizontal scale, control over model parallelism, and
multiple ways to feed traffic in — HTTP, gRPC, and event streams — all funneled
through the standard inference protocol.

**Guiding principle:** capacity and connectivity are explicit and tunable.

---

## 3.1 Scale replicas horizontally

**Status:** Supported (both)

As an MLOps engineer, I want to set replica counts and autoscaling bounds, so
that I can size capacity to load.

**Acceptance criteria**
- `replicas` sets a fixed decode/predictor replica count.
- Predictor `minReplicas`/`maxReplicas` bound autoscaling; `minReplicas: 0`
  enables scale-to-zero.

**Provider mapping**
```yaml
# llm
components: { llmEngine: { replicas: 4 } }
# predictor
components: { predictor: { parameters: { minReplicas: 1, maxReplicas: 8 } } }
```

**Gap to close:** an explicit autoscaling target (e.g. concurrency/QPS or GPU
metric) for the `llm` topology, rather than fixed decode replicas only.

---

## 3.2 Tune tensor and pipeline parallelism

**Status:** Supported (`llm`)

As an MLOps engineer, I want to split a model across GPUs and nodes, so that I
can serve models that do not fit on a single device.

**Acceptance criteria**
- `tensorParallelSize` shards the model across GPUs on a node.
- `pipelineParallelSize` splits stages across nodes.
- Requested GPU resources match the parallelism degree.

**Provider mapping**
```yaml
components:
  llmEngine:
    parameters:
      tensorParallelSize: 4
      pipelineParallelSize: 2
    resources:
      requests: { nvidia.com/gpu: "4" }
      limits:   { nvidia.com/gpu: "4" }
```

---

## 3.3 Run multi-node / data-parallel serving

**Status:** Partial — reachable via `baseRefs` presets (e.g. decode-worker
data-parallel configs); not a first-class field.

As an MLOps engineer, I want multi-node and data-parallel deployments, so that I
can scale a single logical model across many workers.

**Acceptance criteria**
- Data-parallel worker layout is expressible without hand-writing the full
  `LLMInferenceService`.
- Worker count and per-worker resources are configurable.

**Provider mapping (interim)**
```yaml
components:
  llmEngine:
    parameters:
      baseRefs: [kserve-config-llm-decode-worker-data-parallel]
```

**Gap to close:** structured `dataParallelSize` / worker-group fields on
`llmEngine`.

---

## 3.4 Disaggregated prefill/decode for throughput

**Status:** Supported (`llm`)

As an MLOps engineer, I want to separate prefill and decode workloads, so that I
can scale them independently for better GPU utilization (llm-d pattern).

**Acceptance criteria**
- `enablePrefill` creates a distinct prefill deployment.
- `prefillReplicas` scales it independently of decode replicas.

**Provider mapping**
```yaml
topology:
  type: llm
  parameters:
    enablePrefill: true
    prefillReplicas: 2
```

---

## 3.5 Prefix-cache-aware routing across replicas

**Status:** Supported (`llm`)

As an MLOps engineer, I want smart routing across replicas, so that requests
that share a prefix hit a warm KV cache and latency drops.

**Acceptance criteria**
- `enableGatewayRouting` provisions the Gateway API route and the Inference
  Gateway scheduler (Endpoint Picker).
- Routing is prefix-cache aware across decode replicas.

**Provider mapping**
```yaml
topology: { type: llm, parameters: { enableGatewayRouting: true } }
```
> Trade-off: this path manages its own workload ServiceAccount, so HF-token
> injection for gated models does not apply in this combination.

---

## 3.6 Expose the service over multiple protocols (HTTP, gRPC)

**Status:** Partial — HTTP is default; gRPC depends on the runtime and is not a
provider field.

As an MLOps engineer, I want to reach models over HTTP and gRPC, so that I can
integrate high-throughput and streaming clients.

**Acceptance criteria**
- HTTP (OpenAI for llm; V1/V2 for predictor) works by default.
- gRPC (KServe V2 / vLLM) is selectable where the runtime supports it, with the
  right port exposed on the Service.

**Gap to close:** a `protocol`/port selector on the component that opens the gRPC
port and reflects it in the connection details.

---

## 3.7 Consume from event streams (Kafka) through the inference protocol

**Status:** Gap — not exposed by the provider.

As an MLOps engineer, I want models to consume from a Kafka topic and emit
results, so that async/batch pipelines feed inference without a bespoke bridge,
still speaking the standard inference protocol.

**Acceptance criteria**
- A source (e.g. Kafka topic) can be bound to an Instance so messages are
  delivered as inference requests.
- Results are published to a sink; failures are observable.

**Gap to close:** an eventing binding (KServe/Knative Eventing `InferenceService`
source, or a Kafka-source sidecar) surfaced as an opt-in on the topology. Keep it
funneled through the inference protocol so the model contract is unchanged.

---

## 3.8 Control scheduling and placement

**Status:** Partial — standard pod fields flow through; no dedicated topology
field.

As an MLOps engineer, I want to place workloads on the right nodes (GPU type,
zone, taints), so that expensive accelerators are used correctly.

**Acceptance criteria**
- Node selectors / tolerations / affinity reach the generated workload.
- GPU resource requests target the intended accelerator class.

**Provider mapping (interim)** — express placement through inline `config` /
`baseRefs` until first-class scheduling fields exist.
