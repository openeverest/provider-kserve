// Package components contains custom spec types for provider component types.
//
// Each struct here corresponds to a component type defined in versions.yaml
// and is converted to an OpenAPI schema during generation.
// Add fields when a component type needs custom configuration beyond
// what the base Instance spec provides.
//
// +k8s:openapi-gen=true
package components

import corev1 "k8s.io/api/core/v1"

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

// LocalModelCacheSpec configures an Instance-owned KServe
// LocalModelNamespaceCache for the base model.
type LocalModelCacheSpec struct {
	// Enabled selects whether the cache is maintained: "disabled" (default) or "enabled".
	Enabled string `json:"enabled,omitempty"`

	// ModelSize is the storage required for one model copy, for example "10Gi".
	ModelSize string `json:"modelSize,omitempty"`

	// NodeGroup names the existing LocalModelNodeGroup that stores the model.
	NodeGroup string `json:"nodeGroup,omitempty"`

	// ServiceAccountName optionally names the download ServiceAccount in the
	// KServe LocalModel jobs namespace.
	ServiceAccountName string `json:"serviceAccountName,omitempty"`
}

// VllmEngineSpec holds the vLLM engine settings users change most often. Each
// set field becomes one `vllm serve` flag; empty fields keep vLLM's default.
type VllmEngineSpec struct {
	// MaxModelLen caps the context length in tokens (--max-model-len). Lowering
	// it shrinks the KV cache, the usual fix when a model does not fit in GPU
	// memory.
	MaxModelLen *int32 `json:"maxModelLen,omitempty"`

	// GpuMemoryUtilization is the fraction of GPU memory, in (0, 1], vLLM may
	// claim for weights and KV cache (--gpu-memory-utilization). On the CPU
	// profile vLLM applies it to host memory.
	GpuMemoryUtilization *float64 `json:"gpuMemoryUtilization,omitempty"`

	// MaxNumSeqs caps the sequences batched per iteration (--max-num-seqs).
	MaxNumSeqs *int32 `json:"maxNumSeqs,omitempty"`

	// Dtype is the weight and activation precision (--dtype): auto, half,
	// float16, bfloat16, float or float32.
	Dtype string `json:"dtype,omitempty"`

	// Quantization names the weight quantization method (--quantization), e.g.
	// fp8, awq_marlin or bitsandbytes. vLLM detects pre-quantized checkpoints on
	// its own; set it to quantize on the fly or to force a kernel.
	Quantization string `json:"quantization,omitempty"`

	// KVCacheDtype is the KV cache precision (--kv-cache-dtype), e.g. fp8 to fit
	// roughly twice the tokens.
	KVCacheDtype string `json:"kvCacheDtype,omitempty"`

	// ToolCallParser turns on OpenAI-style tool calling with the parser matching
	// the model's chat template (--enable-auto-tool-choice --tool-call-parser),
	// e.g. hermes for Qwen, llama3_json for Llama 3.x, mistral, openai for
	// gpt-oss.
	ToolCallParser string `json:"toolCallParser,omitempty"`

	// ReasoningParser returns the model's thinking in the response's
	// reasoning_content field instead of the answer (--reasoning-parser), e.g.
	// qwen3, deepseek_r1 or openai_gptoss.
	ReasoningParser string `json:"reasoningParser,omitempty"`
}

