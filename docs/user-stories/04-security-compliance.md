# Persona 4 — Security & Compliance Engineers

**Who:** security and compliance engineers responsible for how models are
exposed and governed across teams.

**What they value:** every team reaches models through a controlled **LLM
gateway**, with guardrails, rate limits, and a shared model pool — so access is
auditable and safe by default.

**Guiding principle:** models are not reachable directly; the gateway is the
policy enforcement point.

> Most stories here are **gaps** today: the provider can create the workload and
> a Gateway route, but does not yet layer gateway policy, guardrails, or an LLM
> judge. They are captured so the security surface is designed deliberately
> rather than bolted on. This applies to both `llm` and `predictor`, but the LLM
> gateway concerns are primarily about the `llm` topology.

---

## 4.1 Force all model traffic through an LLM gateway

**Status:** Partial — `enableGatewayRouting` provisions a Gateway/HTTPRoute; it
is not yet a mandatory, policy-bearing enforcement point.

As a security engineer, I want every request to a model to pass through a managed
LLM gateway, so that I have one place to apply auth, logging, and policy.

**Acceptance criteria**
- When gateway routing is enabled, the model is reachable **only** through the
  gateway (direct pod/Service access is restricted).
- The gateway terminates and authenticates client traffic.

**Provider mapping (today)**
```yaml
topology: { type: llm, parameters: { enableGatewayRouting: true } }
```

**Gap to close:** an option to disable direct Service exposure when gateway
routing is on (e.g. `service.serviceType: None` + NetworkPolicy), so the gateway
is the only ingress.

---

## 4.2 Rate-limit and quota per team/consumer

**Status:** Gap.

As a compliance engineer, I want per-team rate limits and token/RPM quotas at the
gateway, so that one consumer cannot exhaust shared capacity or budget.

**Acceptance criteria**
- Limits are expressible per API key / team / route (RPM, tokens/min,
  concurrency).
- Exceeding a limit returns a standard 429 and is recorded.

**Gap to close:** gateway rate-limit policy wired from provider config (e.g. a
`gateway.rateLimits` block rendered onto the Gateway/HTTPRoute or a policy CRD).

---

## 4.3 Shared model pool behind one endpoint

**Status:** Gap.

As a security engineer, I want teams to consume from a governed pool of models
behind a single gateway endpoint, so that access is centralized and models can be
swapped without changing clients.

**Acceptance criteria**
- Multiple backing models are addressable by `model` name through one gateway.
- Adding/removing a model from the pool does not change client configuration.
- Access to specific models can be scoped per consumer.

**Gap to close:** a pool/route abstraction that fronts several `llm` Instances
under one gateway and maps `model` names to backends.

---

## 4.4 Input/output guardrails for LLMs

**Status:** Gap.

As a compliance engineer, I want prompt and response guardrails (PII redaction,
toxicity, jailbreak, topic policy), so that unsafe content is blocked before it
reaches the model or the user.

**Acceptance criteria**
- Guardrail checks run on requests and responses at the gateway.
- A blocked request is rejected with a policy reason and audit record.
- Guardrail policy is configurable without redeploying the model.

**Gap to close:** a guardrails hook (e.g. a filter/extension on the Inference
Gateway) exposed as opt-in provider config referencing a guardrails service.

---

## 4.5 LLM-as-judge evaluation on the path

**Status:** Gap.

As a security engineer, I want an LLM judge to score or gate responses (safety,
policy adherence), so that risky outputs are caught in production, not just in
eval.

**Acceptance criteria**
- Responses can be routed through a judge model that scores against policy.
- The judge decision (allow/flag/block) is recorded and optionally enforced.
- The judge is itself a governed model in the pool.

**Gap to close:** a response-stage extension that calls a judge Instance and
enforces its verdict; reuse the guardrails hook from 4.4.

---

## 4.6 Restrict who and where can reach a model

**Status:** Partial.

As a security engineer, I want to constrain network access to a model, so that
only approved callers or CIDRs can reach it.

**Acceptance criteria**
- `LoadBalancer` exposure can be limited to allowed source CIDRs.
- In-cluster access can be constrained by NetworkPolicy.

**Provider mapping (today)**
```yaml
components:
  llmEngine:
    service:
      serviceType: LoadBalancer
      loadBalancerService:
        sourceRanges: [10.0.0.0/8]
```

**Gap to close:** provider-emitted NetworkPolicy so only the gateway (or named
namespaces) can reach `ClusterIP` workloads.

---

## 4.7 Auditability and traceability

**Status:** Gap.

As a compliance engineer, I want every model call attributable to a consumer with
an audit trail, so that I can answer "who called which model with what" for
reviews and incidents.

**Acceptance criteria**
- Requests carry a consumer identity from gateway auth.
- Access logs (consumer, model, timestamp, decision) are emitted to a sink.
- Guardrail/judge decisions appear in the same trail.

**Gap to close:** structured access/audit logging at the gateway, tied to the
auth identity from 4.1.
