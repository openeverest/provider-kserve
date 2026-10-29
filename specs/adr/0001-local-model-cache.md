# ADR 0001: Local model cache ownership and security boundary

- Status: Accepted
- Date: 2026-10-29
- Implementation baseline: `feat/localmodel-cache`
- KServe baseline: `v0.21.0` at `d1482554fc4f66dd41aee70e01f5174e24f265bd`

## Context

Large model downloads make LLM pod replacement slow and repeat network and storage work. KServe LocalModel downloads one copy per selected node and admits matching workloads against a local PVC.

The provider must expose this capability without changing OpenEverest core or UI source. The existing UI renders fields from the provider definition.

LocalModel crosses namespace and cluster boundaries. Its node agent reads download credentials from a shared job namespace. Its controller manages cluster-scoped node storage. Cache deletion also interacts with running workloads.

This record captures the decisions in `origin/main...feat/localmodel-cache` across the chart, provider, definition, lifecycle, status, tests, and user documentation.

## Decision

Adopt KServe LocalModel for LLM Instances as an optional feature. Use an Instance-owned `LocalModelNamespaceCache`, while administrators retain ownership of node groups, storage, and download credentials.

Accept the official upstream KServe LocalModel RBAC boundary without forking its chart. This acceptance applies only because LocalModel is opt-in and disabled by default. The residual cluster-wide credential and Job risk remains explicit.

The provider enforces tenant authorization for every referenced download ServiceAccount before it changes a cache or workload.

## Detailed decisions

1. The chart pins all KServe components, including `kserve-localmodel-resources`, to `v0.21.0`. One value, `kserveResources.kserve.localmodel.enabled`, controls the LocalModel dependency, admission configuration, CRD hook, and provider feature flag.

2. LocalModel remains disabled by default. A default render contains no LocalModel controller, node agent, or LocalModel CRD install command. Enabling it renders all three and sets `LOCAL_MODEL_ENABLED=true` on the provider.

3. The feature is limited to the LLM topology. No OpenEverest core or UI source change is required. The provider definition adds a Model Cache section that the existing UI renderer consumes.

   The UI-schema component key is `cacheEnabled`, while its API path remains `localModelCache.enabled`. OpenEverest `2.0.0-dev.2` omits `uiType: toggle` controls from form steps. A select renders, but Boolean option values fail form validation with `Expected string, received boolean`. The field therefore uses the string enum `disabled` or `enabled`. This preserves the provider-only integration boundary and matches the existing `ExpertParallel` and `LoraDeployment` conventions.

4. `llmEngine.parameters.localModelCache` contains the `disabled` or `enabled` string, `modelSize`, `nodeGroup`, and optional `serviceAccountName`. An omitted or empty enable mode is disabled. Other values fail provider validation. The UI accepts `modelSize` as a number in Gi and emits a Kubernetes quantity. API clients may submit any positive Kubernetes quantity.

5. Enabling a cache requires chart support, a positive `modelSize`, one valid DNS node-group name, and `disableStorageInitializer=false`. An optional ServiceAccount name must be a valid DNS subdomain.

6. The provider creates one `serving.kserve.io/v1alpha1` `LocalModelNamespaceCache` in the Instance namespace. Its name matches the Instance name, its owner is that Instance UID, and its `sourceModelUri` always matches the Instance model URI.

7. The provider writes exactly one administrator-created `LocalModelNodeGroup` into `spec.nodeGroups`. It does not create, update, or delete node groups. Node labels, local storage, PV policy, and capacity remain administrator concerns.

8. Download Secrets and ServiceAccounts remain in KServe's configured job namespace, normally `kserve-localmodel-jobs`. The chart passes that namespace to the provider as `LOCAL_MODEL_JOB_NAMESPACE`. The provider writes only `serviceAccountName`; it does not copy Secrets from the Instance namespace.

9. A referenced download ServiceAccount MUST carry the administrator-controlled `openeverest.io/instance-namespace` annotation. Its value MUST equal the Instance namespace. The provider rejects an absent, unbound, or differently bound ServiceAccount during validation and again before cache reconciliation.

   Public models may omit `serviceAccountName`. Tenant binding limits which credential set a tenant may select, but it does not reduce the node agent's own RBAC if that agent is compromised.

