# Persona 2 — ML Engineers

**Who:** ML engineers who train and fine-tune models and then need to serve
specific weights or adapters.

**What they value:** point serving at exactly the right artifact, serve LoRA
adapters on top of a base model, and avoid re-downloading large weights on every
rollout.

**Guiding principle:** the model artifact is the unit of work — its location,
its adapters, and how it gets onto the node.

---

## 2.1 Serve a specific model or fine-tune from any artifact store

**Status:** Supported (both)

As an ML engineer, I want to point serving at an exact model in HF, S3, GCS, or
a PVC, so that I can ship the fine-tune I just produced.

**Acceptance criteria**
- `hf://`, `s3://`, `gs://`, `pvc://` (and `oci://` for predictor) are accepted.
- A pinned revision/path resolves to that exact artifact.

**Provider mapping**
```yaml
# llm
components: { llmEngine: { parameters: { modelURI: s3://models/my-ft/v3 } } }
# predictor
components: { predictor: { parameters: { storageURI: pvc://ft-claim/my-model } } }
```

---

## 2.2 Serve LoRA adapters on top of a base model

**Status:** Supported (`llm`)

As an ML engineer, I want to attach one or more LoRA adapters to a base model,
so that I can serve many fine-tunes without loading a full model per variant.

**Acceptance criteria**
- I can declare a base model plus a list of adapters (name + URI).
- Each adapter is addressable as its own `model` name in requests.
- Adapters can be added/removed without redeploying the base model.

**Provider mapping**
```yaml
components:
  llmEngine:
    parameters:
      modelURI: hf://meta-llama/Llama-3.2-1B-Instruct
      lora:
        maxRank: 16
        adapters:
          - name: sentiment-ft
            uri: hf://my-org/llama-sentiment-lora
          - name: support-ft
            uri: s3://models-bucket/loras/support-v3
```

KServe downloads `hf://` / `s3://` adapters in the storage-initializer,
mounts `pvc://` adapters directly, and injects vLLM `--lora-modules` flags.
See `examples/instance-llm-lora.yaml`.

> UI note: enable **LoRA deployment** in the wizard and pick up to three adapters
> from the operator catalog (`chart loraAdapters` / `spec.uiSchema.llm.loraCatalog`).
> Alternatively, set `lora.adapters` in Instance YAML when deployment mode is disabled.

---

## 2.3 Pre-pull / cache model weights on nodes

**Status:** Partial — `disableStorageInitializer` supports pre-loaded models;
the `LocalModelCache` wiring is not exposed as a provider field.

As an ML engineer, I want large weights pre-pulled and cached on nodes, so that
new replicas start fast and I do not re-download gigabytes on every rollout.

**Acceptance criteria**
- A model can be pre-populated onto nodes (LocalModelCache / modelcar / PVC).
- When a model is pre-loaded, the storage-initializer can be skipped.
- Cold-start time for a new replica drops to node-local read speed.

**Provider mapping**
```yaml
components:
  llmEngine:
    parameters:
      disableStorageInitializer: true   # model already on-node (cache/modelcar)
```

**Gap to close:** expose a `LocalModelCache` reference (or a `cache: true`
convenience) so the provider provisions the cache rather than requiring the
model to be staged out-of-band.

---

## 2.4 Authenticate to gated models and private buckets

**Status:** Supported (HF token); Partial for cloud credentials.

As an ML engineer, I want serving to authenticate to gated HF models and my
private storage, so that I can serve licensed or internal weights.

**Acceptance criteria**
- An `HF_TOKEN` secret is injected for gated model downloads.
- Cloud/storage credentials resolve for `s3://`/`gs://` artifacts.

**Provider mapping** — set `huggingface.tokenSecretName` in the chart; the token
is attached to the workload's model-puller ServiceAccount.
> Known limitation: HF token injection does not apply when
> `enableGatewayRouting` is on (KServe manages its own ServiceAccount).

---

## 2.5 Pin runtime version for reproducibility

**Status:** Supported (both)

As an ML engineer, I want to pin the serving runtime version, so that a model
that passed evaluation serves identically in production.

**Acceptance criteria**
- I can pin the runtime version (predictor) or select a vLLM version bundle (llm).
- Re-deploying yields the same runtime image.

**Provider mapping**
```yaml
version: "0.15"                 # curated runtime bundle (both)
components:
  predictor:
    parameters:
      runtimeVersion: "v2"      # predictor: pin runtime version
      runtime: kserve-sklearnserver   # or pin the exact runtime
```

---

## 2.6 Serve a Hugging Face model with the built-in runtime

**Status:** Supported (`predictor`)

As an ML engineer, I want to serve a transformers model via the KServe
huggingface runtime, so that I can expose encoder/embedding/classification
models without packaging a custom server.

**Acceptance criteria**
- `modelFormat: huggingface` selects the huggingface ServingRuntime.
- The model answers the standard inference protocol.

**Provider mapping**
```yaml
components: { predictor: { parameters: { modelFormat: huggingface, storageURI: hf://... } } }
```
