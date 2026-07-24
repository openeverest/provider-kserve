// Package components contains custom spec types for provider component types.
//
// Each struct here corresponds to a component type defined in versions.yaml
// and is converted to an OpenAPI schema during generation.
// Add fields when a component type needs custom configuration beyond
// what the base Instance spec provides.
//
// +k8s:openapi-gen=true
package components

// VllmCustomSpec defines custom configuration for the vllm component type,
// which is rendered into a KServe LLMInferenceService (serving.kserve.io/v1alpha2).
type VllmCustomSpec struct {
	// ModelURI is the location of the model artifacts, e.g.
	// "hf://meta-llama/Llama-3.1-8B-Instruct", "s3://bucket/model" or "pvc://claim/path".
	// The storage-initializer uses this URI to download the model.
	ModelURI string `json:"modelURI,omitempty"`

	// ModelName is the name advertised in the "model" field of inference requests.
	// Defaults to the Instance name when empty.
	ModelName string `json:"modelName,omitempty"`

	// TensorParallelSize configures vLLM tensor parallelism (maps to the runtime
	// --tensor-parallel-size). Use for models that span multiple GPUs on a node.
	TensorParallelSize *int32 `json:"tensorParallelSize,omitempty"`

	// PipelineParallelSize configures vLLM pipeline parallelism across nodes
	// (maps to the runtime --pipeline-parallel-size).
	PipelineParallelSize *int32 `json:"pipelineParallelSize,omitempty"`

	// ComputeProfile selects the compute profile for the workload. "cpu"
	// composes the CPU-only LLMInferenceServiceConfig (via baseRefs) so the
	// model runs without a GPU; any other value (default "gpu") uses the bundled
	// CUDA presets. This is a convenience over setting baseRefs directly.
	ComputeProfile string `json:"computeProfile,omitempty"`

	// KVCacheSpaceGi overrides VLLM_CPU_KVCACHE_SPACE (GiB) for the CPU profile.
	// Left unset, vLLM auto-sizes the KV cache from the memory that is free at
	// startup (recommended), which adapts to the node. Set it only to cap the KV
	// cache to a specific size; vLLM refuses to start if it exceeds host-free
	// memory at that moment. Ignored for the GPU profile.
	KVCacheSpaceGi *int32 `json:"kvCacheSpaceGi,omitempty"`

	// BaseRefs is an ordered list of LLMInferenceServiceConfig names whose specs
	// are inherited and merged (later entries win, this Instance wins over all).
	// This is the primary escape hatch for full runtime customization.
	BaseRefs []string `json:"baseRefs,omitempty"`

	// Config is an inline LLMInferenceServiceConfig spec body (YAML) authored in
	// the UI's Advanced section. The provider materializes it as an
	// Instance-owned LLMInferenceServiceConfig and attaches it to the generated
	// LLMInferenceService via baseRefs (last, so it overrides presets and any
	// BaseRefs above, while the Instance's own structured fields still win). Use
	// it for runtime settings not exposed as first-class fields (extra vLLM
	// args, env, scheduling). Only the config spec body is provided here; the
	// provider supplies metadata.
	Config string `json:"config,omitempty"`

	// DisableStorageInitializer skips the storage-initializer init container.
	// Useful when models are pre-loaded via a LocalModelCache, modelcar, or an
	// alternative streamer (e.g. RunAI Model Streamer).
	DisableStorageInitializer *bool `json:"disableStorageInitializer,omitempty"`
}

// ModelServerCustomSpec defines custom configuration for the modelServer
// component type, which is rendered into a KServe InferenceService predictor
// (serving.kserve.io/v1beta1).
type ModelServerCustomSpec struct {
	// ModelFormat is the model framework, e.g. "sklearn", "pytorch",
	// "tensorflow", "xgboost", "onnx", "triton", "huggingface". KServe uses it
	// to auto-select a supporting ServingRuntime.
	ModelFormat string `json:"modelFormat,omitempty"`

	// StorageURI is the location of the model artifacts (s3://, gs://, hf://,
	// pvc://, oci:// or a local path).
	StorageURI string `json:"storageURI,omitempty"`

	// RuntimeVersion pins the serving runtime version. Optional; KServe defaults
	// based on the selected runtime when empty.
	RuntimeVersion string `json:"runtimeVersion,omitempty"`

	// Runtime explicitly selects a (Cluster)ServingRuntime by name instead of
	// letting KServe auto-select one from the model format.
	Runtime string `json:"runtime,omitempty"`

	// MinReplicas is the autoscaling floor (0 enables scale-to-zero).
	MinReplicas *int32 `json:"minReplicas,omitempty"`

	// MaxReplicas is the autoscaling ceiling.
	MaxReplicas *int32 `json:"maxReplicas,omitempty"`
}
