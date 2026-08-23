// Package components contains custom spec types for provider component types.
//
// Each struct here corresponds to a component type defined in versions.yaml
// and is converted to an OpenAPI schema during generation.
// Add fields when a component type needs custom configuration beyond
// what the base Instance spec provides.
//
// +k8s:openapi-gen=true
package components

// LoRAAdapterSpec identifies one LoRA fine-tune served alongside the base model.
// Each adapter is addressable by name in OpenAI "model" requests once KServe
// configures vLLM (--lora-modules).
type LoRAAdapterSpec struct {
	// Name is the model name clients use in requests (e.g. "my-ft-v2").
	Name string `json:"name"`

	// URI is the adapter artifact location (hf://, s3://, or pvc://).
	URI string `json:"uri"`
}

// LoRASpec configures LoRA adapters on top of the base modelURI.
type LoRASpec struct {
	// Adapters is the list of LoRA modules to load. KServe downloads hf:// and
	// s3:// adapters via the storage-initializer and mounts pvc:// adapters directly.
	Adapters []LoRAAdapterSpec `json:"adapters,omitempty"`

	// MaxRank maps to vLLM --max-lora-rank (default 16 when unset).
	MaxRank *int32 `json:"maxRank,omitempty"`

	// MaxAdapters maps to vLLM --max-loras (defaults to adapter count when unset).
	MaxAdapters *int32 `json:"maxAdapters,omitempty"`

	// MaxCpuAdapters maps to vLLM --max-cpu-loras (defaults to adapter count when unset).
	MaxCpuAdapters *int32 `json:"maxCpuAdapters,omitempty"`
}

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

	// DataParallelSize configures vLLM data parallelism (maps to the runtime
	// ParallelismSpec.Data). Use to run multiple model replicas that share the
	// same expert layers, the common layout for Mixture-of-Experts models.
	DataParallelSize *int32 `json:"dataParallelSize,omitempty"`

	// ExpertParallel toggles expert parallelism (maps to ParallelismSpec.Expert)
	// for Mixture-of-Experts models (e.g. Mixtral, DeepSeek, Qwen-MoE):
	// "disabled" (default) or "enabled". Typically combined with dataParallelSize.
	ExpertParallel string `json:"expertParallel,omitempty"`

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
	// alternative streamer (e.g. RunAI Model Streamer). Must remain false when
	// LoRA adapters use hf:// or s3:// URIs.
	DisableStorageInitializer *bool `json:"disableStorageInitializer,omitempty"`

	// LoRA attaches one or more LoRA adapters to the base model. Rendered to
	// LLMInferenceService.spec.model.lora; KServe handles download and vLLM flags.
	// Adapters may also be selected via loraDeployment + loraSlot1..3 (UI catalog).
	LoRA *LoRASpec `json:"lora,omitempty"`

	// LoraDeployment toggles LoRA serving in the UI wizard: "disabled" (default) or
	// "enabled". When enabled, loraSlot1..3 name keys resolve against the chart
	// loraAdapters catalog (LORA_ADAPTER_CATALOG env on the provider).
	LoraDeployment string `json:"loraDeployment,omitempty"`

	// LoraSlot1..3 hold catalog adapter names chosen in the UI (empty = unused).
	LoraSlot1 string `json:"loraSlot1,omitempty"`
	LoraSlot2 string `json:"loraSlot2,omitempty"`
	LoraSlot3 string `json:"loraSlot3,omitempty"`
}

const (
	LoraDeploymentDisabled = "disabled"
	LoraDeploymentEnabled  = "enabled"
)

const (
	ExpertParallelDisabled = "disabled"
	ExpertParallelEnabled  = "enabled"
)

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
