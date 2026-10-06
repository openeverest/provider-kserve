# Serving LLMs Through the AI Gateway: Step-by-Step Setup

This guide deploys the full stack needed to expose `llm` Instances on one HTTPS
endpoint, where clients pick a model with the OpenAI `model` field and
authenticate with a per-model API key:

```
https://<your-domain>/v1   +   Authorization: Bearer <key>   +   {"model": "<name>"}
```

It was verified end to end on a Linode LKE cluster with three
NVIDIA RTX 4000 Ada GPUs, using `nip.io` and Let's Encrypt.

| Component | Required | Installed by |
|---|---|---|
| GPU nodes + NVIDIA GPU Operator (or device plugin) | yes | you |
| A `LoadBalancer` implementation with a public IP | yes | your cloud, or MetalLB on-prem |
| Gateway API CRDs | yes | step 1 |
| cert-manager | yes (KServe webhooks, gateway TLS) | step 2 |
| OpenEverest core | yes | step 3 |
| KServe controllers, Envoy Gateway, Envoy AI Gateway | yes | provider chart, step 4 |
| A domain name | yes (or `nip.io` for tests) | you, step 5 |
| Redis/Valkey | **only for token quotas** | you, step 6 |

You do **not** install Envoy separately: the provider chart bundles Envoy
Gateway (the proxy and its controller) and Envoy AI Gateway, and creates one
shared `Gateway` with a public `LoadBalancer`.

## Prerequisites

- A recent Kubernetes (verified on 1.36), `kubectl` and `helm` 3.
- GPUs advertised as `nvidia.com/gpu` (check with
  `kubectl get nodes -o custom-columns=NAME:.metadata.name,GPU:.status.allocatable.nvidia\\.com/gpu`).
