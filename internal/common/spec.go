// Package common defines shared constants used across the provider.
package common

const (
	// ProviderName is the canonical name of this provider.
	ProviderName = "provider-kserve"

	// Component names — logical roles referenced by topologies and the
	// Instance spec (spec.components.<name>).
	ComponentLlmEngine = "llmEngine"
	ComponentPredictor = "predictor"

	// Component types — the software each component runs.
	ComponentTypeVllm        = "vllm"
	ComponentTypeModelServer = "modelServer"

	// Topology names — deployment architectures selected by spec.topology.type.
	TopologyLLM       = "llm"
	TopologyPredictor = "predictor"

	// DeploymentModeAnnotation and DeploymentModeRaw select KServe's
	// RawDeployment mode (plain Kubernetes Deployment + HPA, no Knative).
	DeploymentModeAnnotation = "serving.kserve.io/deploymentMode"
	DeploymentModeRaw        = "RawDeployment"
)