// VllmCustomSpec defines custom configuration for the vllm component type,
// which is rendered into a KServe LLMInferenceService (serving.kserve.io/v1alpha2).
type VllmCustomSpec struct {
	// ModelURI is the location of the model artifacts, e.g.
	// "hf://meta-llama/Llama-3.1-8B-Instruct", "s3://bucket/model",
	// "pvc://claim/path", "oci://registry/repo:tag" (a modelcar image) or
	// "oci+native://registry/repo:tag" (mounted as a Kubernetes image volume).
	ModelURI string `json:"modelURI,omitempty"`

	// ModelName is the name advertised in the "model" field of inference requests.
	// Defaults to the Instance name when empty.
	ModelName string `json:"modelName,omitempty"`

	// LocalModelCache creates a namespace-scoped cache for ModelURI. The node
	// group and optional download ServiceAccount must already exist.
	LocalModelCache *LocalModelCacheSpec `json:"localModelCache,omitempty"`

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

	// GpuCount is the number of GPUs (nvidia.com/gpu) each vLLM pod requests
	// (head and every worker). The GPU presets carry no GPU limit, so the provider
	// injects this into the model container's resource limits; without it the pod
	// schedules onto any node and vLLM fails to find a device. Defaults to 1 on
	// the GPU profile. Leave unset for the CPU profile. Must be >= tensorParallelSize.
	GpuCount *int32 `json:"gpuCount,omitempty"`

	// KVCacheSpaceGi overrides VLLM_CPU_KVCACHE_SPACE (GiB) for the CPU profile.
	// Left unset, vLLM auto-sizes the KV cache from the memory that is free at
	// startup (recommended), which adapts to the node. Set it only to cap the KV
	// cache to a specific size; vLLM refuses to start if it exceeds host-free
	// memory at that moment. Ignored for the GPU profile.
	KVCacheSpaceGi *int32 `json:"kvCacheSpaceGi,omitempty"`

	// Engine holds first-class vLLM engine settings (context length, memory,
	// precision, tool calling, reasoning). The provider appends them after the
	// args the workload inherits from the compute profile, baseRefs and the
	// inline config, so they win over the same flags set there.
	Engine *VllmEngineSpec `json:"engine,omitempty"`

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

	// DisableStorageInitializer skips the step that makes the model available
	// to vLLM: KServe then attaches nothing, so the model must already be at
	// /mnt/models (e.g. a LocalModelCache or a volume from the inline config).
	// Must remain false for oci:// and oci+native:// models, and when LoRA
	// adapters use hf:// or s3:// URIs.
	DisableStorageInitializer *bool `json:"disableStorageInitializer,omitempty"`

	// ImagePullSecret names a Secret in the Instance namespace used to pull
	// images from a private registry, typically an oci:// model image. It is
	// set on every vLLM pod: head, workers and prefill.
	ImagePullSecret string `json:"imagePullSecret,omitempty"`

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

	// MinReplicas is the WVA autoscaling floor for the decode workload.
	// Setting MinReplicas or MaxReplicas enables KServe WorkloadSpec.Scaling,
	// and llmEngine.replicas is then ignored. KServe requires ≥ 1; WVA does
	// not scale to zero.
	MinReplicas *int32 `json:"minReplicas,omitempty"`

	// MaxReplicas is the WVA autoscaling ceiling for the decode workload.
	// Required when autoscaling is enabled.
	MaxReplicas *int32 `json:"maxReplicas,omitempty"`

	// ScalingActuator selects the WVA actuator backend: "keda" (default) or "hpa".
	// KEDA queries Prometheus directly; HPA needs a Prometheus Adapter.
	ScalingActuator string `json:"scalingActuator,omitempty"`

	// WorkerCount is the number of extra worker pods besides the head for a
	// pipeline-parallel multi-node model. Presence turns on
	// LLMInferenceService.spec.worker. Must equal pipelineParallelSize - 1.
	// PipelineParallelSize alone does not create workers.
	WorkerCount *int32 `json:"workerCount,omitempty"`

	// WorkerResources are optional CPU/memory/GPU requests for worker pods.
	// Unset copies llmEngine.resources. Setting this without WorkerCount is an error.
	WorkerResources *corev1.ResourceRequirements `json:"workerResources,omitempty"`
}

const (
	LoraDeploymentDisabled = "disabled"
	LoraDeploymentEnabled  = "enabled"
)

const (
	LocalModelCacheDisabled = "disabled"
	LocalModelCacheEnabled  = "enabled"
)

const (
	ScalingActuatorHPA  = "hpa"
	ScalingActuatorKEDA = "keda"
)

const (
	ExpertParallelDisabled = "disabled"
	ExpertParallelEnabled  = "enabled"
)

