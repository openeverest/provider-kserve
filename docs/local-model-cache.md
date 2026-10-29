# Cache model weights on nodes

KServe LocalModel stores model weights on selected nodes. New model Pods mount the cached PVC instead of downloading the model again.

The feature is off by default. It installs cluster-wide RBAC, a controller, a node agent DaemonSet, download Jobs, PVs, and PVCs.

## Enable LocalModel

Enable the controller during chart installation or upgrade:

```bash
helm upgrade --install provider-kserve \
  oci://ghcr.io/openeverest/charts/provider-kserve \
  --namespace everest-system \
  --set kserveResources.kserve.localmodel.enabled=true
```

Create the namespace that runs model download Jobs. The chart defaults to
`kserve-localmodel-jobs`; set
`kserveResources.kserve.localmodel.jobNamespace` when using another namespace.

```bash
kubectl create namespace kserve-localmodel-jobs
```

The chart installs these LocalModel CRDs when the feature is enabled:

- `LocalModelNodeGroup`
- `LocalModelCache`
- `LocalModelNamespaceCache`
- `LocalModelNode`

## Prepare nodes

Label each cache node:

```bash
kubectl label node <node-name> kserve/localmodel=worker
```

Read the host path from the installed node agent. Use this exact path in the node group:

```bash
kubectl -n everest-system get daemonset kserve-localmodelnode-agent \
  -o jsonpath='{.spec.template.spec.volumes[?(@.name=="models")].hostPath.path}{"\n"}'
```

KServe v0.21 currently renders `/models`. Do not use the container mount path `/mnt/models` in the node group.

Apply the complete public-model example. It creates the node group and an
Instance whose provider-managed namespace cache is enabled:

```bash
kubectl apply -f examples/local-model-cache.yaml
```

## Configure download credentials

Download Jobs run in `kserve-localmodel-jobs`. Create credential Secrets and ServiceAccounts in that namespace.

For a gated Hugging Face model, create these resources:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: hf-secret
  namespace: kserve-localmodel-jobs
type: Opaque
stringData:
  HF_TOKEN: <token>
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: hf-downloader
  namespace: kserve-localmodel-jobs
  annotations:
    openeverest.io/instance-namespace: models
secrets:
  - name: hf-secret
```

Set `llmEngine.parameters.localModelCache.serviceAccountName` to
`hf-downloader` on an Instance in the `models` namespace. The provider reads
the ServiceAccount from the configured job namespace and requires its
`openeverest.io/instance-namespace` annotation to match the Instance namespace.
An absent, unbound, or differently bound ServiceAccount is rejected before the
provider changes the cache or workload. The provider does not copy Secrets.

## Configure an Instance cache

The provider definition adds a Model Cache section to the LLM Instance form.
Enable it and set:

- Model size: storage required for one model copy.
- LocalModelNodeGroup: an existing cluster-scoped node group.
- Download service account: optional credentials reference.

The equivalent Instance YAML is:

```yaml
spec:
  components:
    llmEngine:
      parameters:
        modelURI: hf://HuggingFaceTB/SmolLM2-135M-Instruct
        localModelCache:
          enabled: enabled
          modelSize: 1Gi
          nodeGroup: model-workers
          serviceAccountName: hf-downloader
```

The provider creates a `LocalModelNamespaceCache` in the Instance namespace.
Its name matches the Instance name, and the Instance owns it. Select Disabled
in the form to remove the cache and recreate the workload without cache admission.

The cache URI always matches the Instance model URI. KServe admission adds
cache labels and rewrites the workload to the cached PVC.

Do not set `disableStorageInitializer: true` on the Instance. KServe performs
the cache rewrite inside the storage initialization path.

The first cache population still downloads the model. Later Pods reuse the
node-local copy.

The chart gives KServe download Jobs a 4 GiB memory limit. This overrides
KServe's 1 GiB default, which is insufficient for the end-to-end model download.

Architecture and security decisions are recorded in
[`specs/adr/0001-local-model-cache.md`](../specs/adr/0001-local-model-cache.md).

## Check cache state

Inspect the cache:

```bash
kubectl -n models get localmodelnamespacecache smollm2-cached -o yaml
```

Check the Instance message:

```bash
kubectl -n models get instance smollm2-cached \
  -o jsonpath='{.status.message}{"\n"}'
```

A populated cache reports a message such as:

```text
Model cache "smollm2-cached": 1/1 nodes ready
```

Verify that the admitted service uses the cache:

```bash
kubectl -n models get llminferenceservice smollm2-cached \
  -o jsonpath='{.metadata.labels.internal\.serving\.kserve\.io/localmodel}{"\n"}'
```

Verify that the model Pod mounts a PVC and has no storage initializer:

```bash
kubectl -n models get pod -l app.kubernetes.io/name=smollm2-cached -o yaml
```

## Capacity and deletion

`spec.modelSize` reserves capacity. Each selected node stores one full model
copy. The provider refuses zero or invalid sizes.

Disable the Instance cache before removing its node group. Deleting the
Instance also deletes its namespace cache through the owner reference.

Node replacement removes that node's local copy. KServe downloads the model
again on the replacement node.