- For HTTP-01 certificates: the load balancer must be reachable from the
  internet on ports 80 and 443. Otherwise use DNS-01
  ([ai-gateway-tls.md](ai-gateway-tls.md#dns-01-certificates)).
- A provider image that includes API-key support. 
- From a source checkout, vendor the chart dependencies once:
  `make helm-deps`.

In the commands below, `NS=provider-kserve` is the release namespace.

## 1. Gateway API CRDs

cert-manager's Gateway API support (which issues the Gateway's TLS certificate)
refuses to start if the CRDs are missing, so install the exact set bundled with
the Envoy Gateway version the chart pins (v1.5.9):

```sh
helm pull oci://docker.io/envoyproxy/gateway-helm --version v1.5.9 --untar -d /tmp/eg
kubectl apply --server-side -f /tmp/eg/gateway-helm/crds/gatewayapi-crds.yaml
```

## 2. cert-manager (with Gateway API support)

Skip the install if cert-manager already runs, but make sure
`enableGatewayAPI` is on (and restart it if you install the CRDs afterwards).

```sh
helm repo add jetstack https://charts.jetstack.io
helm upgrade -i cert-manager jetstack/cert-manager -n cert-manager --create-namespace \
  --version v1.16.2 \
  --set crds.enabled=true \
  --set config.apiVersion=controller.config.cert-manager.io/v1alpha1 \
  --set config.kind=ControllerConfiguration \
  --set config.enableGatewayAPI=true \
  --wait
```

## 3. OpenEverest core

```sh
helm repo add openeverest https://openeverest.github.io/helm-charts/
helm upgrade -i everest openeverest/openeverest -n everest-system --create-namespace \
  --devel --version 2.0.0-dev.2 \
  --set server.initialAdminPassword=<admin-password> \
  --wait

# The 2.0.0-dev.2 chart ships older CRD schemas; apply the release-2.0 ones.
base=https://raw.githubusercontent.com/openeverest/openeverest/main/config/crd/bases
for f in core.openeverest.io_providers core.openeverest.io_instances \
         core.openeverest.io_instancepresets monitoring.openeverest.io_monitoringconfigs \
         backup.openeverest.io_backupclasses backup.openeverest.io_backups \
         backup.openeverest.io_restores backup.openeverest.io_backupstorages; do
  kubectl apply --server-side --force-conflicts -f $base/$f.yaml
done
```

## 4. Provider chart

Create a values file. The `aiGateway` section is completed in step 5.

```yaml
# values.yaml
image:
  repository: <registry>/provider-kserve
  tag: <tag>
envoy-gateway:
  enabled: true           # false if Envoy Gateway already runs (see below)
envoy-ai-gateway:
  enabled: true           # false if Envoy AI Gateway already runs
aiGateway:
  enabled: true
  gatewayService:
    type: LoadBalancer
```

cert-manager stays off (`cert-manager.enabled=false` is the default) because
it was installed in step 2. If the cluster already runs Envoy Gateway (v1.5+),
leave `envoy-gateway.enabled` off and apply the `envoy-gateway.config`
extension-manager settings from the chart's `values.yaml` to it instead;
running two Envoy Gateways makes them fight over the same Gateways.

The chart fetches the KServe CRDs from `ghcr.io` in a hook Job
(`kserveCRDs.install`, on by default) and applies the KServe runtimes and LLM
presets in a second hook Job once the controllers serve their webhooks, so one
command installs everything:

```sh
CHART=oci://ghcr.io/openeverest/charts/provider-kserve   # or charts/provider-kserve
helm upgrade -i provider-kserve $CHART -n $NS --create-namespace -f values.yaml \
  --set aiGateway.auth.allowInsecureHTTP=true        # temporary, until step 5
```

It takes 2–3 minutes.

Get the public address of the shared Gateway:

```sh
kubectl -n $NS get gateway provider-kserve-ai-gateway \
  -o jsonpath='{.status.addresses[0].value}'
```

## 5. Domain and HTTPS

API keys are only accepted over HTTPS. The chart refuses to render with
`aiGateway.auth.enabled` (the default) and TLS off, unless
`aiGateway.auth.allowInsecureHTTP=true` (development only).

1. **Pick a hostname** and point an `A` record at the address from step 4.
   For tests, use `<ip-with-dashes>.nip.io` (for example `203-0-113-10.nip.io`),
   which needs no DNS setup.
2. **Create an issuer.** For HTTP-01 through the Gateway:

   ```yaml
   apiVersion: cert-manager.io/v1
   kind: ClusterIssuer
   metadata:
     name: letsencrypt-prod-ai-gateway
   spec:
     acme:
       server: https://acme-v02.api.letsencrypt.org/directory
       privateKeySecretRef:
         name: letsencrypt-prod-ai-gateway-account
       solvers:
         - http01:
             gatewayHTTPRoute:
               parentRefs:
                 - name: provider-kserve-ai-gateway
                   namespace: provider-kserve
                   kind: Gateway
                   sectionName: acme-http01
   ```

   Use the Let's Encrypt staging server first if you expect many retries. For
   DNS-01 (wildcards, or no public port 80) see
   [ai-gateway-tls.md](ai-gateway-tls.md#dns-01-certificates) and leave
   `acmeHTTP01` off.
3. **Enable TLS** in `values.yaml` and upgrade, now without
   `allowInsecureHTTP`:

   ```yaml
   aiGateway:
     enabled: true
     tls:
       enabled: true
       hostname: llm.example.com
       acmeHTTP01: true           # port-80 listener for ACME challenges only
       issuerRef:
         name: letsencrypt-prod-ai-gateway
         kind: ClusterIssuer
   ```

   ```sh
   helm upgrade provider-kserve $CHART -n $NS -f values.yaml
   kubectl -n $NS wait certificate/provider-kserve-ai-gateway-tls --for=condition=Ready --timeout=10m
   curl -s -o /dev/null -w '%{http_code}\n' https://llm.example.com/v1/models   # 401 without a key
   ```

   The load balancer address stays the same when listeners change. Model
   routes and the API key policy attach only to the HTTPS listener; port 80
   answers ACME challenges and returns 404 for everything else.

## 6. Token quotas (optional, needs Redis/Valkey)

Without Redis everything above works: keys, per-model authorization, and token
metrics. `tokenLimitPerHour` is simply not enforced. To enforce it, point the
bundled Envoy Gateway at an existing Redis-compatible service:

```yaml
envoy-gateway:
  config:
    envoyGateway:
      rateLimit:
        backend:
          type: Redis
          redis:
            url: redis.provider-kserve.svc.cluster.local:6379
```

After `helm upgrade`, Envoy Gateway starts an `envoy-ratelimit` Deployment
and the provider creates a token-limit policy per model. Quotas count real
tokens per API key and model; exhausted quotas return HTTP 429. Use a durable,
highly available Redis/Valkey in production: counters are lost if it restarts.

## 7. Model download memory

KServe's storage initializer, which downloads `hf://` and `s3://` models, is
limited to 1 GiB of memory by default and is OOM-killed on multi-GB downloads
(seen with Qwen3 0.6B and 1.7B). Raise it for all llm Instances in
`values.yaml` and upgrade:

```yaml
storageInitializer:
  resources:
    limits:
      memory: 4Gi
      cpu: "2"
```

The provider applies it to every model pod it creates, so it survives
`helm upgrade` (unlike editing KServe's `inferenceservice-config`).

## 8. Deploy a model

```yaml
apiVersion: core.openeverest.io/v1alpha1
kind: Instance
metadata:
  name: qwen-small
  namespace: team-a
spec:
  providerRef:
    name: provider-kserve
  topology:
    type: llm
    parameters:
      externalAccess: EnvoyAIGateway
      tokenLimitPerHour: 3000      # enforced only with step 6
  components:
    llmEngine:
      type: vllm
      replicas: 1
      resources:
        requests: { cpu: "3", memory: 12Gi, nvidia.com/gpu: "1" }
        limits: { memory: 12Gi, nvidia.com/gpu: "1" }
      parameters:
        modelURI: hf://Qwen/Qwen3-0.6B
        modelName: qwen3-0.6b      # must be unique across the Gateway
```

Wait until the Instance is `Ready` (image pull and download take a few
minutes on first start):

```sh
kubectl -n team-a get instance qwen-small -w
```

## 9. Use it

The provider generates a random key per Instance (kept for the life of the
Instance, like a database password) and publishes everything a client needs
in the Instance connection details: the UI, or the `<instance>-conn` Secret.

| Field | Content |
|---|---|
| `uri` | `https://<domain>/v1` — use as the OpenAI `base_url` |
| `password` | the API key |
| `username` | the key ID (not secret; appears in metrics) |
| `model` | the name to send in the `model` field |

```sh
c() { kubectl -n team-a get secret qwen-small-conn -o jsonpath="{.data.$1}" | base64 -d; }
curl "$(c uri)/chat/completions" \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $(c password)" \
  -d "{\"model\":\"$(c model)\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello\"}]}"
```

```python
from openai import OpenAI
client = OpenAI(base_url="<uri>", api_key="<password>")
client.chat.completions.create(model="<model>", messages=[{"role": "user", "content": "Hello"}])
```

Anthropic-style clients can send the key in `x-api-key` instead.

| Response | Meaning |
|---|---|
| 401 | missing, unknown or rotated key |
| 403 | valid key, but for a different model (or an unknown model) |
| 404 on port 80 | plain HTTP is not served; use `https://` |
| 429 | token quota for this key and model is used up |

## 10. Operations

- **Rotate a key:** `kubectl -n <ns> delete secret <instance>-ai-gateway-key`.
  A new key is generated within seconds, connection details update, and the
  old key stops working.
- **Remove a model:** deleting the Instance deletes its key. Switching
  `externalAccess` away from `EnvoyAIGateway` keeps the key but it stops
  working until the Instance is exposed through the Gateway again.
- **Metrics:** gateway GenAI metrics carry an `openeverest_key_id` label
  (see [observability-gateway.md](observability-gateway.md)).
- **Inspect the rendered auth policy:**
  `kubectl -n $NS get securitypolicy provider-kserve-ai-gateway-api-keys -o yaml`.

## Troubleshooting

| Symptom | Check |
|---|---|
| cert-manager crash-loops: "Gateway API CRDs do not seem to be present" | step 1, then `kubectl -n cert-manager rollout restart deploy/cert-manager` |
| `llmisvc-controller-manager` stuck in `ContainerCreating` | step 4 pass b) not run yet (webhook certificate Issuer comes from `kserveResources`) |
| Certificate not Ready | `kubectl -n $NS get challenge`; the hostname must resolve to the Gateway address and port 80 must be reachable |
| Model pod `Init:OOMKilled` | step 7 |
| New model pod `Pending` (`Insufficient nvidia.com/gpu`) after a change | the old pod still holds the GPU; GPU Instances roll out without surge (`rolloutStrategy`), so check `computeProfile` is `gpu` |
| Instance stays `Provisioning` with "no Gateway controller has accepted it yet" | provider image older than this guide; upgrade the provider |
| Chart fails: "aiGateway.auth requires HTTPS" | finish step 5, or set `aiGateway.auth.allowInsecureHTTP=true` for local development |
| Instance `Failed`: model name already served | another Instance on the Gateway uses the same `modelName`; pick a unique one |