10. The chart raises KServe's LocalModel download-job memory limit from 1 GiB to 4 GiB. The 1 GiB upstream default OOM-killed the 135M-parameter end-to-end model download.

   The single-node end-to-end fixture limits the SmolLM2 model server to 2 GiB so it can run beside the 4 GiB download Job on an 8 GiB k3d node. Production Instance sizing remains user-controlled.

11. The storage initializer setting must remain enabled on the requested LLM service. KServe performs LocalModel matching in that path, then admission replaces the download path with the cached PVC. The admitted model pod therefore has no storage-initializer container.

12. The provider may apply mutable cache fields in place only while the existing cache has the same source URI and the expected Instance controller owner. It rejects unowned or foreign-owned same-name resources without changing either the cache or workload.

13. `sourceModelUri` is immutable upstream. A URI change, cache disable, chart-disable cleanup, or Instance deletion follows three reconciles: delete the `LLMInferenceService`, delete the owned cache after the workload is absent, then create the desired cache or uncached workload.

14. Workload-first deletion is required because KServe rejects deletion of a cache referenced by an `InferenceService` or `LLMInferenceService`. Provider cleanup returns a wait result after requesting workload deletion.

15. The provider watches owned namespace caches only when LocalModel is enabled. A missing LocalModel CRD is harmless when caching is disabled. A request to enable caching still fails if chart support is off.

16. The provider may read cluster-scoped `LocalModelCache` objects for status but does not manage them. It has create, update, patch, and delete access only for namespace-cache reconciliation.

17. Instance readiness remains the workload's readiness. Cache state is appended to the Instance message as preparing, missing, ready copies, or failed copies. A cache message does not hide the endpoint or change a Ready workload to Provisioning.

18. Unit tests cover validation, create and apply, immutable-URI replacement, disable and chart-disable cleanup, staged Instance cleanup, owner rejection, credential authorization, and status messages. Helm tests cover disabled and enabled renders. The end-to-end test proves PVC admission, pod reuse without another download Job, status propagation, and clean disable.

## Security boundary

The upstream `kserve-localmodelnode-agent-role` is a `ClusterRole` bound with a `ClusterRoleBinding`. It grants `get`, `list`, and `watch` on Secrets and ServiceAccounts in every namespace. It grants cluster-wide create, update, patch, and delete on Jobs.

A compromised node agent can therefore read Secret data across tenants and create Jobs outside the configured download namespace. Restricting normal controller code to `kserve-localmodel-jobs` does not constrain stolen Kubernetes credentials.

The LocalModel controller also has cluster-wide PV, PVC, node, and cache permissions. The provider itself runs cluster-wide and can manage namespace caches, with Instance UID ownership checks as an application-level guard.

We accept these upstream boundaries only for an explicitly enabled installation. Disabled-by-default is exposure control, not privilege reduction after enablement. Operators MUST treat LocalModel enablement as granting a highly privileged cluster component.

We will not fork the KServe chart to narrow these roles. Each KServe upgrade MUST repeat the rendered-RBAC and source review below. If the feature becomes enabled by default, tenant ServiceAccount authorization remains absent, or upstream privileges expand, this decision must be revisited.

`modelSize` is not a security limit. In KServe `v0.21.0`, it is a required quantity but is not passed to the download Job or storage initializer as a byte ceiling. Filesystem or PV capacity may fail a large download, but callers MUST NOT rely on `modelSize` to bound untrusted content.

KServe's deletion webhook relies on cache status references and documents a race before status catches a new consumer. Provider workload-first deletion avoids expected in-use rejection. It does not turn the upstream status check into a transactional deletion guarantee.

## Alternatives rejected

### Manage cluster-scoped caches

Rejected because `LocalModelCache` is visible to workloads in every namespace and broadens ownership beyond one Instance. Namespace caches preserve the workload namespace as the sharing boundary.

### Let the provider manage node groups

Rejected because node affinity, local paths, storage classes, PV reclaim policy, and capacity are cluster operations. Instance authors choose an existing group but do not control node storage.

### Copy Instance Secrets into the job namespace

Rejected because it creates credential replicas and makes the provider a secret-distribution controller. Administrators provision tenant-bound download ServiceAccounts in the shared job namespace instead.

