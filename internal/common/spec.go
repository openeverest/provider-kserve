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

	// DeploymentModeAnnotation and DeploymentModeStandard select KServe's
	// Standard mode (plain Kubernetes Deployment + HPA, no Knative). KServe
	// deprecated the old name for this mode, "RawDeployment", in favour of
	// "Standard"; the value is unchanged in behaviour.
	DeploymentModeAnnotation = "serving.kserve.io/deploymentMode"
	DeploymentModeStandard   = "Standard"

	// hfTokenSecretEnvVar names the environment variable (set by the Helm chart
	// from .Values.huggingface.tokenSecretName) holding the Secret that provides
	// the HuggingFace token for gated model downloads.
	hfTokenSecretEnvVar = "HF_TOKEN_SECRET_NAME"

	aiGatewayNameEnvVar      = "AI_GATEWAY_NAME"
	aiGatewayNamespaceEnvVar = "AI_GATEWAY_NAMESPACE"
	aiGatewayEnabledEnvVar   = "AI_GATEWAY_ENABLED"
	aiGatewaySchemeEnvVar    = "AI_GATEWAY_SCHEME"
	aiGatewayPortEnvVar      = "AI_GATEWAY_PORT"
	aiGatewayHostnameEnvVar  = "AI_GATEWAY_HOSTNAME"
	rateLimitRedisURLEnvVar  = "AI_GATEWAY_RATE_LIMIT_REDIS_URL"

	podMonitorEnabledEnvVar  = "ENABLE_POD_MONITOR"
	podMonitorIntervalEnvVar = "POD_MONITOR_INTERVAL"
)

// HFTokenSecretName returns the configured HuggingFace token Secret name, or an
// empty string when gated-model support is disabled. The Secret must contain an
// HF_TOKEN key and live in the Instance's namespace.
func HFTokenSecretName() string {
	return os.Getenv(hfTokenSecretEnvVar)
}

// AIGatewayName returns the shared Gateway name configured by the chart.
func AIGatewayName() string {
	return os.Getenv(aiGatewayNameEnvVar)
}

// AIGatewayEnabled reports whether the optional gateway stack is configured.
func AIGatewayEnabled() bool {
	return os.Getenv(aiGatewayEnabledEnvVar) == "true"
}

// AIGatewayNamespace returns the namespace containing the shared Gateway.
func AIGatewayNamespace() string {
	return os.Getenv(aiGatewayNamespaceEnvVar)
}

// AIGatewayScheme returns the scheme used in published gateway URLs.
func AIGatewayScheme() string {
	if scheme := os.Getenv(aiGatewaySchemeEnvVar); scheme != "" {
		return scheme
	}
	return "http"
}

// AIGatewayPort returns the externally exposed Gateway listener port.
func AIGatewayPort() string {
	if port := os.Getenv(aiGatewayPortEnvVar); port != "" {
		return port
	}
	if AIGatewayScheme() == "https" {
		return "443"
	}
	return "80"
}

// AIGatewayHostname returns the public DNS name configured for Gateway TLS.
func AIGatewayHostname() string {
	return os.Getenv(aiGatewayHostnameEnvVar)
}

// RateLimitRedisURL returns the Redis-compatible global rate-limit backend.
func RateLimitRedisURL() string {
	return os.Getenv(rateLimitRedisURLEnvVar)
}

// PodMonitorEnabled reports whether the provider emits a Prometheus Operator
// PodMonitor per llm Instance. Off by default so clusters without the
// monitoring.coreos.com CRDs are never touched.
func PodMonitorEnabled() bool {
	return os.Getenv(podMonitorEnabledEnvVar) == "true"
}

// PodMonitorInterval returns the scrape interval for generated PodMonitors,
// defaulting to 30s when unset.
func PodMonitorInterval() string {
	if interval := os.Getenv(podMonitorIntervalEnvVar); interval != "" {
		return interval
	}
	return "30s"
}
