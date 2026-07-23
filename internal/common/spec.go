// Package common defines shared constants used across the provider.
package common

import "os"

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

	// hfTokenSecretEnvVar names the environment variable (set by the Helm chart
	// from .Values.huggingface.tokenSecretName) holding the Secret that provides
	// the HuggingFace token for gated model downloads.
	hfTokenSecretEnvVar = "HF_TOKEN_SECRET_NAME"
)

// HFTokenSecretName returns the configured HuggingFace token Secret name, or an
// empty string when gated-model support is disabled. The Secret must contain an
// HF_TOKEN key and live in the Instance's namespace.
func HFTokenSecretName() string {
	return os.Getenv(hfTokenSecretEnvVar)
}
