// Package llm contains parameter types for the llm topology.
//
// Add fields to LlmTopologyParameters and reference it via parametersSchema in
// topology.yaml when this topology needs parameters.
//
// +k8s:openapi-gen=true
package llm

import (
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// External access modes for the llm topology UI / parameters.
const (
	ExternalAccessClusterIP      = "ClusterIP"
	ExternalAccessLoadBalancer   = "LoadBalancer"
	ExternalAccessNodePort       = "NodePort"
	ExternalAccessEnvoyAIGateway = "EnvoyAIGateway"
)

// LlmTopologyParameters defines the topology-level parameters for the llm
// topology. Component-level parameters (model URI, parallelism, etc.) live on
// the llmEngine component; these are cross-cutting deployment options that
// shape the generated LLMInferenceService.
type LlmTopologyParameters struct {
	// ExternalAccess is the client access path shown in the Routing UI:
	// ClusterIP, LoadBalancer, NodePort, or EnvoyAIGateway. When set it takes
	// precedence over EnableAIGateway and llmEngine.service.serviceType.
	ExternalAccess string `json:"externalAccess,omitempty"`

	// EnableGatewayRouting provisions a managed Gateway API route plus an
	// Inference Gateway scheduler (Endpoint Picker) for prefix-cache aware
	// routing across replicas. When false, the LLMInferenceService is exposed
	// with its default networking. Envoy AI Gateway implies gateway routing.
	EnableGatewayRouting bool `json:"enableGatewayRouting,omitempty"`

	// EnableAIGateway exposes the model through the shared Envoy AI Gateway.
	// Legacy alias for ExternalAccess=EnvoyAIGateway when ExternalAccess is unset.
	EnableAIGateway bool `json:"enableAIGateway,omitempty"`

	// TokenLimitPerHour is the per-user token quota for this model. It is used
	// only when the Envoy AI Gateway is selected and the provider has a
	// Redis-compatible rate-limit backend configured. Zero/unset uses the
	// default limit.
	TokenLimitPerHour *int32 `json:"tokenLimitPerHour,omitempty"`

	// EnablePrefill turns on disaggregated serving by creating a separate
	// prefill deployment in addition to the decode workload (llm-d pattern).
	EnablePrefill bool `json:"enablePrefill,omitempty"`

	// PrefillReplicas sets the number of replicas for the prefill workload.
	// Only used when EnablePrefill is true. Ignored when prefill autoscaling
	// (PrefillMinReplicas / PrefillMaxReplicas) is set.
	PrefillReplicas *int32 `json:"prefillReplicas,omitempty"`

	// PrefillMinReplicas is the WVA autoscaling floor for the prefill workload.
	// Setting PrefillMinReplicas or PrefillMaxReplicas enables
	// spec.prefill.scaling (mutually exclusive with PrefillReplicas).
	// Requires EnablePrefill. KServe requires ≥ 1.
	PrefillMinReplicas *int32 `json:"prefillMinReplicas,omitempty"`

	// PrefillMaxReplicas is the WVA autoscaling ceiling for the prefill
	// workload. Required when prefill autoscaling is enabled.
	PrefillMaxReplicas *int32 `json:"prefillMaxReplicas,omitempty"`

	// PrefillScalingActuator selects the prefill WVA actuator: "keda" (default)
	// or "hpa". When both decode and prefill autoscale, KServe requires the
	// same actuator on both sides.
	PrefillScalingActuator string `json:"prefillScalingActuator,omitempty"`

	// PrefillPipelineParallelSize is vLLM pipeline parallelism for the prefill
	// workload. Required when PrefillWorkerCount is set (must equal
	// PrefillWorkerCount + 1). Alone it is a flag only — no prefill workers.
	PrefillPipelineParallelSize *int32 `json:"prefillPipelineParallelSize,omitempty"`

	// PrefillWorkerCount is extra prefill worker pods besides the prefill head.
	// Presence turns on spec.prefill.worker. Requires EnablePrefill.
	PrefillWorkerCount *int32 `json:"prefillWorkerCount,omitempty"`

	// PrefillWorkerResources are optional resources for prefill worker pods.
	// Unset copies llmEngine.resources. Requires PrefillWorkerCount.
	PrefillWorkerResources *corev1.ResourceRequirements `json:"prefillWorkerResources,omitempty"`

	// EnableMetrics emits a Prometheus Operator PodMonitor for this instance's
	// vLLM pods (/metrics). Nil/unset defaults to true for backward compatibility.
	EnableMetrics *bool `json:"enableMetrics,omitempty"`

	// EnableTracing turns on KServe's built-in distributed tracing (OTLP) across
	// the gateway, scheduler and model components. Disabled by default. When
	// enabled without an endpoint, KServe applies its default OTLP exporter,
	// sampler and sampler-arg settings.
	EnableTracing bool `json:"enableTracing,omitempty"`

	// TracingEndpoint overrides the OTLP exporter endpoint
	// (OTEL_EXPORTER_OTLP_ENDPOINT). Only used when EnableTracing is true; empty
	// keeps KServe's default collector endpoint.
	TracingEndpoint string `json:"tracingEndpoint,omitempty"`
}

// MetricsEnabled reports whether this instance should emit a vLLM PodMonitor.
func (t LlmTopologyParameters) MetricsEnabled() bool {
	if t.EnableMetrics == nil {
		return true
	}
	return *t.EnableMetrics
}

// UsesAIGateway reports whether this instance should register on the shared
// Envoy AI Gateway.
func (t LlmTopologyParameters) UsesAIGateway() bool {
	switch t.ExternalAccess {
	case ExternalAccessEnvoyAIGateway:
		return true
	case "":
		return t.EnableAIGateway
	default:
		return false
	}
}

// ResolvedServiceType returns the Kubernetes Service type implied by
// ExternalAccess. Empty means "defer to llmEngine.service.serviceType".
func (t LlmTopologyParameters) ResolvedServiceType() corev1.ServiceType {
	switch t.ExternalAccess {
	case ExternalAccessLoadBalancer:
		return corev1.ServiceTypeLoadBalancer
	case ExternalAccessNodePort:
		return corev1.ServiceTypeNodePort
	case ExternalAccessClusterIP, ExternalAccessEnvoyAIGateway:
		return corev1.ServiceTypeClusterIP
	default:
		return ""
	}
}

// UnmarshalJSON accepts JSON booleans and UI select strings ("true"/"false")
// for flag fields. Everest's form generator has no switch uiType, so flags are
// rendered as Enabled/Disabled selects.
func (t *LlmTopologyParameters) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["externalAccess"]; ok && string(v) != "null" {
		if err := json.Unmarshal(v, &t.ExternalAccess); err != nil {
			return fmt.Errorf("externalAccess: %w", err)
		}
	}
	if v, ok := raw["tokenLimitPerHour"]; ok && string(v) != "null" {
		var n int32
		if err := json.Unmarshal(v, &n); err != nil {
			return fmt.Errorf("tokenLimitPerHour: %w", err)
		}
		t.TokenLimitPerHour = &n
	}
	if v, ok := raw["prefillReplicas"]; ok && string(v) != "null" {
		var n int32
		if err := json.Unmarshal(v, &n); err != nil {
			return fmt.Errorf("prefillReplicas: %w", err)
		}
		t.PrefillReplicas = &n
	}
	if v, ok := raw["prefillMinReplicas"]; ok && string(v) != "null" {
		var n int32
		if err := json.Unmarshal(v, &n); err != nil {
			return fmt.Errorf("prefillMinReplicas: %w", err)
		}
		t.PrefillMinReplicas = &n
	}
	if v, ok := raw["prefillMaxReplicas"]; ok && string(v) != "null" {
		var n int32
		if err := json.Unmarshal(v, &n); err != nil {
			return fmt.Errorf("prefillMaxReplicas: %w", err)
		}
		t.PrefillMaxReplicas = &n
	}
	if v, ok := raw["prefillScalingActuator"]; ok && string(v) != "null" {
		if err := json.Unmarshal(v, &t.PrefillScalingActuator); err != nil {
			return fmt.Errorf("prefillScalingActuator: %w", err)
		}
	}
	if v, ok := raw["prefillPipelineParallelSize"]; ok && string(v) != "null" {
		var n int32
		if err := json.Unmarshal(v, &n); err != nil {
			return fmt.Errorf("prefillPipelineParallelSize: %w", err)
		}
		t.PrefillPipelineParallelSize = &n
	}
	if v, ok := raw["prefillWorkerCount"]; ok && string(v) != "null" {
		var n int32
		if err := json.Unmarshal(v, &n); err != nil {
			return fmt.Errorf("prefillWorkerCount: %w", err)
		}
		t.PrefillWorkerCount = &n
	}
	if v, ok := raw["prefillWorkerResources"]; ok && string(v) != "null" {
		var res corev1.ResourceRequirements
		if err := json.Unmarshal(v, &res); err != nil {
			return fmt.Errorf("prefillWorkerResources: %w", err)
		}
		t.PrefillWorkerResources = &res
	}
	if v, ok := raw["tracingEndpoint"]; ok && string(v) != "null" {
		if err := json.Unmarshal(v, &t.TracingEndpoint); err != nil {
			return fmt.Errorf("tracingEndpoint: %w", err)
		}
	}
	var err error
	if t.EnableGatewayRouting, err = unmarshalBoolFlag(raw, "enableGatewayRouting"); err != nil {
		return err
	}
	if t.EnableAIGateway, err = unmarshalBoolFlag(raw, "enableAIGateway"); err != nil {
		return err
	}
	if t.EnablePrefill, err = unmarshalBoolFlag(raw, "enablePrefill"); err != nil {
		return err
	}
	if t.EnableMetrics, err = unmarshalOptionalBoolFlag(raw, "enableMetrics"); err != nil {
		return err
	}
	if t.EnableTracing, err = unmarshalBoolFlag(raw, "enableTracing"); err != nil {
		return err
	}
	return nil
}

func unmarshalOptionalBoolFlag(raw map[string]json.RawMessage, key string) (*bool, error) {
	v, ok := raw[key]
	if !ok || string(v) == "null" {
		return nil, nil
	}
	b, err := unmarshalBoolFlag(raw, key)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func unmarshalBoolFlag(raw map[string]json.RawMessage, key string) (bool, error) {
	v, ok := raw[key]
	if !ok || string(v) == "null" {
		return false, nil
	}
	var b bool
	if err := json.Unmarshal(v, &b); err == nil {
		return b, nil
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "enabled", "yes":
		return true, nil
	case "false", "0", "disabled", "no", "":
		return false, nil
	default:
		return false, fmt.Errorf("%s: invalid boolean value %q", key, s)
	}
}