### Trust any ServiceAccount name

Rejected because KServe resolves the name in a shared namespace and does not authorize it against the cache namespace. Name validation alone lets one tenant select another tenant's credential source.

### Disable the storage initializer for cached Instances

Rejected because KServe's matching and PVC rewrite use the storage initialization path. Disabling it prevents LocalModel admission rather than optimizing it.

### Delete cache and workload together

Rejected because the KServe validating webhook protects referenced caches. Ordered deletion is deterministic and preserves the cache until the workload no longer uses it.

### Fork upstream RBAC

Rejected by decision. A fork would create an untested security and upgrade delta from the supported KServe chart. Opt-in deployment and explicit risk acceptance do not remove the residual risk.

## Consequences

Repeated pods on a cached node reuse one downloaded copy. The first population still downloads the full model, and a replacement node downloads it again.

Changing the model URI or disabling caching causes workload recreation. Cache changes can temporarily remove the serving endpoint while KServe releases the old reference and the provider creates the replacement.

Operators must install local storage, label nodes, create the job namespace, provision tenant-bound credentials, and monitor node capacity. Instance authors cannot create node groups through the provider.

The provider gains a dependency on KServe LocalModel `v1alpha1` APIs and upstream labels and status fields. A KServe upgrade requires compatibility, RBAC, authorization, size-limit, and deletion-lifecycle review.

Credentialed multi-tenant caches require administrators to bind each shared download ServiceAccount to one Instance namespace. Public caches may omit `serviceAccountName`.

## Release review

As of 2026-10-29, GitHub's official latest-release endpoint identifies `v0.21.0` as the newest non-draft, non-prerelease KServe release. It was published on 2026-09-25 and points to commit `d1482554fc4f66dd41aee70e01f5174e24f265bd`.

The implementation is already pinned to that release. There is no newer stable release to provide a fix, so the `v0.21.0` and newest-stable columns are the same source snapshot.

| Question | KServe `v0.21.0` | Newest stable | Decision impact |
| --- | --- | --- | --- |
| Is node-agent Secret, ServiceAccount, and Job RBAC still cluster-wide? | Yes. A ClusterRole grants Secret and ServiceAccount read access and Job management, and a ClusterRoleBinding binds it to the agent. | Yes, because newest stable is `v0.21.0`. | Residual cross-tenant Secret disclosure and arbitrary Job risk remains accepted only for opt-in installs. |
| Does `LocalModelNamespaceCache.serviceAccountName` gain tenant or namespace authorization? | No. The API accepts a string. Create and update validation check node groups only, then the agent resolves that ServiceAccount in the shared job namespace. | No, because newest stable is `v0.21.0`. | Provider-side tenant-bound authorization remains required and pending. |
| Is `modelSize` enforced as a download byte ceiling? | No. The quantity is required, but the download Job receives only the source URI and mount path. | No, because newest stable is `v0.21.0`. | Treat it as capacity metadata, not protection against oversized content. |
| Does lifecycle behavior remove the provider's need for workload-first cache deletion? | No. The deletion webhook rejects caches still referenced by ISVC or LLMISVC resources and notes a status-race gap. | No, because newest stable is `v0.21.0`. | Keep staged workload, cache, then workload reconciliation. |

## Sources

All release and security claims use official KServe sources pinned to the reviewed release.

