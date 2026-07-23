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

## Usage

See [examples/](examples/) for complete `Instance` manifests:

- [examples/instance-llm.yaml](examples/instance-llm.yaml) — serve Llama 3.1 8B with vLLM.
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

> **Note:** `go.mod` uses a local `replace github.com/kserve/kserve => ../kserve`
> to build against the KServe API in this workspace. Replace it with a published
> version tag for standalone builds. The accompanying alignment directives pin
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

The CRDs are copied from the pinned KServe charts (`../kserve/charts`, matching
the `replace` directive in `go.mod`) by `make sync-crds`, which runs
automatically as part of `make generate`. When you bump the KServe version,
re-run `make generate` and commit the refreshed `crds/`. `make verify` fails in
CI if they drift.

```sh
make sync-crds   # refresh charts/provider-kserve/crds/ from $(KSERVE_CHARTS)
```

Helm does **not** upgrade CRDs already present in a cluster from `crds/`. To roll
out CRD schema changes to an existing cluster, apply them out of band:

```sh
kubectl apply --server-side -f charts/provider-kserve/crds/
```

