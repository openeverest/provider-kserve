# provider-kserve

The OpenEverest provider for [KServe](https://kserve.github.io/website/) — run
Large Language Models and inference workloads on Kubernetes.

This provider translates OpenEverest `Instance` resources into KServe custom
resources, exposing two serving architectures ("topologies"):

| Topology    | KServe resource                          | Use case |
|-------------|------------------------------------------|----------|
| `llm`       | `LLMInferenceService` (`v1alpha2`)       | Large Language Models served with vLLM — OpenAI-compatible endpoints, tensor/pipeline parallelism, Gateway API inference routing, and optional disaggregated prefill/decode (llm-d pattern). |
| `predictor` | `InferenceService` (`v1beta1`)           | Predictive models (sklearn, pytorch, tensorflow, xgboost, onnx, triton, huggingface, ...). KServe auto-selects a ServingRuntime from the model format. |

All resources are created in **RawDeployment** mode (plain Kubernetes
Deployments + HPA, no Knative dependency).

## Components

| Component   | Type          | Backs                  |
|-------------|---------------|------------------------|
| `llmEngine` | `vllm`        | `LLMInferenceService`  |
| `predictor` | `modelServer` | `InferenceService`     |

### `llmEngine` (vllm) parameters

| Field                        | Type       | Description |
|------------------------------|------------|-------------|
| `modelURI`                   | string     | Model artifacts location (`hf://`, `s3://`, `gs://`, `pvc://`). **Required.** |
| `modelName`                  | string     | Name advertised in the request `model` field. Defaults to the Instance name. |
| `tensorParallelSize`         | int32      | vLLM tensor parallelism (`--tensor-parallel-size`). |
| `pipelineParallelSize`       | int32      | vLLM pipeline parallelism (`--pipeline-parallel-size`). |
| `computeProfile`             | string     | `gpu` (default) uses the bundled CUDA presets; `cpu` composes the CPU-only `kserve-config-llm-cpu` config via `baseRefs`. See [Compute profile](#compute-profile-cpu-serving). |
| `baseRefs`                   | []string   | `LLMInferenceServiceConfig` names to inherit/merge. |
| `disableStorageInitializer`  | bool       | Skip the storage-initializer init container. |

### `predictor` (modelServer) parameters

| Field            | Type   | Description |
|------------------|--------|-------------|
| `modelFormat`    | string | Model framework (e.g. `sklearn`). **Required.** |
| `storageURI`     | string | Model artifacts location. **Required.** |
| `runtimeVersion` | string | Pin the serving runtime version. |
| `runtime`        | string | Explicitly select a (Cluster)ServingRuntime by name. |
| `minReplicas`    | int32  | Autoscaling floor (`0` enables scale-to-zero). |
| `maxReplicas`    | int32  | Autoscaling ceiling. |

## Topology parameters (`llm`)

| Field                  | Type  | Description |
|------------------------|-------|-------------|
| `enableGatewayRouting` | bool  | Provision a Gateway API route + Inference Gateway scheduler (Endpoint Picker) for prefix-cache aware routing. |
| `enablePrefill`        | bool  | Enable disaggregated serving with a separate prefill workload. |
| `prefillReplicas`      | int32 | Replicas for the prefill workload (when `enablePrefill` is true). |

## Model catalog

The models offered in the `llm` topology's **Model** dropdown are **not**
hardcoded in the generated schema — they are driven by the chart's
`.Values.models` and rendered into the `Provider` CR at install time
(`spec.uiSchema.llm.modelCatalog`). Edit the list to add, remove, or reorder models;
changes take effect on `helm upgrade`, with no regeneration required.

```yaml
# values.yaml
models:
  - label: "Qwen2.5 0.5B Instruct"     # shown in the dropdown
    uri: "hf://Qwen/Qwen2.5-0.5B-Instruct"
  - label: "Llama 3.2 1B Instruct"
    uri: "hf://meta-llama/Llama-3.2-1B-Instruct"
    gated: true                         # adds a "(gated)" suffix; needs a token
```

| Field   | Type   | Description |
|---------|--------|-------------|
| `label` | string | Display name shown in the UI. |
| `uri`   | string | Model location (`hf://`, `s3://`, `pvc://`, …). |
| `gated` | bool   | Optional. Marks a model that needs a HuggingFace token (see below). Gated entries get a `(gated)` suffix in the UI. |

The default catalog ships a range of ungated models (including tiny,
CPU-friendly ones such as SmolLM2 135M/360M, Qwen2.5 0.5B, and TinyLlama 1.1B)
plus common gated models.

> The UI resolves the dropdown from `spec.uiSchema.llm.modelCatalog` on the
> `Provider` CR (there is no runtime ConfigMap lookup), which is why the catalog
> is rendered from Helm values rather than read at request time. It lives under
> `spec.uiSchema` because that is the Provider CRD's only
> `PreserveUnknownFields` region — a top-level `spec.modelCatalog` field would be
> pruned by the apiserver as an unknown field. It is nested inside the `llm`
> topology node (not a top-level `uiSchema` key) so it is not enumerated as a
> selectable topology in the UI's topology dropdown.

### Gated models (HuggingFace token)

Gated models (Llama, Mistral, Gemma, …) require a HuggingFace token and prior
access approval. Create a `Secret` holding the token — with an **`HF_TOKEN`**
key — in the **same namespace as your `Instance`s**, before deploying them:

```sh
kubectl create secret generic hf-token --from-literal=HF_TOKEN=hf_xxx
```

Then point the provider at it:

```yaml
# values.yaml
huggingface:
  tokenSecretName: hf-token
```

When set, the provider attaches the secret to a dedicated
`<instance>-model-puller` `ServiceAccount` on each `llm` workload, so KServe's
storage-initializer authenticates the download and injects `HF_TOKEN`. Leave
`tokenSecretName` empty to disable — only ungated models will then download.

> **Limitation:** when Gateway API routing (`enableGatewayRouting`) is enabled,
> KServe manages its own workload `ServiceAccount` and ignores the provider's,
> so the token is not injected in that combination. Gated downloads without
> gateway routing work as described.

## Compute profile (CPU serving)

The bundled `LLMInferenceServiceConfig` presets use a CUDA (GPU) vLLM image. To
serve a model on nodes without a GPU, set the `computeProfile` parameter to
`cpu` (exposed as the **Compute Profile** selector in the UI). The provider then
adds the chart-shipped `kserve-config-llm-cpu` config to the service's
`baseRefs`, which strategic-merges a CPU-only vLLM image over the base template.
Because the base command uses the generic `vllm` CLI, only the image changes —
the command, env, and probes are inherited.

Configure the CPU image in the chart:

```yaml
cpuProfile:
  enabled: true
  image: vllm/vllm-openai-cpu:v0.25.1
```

The config is rendered into the release namespace alongside the other presets,
so it resolves for instances in any namespace. It requires `llmPresets.enabled`
(it layers onto those presets). Use `baseRefs` directly if you need a fully
custom `LLMInferenceServiceConfig` instead.

### CPU memory sizing

vLLM CPU has a large, model-independent runtime footprint — the torch import and
compilation in the worker consume **several GiB before the model or KV cache are
loaded** (it can be ~5 GiB even for a 125M model). On top of that, vLLM is **not**
cgroup-aware, and two independent startup checks apply:

- **Startup reservation** — vLLM reserves `--gpu-memory-utilization` × the
  detected memory (on the CPU backend this flag controls *CPU* memory despite
  its name; default `0.92`) and validates it against the memory that is *free*
  when the worker starts. Once the runtime footprint is loaded, the 0.92 default
  overshoots what is free and fails with
  `Available memory on node … on startup is less than desired CPU memory utilization`.
- **KV cache** — `VLLM_CPU_KVCACHE_SPACE`, if set, then carves the KV cache out
  of that reservation and is likewise checked against free memory.

To make the CPU profile start on a modest node, the `kserve-config-llm-cpu`
preset applies a set of default vLLM args (`cpuProfile.defaultArgs`) to the
model-server container:

```yaml
cpuProfile:
  defaultArgs:
    - --gpu-memory-utilization=0.3   # don't pre-grab 92% of memory
    - --enforce-eager                # skip torch.compile
    - --max-model-len=2048           # smaller context = smaller KV arena
```

These flow into the vLLM command as container args. To override them for a
single instance, set `args` on the `main` container in the topology's
**Advanced** (inline `LLMInferenceServiceConfig`) section — KServe treats a
container's `args` as atomic, so your list **replaces** the preset defaults
entirely (re-specify any you still want):

```yaml
config: |
  template:
    containers:
      - name: main
        args:
          - --gpu-memory-utilization=0.5
          - --max-model-len=4096
```

Set **CPU KV Cache Space** (`parameters.kvCacheSpaceGi`) only to additionally cap
`VLLM_CPU_KVCACHE_SPACE`.

The provider also mirrors the memory (and CPU) **limit into the request**
(Guaranteed QoS) so the scheduler reserves that memory on the node.

**Size memory generously.** Because of the multi-GiB runtime floor, a tight
limit (e.g. 8 GiB) leaves too little free for even a modest reservation and fails
the startup check regardless of model size. Give CPU instances enough headroom
above that floor, and/or lower `--gpu-memory-utilization`.

### Advanced customization (inline config)

The structured fields cover the common knobs (model, resources, parallelism, KV
cache). For anything else — extra vLLM args, environment variables, scheduling —
use the **Advanced** section (`llmEngine.parameters.config`) to author an inline
`LLMInferenceServiceConfig` **spec body**:

```yaml
components:
  llmEngine:
    parameters:
      config: |
        template:
          containers:
            - name: main
              args:
                - --enforce-eager      # skip torch.compile (lowers CPU memory)
                - --max-model-len=2048 # cap context length (shrinks KV cache)
```

On reconcile the provider materializes this as an `LLMInferenceServiceConfig`
named `<instance>-config` in the Instance's namespace, owned by the Instance
(so it is garbage-collected with it), and attaches it to the
`LLMInferenceService` via `baseRefs`. It is applied **last**, so it overrides the
compute-profile preset and any `baseRefs` you set, while the Instance's own
structured fields (model, resources, the derived KV cache env) still win over it.
Enter only the config spec body — the provider supplies the metadata.

To reference *pre-existing* shared configs instead of authoring one inline, list
their names in `llmEngine.parameters.baseRefs` (API-only).

## Usage

See [examples/](examples/) for complete `Instance` manifests:

- [examples/instance-llm.yaml](examples/instance-llm.yaml) — serve Llama 3.1 8B with vLLM.
- [examples/instance-llm-cpu.yaml](examples/instance-llm-cpu.yaml) — serve a small model on CPU (compute profile, KV cache sizing, inline advanced config).
- [examples/instance-predictor.yaml](examples/instance-predictor.yaml) — serve an sklearn model.

## Development

```sh
make generate   # regenerate RBAC + provider spec from definition/
go build ./...  # compile
go test ./...   # run tests
```

The provider definition lives under [definition/](definition/):

- `provider.yaml` — provider identity and components.
- `versions.yaml` — component version catalog and version bundles.
- `components/types.go` — component parameter schemas.
- `topologies/<name>/` — topology config, UI schema, and parameter types.

> **Note:** `go.mod` requires the published `github.com/kserve/kserve` module
> tag directly (no local `replace`). The accompanying alignment directives pin
> `controller-runtime` and `k8s.io` versions to match KServe's (KEDA v2.18
> compatibility).

## KServe CRDs

Installing the provider chart also installs the KServe CustomResourceDefinitions
the provider translates `Instance`s into (`InferenceService`,
`LLMInferenceService`, and their supporting kinds). The CRDs are **vendored**
into the chart's [`crds/`](charts/provider-kserve/crds/) directory rather than
pulled in as templated subchart dependencies.

### Why `crds/` instead of a subchart

Helm handles a chart's `crds/` directory specially: it installs the CRDs if they
are absent, **skips** them if they already exist (no error), and **never
deletes** them on `helm uninstall`. This is the same mechanism the
`valkey-operator` chart uses for its CRDs.

The upstream `kserve-crd` / `kserve-llmisvc-crd` charts instead ship their CRDs
under `templates/`, which makes them Helm-release-tracked resources. In a
dev/test loop that is fragile: on uninstall Helm tries to delete the CRDs, but
CRs left behind by tests keep finalizers (there is no KServe controller running
to clear them), so the deletion stalls and the CRDs are orphaned. The next
install then fails with `... already exists`, because Helm will not adopt
pre-existing resources on a fresh install. Vendoring the CRDs under `crds/`
avoids this entirely.

### Keeping the CRDs up to date

The CRDs are copied from the local KServe charts checkout (`../kserve/charts`,
via the `KSERVE_CHARTS` Makefile variable) by `make sync-crds`, which runs
automatically as part of `make generate`. The `LLMInferenceServiceConfig`
presets are vendored the same way by `make sync-llm-presets` (into
`files/llmisvcconfigs/`). When you bump the KServe version, re-run
`make generate` and commit the refreshed `crds/` and `files/`. `make verify`
fails in CI if they drift.

```sh
make sync-crds         # refresh charts/provider-kserve/crds/ from $(KSERVE_CHARTS)
make sync-llm-presets  # refresh charts/provider-kserve/files/ from $(KSERVE_CHARTS)
```

Helm does **not** upgrade CRDs already present in a cluster from `crds/`. To roll
out CRD schema changes to an existing cluster, apply them out of band:

```sh
kubectl apply --server-side -f charts/provider-kserve/crds/
```

## Bundled KServe controllers

The provider only *translates* `Instance`s into KServe custom resources — it does
not reconcile them. The controllers that do are bundled as Helm subchart
dependencies so a single `helm install` yields a working stack (mirroring how
`provider-valkey` bundles the `valkey-operator`):

| Dependency                  | Reconciles / provides             | Toggle |
|-----------------------------|-----------------------------------|--------|
| `kserve-resources`          | `InferenceService` (predictor)    | `kserveResources.enabled` |
| `kserve-llmisvc-resources`  | `LLMInferenceService` (llm)       | `kserveLlmisvcResources.enabled` |
| `kserve-runtime-configs`    | `ClusterServingRuntime`s (predictor) | `kserveRuntimeConfigs.enabled` |
| `cert-manager`              | webhook certificates for both     | `cert-manager.enabled` |

The `kserve-runtime-configs` chart ships the `ClusterServingRuntime`s the
InferenceService controller selects from by model format — without them the
`predictor` topology has no runtime to schedule.

The `LLMInferenceServiceConfig` presets (`kserve-config-llm-template`, …) that
the llmisvc controller merges into every `LLMInferenceService` are **not** taken
from that subchart. Upstream hardcodes them to the `kserve` namespace, and the
llmisvc controller resolves presets from its own pod namespace (`POD_NAMESPACE`),
which would force the whole stack into `kserve`. Instead the provider vendors the
preset file (see `make sync-llm-presets`) under the chart's
`files/llmisvcconfigs/` directory and renders it into the **release namespace**
(toggle `llmPresets.enabled`). This is what lets the provider install into any
namespace. Without the presets the `llm` topology stalls with
`ConfigNotFound: kserve-config-llm-template`.


Both controllers run admission webhooks whose certificates are issued by
**cert-manager**, so it is a hard requirement. It is bundled and installed by
default; set `cert-manager.enabled=false` when the cluster already provides it
(a common case).

Both controllers default to KServe's **RawDeployment** mode (plain Deployments +
HPA, no Knative/Istio), matching the deployment mode the provider annotates on
every resource it creates. `kserve-resources` owns the shared KServe resources
(config `ConfigMap`, self-signed `Issuer`, `ClusterStorageContainer`); the
llmisvc chart has them disabled to avoid collisions within one release.

Vendor the dependencies before installing:

```sh
make helm-deps   # helm dependency build (adds the jetstack repo for cert-manager)
```

> **Note on fresh clusters:** installing cert-manager in the *same* Helm release
> as the resources that consume it can race (the cert-manager webhook may not be
> ready when the KServe `Issuer`/`Certificate` objects are admitted). If a first
> install fails on a cert-manager webhook error, either re-run it, or install
> cert-manager first and set `cert-manager.enabled=false`. The dev
> [Tiltfile](dev/Tiltfile) does the latter automatically.

