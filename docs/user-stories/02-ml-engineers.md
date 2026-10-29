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

As an ML engineer, I want to point serving at an exact model in HF, S3, a PVC or
a container image (plus GCS and other stores for `predictor`), so that I can ship
the fine-tune I just produced.

**Acceptance criteria**
- `llm`: `hf://`, `s3://`, `pvc://`, `oci://` and `oci+native://` are accepted;
  other schemes are rejected up front.
- `predictor`: `hf://`, `s3://`, `gs://`, `pvc://`, `oci://` and the other
  KServe storage URIs are accepted.
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

Status: Supported through OCI model images and the optional KServe LocalModel controller.

As an ML engineer, I want large weights cached on selected nodes. New replicas
can then start without downloading the same model again.

Acceptance criteria
- A model can be cached on nodes through an OCI model image or `LocalModelNamespaceCache`.
- A restart, rollout, or new replica on a node that has the model does not download it again.
- An administrator prepares LocalModel node groups with Kubernetes YAML.
- An ML engineer enables and manages an Instance namespace cache in the provider-driven OpenEverest form.
- Kubernetes YAML and GitOps remain supported.
- KServe matches the Instance model URI to a cache automatically.
- The Instance status shows available, total, and failed node copies.
- A cached model Pod mounts the cache PVC without a storage-initializer container.

Provider mapping

For OCI model images, package the model as an image. See "OCI model images" in the README and `examples/instance-llm-oci.yaml`.

```yaml
components:
  llmEngine:
    parameters:
      modelURI: oci+native://registry.example.com/models/qwen3-8b:v1  # or oci://
      imagePullSecret: registry-pull   # private registry only
```

For KServe LocalModel caching, enable the chart feature and configure the Instance cache.

```yaml
# Helm values
kserveResources:
  kserve:
    localmodel:
      enabled: true

---
apiVersion: core.openeverest.io/v1alpha1
kind: Instance
metadata:
  name: qwen3-4b
  namespace: models
spec:
  providerRef:
    name: provider-kserve
  topology:
    type: llm
  components:
    llmEngine:
      type: vllm
      parameters:
        modelURI: hf://Qwen/Qwen3-4B
        localModelCache:
          enabled: enabled
          modelSize: 10Gi
          nodeGroup: model-workers
```

The provider creates and owns the `LocalModelNamespaceCache`. Leave `disableStorageInitializer` unset. KServe uses the storage initialization path to replace the URI with the cached PVC. It must remain disabled only for OCI images.

See `docs/local-model-cache.md` and `examples/local-model-cache.yaml`.

---

## 2.4 Authenticate to gated models and private buckets

**Status:** Supported (HF token, private registries for OCI images); Partial for
cloud credentials.

As an ML engineer, I want serving to authenticate to gated HF models and my
private storage, so that I can serve licensed or internal weights.

**Acceptance criteria**
- An `HF_TOKEN` secret is injected for gated model downloads.
- A registry pull secret is used for private OCI model images.
- Cloud/storage credentials resolve for `s3://`/`gs://` artifacts.

**Provider mapping** — set `huggingface.tokenSecretName` in the chart; the token
is attached to the workload's model-puller ServiceAccount. For a private OCI
model image, set `llmEngine.parameters.imagePullSecret`.
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
version: "0.20"                 # curated runtime bundle (both)
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
