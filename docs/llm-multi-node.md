# Serve a model that does not fit on one node

Use this when **one copy of the model is too big for a single server**, even after
you split it across GPUs **in that server**. Clients still call the same
OpenAI-compatible URL. They never pick “head” or “worker.”

This is **not** “more traffic → more copies.” That is `replicas` or WVA min/max.
Here, several machines hold **pieces of the same model**. If one worker dies, that
model is down.

KServe implements this as a **head + workers** layout (`LLMInferenceService.spec.worker`).
The cluster must already run KServe’s multi-node path (LeaderWorkerSet).

## Tensor parallelism vs workers vs replicas

| You set | What it means | Typical case |
|---|---|---|
| `tensorParallelSize` | Cut the model across **GPUs in one pod/node** | Fits on one 8-GPU box |
| `workerCount` + `pipelineParallelSize` | Cut the model across **several nodes** (head + workers) | Still too big for one box |
| `replicas` / `minReplicas` / `maxReplicas` | How many **full copies** of that layout | More traffic |

`tensorParallelSize: 8` plus `nvidia.com/gpu: 8` on the head is “eight cards, one box.”

`workerCount: 1` and `pipelineParallelSize: 2` is “one head box + one worker box.”
Each box can still use tensor parallelism (same GPU count per node).

```
Client  →  Service / gateway  →  HEAD (front door + some layers)
                                      ↓
                                 WORKER (the other layers)
                                      ↓
                                 HEAD streams tokens back
```

`pipelineParallelSize` alone only sets a vLLM flag. It does **not** create worker
pods. `workerCount` is the switch.

## Decode (llmEngine)

Required when you want workers:

- `workerCount` — extra pods **besides** the head (minimum 1)
- `pipelineParallelSize` — must equal `workerCount + 1` (head + workers)

Optional:

- `workerResources` — CPU / memory / GPU for workers. If you omit it, workers
  copy `llmEngine.resources`. If you set only some keys (the UI writes CPU/memory),
  the rest — including `nvidia.com/gpu` — is copied from the head. Setting
  resources without `workerCount` is rejected.

```yaml
spec:
  topology:
    type: llm
  components:
    llmEngine:
      type: vllm
      replicas: 1          # one full ring (head + all workers)
      resources:
        limits:
          nvidia.com/gpu: "8"
      parameters:
        modelURI: hf://meta-llama/Llama-3.1-70B-Instruct
        tensorParallelSize: 8
        pipelineParallelSize: 2   # must be workerCount + 1
        workerCount: 1
        # workerResources:        # optional; defaults to the head resources above
        #   limits:
        #     nvidia.com/gpu: "8"
```

Rules the provider checks:

- `workerCount` ≥ 1
- `pipelineParallelSize == workerCount + 1`
- If `nvidia.com/gpu` is set on the head or on the **merged** worker resources,
  that count must be ≥ `tensorParallelSize`. `computeProfile: cpu` or no GPU key
  skips this check.
- `dataParallelSize` and `pipelineParallelSize` stay mutually exclusive.

One replica (or one WVA step) is **the whole ring**, not one extra GPU. Two
replicas means two heads and two worker sets.

## Prefill

If you split prefill and decode (`enablePrefill: true`), prefill can have its own
ring. Same rules, different fields:

| Decode | Prefill |
|---|---|
| `workerCount` | `prefillWorkerCount` |
| `pipelineParallelSize` | `prefillPipelineParallelSize` |
| `workerResources` | `prefillWorkerResources` |

`prefillWorkerCount` requires `enablePrefill`. Prefill pipeline size must equal
`prefillWorkerCount + 1`. Prefill worker resources overlay the head the same way
as decode. The prefill **head** gets `llmEngine.resources` on `spec.prefill.template`
so it stays distinct from prefill workers. Prefill pipeline size is only the
prefill PP flag — it does not copy decode `tensorParallelSize`.

Prefill pipeline size **without** `prefillWorkerCount` is flag-only (no prefill
workers), same as decode.

```yaml
spec:
  topology:
    type: llm
    parameters:
      enablePrefill: true
      prefillPipelineParallelSize: 2
      prefillWorkerCount: 1
```

## What clients see

Nothing new. Same `model` name, same `/v1/chat/completions`. First token may take
longer while every node in the ring loads. After that it is one logical model.

## If the Instance never becomes Ready

- `workerCount` set but `pipelineParallelSize` missing or not `count + 1`
- GPU limit smaller than `tensorParallelSize`
- Worker resources set without `workerCount`
- Nodes do not have enough GPUs for **each** pod in the ring
- LeaderWorkerSet CRD / KServe multi-node not installed on the cluster
