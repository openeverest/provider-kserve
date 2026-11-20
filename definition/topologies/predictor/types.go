// Package predictor contains parameter types for the predictor topology.
//
// Add fields to PredictorTopologyParameters and reference it via
// parametersSchema in topology.yaml when this topology needs parameters.
//
// +k8s:openapi-gen=true
package predictor

import (
	"encoding/json"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
)

// External access modes for the predictor topology.
const (
	ExternalAccessClusterIP      = "ClusterIP"
	ExternalAccessLoadBalancer   = "LoadBalancer"
	ExternalAccessNodePort       = "NodePort"
	ExternalAccessEnvoyAIGateway = "EnvoyAIGateway"
)

// PredictorTopologyParameters defines the topology-level parameters for the
// predictor topology. Per-model configuration lives on the predictor component;
// these are cross-cutting deployment options that shape the generated
// InferenceService.
type PredictorTopologyParameters struct {
	// ExternalAccess is the client access path: ClusterIP, LoadBalancer,
	// NodePort, or EnvoyAIGateway (embedding models and custom containers
	// that serve the OpenAI or TypeSafe API, through the provider's shared
	// Gateway). When set it takes precedence over predictor.service.serviceType.
	ExternalAccess string `json:"externalAccess,omitempty"`

	// TokenLimitPerHour is the per-API-key token quota for this model through
	// the Envoy AI Gateway. Used only with a Redis-compatible rate-limit
	// backend; unset uses the provider default.
	TokenLimitPerHour *int32 `json:"tokenLimitPerHour,omitempty"`

	// EnableMetrics publishes the model server's metrics endpoint in
	// status.monitoring.sources. Unset means enabled, as for the llm topology;
	// turn it off for a custom container without a Prometheus endpoint.
	EnableMetrics *bool `json:"enableMetrics,omitempty"`
}

// MetricsEnabled reports whether this instance publishes its metrics endpoint.
func (t PredictorTopologyParameters) MetricsEnabled() bool {
	return t.EnableMetrics == nil || *t.EnableMetrics
}

// UnmarshalJSON also accepts "true"/"false" strings for enableMetrics, which
// Instances created before the UI rendered it as a toggle store.
func (t *PredictorTopologyParameters) UnmarshalJSON(data []byte) error {
	type plain PredictorTopologyParameters
	aux := struct {
		*plain
		EnableMetrics json.RawMessage `json:"enableMetrics,omitempty"`
	}{plain: (*plain)(t)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	t.EnableMetrics = nil
	if len(aux.EnableMetrics) == 0 || string(aux.EnableMetrics) == "null" {
		return nil
	}
	var b bool
	if err := json.Unmarshal(aux.EnableMetrics, &b); err != nil {
		var s string
		if err := json.Unmarshal(aux.EnableMetrics, &s); err != nil {
			return fmt.Errorf("enableMetrics: %w", err)
		}
		if b, err = strconv.ParseBool(s); err != nil {
			return fmt.Errorf("enableMetrics: %w", err)
		}
	}
	t.EnableMetrics = &b
	return nil
}

// UsesAIGateway reports whether clients reach the predictor through the Envoy
// AI Gateway.
func (t PredictorTopologyParameters) UsesAIGateway() bool {
	return t.ExternalAccess == ExternalAccessEnvoyAIGateway
}

// ResolvedServiceType returns the Kubernetes Service type implied by
// ExternalAccess. Empty means "defer to predictor.service.serviceType".
func (t PredictorTopologyParameters) ResolvedServiceType() corev1.ServiceType {
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