1. Latest stable release metadata: <https://api.github.com/repos/kserve/kserve/releases/latest>; KServe `v0.21.0` release: <https://github.com/kserve/kserve/releases/tag/v0.21.0>
2. Reviewed tag commit: <https://github.com/kserve/kserve/commit/d1482554fc4f66dd41aee70e01f5174e24f265bd>
3. Upstream LocalModel default is disabled: <https://github.com/kserve/kserve/blob/d1482554fc4f66dd41aee70e01f5174e24f265bd/charts/kserve-resources/values.yaml#L63-L64>
4. Node-agent ClusterRole permissions: <https://github.com/kserve/kserve/blob/d1482554fc4f66dd41aee70e01f5174e24f265bd/charts/kserve-localmodel-resources/files/resources.yaml#L106-L183>
5. Node-agent ClusterRoleBinding: <https://github.com/kserve/kserve/blob/d1482554fc4f66dd41aee70e01f5174e24f265bd/charts/kserve-localmodel-resources/files/resources.yaml#L218-L232>
6. Namespace-cache API and ServiceAccount field: <https://github.com/kserve/kserve/blob/d1482554fc4f66dd41aee70e01f5174e24f265bd/pkg/apis/serving/v1alpha1/local_model_namespace_cache_types.go#L24-L44>
7. Namespace-cache validation checks only node groups on create and update: <https://github.com/kserve/kserve/blob/d1482554fc4f66dd41aee70e01f5174e24f265bd/pkg/webhook/admission/localmodelnamespacecache/local_model_namespace_cache_validation.go#L52-L74>
8. The agent resolves cache credentials in the shared job namespace: <https://github.com/kserve/kserve/blob/d1482554fc4f66dd41aee70e01f5174e24f265bd/pkg/controller/v1alpha1/localmodelnode/controller.go#L246-L274>
9. The download Job receives the URI and mount path, not `modelSize`: <https://github.com/kserve/kserve/blob/d1482554fc4f66dd41aee70e01f5174e24f265bd/pkg/controller/v1alpha1/localmodelnode/controller.go#L137-L221>
10. In-use deletion checks and the documented status race: <https://github.com/kserve/kserve/blob/d1482554fc4f66dd41aee70e01f5174e24f265bd/pkg/webhook/admission/localmodelnamespacecache/local_model_namespace_cache_validation.go#L77-L126>

## Verification

Verify the provider behavior from the implementation baseline:

```bash
go test ./internal/provider -run 'Test(ValidateLLMLocalModelCache|SyncLocalModelCache|SyncLocalModelCacheReplacesImmutableURI|SyncLocalModelCacheChartDisabledCleanup|ProviderCleanupStagesLocalModelCacheAfterWorkload|LocalModelCacheOwnershipProtection|StatusLLMLocalModelCache)$' -count=1
make test-helm
ENABLE_LOCAL_MODEL=true make test-e2e
```

Render and inspect the effective upstream permissions:

```bash
helm template provider-kserve charts/provider-kserve \
  --namespace everest-system \
  --set kserveResources.kserve.localmodel.enabled=true \
  >/tmp/provider-kserve-localmodel.yaml

yq ea 'select(.kind == "ClusterRole" and .metadata.name == "kserve-localmodelnode-agent-role")' \
  /tmp/provider-kserve-localmodel.yaml

kubectl auth can-i \
  --as=system:serviceaccount:everest-system:kserve-localmodelnode-agent \
  get secrets --all-namespaces
kubectl auth can-i \
  --as=system:serviceaccount:everest-system:kserve-localmodelnode-agent \
  create jobs.batch --all-namespaces
```

Verify ownership, admission, status, and reuse on an installed test Instance:

```bash
NS=models INSTANCE=smollm2-cached
kubectl -n "$NS" get localmodelnamespacecache "$INSTANCE" \
  -o jsonpath='{.metadata.ownerReferences[0].uid}{"\n"}{.spec.sourceModelUri}{"\n"}{.status.copies}{"\n"}'
kubectl -n "$NS" get llminferenceservice "$INSTANCE" \
  -o jsonpath='{.metadata.labels.internal\.serving\.kserve\.io/localmodel}{"\n"}{.metadata.labels.internal\.serving\.kserve\.io/localmodel-namespace}{"\n"}'
kubectl -n "$NS" get instance "$INSTANCE" -o jsonpath='{.status.message}{"\n"}'
```

Tenant-bound ServiceAccount authorization has positive, missing, unbound, cross-tenant, custom-namespace, and no-mutation regression cases:

```bash
go test ./internal/provider \
  -run 'TestValidateLLMLocalModelCacheServiceAccountAuthorization|TestSyncLocalModelCacheRejectsUnauthorizedServiceAccountBeforeMutation' \
  -count=1
```

Recheck the official release and tag before any KServe upgrade:

```bash
curl -fsSL https://api.github.com/repos/kserve/kserve/releases/latest | \
  jq -r '.tag_name, .draft, .prerelease, .published_at'
git ls-remote https://github.com/kserve/kserve.git refs/tags/v0.21.0
```