// ModelServerCustomSpec defines custom configuration for the modelServer
// component type, which is rendered into a KServe InferenceService predictor
// (serving.kserve.io/v1beta1).
type ModelServerCustomSpec struct {
	// Model names an entry of the chart's predictorModels catalog (the UI model
	// picker). It supplies modelFormat, storageURI and task unless modelFormat
	// or storageURI is set, in which case the catalog entry is ignored.
	Model string `json:"model,omitempty"`

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

	// Config is an inline InferenceService spec body (YAML). Nested maps are
	// merged with the generated spec; structured fields win and lists are
	// replaced, not appended. The merged resource is validated by the API server.
	Config string `json:"config,omitempty"`

	// MinReplicas is the autoscaling floor, at least 1: KServe Standard mode
	// does not scale to zero.
	MinReplicas *int32 `json:"minReplicas,omitempty"`

	// MaxReplicas is the autoscaling ceiling.
	MaxReplicas *int32 `json:"maxReplicas,omitempty"`

	// StorageContainer names a ClusterStorageContainer. When set, KServe uses
	// that initializer instead of matching on the storage URI scheme
	// (spec.predictor.storageContainerName), e.g. one with a larger memory limit
	// for big models. It must exist, be enabled, and support StorageURI.
	StorageContainer string `json:"storageContainer,omitempty"`

	// Env is added to the model server container (e.g. OMP_NUM_THREADS, or
	// HF_TOKEN from a Secret via valueFrom).
	Env []corev1.EnvVar `json:"env,omitempty"`

	// Args is appended to the serving runtime's arguments (e.g.
	// --task=text_embedding for the huggingface runtime). For huggingface
	// without a GPU the provider adds --backend=huggingface unless set here.
	Args []string `json:"args,omitempty"`

	// Task selects the huggingface runtime task (--task), e.g. text_embedding
	// or sequence_classification. Empty lets the runtime infer it from the
	// model config. Only valid with modelFormat huggingface; a --task in Args
	// wins.
	Task string `json:"task,omitempty"`

	// GpuCount is the number of GPUs (nvidia.com/gpu) each predictor pod
	// requests. Unset takes the catalog entry's value; 0 runs on CPU.
	GpuCount *int32 `json:"gpuCount,omitempty"`

	// ServiceAccountName is the ServiceAccount the predictor pods run as (e.g.
	// one bound to cloud IAM for model downloads). Empty uses the namespace
	// default.
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// Labels are added to the InferenceService; KServe copies them to the
	// predictor pods. Keys the provider manages are rejected.
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations are added to the InferenceService; KServe copies them to
	// the predictor pods. serving.kserve.io/deploymentMode is rejected.
	Annotations map[string]string `json:"annotations,omitempty"`

	// SecurityContext is the predictor pod's security context.
	SecurityContext *corev1.PodSecurityContext `json:"securityContext,omitempty"`

	// ContainerSecurityContext is the model server container's security
	// context.
	ContainerSecurityContext *corev1.SecurityContext `json:"containerSecurityContext,omitempty"`

	// Image serves the model with this container image instead of a KServe
	// serving runtime; model, modelFormat, runtime, runtimeVersion and task
	// do not apply. Args, env, gpuCount and the component resources apply to
	// this container, and storageURI (optional) is downloaded to /mnt/models.
	Image string `json:"image,omitempty"`

	// Command overrides the image entrypoint. Requires image.
	Command []string `json:"command,omitempty"`

	// Port is the HTTP port the image listens on (default 8080). Requires
	// image.
	Port *int32 `json:"port,omitempty"`

	// HealthPath is an HTTP path on port that returns 200 once the model is
	// loaded (e.g. /health); the pod gets no traffic before that. Requires
	// image.
	HealthPath string `json:"healthPath,omitempty"`

	// OpenAICompatible marks an image that serves the OpenAI API under /v1,
	// which makes it eligible for the Envoy AI Gateway. Shorthand for
	// apiSchema OpenAI. Requires image.
	OpenAICompatible bool `json:"openAICompatible,omitempty"`

	// APISchema is the API the image serves, which makes it eligible for the
	// Envoy AI Gateway: OpenAI (under /v1) or TypeSafe (the System One
	// decision API, POST /v1/systemone, served e.g. by OpenJev). Requires
	// image.
	APISchema string `json:"apiSchema,omitempty"`

	// BackendAPIKeySecret names a Secret in the Instance namespace whose
	// apiKey entry the Envoy AI Gateway sends to the image as a bearer token,
	// for images that require their own API key. Requires image and
	// externalAccess EnvoyAIGateway.
	BackendAPIKeySecret string `json:"backendAPIKeySecret,omitempty"`

	// ServedModelName is the model name the image expects in requests. The
	// connection details report it, and the AI Gateway rewrites the Instance
	// name to it. Requires image.
	ServedModelName string `json:"servedModelName,omitempty"`

	// ImagePullSecret names a Secret for pulling image from a private
	// registry. Requires image.
	ImagePullSecret string `json:"imagePullSecret,omitempty"`
}

// CustomContainerDefaultPort is the port a custom image is expected to listen
// on when port is unset; KServe runtimes use the same.
const CustomContainerDefaultPort int32 = 8080

// API schemas a custom image can serve to the Envoy AI Gateway.
const (
	APISchemaOpenAI   = "OpenAI"
	APISchemaTypeSafe = "TypeSafe"
)

// ResolvedAPISchema is the API the custom image serves: apiSchema, or OpenAI
// when only openAICompatible is set. Empty means none the gateway can route.
func (s ModelServerCustomSpec) ResolvedAPISchema() string {
	if s.APISchema != "" {
		return s.APISchema
	}
	if s.OpenAICompatible {
		return APISchemaOpenAI
	}
	return ""
}

// UsesCustomContainer reports whether the predictor runs a user image instead
// of a KServe serving runtime.
func (s ModelServerCustomSpec) UsesCustomContainer() bool {
	return s.Image != ""
}

// ContainerPort is the port the custom image listens on.
func (s ModelServerCustomSpec) ContainerPort() int32 {
	if s.Port != nil {
		return *s.Port
	}
	return CustomContainerDefaultPort
}
