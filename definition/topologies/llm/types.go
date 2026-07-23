// Package llm contains parameter types for the llm topology.
//
// Add fields to LlmTopologyParameters and reference it via parametersSchema in
// topology.yaml when this topology needs parameters.
//
// +k8s:openapi-gen=true
package llm

// LlmTopologyParameters defines the topology-level parameters for the llm
// topology. Component-level parameters (model URI, parallelism, etc.) live on
// the llmEngine component; these are cross-cutting deployment options that
// shape the generated LLMInferenceService.
type LlmTopologyParameters struct {
	// EnableGatewayRouting provisions a managed Gateway API route plus an
	// Inference Gateway scheduler (Endpoint Picker) for prefix-cache aware
	// routing across replicas. When false, the LLMInferenceService is exposed
	// with its default networking.
	EnableGatewayRouting bool `json:"enableGatewayRouting,omitempty"`

	// EnablePrefill turns on disaggregated serving by creating a separate
	// prefill deployment in addition to the decode workload (llm-d pattern).
	EnablePrefill bool `json:"enablePrefill,omitempty"`

	// PrefillReplicas sets the number of replicas for the prefill workload.
	// Only used when EnablePrefill is true.
	PrefillReplicas *int32 `json:"prefillReplicas,omitempty"`
}
