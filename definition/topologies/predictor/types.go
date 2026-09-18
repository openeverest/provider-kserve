// Package predictor contains parameter types for the predictor topology.
//
// Add fields to PredictorTopologyParameters and reference it via
// parametersSchema in topology.yaml when this topology needs parameters.
//
// +k8s:openapi-gen=true
package predictor

import corev1 "k8s.io/api/core/v1"

// External access modes for the predictor topology.
const (
	ExternalAccessClusterIP    = "ClusterIP"
	ExternalAccessLoadBalancer = "LoadBalancer"
	ExternalAccessNodePort     = "NodePort"
)

// PredictorTopologyParameters defines the topology-level parameters for the
// predictor topology. Per-model configuration lives on the predictor component;
// these are cross-cutting deployment options that shape the generated
// InferenceService.
type PredictorTopologyParameters struct {
	// ExternalAccess is the client access path: ClusterIP, LoadBalancer, or
	// NodePort. When set it takes precedence over predictor.service.serviceType.
	ExternalAccess string `json:"externalAccess,omitempty"`
}

// ResolvedServiceType returns the Kubernetes Service type implied by
// ExternalAccess. Empty means "defer to predictor.service.serviceType".
func (t PredictorTopologyParameters) ResolvedServiceType() corev1.ServiceType {
	switch t.ExternalAccess {
	case ExternalAccessLoadBalancer:
		return corev1.ServiceTypeLoadBalancer
	case ExternalAccessNodePort:
		return corev1.ServiceTypeNodePort
	case ExternalAccessClusterIP:
		return corev1.ServiceTypeClusterIP
	default:
		return ""
	}
}
