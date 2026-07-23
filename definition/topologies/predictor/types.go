// Package predictor contains parameter types for the predictor topology.
//
// Add fields to PredictorTopologyParameters and reference it via
// parametersSchema in topology.yaml when this topology needs parameters.
//
// +k8s:openapi-gen=true
package predictor

// PredictorTopologyParameters defines the topology-level parameters for the
// predictor topology. Currently empty — per-model configuration lives on the
// predictor component parameters. Add cross-cutting options (e.g. deployment
// mode) here as the topology grows.
type PredictorTopologyParameters struct{}
