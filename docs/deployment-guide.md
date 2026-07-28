# Deployment Guide: Envoy AI Gateway Networking

This guide covers how the Envoy AI Gateway's `LoadBalancer` service gets an
external address across different environments — local development, cloud
providers, and on-prem GPU infrastructure.

## Table of Contents

- [Architecture Overview](#architecture-overview)
- [How the Gateway Gets Its Address](#how-the-gateway-gets-its-address)
- [Local Development (k3d / kind / minikube)](#local-development)
- [Cloud Providers](#cloud-providers)
  - [AWS (EKS)](#aws-eks)
  - [Google Cloud (GKE)](#google-cloud-gke)
  - [Azure (AKS)](#azure-aks)
- [On-Prem / Bare-Metal GPU Clusters](#on-prem--bare-metal-gpu-clusters)
  - [MetalLB (L2 / BGP)](#metallb)
  - [kube-vip](#kube-vip)
  - [PureLB](#purelb)
- [GPU Cloud Providers (Modal, CoreWeave, Lambda, etc.)](#gpu-cloud-providers)
  - [CoreWeave](#coreweave)
  - [Lambda Cloud](#lambda-cloud)
  - [Modal](#modal)
  - [RunPod](#runpod)
  - [Crusoe Cloud](#crusoe-cloud)
- [Alternative: NodePort + External LB](#alternative-nodeport--external-lb)
- [Alternative: Ingress Controller Integration](#alternative-ingress-controller-integration)
- [TLS / Production Hardening](#tls--production-hardening)
- [Verifying the Gateway](#verifying-the-gateway)

---

## Architecture Overview

```
                         ┌──────────────────────────┐
                         │    External Client        │
                         │  curl $GATEWAY_URL/v1/... │
                         └────────────┬─────────────┘
                                      │
                              ┌───────▼────────┐
                              │  LoadBalancer   │
                              │  (External IP)  │
                              │  :80 / :443     │
                              └───────┬────────┘
                                      │
                    ┌─────────────────▼──────────────────┐
                    │     Envoy Gateway (single LB)      │
                    │   provider-kserve-ai-gateway        │
                    │                                     │
                    │  Routes by x-ai-eg-model header     │
                    │  Token metering per model            │
                    │  Per-user rate limiting (x-user-id)  │
                    └──┬──────────────┬──────────────┬────┘
                       │              │              │
              ┌────────▼───┐  ┌──────▼─────┐  ┌────▼────────┐
              │ AIGateway  │  │ AIGateway  │  │ AIGateway   │
              │ Route A    │  │ Route B    │  │ Route C     │
              │ model:     │  │ model:     │  │ model:      │
              │ smollm     │  │ llama-8b   │  │ mixtral     │
              └────┬───────┘  └─────┬──────┘  └──────┬──────┘
                   │                │                 │
              ┌────▼───────┐  ┌─────▼──────┐  ┌──────▼──────┐
              │ Inference  │  │ Inference  │  │ Inference   │
              │ Pool       │  │ Pool       │  │ Pool        │
              │ (vLLM pods)│  │ (vLLM pods)│  │ (vLLM pods) │
              └────────────┘  └────────────┘  └─────────────┘
```

**Key:** There is one shared Gateway (`LoadBalancer`) per provider installation.
Each model Instance with `enableAIGateway: true` registers an `AIGatewayRoute`
on that Gateway. Multi-tenancy is achieved through header-based routing
(`x-ai-eg-model`) and per-user token quotas (`x-user-id`).

---

## How the Gateway Gets Its Address

The Envoy AI Gateway creates a Kubernetes `Service` of type `LoadBalancer`:

```yaml
# values.yaml
aiGateway:
  enabled: true
  gatewayService:
    type: LoadBalancer   # <-- this determines how you get an external address
```

Kubernetes itself does **not** assign external IPs — it delegates to a
**LoadBalancer controller** that runs in the cluster. Different environments
provide this controller differently:

| Environment | Controller | IP Assignment |
|---|---|---|
| AWS EKS | AWS Load Balancer Controller | Elastic IP / NLB DNS |
| GCP GKE | GKE built-in | Static/ephemeral external IP |
| Azure AKS | Azure built-in | Public IP resource |
| On-prem bare metal | MetalLB / kube-vip / PureLB | IP from a configured pool |
| Local k3d/kind | None by default | `<pending>` forever |
| GPU cloud (CoreWeave) | Cloud LB or MetalLB | Depends on provider |

When the external IP is assigned, the Gateway's `status.addresses` is populated
and the provider publishes it in the Instance connection details:

```bash
export GATEWAY_URL="http://$(kubectl get gateway \
  provider-kserve-ai-gateway \
  -o jsonpath='{.status.addresses[0].value}')"

curl "$GATEWAY_URL/v1/chat/completions" \
  -H 'Content-Type: application/json' \
  -H 'x-user-id: user123' \
  -d '{"model":"llama-3.1-8b-instruct","messages":[{"role":"user","content":"Hello"}]}'
```

---

## Local Development

### Problem

k3d, kind, and minikube run Kubernetes inside Docker containers. The node IPs
(e.g. `172.21.0.x`) are on Docker's internal network and are not roachable from
the host. The default k3d config disables `servicelb`:

```yaml
# dev/k3d_config.yaml
options:
  k3s:
    extraArgs:
      - arg: "--disable=traefik,metrics-server,servicelb"
```

So `LoadBalancer` services stay `<pending>` forever.

### Solution: Install MetalLB

```bash
# 1. Find the Docker network CIDR for your k3d cluster
docker network inspect k3d-provider-kserve-test \
  -f '{{(index .IPAM.Config 0).Subnet}}'
# Example output: 172.21.0.0/16

# 2. Install MetalLB
kubectl apply -f https://raw.githubusercontent.com/metallb/metallb/v0.14.9/config/manifests/metallb-native.yaml

kubectl wait --namespace metallb-system \
  --for=condition=ready pod \
  --selector=app=metallb \
  --timeout=120s

# 3. Configure an IP pool from the Docker network range
#    (pick a range that won't conflict with existing containers)
cat <<EOF | kubectl apply -f -
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: k3d-pool
  namespace: metallb-system
spec:
  addresses:
  - 172.21.255.200-172.21.255.250
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: k3d-l2
  namespace: metallb-system
EOF

# 4. Verify the Gateway got an IP
kubectl get svc -l gateway.envoyproxy.io/owning-gateway-name
# EXTERNAL-IP should now show 172.21.255.200 (or similar)
```

> **For kind:** use the kind Docker network (`kind`). For minikube, use
> `minikube tunnel` instead (it acts as a built-in LoadBalancer controller).

### Alternative: port-forward (quick testing only)

```bash
kubectl port-forward svc/envoy-default-provider-kserve-ai-gateway-<hash> 8080:80
curl http://localhost:8080/v1/chat/completions ...
```

This bypasses the LoadBalancer entirely and should not be used for integration
testing or production validation.

---

## Cloud Providers

On managed Kubernetes, `LoadBalancer` services work out of the box — the cloud
provider's controller assigns an external IP or DNS name automatically.

### AWS (EKS)

**How it works:** EKS uses the
[AWS Load Balancer Controller](https://kubernetes-sigs.github.io/aws-load-balancer-controller/)
to provision a Network Load Balancer (NLB) for each `LoadBalancer` service.

```bash
# Install the AWS LB controller (if not already present)
helm repo add eks https://aws.github.io/eks-charts
helm install aws-load-balancer-controller eks/aws-load-balancer-controller \
  -n kube-system \
  --set clusterName=<your-cluster>

# Deploy the provider with AI Gateway enabled
helm install provider-kserve ./charts/provider-kserve \
  --set aiGateway.enabled=true

# The NLB is provisioned automatically. Get the DNS name:
kubectl get svc -l gateway.envoyproxy.io/owning-gateway-name
# EXTERNAL-IP: k8s-default-envoy-xxxx.elb.us-east-1.amazonaws.com
```

**Optional annotations for the Envoy proxy service:**

```yaml
# values.yaml
aiGateway:
  enabled: true
  gatewayService:
    type: LoadBalancer
```

To customize NLB annotations, patch the EnvoyProxy resource or use an
`EnvoyPatchPolicy`:

```yaml
# Example: internal NLB (private subnets only)
# Add via EnvoyProxy spec.provider.kubernetes.envoyService.annotations
service.beta.kubernetes.io/aws-load-balancer-scheme: internal
service.beta.kubernetes.io/aws-load-balancer-type: external
service.beta.kubernetes.io/aws-load-balancer-nlb-target-type: ip
```

**GPU nodes on EKS:** Use
[EKS GPU AMIs](https://docs.aws.amazon.com/eks/latest/userguide/eks-optimized-ami.html)
with the NVIDIA device plugin. The LB setup is independent of GPU scheduling —
Envoy runs on any node, vLLM pods are scheduled to GPU nodes via resource
requests (`nvidia.com/gpu`).

### Google Cloud (GKE)

**How it works:** GKE has a built-in controller that creates a Google Cloud
Network Load Balancer for each `LoadBalancer` service. No additional setup.

```bash
# Deploy — GKE handles the rest
helm install provider-kserve ./charts/provider-kserve \
  --set aiGateway.enabled=true

# Get the external IP
kubectl get svc -l gateway.envoyproxy.io/owning-gateway-name
# EXTERNAL-IP: 34.123.45.67
```

**Static IP (recommended for production):**

```bash
# Reserve a static IP
gcloud compute addresses create ai-gateway-ip --region=us-central1

# Reference it via annotation on the service
# (patch the EnvoyProxy or use EnvoyPatchPolicy)
```

**GPU nodes on GKE:** Use
[GKE GPU node pools](https://cloud.google.com/kubernetes-engine/docs/how-to/gpus)
with auto-provisioning. The NVIDIA device plugin is installed automatically.

### Azure (AKS)

**How it works:** AKS provisions an Azure Load Balancer for each `LoadBalancer`
service.

```bash
helm install provider-kserve ./charts/provider-kserve \
  --set aiGateway.enabled=true

kubectl get svc -l gateway.envoyproxy.io/owning-gateway-name
# EXTERNAL-IP: 20.84.123.45
```

**Internal LB (private VNet only):**

```yaml
# Annotation on the Envoy service
service.beta.kubernetes.io/azure-load-balancer-internal: "true"
```

**GPU nodes on AKS:** Use
[AKS GPU node pools](https://learn.microsoft.com/en-us/azure/aks/gpu-cluster)
(NC/ND-series VMs). Install the NVIDIA device plugin via the AKS GPU extension.

---

## On-Prem / Bare-Metal GPU Clusters

On-prem clusters (with NVIDIA DGX, HGX, or commodity GPU servers) don't have a
cloud LB controller. You need a software LoadBalancer implementation.

### MetalLB

[MetalLB](https://metallb.io/) is the most widely used bare-metal LoadBalancer.
It supports L2 (ARP/NDP) and BGP modes.

**L2 mode (simplest, single subnet):**

```bash
# Install MetalLB
kubectl apply -f https://raw.githubusercontent.com/metallb/metallb/v0.14.9/config/manifests/metallb-native.yaml

kubectl wait --namespace metallb-system \
  --for=condition=ready pod --selector=app=metallb --timeout=120s

# Configure IP pool — use free IPs on your network
cat <<EOF | kubectl apply -f -
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: ai-gateway-pool
  namespace: metallb-system
spec:
  addresses:
  - 10.0.50.200-10.0.50.210      # adjust to your network
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: ai-gateway-l2
  namespace: metallb-system
EOF
```

**BGP mode (multi-rack, production):**

```yaml
apiVersion: metallb.io/v1beta2
kind: BGPPeer
metadata:
  name: tor-switch
  namespace: metallb-system
spec:
  myASN: 64512
  peerASN: 64513
  peerAddress: 10.0.0.1        # your ToR switch
---
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: ai-gateway-pool
  namespace: metallb-system
spec:
  addresses:
  - 203.0.113.100-203.0.113.110  # routable IPs from your network team
---
apiVersion: metallb.io/v1beta1
kind: BGPAdvertisement
metadata:
  name: ai-gateway-bgp
  namespace: metallb-system
spec:
  ipAddressPools:
  - ai-gateway-pool
```

> **L2 vs BGP:** L2 is simpler but limited to a single network segment and
> provides failover (not load balancing) at the IP level. BGP integrates with
> your network fabric and supports ECMP for true load distribution. For most
> on-prem GPU clusters, L2 mode is sufficient since Envoy itself handles request
> routing.

### kube-vip

[kube-vip](https://kube-vip.io/) provides both a virtual IP for the control
plane and LoadBalancer services, using ARP or BGP. It runs as a DaemonSet.

```bash
# Install kube-vip cloud controller
kubectl apply -f https://raw.githubusercontent.com/kube-vip/kube-vip-cloud-provider/main/manifest/kube-vip-cloud-controller.yaml

# Configure the IP range via ConfigMap
kubectl create configmap -n kube-system kubevip \
  --from-literal range-global=10.0.50.200-10.0.50.210
```

### PureLB

[PureLB](https://purelb.gitlab.io/docs/) is an alternative that uses standard
Linux networking (no ARP/BGP). Useful when your network team restricts
gratuitous ARP.

---

## GPU Cloud Providers

These are Kubernetes-based GPU cloud platforms. The LoadBalancer support varies.

### CoreWeave

CoreWeave runs managed Kubernetes with direct `LoadBalancer` support.

```bash
# Deploy normally — CoreWeave assigns an external IP
helm install provider-kserve ./charts/provider-kserve \
  --set aiGateway.enabled=true

# GPU scheduling is automatic — request nvidia.com/gpu in vLLM pod resources
# CoreWeave supports A100, H100, and other NVIDIA GPUs natively
```

CoreWeave clusters behave like a cloud provider — `LoadBalancer` services get
external IPs without additional setup.

### Lambda Cloud

Lambda provides GPU VMs (not managed Kubernetes). You self-manage Kubernetes:

1. Provision GPU VMs (A100/H100) from Lambda Cloud
2. Bootstrap Kubernetes with [k3s](https://k3s.io/), [kubeadm](https://kubernetes.io/docs/setup/production-environment/tools/kubeadm/), or [Talos](https://www.talos.dev/)
3. Install [NVIDIA GPU Operator](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/getting-started.html) for device plugin + drivers
4. Install **MetalLB** (L2 mode, using Lambda's private network range)
5. Deploy the provider chart with `aiGateway.enabled=true`

```bash
# On Lambda GPU VMs with k3s
curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="--disable=servicelb,traefik" sh -

# Install GPU operator
helm install gpu-operator nvidia/gpu-operator \
  --namespace gpu-operator --create-namespace

# Install MetalLB with Lambda's subnet
# (check your VM's private IP range)
```

### Modal

Modal is a **serverless GPU platform** — it does **not** expose Kubernetes.
You cannot deploy Kubernetes workloads or Envoy to Modal directly.

**Integration options:**

1. **Modal as a backend only:** Run the Envoy AI Gateway on your own
   Kubernetes cluster (cloud or on-prem) and configure an `AIServiceBackend`
   pointing to Modal's API endpoint. This requires custom `AIGatewayRoute`
   configuration outside the provider's automated flow.

2. **Don't use Modal for this:** Modal is designed for batch/serverless
   workloads, not persistent Kubernetes services. Use CoreWeave, Lambda, or
   RunPod instead if you need GPU Kubernetes.

### RunPod

RunPod offers both serverless GPU and
[GPU Kubernetes pods](https://docs.runpod.io/pods/overview). For persistent
serving:

1. Use RunPod's pod (VM-like) offering with Kubernetes self-managed on top
2. Install MetalLB or use RunPod's networking for external access
3. Deploy the provider chart normally

### Crusoe Cloud

Crusoe provides managed Kubernetes with GPU support and built-in `LoadBalancer`
service support, similar to CoreWeave:

```bash
helm install provider-kserve ./charts/provider-kserve \
  --set aiGateway.enabled=true
# External IP assigned automatically
```

---

## Alternative: NodePort + External LB

If you can't install MetalLB (e.g. restricted cluster), use `NodePort` with an
external load balancer (HAProxy, nginx, F5, etc.):

```yaml
# values.yaml
aiGateway:
  enabled: true
  gatewayService:
    type: NodePort
```

Then configure your external LB to forward to `<any-node-ip>:<nodePort>`.

> **Note:** The Gateway `status.addresses` will not be populated with NodePort.
> You'll need to manually configure the external URL.

---

## Alternative: Ingress Controller Integration

If you already have an Ingress controller (nginx, Traefik, HAProxy Ingress) with
an external IP, you can front the Envoy Gateway with an Ingress:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: ai-gateway-ingress
  annotations:
    nginx.ingress.kubernetes.io/proxy-read-timeout: "120"
    nginx.ingress.kubernetes.io/proxy-send-timeout: "120"
spec:
  rules:
  - host: ai-gateway.example.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: envoy-default-provider-kserve-ai-gateway-<hash>
            port:
              number: 80
```

Set the Envoy service to `ClusterIP` in this case:

```yaml
aiGateway:
  gatewayService:
    type: ClusterIP
```

---

## TLS / Production Hardening

The default setup uses plain HTTP. For production:

### 1. TLS termination at the Gateway

```yaml
# values.yaml
aiGateway:
  scheme: https
  listener:
    protocol: HTTPS
    port: 443
```

You'll also need a TLS certificate. Use cert-manager with Let's Encrypt or
provide a pre-existing `Secret`:

```yaml
# Add to the Gateway listener spec
tls:
  mode: Terminate
  certificateRefs:
  - kind: Secret
    name: ai-gateway-tls
```

### 2. Authentication

The AI Gateway does not enforce authentication by default. Add API key or
JWT validation via Envoy Gateway's `SecurityPolicy`:

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: SecurityPolicy
metadata:
  name: ai-gateway-auth
spec:
  targetRefs:
  - group: gateway.networking.k8s.io
    kind: Gateway
    name: provider-kserve-ai-gateway
  jwt:
    providers:
    - name: my-oidc
      issuer: https://auth.example.com
      remoteJWKS:
        uri: https://auth.example.com/.well-known/jwks.json
```

### 3. Network policies

Restrict access to the Gateway and backend pods:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ai-gateway-ingress
spec:
  podSelector:
    matchLabels:
      gateway.envoyproxy.io/owning-gateway-name: provider-kserve-ai-gateway
  ingress:
  - from:
    - ipBlock:
        cidr: 10.0.0.0/8     # internal network only
    ports:
    - port: 80
```

---

## Verifying the Gateway

After deployment, verify the full flow:

```bash
# 1. Check the Gateway has an address
kubectl get gateway provider-kserve-ai-gateway
# NAME                         CLASS                        ADDRESS         PROGRAMMED
# provider-kserve-ai-gateway   provider-kserve-ai-gateway   34.123.45.67    True

# 2. Check routes are accepted
kubectl get aigatewayroute
# NAME                           STATUS
# ai-gateway-smollm-ai-gateway   Accepted

# 3. Check the Envoy service has an external IP
kubectl get svc -l gateway.envoyproxy.io/owning-gateway-name
# NAME                                                 TYPE           EXTERNAL-IP
# envoy-default-provider-kserve-ai-gateway-233f5692    LoadBalancer   34.123.45.67

# 4. Set the gateway URL
export GATEWAY_URL="http://$(kubectl get gateway \
  provider-kserve-ai-gateway \
  -o jsonpath='{.status.addresses[0].value}')"

# 5. Send a test request
curl -s "$GATEWAY_URL/v1/chat/completions" \
  -H 'Content-Type: application/json' \
  -H 'x-user-id: test-user' \
  -d '{
    "model": "smollm2-135m-instruct",
    "messages": [{"role": "user", "content": "Say hello in one sentence."}],
    "max_tokens": 50
  }' | jq .

# 6. Check token metering (if rate limiting is configured)
curl -s "$GATEWAY_URL/v1/chat/completions" \
  -H 'Content-Type: application/json' \
  -H 'x-user-id: test-user' \
  -d '{
    "model": "smollm2-135m-instruct",
    "messages": [{"role": "user", "content": "Count to 100."}],
    "max_tokens": 500
  }' -w "\nHTTP Status: %{http_code}\n"
# Eventually returns HTTP 429 when quota is exhausted
```

---

## Summary: Which LoadBalancer for Which Environment

| Environment | Recommended LoadBalancer | Setup Effort |
|---|---|---|
| **AWS EKS** | AWS LB Controller (NLB) | Low (built-in or one helm install) |
| **GCP GKE** | GKE built-in | Zero |
| **Azure AKS** | Azure built-in | Zero |
| **On-prem (DGX/HGX)** | MetalLB (BGP for multi-rack, L2 for single subnet) | Medium |
| **CoreWeave** | Built-in | Zero |
| **Crusoe Cloud** | Built-in | Zero |
| **Lambda Cloud** | MetalLB on self-managed k8s | Medium-High |
| **RunPod** | MetalLB on self-managed k8s | Medium-High |
| **Modal** | N/A (serverless, no k8s) | Not applicable |
| **Local k3d/kind** | MetalLB (L2 on Docker network) | Low |
| **minikube** | `minikube tunnel` | Zero |
