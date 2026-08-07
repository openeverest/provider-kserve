package provider

// Run `make manifests` to regenerate config/rbac/role.yaml from these markers.
// This file contains kubebuilder RBAC markers for controller-gen.
// See: https://book.kubebuilder.io/reference/markers/rbac

// Base RBAC (required by all providers):
// +kubebuilder:rbac:groups=core.openeverest.io,resources=instances,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=core.openeverest.io,resources=instances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.openeverest.io,resources=instances/finalizers,verbs=update
// +kubebuilder:rbac:groups=core.openeverest.io,resources=providers,verbs=get;list;watch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// =============================================================================
// PROVIDER-SPECIFIC RBAC — KServe serving resources.
// =============================================================================

// LLM serving (serving.kserve.io/v1alpha2):
// +kubebuilder:rbac:groups=serving.kserve.io,resources=llminferenceservices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=serving.kserve.io,resources=llminferenceservices/status,verbs=get
// +kubebuilder:rbac:groups=serving.kserve.io,resources=llminferenceservices/finalizers,verbs=update
// The provider materializes a per-Instance LLMInferenceServiceConfig from the
// inline Advanced config, so it needs write verbs (not just read) on configs.
// +kubebuilder:rbac:groups=serving.kserve.io,resources=llminferenceserviceconfigs,verbs=get;list;watch;create;update;patch;delete

// Predictive serving (serving.kserve.io/v1beta1):
// +kubebuilder:rbac:groups=serving.kserve.io,resources=inferenceservices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=serving.kserve.io,resources=inferenceservices/status,verbs=get
// +kubebuilder:rbac:groups=serving.kserve.io,resources=inferenceservices/finalizers,verbs=update

// Envoy AI Gateway resources used by the optional llm access path:
// +kubebuilder:rbac:groups=aigateway.envoyproxy.io,resources=aigatewayroutes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=aigateway.envoyproxy.io,resources=aigatewayroutes/status,verbs=get
// +kubebuilder:rbac:groups=gateway.envoyproxy.io,resources=backendtrafficpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch

// Prometheus Operator PodMonitor emitted per llm Instance (optional metrics):
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=podmonitors,verbs=get;list;watch;create;update;patch;delete

// Core resources referenced by model serving (model credentials, endpoints).
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// The provider creates an external Service (LoadBalancer/NodePort) fronting the
// llm workload when the user selects that expose type, and removes legacy
// Services when AI Gateway takes over external access.
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// ServiceAccounts carry the HuggingFace token Secret for gated model downloads
// (attached to the llm workload so KServe injects HF_TOKEN).
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
