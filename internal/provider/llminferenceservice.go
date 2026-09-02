package provider

import (
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	"sigs.k8s.io/yaml"

	kservev1alpha2 "github.com/kserve/kserve/pkg/apis/serving/v1alpha2"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/definition/components"
	"github.com/openeverest/provider-kserve/definition/topologies/llm"
	"github.com/openeverest/provider-kserve/internal/common"
)

const (
	// computeProfileCPU is the VllmCustomSpec.ComputeProfile value that composes
	// the CPU-only LLMInferenceServiceConfig instead of the default GPU presets.
	computeProfileCPU = "cpu"

	// cpuProfileConfigName is the name of the bundled CPU LLMInferenceServiceConfig
	// rendered by the chart (templates/llmisvcconfig-cpu.yaml). It must match the
	// chart's metadata.name.
	cpuProfileConfigName = "kserve-config-llm-cpu"

	// instanceConfigSuffix names the per-Instance LLMInferenceServiceConfig the
	// provider materializes from the inline Advanced config
	// (llmEngine.parameters.config).
	instanceConfigSuffix = "-config"

	// cpuKVCacheEnvVar is the vLLM knob for the CPU KV cache size (in GiB). Left
	// unset, vLLM auto-sizes the KV cache from the memory that is actually free at
	// startup, which adapts to the node far better than any value derived from the
	// container limit (the CPU runtime footprint dwarfs small models). The
	// provider only sets it when the user explicitly overrides it
	// (kvCacheSpaceGi), leaving the adaptive default otherwise.
	cpuKVCacheEnvVar = "VLLM_CPU_KVCACHE_SPACE"

	// externalServiceSuffix names the extra Service the provider creates to
	// publish the model externally when the user selects LoadBalancer or NodePort
	// (KServe's own workload Service is always ClusterIP and provider-unowned).
	externalServiceSuffix = "-external"

	// vllmServingPort is the port the vLLM OpenAI-compatible API listens on and
	// the port KServe's workload Service targets.
	vllmServingPort = 8000

	nvidiaGPUResource = corev1.ResourceName("nvidia.com/gpu")
)

// workloadPodSelector returns the label selector KServe stamps on an
// LLMInferenceService's model pods, so a provider-owned Service can front them.
// Keep in sync with KServe's GetWorkloadLabelSelector.
func workloadPodSelector(instance string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":    instance,
		"app.kubernetes.io/part-of": "llminferenceservice",
		"kserve.io/component":       "workload",
	}
}

// externalServiceName is the name of the provider-owned external Service.
func externalServiceName(instance string) string {
	return instance + externalServiceSuffix
}

// validateLLM checks the Instance spec for the llm topology.
func validateLLM(c *controller.Context) error {
	comp, ok := c.Instance().Spec.Components[common.ComponentLlmEngine]
	if !ok {
		return fmt.Errorf("the %q component is required for the %q topology", common.ComponentLlmEngine, common.TopologyLLM)
	}

	var params components.VllmCustomSpec
	c.TryDecodeComponentParameters(comp, &params)
	if params.ModelURI == "" {
		return fmt.Errorf("%s.parameters.modelURI is required", common.ComponentLlmEngine)
	}
	if _, err := apis.ParseURL(params.ModelURI); err != nil {
		return fmt.Errorf("invalid %s.parameters.modelURI %q: %w", common.ComponentLlmEngine, params.ModelURI, err)
	}
	if comp.Replicas != nil && *comp.Replicas < 0 {
		return fmt.Errorf("%s replicas must not be negative", common.ComponentLlmEngine)
	}
	if params.DataParallelSize != nil && *params.DataParallelSize > 1 &&
		params.PipelineParallelSize != nil && *params.PipelineParallelSize > 1 {
		return fmt.Errorf("%s.parameters: dataParallelSize and pipelineParallelSize are mutually exclusive; set only one", common.ComponentLlmEngine)
	}
	if strings.TrimSpace(params.Config) != "" {
		if _, err := parseLLMConfigSpec(params.Config); err != nil {
			return err
		}
	}
	if comp.Service != nil {
		switch comp.Service.ServiceType {
		case "", corev1.ServiceTypeClusterIP, corev1.ServiceTypeLoadBalancer, corev1.ServiceTypeNodePort:
		default:
			return fmt.Errorf("%s.service.serviceType must be one of ClusterIP, LoadBalancer or NodePort", common.ComponentLlmEngine)
		}
	}

	var topo llm.LlmTopologyParameters
	c.TryDecodeTopologyParameters(&topo)
	switch topo.ExternalAccess {
	case "", llm.ExternalAccessClusterIP, llm.ExternalAccessLoadBalancer, llm.ExternalAccessNodePort, llm.ExternalAccessEnvoyAIGateway:
	default:
		return fmt.Errorf("externalAccess must be one of ClusterIP, LoadBalancer, NodePort or EnvoyAIGateway")
	}
	if topo.UsesAIGateway() && !common.AIGatewayEnabled() {
		return fmt.Errorf("externalAccess EnvoyAIGateway requires aiGateway.enabled in the provider chart")
	}
	if topo.TokenLimitPerHour != nil {
		if !topo.UsesAIGateway() {
			return fmt.Errorf("tokenLimitPerHour requires External access = EnvoyAIGateway")
		}
		if *topo.TokenLimitPerHour < 1 {
			return fmt.Errorf("tokenLimitPerHour must be greater than zero")
		}
	}
	decode := decodeScaling(params)
	if err := validateWorkloadScaling(
		decode,
		common.ComponentLlmEngine+".parameters.minReplicas",
		common.ComponentLlmEngine+".parameters.maxReplicas",
		common.ComponentLlmEngine+".parameters.scalingActuator",
	); err != nil {
		return err
	}
	prefill := prefillScaling(topo)
	if prefill.enabled() && !topo.EnablePrefill {
		return fmt.Errorf("prefill autoscaling requires enablePrefill")
	}
	if err := validateWorkloadScaling(
		prefill,
		"topology.parameters.prefillMinReplicas",
		"topology.parameters.prefillMaxReplicas",
		"topology.parameters.prefillScalingActuator",
	); err != nil {
		return err
	}
	if decode.enabled() && prefill.enabled() && resolvedActuator(decode.actuator) != resolvedActuator(prefill.actuator) {
		return fmt.Errorf("decode and prefill must use the same scalingActuator (KServe WVA)")
	}
	if err := validateWorkerGroup(
		params.WorkerCount,
		params.PipelineParallelSize,
		params.WorkerResources != nil,
		common.ComponentLlmEngine+".parameters.workerCount",
		common.ComponentLlmEngine+".parameters.pipelineParallelSize",
		common.ComponentLlmEngine+".parameters.workerResources",
	); err != nil {
		return err
	}
	cpuProfile := strings.EqualFold(params.ComputeProfile, computeProfileCPU)
	if err := validateTensorGPU(comp.Resources, params.TensorParallelSize, cpuProfile, common.ComponentLlmEngine+".resources"); err != nil {
		return err
	}
	if params.WorkerCount != nil {
		if err := validateTensorGPU(overlayResources(comp.Resources, params.WorkerResources), params.TensorParallelSize, cpuProfile, common.ComponentLlmEngine+".parameters.workerResources"); err != nil {
			return err
		}
	}
	prefillWorkers := topo.PrefillWorkerCount != nil || topo.PrefillWorkerResources != nil
	if prefillWorkers && !topo.EnablePrefill {
		return fmt.Errorf("prefill worker fields require enablePrefill")
	}
	if err := validateWorkerGroup(
		topo.PrefillWorkerCount,
		topo.PrefillPipelineParallelSize,
		topo.PrefillWorkerResources != nil,
		"topology.parameters.prefillWorkerCount",
		"topology.parameters.prefillPipelineParallelSize",
		"topology.parameters.prefillWorkerResources",
	); err != nil {
		return err
	}
	if topo.PrefillWorkerCount != nil {
		if err := validateTensorGPU(overlayResources(comp.Resources, topo.PrefillWorkerResources), params.TensorParallelSize, cpuProfile, "topology.parameters.prefillWorkerResources"); err != nil {
			return err
		}
	}
	return validateLoRAParams(c.Name(), params)
}

// workloadScaling is the min/max/actuator triple shared by decode and prefill.
type workloadScaling struct {
	min      *int32
	max      *int32
	actuator string
}

func (s workloadScaling) enabled() bool {
	return s.min != nil || s.max != nil || s.actuator != ""
}

func decodeScaling(params components.VllmCustomSpec) workloadScaling {
	return workloadScaling{min: params.MinReplicas, max: params.MaxReplicas, actuator: params.ScalingActuator}
}

func prefillScaling(topo llm.LlmTopologyParameters) workloadScaling {
	return workloadScaling{min: topo.PrefillMinReplicas, max: topo.PrefillMaxReplicas, actuator: topo.PrefillScalingActuator}
}

func resolvedActuator(actuator string) string {
	if strings.EqualFold(strings.TrimSpace(actuator), components.ScalingActuatorHPA) {
		return components.ScalingActuatorHPA
	}
	return components.ScalingActuatorKEDA
}

func validateWorkloadScaling(s workloadScaling, minField, maxField, actuatorField string) error {
	if !s.enabled() {
		return nil
	}
	if s.max == nil {
		return fmt.Errorf("%s is required when autoscaling is enabled", maxField)
	}
	if *s.max < 1 {
		return fmt.Errorf("%s must be at least 1", maxField)
	}
	if s.min != nil {
		if *s.min < 1 {
			return fmt.Errorf("%s must be at least 1 (KServe WVA does not support scale-to-zero)", minField)
		}
		if *s.min > *s.max {
			return fmt.Errorf("%s cannot exceed %s", minField, maxField)
		}
	}
	switch strings.ToLower(strings.TrimSpace(s.actuator)) {
	case "", components.ScalingActuatorHPA, components.ScalingActuatorKEDA:
	default:
		return fmt.Errorf("%s must be %s or %s", actuatorField, components.ScalingActuatorHPA, components.ScalingActuatorKEDA)
	}
	return nil
}

func validateWorkerGroup(count, pipeline *int32, resourcesSet bool, countField, pipelineField, resourcesField string) error {
	if count == nil {
		if resourcesSet {
			return fmt.Errorf("%s requires %s", resourcesField, countField)
		}
		return nil
	}
	if *count < 1 {
		return fmt.Errorf("%s must be at least 1", countField)
	}
	if pipeline == nil {
		return fmt.Errorf("%s is required when %s is set", pipelineField, countField)
	}
	if *pipeline != *count+1 {
		return fmt.Errorf("%s must equal %s + 1 (got %d, want %d)", pipelineField, countField, *pipeline, *count+1)
	}
	return nil
}

func validateTensorGPU(res *corev1.ResourceRequirements, tp *int32, cpuProfile bool, path string) error {
	if cpuProfile || tp == nil || *tp < 1 || res == nil {
		return nil
	}
	q, ok := res.Limits[nvidiaGPUResource]
	if !ok {
		q, ok = res.Requests[nvidiaGPUResource]
	}
	if !ok {
		return nil
	}
	if q.Value() < int64(*tp) {
		return fmt.Errorf("%s nvidia.com/gpu (%d) must be >= tensorParallelSize (%d)", path, q.Value(), *tp)
	}
	return nil
}

// overlayResources copies head, then applies overlay keys (UI may set only CPU/memory).
func overlayResources(head, overlay *corev1.ResourceRequirements) *corev1.ResourceRequirements {
	if overlay == nil {
		return head
	}
	if head == nil {
		return overlay
	}
	out := head.DeepCopy()
	if len(overlay.Limits) > 0 {
		if out.Limits == nil {
			out.Limits = corev1.ResourceList{}
		}
		for k, v := range overlay.Limits {
			out.Limits[k] = v
		}
	}
	if len(overlay.Requests) > 0 {
		if out.Requests == nil {
			out.Requests = corev1.ResourceList{}
		}
		for k, v := range overlay.Requests {
			out.Requests[k] = v
		}
	}
	return out
}

func buildWorkerPodSpec(res *corev1.ResourceRequirements) *corev1.PodSpec {
	c := corev1.Container{Name: "main"}
	if res != nil {
		c.Resources = *res
	}
	return &corev1.PodSpec{Containers: []corev1.Container{c}}
}

func buildScalingSpec(s workloadScaling) *kservev1alpha2.ScalingSpec {
	if !s.enabled() || s.max == nil {
		return nil
	}
	spec := &kservev1alpha2.ScalingSpec{
		MinReplicas: s.min,
		MaxReplicas: *s.max,
		WVA:         &kservev1alpha2.WVASpec{},
	}
	if resolvedActuator(s.actuator) == components.ScalingActuatorHPA {
		spec.WVA.HPA = &kservev1alpha2.HPAScalingSpec{}
	} else {
		spec.WVA.KEDA = &kservev1alpha2.KEDAScalingSpec{}
	}
	return spec
}

// validateLoRAParams checks llmEngine LoRA settings before sync.
func validateLoRAParams(instanceName string, params components.VllmCustomSpec) error {
	if err := validateLoRASlots(params); err != nil {
		return err
	}

	adapters, err := resolvedLoRAAdapters(params)
	if err != nil {
		return err
	}
	if len(adapters) == 0 {
		return nil
	}

	baseName := servedModelName(instanceName, params.ModelName)
	seen := make(map[string]int, len(adapters))

	for i, adapter := range adapters {
		path := fmt.Sprintf("%s.parameters.lora.adapters[%d]", common.ComponentLlmEngine, i)
		name := strings.TrimSpace(adapter.Name)
		if name == "" {
			return fmt.Errorf("%s.name is required", path)
		}
		if name == "." || name == ".." {
			return fmt.Errorf("%s.name %q is not a valid adapter name", path, name)
		}
		if name == baseName {
			return fmt.Errorf("%s.name %q must differ from the base served model name %q", path, name, baseName)
		}
		if prev, ok := seen[name]; ok {
			return fmt.Errorf("%s.name %q duplicates adapters[%d].name", path, name, prev)
		}
		seen[name] = i

		if strings.TrimSpace(adapter.URI) == "" {
			return fmt.Errorf("%s.uri is required", path)
		}
		if _, err := apis.ParseURL(adapter.URI); err != nil {
			return fmt.Errorf("invalid %s.uri %q: %w", path, adapter.URI, err)
		}
	}

	if params.LoRA != nil {
		if params.LoRA.MaxRank != nil && *params.LoRA.MaxRank < 1 {
			return fmt.Errorf("%s.parameters.lora.maxRank must be at least 1", common.ComponentLlmEngine)
		}
		if params.LoRA.MaxAdapters != nil && *params.LoRA.MaxAdapters < 1 {
			return fmt.Errorf("%s.parameters.lora.maxAdapters must be at least 1", common.ComponentLlmEngine)
		}
		if params.LoRA.MaxCpuAdapters != nil && *params.LoRA.MaxCpuAdapters < 1 {
			return fmt.Errorf("%s.parameters.lora.maxCpuAdapters must be at least 1", common.ComponentLlmEngine)
		}
	}

	if params.DisableStorageInitializer != nil && *params.DisableStorageInitializer {
		for i, adapter := range adapters {
			schema, _, ok := strings.Cut(adapter.URI, "://")
			if !ok {
				continue
			}
			switch schema + "://" {
			case "hf://", "s3://":
				return fmt.Errorf("%s.parameters.lora.adapters[%d] uses %s which requires the storage initializer; set disableStorageInitializer to false", common.ComponentLlmEngine, i, schema+"://")
			}
		}
	}
	return nil
}

func validateLoRASlots(params components.VllmCustomSpec) error {
	switch strings.TrimSpace(params.LoraDeployment) {
	case "", components.LoraDeploymentDisabled:
		return nil
	case components.LoraDeploymentEnabled:
		// continue
	default:
		return fmt.Errorf("%s.parameters.loraDeployment must be %q or %q", common.ComponentLlmEngine, components.LoraDeploymentDisabled, components.LoraDeploymentEnabled)
	}
	slots := loraSlotNames(params)
	if len(slots) == 0 {
		return fmt.Errorf("%s.parameters.loraDeployment enabled requires at least one adapter slot", common.ComponentLlmEngine)
	}
	seen := make(map[string]string, len(slots))
	for _, item := range []struct {
		field string
		name  string
	}{
		{"loraSlot1", params.LoraSlot1},
		{"loraSlot2", params.LoraSlot2},
		{"loraSlot3", params.LoraSlot3},
	} {
		name := strings.TrimSpace(item.name)
		if name == "" {
			continue
		}
		if prev, ok := seen[name]; ok {
			return fmt.Errorf("%s.parameters.%s and %s both select adapter %q", common.ComponentLlmEngine, prev, item.field, name)
		}
		seen[name] = item.field
		if _, ok := common.LookupLoRAAdapter(name); !ok {
			return fmt.Errorf("%s.parameters.%s references unknown adapter %q (add it to chart loraAdapters)", common.ComponentLlmEngine, item.field, name)
		}
	}
	return nil
}

func loraSlotNames(params components.VllmCustomSpec) []string {
	var slots []string
	for _, slot := range []string{params.LoraSlot1, params.LoraSlot2, params.LoraSlot3} {
		if name := strings.TrimSpace(slot); name != "" {
			slots = append(slots, name)
		}
	}
	return slots
}

// resolvedLoRAAdapters returns adapters from UI catalog slots or explicit lora.adapters YAML.
func resolvedLoRAAdapters(params components.VllmCustomSpec) ([]components.LoRAAdapterSpec, error) {
	if params.LoraDeployment == components.LoraDeploymentEnabled {
		var adapters []components.LoRAAdapterSpec
		for _, name := range loraSlotNames(params) {
			entry, ok := common.LookupLoRAAdapter(name)
			if !ok {
				return nil, fmt.Errorf("unknown lora adapter %q (not in chart loraAdapters catalog)", name)
			}
			adapters = append(adapters, components.LoRAAdapterSpec{Name: entry.Name, URI: entry.URI})
		}
		return adapters, nil
	}
	if params.LoRA != nil {
		return params.LoRA.Adapters, nil
	}
	return nil, nil
}

// buildModelLoRA maps provider LoRA parameters onto LLMInferenceService.spec.model.lora.
func buildModelLoRA(params components.VllmCustomSpec) (*kservev1alpha2.LoRASpec, error) {
	adapters, err := resolvedLoRAAdapters(params)
	if err != nil {
		return nil, err
	}
	if len(adapters) == 0 {
		return nil, nil
	}

	out := &kservev1alpha2.LoRASpec{
		Adapters: make([]kservev1alpha2.LLMModelSpec, 0, len(adapters)),
	}
	if params.LoRA != nil {
		out.MaxRank = params.LoRA.MaxRank
		out.MaxAdapters = params.LoRA.MaxAdapters
		out.MaxCpuAdapters = params.LoRA.MaxCpuAdapters
	}

	for _, adapter := range adapters {
		uri, err := apis.ParseURL(adapter.URI)
		if err != nil {
			return nil, fmt.Errorf("invalid lora adapter URI %q: %w", adapter.URI, err)
		}
		out.Adapters = append(out.Adapters, kservev1alpha2.LLMModelSpec{
			Name: ptr.To(strings.TrimSpace(adapter.Name)),
			URI:  *uri,
		})
	}
	return out, nil
}

// parseLLMConfigSpec decodes the inline Advanced config (YAML spec body) into an
// LLMInferenceServiceSpec. Parsing is lenient (unknown fields are ignored) so a
// config authored against a newer KServe than the one vendored here still loads.
func parseLLMConfigSpec(raw string) (kservev1alpha2.LLMInferenceServiceSpec, error) {
	var spec kservev1alpha2.LLMInferenceServiceSpec
	if err := yaml.Unmarshal([]byte(raw), &spec); err != nil {
		return spec, fmt.Errorf("invalid %s.parameters.config: %w", common.ComponentLlmEngine, err)
	}
	return spec, nil
}

// instanceConfigName is the name of the Instance-owned LLMInferenceServiceConfig
// materialized from the inline Advanced config.
func instanceConfigName(instance string) string {
	return instance + instanceConfigSuffix
}

// servedModelName returns the model name advertised in the OpenAI API.
func servedModelName(instanceName, configuredName string) string {
	if configuredName != "" {
		return configuredName
	}
	return instanceName
}

// gatewayRoutingEnabled returns true when either Gateway API routing or the
// Envoy AI Gateway is enabled (the AI Gateway implies gateway routing).
func gatewayRoutingEnabled(topo llm.LlmTopologyParameters) bool {
	return topo.EnableGatewayRouting || topo.UsesAIGateway()
}

// routingConfigRefs returns the LLMInferenceServiceConfig baseRefs needed for
// Gateway API routing (scheduler + optional route config).
func routingConfigRefs(topo llm.LlmTopologyParameters) []string {
	if !gatewayRoutingEnabled(topo) {
		return nil
	}

	refs := []string{
		"kserve-config-llm-scheduler",
	}
	if topo.EnableGatewayRouting {
		refs = append(refs, "kserve-config-llm-router-route")
	}
	return refs
}

// buildLLMInferenceService translates an Instance into an LLMInferenceService.
func buildLLMInferenceService(c *controller.Context) (*kservev1alpha2.LLMInferenceService, error) {
	comp := c.Instance().Spec.Components[common.ComponentLlmEngine]

	var params components.VllmCustomSpec
	c.TryDecodeComponentParameters(comp, &params)

	if params.ModelURI == "" {
		return nil, fmt.Errorf("%s.parameters.modelURI is required", common.ComponentLlmEngine)
	}
	uri, err := apis.ParseURL(params.ModelURI)
	if err != nil {
		return nil, fmt.Errorf("invalid modelURI %q: %w", params.ModelURI, err)
	}

	spec := kservev1alpha2.LLMInferenceServiceSpec{
		Model: kservev1alpha2.LLMModelSpec{URI: *uri},
	}
	spec.Model.Name = ptr.To(servedModelName(c.Name(), params.ModelName))

	lora, err := buildModelLoRA(params)
	if err != nil {
		return nil, err
	}
	spec.Model.LoRA = lora

	// Static replicas, or WVA autoscaling (mutually exclusive on the CR).
	if decode := decodeScaling(params); decode.enabled() {
		spec.Scaling = buildScalingSpec(decode)
	} else if comp.Replicas != nil {
		spec.Replicas = comp.Replicas
	}

	// Runtime parallelism. KServe rejects pipeline and data parallelism set
	// together, and requires dataLocal whenever data is set. Treat data
	// parallelism as requested only when dataParallelSize > 1 (1 is a no-op) so
	// the default UI value never displaces pipeline parallelism. For a
	// single-node data-parallel layout dataLocal mirrors data.
	expertParallel := strings.EqualFold(params.ExpertParallel, components.ExpertParallelEnabled)
	dataParallel := params.DataParallelSize != nil && *params.DataParallelSize > 1
	if params.TensorParallelSize != nil || params.PipelineParallelSize != nil ||
		dataParallel || expertParallel {
		p := &kservev1alpha2.ParallelismSpec{
			Tensor: params.TensorParallelSize,
			Expert: expertParallel,
		}
		if dataParallel {
			p.Data = params.DataParallelSize
			p.DataLocal = params.DataParallelSize
		} else {
			p.Pipeline = params.PipelineParallelSize
		}
		spec.Parallelism = p
	}

	if params.WorkerCount != nil {
		spec.Worker = buildWorkerPodSpec(overlayResources(comp.Resources, params.WorkerResources))
	}

	// Optional pod resource requirements from the component spec. KServe
	// strategic-merges the Instance spec last, so a "main" container override
	// here wins over the preset and any baseRefs.
	mainContainer := corev1.Container{Name: "main"}
	haveMainOverride := false
	isCPUProfile := strings.EqualFold(params.ComputeProfile, computeProfileCPU)
	if comp.Resources != nil {
		res := *comp.Resources
		// For the CPU profile, mirror limits into requests (Guaranteed QoS).
		if isCPUProfile {
			res = mirrorLimitsToRequests(res)
		}
		mainContainer.Resources = res
		haveMainOverride = true
	}

	// vLLM CPU auto-sizes its KV cache from host-free memory when
	// VLLM_CPU_KVCACHE_SPACE is unset.
	if isCPUProfile && params.KVCacheSpaceGi != nil {
		kv := int(*params.KVCacheSpaceGi)
		if kv < 1 {
			kv = 1
		}
		mainContainer.Env = append(mainContainer.Env, corev1.EnvVar{Name: cpuKVCacheEnvVar, Value: strconv.Itoa(kv)})
		haveMainOverride = true
	}

	if haveMainOverride {
		spec.Template = &corev1.PodSpec{
			Containers: []corev1.Container{mainContainer},
		}
	}

	// Attach the HuggingFace token ServiceAccount.
	if common.HFTokenSecretName() != "" {
		if spec.Template == nil {
			spec.Template = &corev1.PodSpec{}
		}
		spec.Template.ServiceAccountName = modelPullerSAName(c.Name())
	}

	// Config inheritance. The CPU compute profile composes the bundled CPU-only
	// LLMInferenceServiceConfig ahead of any user baseRefs.
	if isCPUProfile {
		spec.BaseRefs = append(spec.BaseRefs, corev1.LocalObjectReference{Name: cpuProfileConfigName})
	}
	var topo llm.LlmTopologyParameters
	c.TryDecodeTopologyParameters(&topo)
	for _, name := range routingConfigRefs(topo) {
		spec.BaseRefs = append(spec.BaseRefs, corev1.LocalObjectReference{Name: name})
	}
	for _, ref := range params.BaseRefs {
		spec.BaseRefs = append(spec.BaseRefs, corev1.LocalObjectReference{Name: ref})
	}
	// The inline Advanced config (materialized by ensureLLMConfig) is attached last.
	if strings.TrimSpace(params.Config) != "" {
		spec.BaseRefs = append(spec.BaseRefs, corev1.LocalObjectReference{Name: instanceConfigName(c.Name())})
	}

	// Storage initializer toggle.
	if params.DisableStorageInitializer != nil {
		spec.StorageInitializer = &kservev1alpha2.StorageInitializerSpec{
			Enabled: ptr.To(!*params.DisableStorageInitializer),
		}
	}

	if topo.EnablePrefill {
		prefill := &kservev1alpha2.WorkloadSpec{}
		if scale := prefillScaling(topo); scale.enabled() {
			prefill.Scaling = buildScalingSpec(scale)
		} else if topo.PrefillReplicas != nil {
			prefill.Replicas = topo.PrefillReplicas
		}
		if topo.PrefillPipelineParallelSize != nil {
			prefill.Parallelism = &kservev1alpha2.ParallelismSpec{
				Pipeline: topo.PrefillPipelineParallelSize,
			}
		}
		if comp.Resources != nil {
			res := *comp.Resources
			if isCPUProfile {
				res = mirrorLimitsToRequests(res)
			}
			prefill.Template = &corev1.PodSpec{
				Containers: []corev1.Container{{Name: "main", Resources: res}},
			}
		}
		if topo.PrefillWorkerCount != nil {
			prefill.Worker = buildWorkerPodSpec(overlayResources(comp.Resources, topo.PrefillWorkerResources))
		}
		spec.Prefill = prefill
	}

	// Distributed tracing. A present-but-empty TracingSpec enables KServe's
	// default OTLP instrumentation; an endpoint override is applied when set.
	if topo.EnableTracing {
		tracing := &kservev1alpha2.TracingSpec{}
		if ep := strings.TrimSpace(topo.TracingEndpoint); ep != "" {
			tracing.ExporterEndpoint = ptr.To(ep)
		}
		spec.Tracing = tracing
	}

	// No deploymentMode annotation here: the llmisvc controller has no
	// Knative path and never reads it (grep DeploymentMode under
	// pkg/controller/v1alpha1/llmisvc — no hits). Only InferenceService uses it.
	return &kservev1alpha2.LLMInferenceService{
		ObjectMeta: c.ObjectMeta(c.Name()),
		Spec:       spec,
	}, nil
}

// syncLLM creates or updates the LLMInferenceService.
func (p *Provider) syncLLM(c *controller.Context) error {
	// Provision the HuggingFace token ServiceAccount before the workload so
	// KServe finds it when it reconciles the storage-initializer.
	if common.HFTokenSecretName() != "" {
		if err := ensureModelPullerServiceAccount(c); err != nil {
			return err
		}
	}

	// Materialize the inline Advanced config before the workload.
	if err := ensureLLMConfig(c); err != nil {
		return err
	}

	llmisvc, err := buildLLMInferenceService(c)
	if err != nil {
		return err
	}
	if err := common.Apply(c.Context(), c.Client(), c.Instance(), llmisvc); err != nil {
		return err
	}

	var topo llm.LlmTopologyParameters
	c.TryDecodeTopologyParameters(&topo)

	// Per-instance vLLM PodMonitor (guarded by chart + instance flags).
	if err := syncPodMonitor(c, topo); err != nil {
		return err
	}

	// Publish the model externally when requested (LoadBalancer/NodePort).
	// When the AI Gateway is enabled, it takes over external access and any
	// previously created external Service is cleaned up.
	if topo.UsesAIGateway() {
		// Remove any legacy external Service (AI Gateway replaces it).
		stale := &corev1.Service{ObjectMeta: c.ObjectMeta(externalServiceName(c.Name()))}
		if err := c.Delete(stale); err != nil {
			return err
		}
		comp := c.Instance().Spec.Components[common.ComponentLlmEngine]
		var params components.VllmCustomSpec
		c.TryDecodeComponentParameters(comp, &params)
		return syncAIGateway(c, servedModelName(c.Name(), params.ModelName), topo.TokenLimitPerHour)
	}

	// AI Gateway not enabled — clean up any leftover AI Gateway resources.
	if common.AIGatewayEnabled() {
		if err := cleanupAIGateway(c); err != nil {
			return err
		}
	}

	// Reconcile the external Service for LoadBalancer/NodePort.
	return ensureExternalService(c)
}

// ensureExternalService reconciles the provider-owned external Service for the
// llm workload. It creates a LoadBalancer/NodePort Service selecting the KServe
// workload pods when the user picks that expose type, and deletes any stale one
// when the expose type is ClusterIP (the default) so switching back cleans up.
func ensureExternalService(c *controller.Context) error {
	comp := c.Instance().Spec.Components[common.ComponentLlmEngine]

	var topo llm.LlmTopologyParameters
	c.TryDecodeTopologyParameters(&topo)

	svcType := corev1.ServiceTypeClusterIP
	if resolved := topo.ResolvedServiceType(); resolved != "" {
		svcType = resolved
	} else if comp.Service != nil && comp.Service.ServiceType != "" {
		svcType = comp.Service.ServiceType
	}

	if svcType == corev1.ServiceTypeClusterIP {
		// No external Service needed; remove a previously created one if the
		// user switched away from LoadBalancer/NodePort.
		stale := &corev1.Service{ObjectMeta: c.ObjectMeta(externalServiceName(c.Name()))}
		return c.Delete(stale)
	}

	svc := buildExternalService(c, comp.Service, svcType)
	return common.Apply(c.Context(), c.Client(), c.Instance(), svc)
}

// buildExternalService builds the LoadBalancer/NodePort Service that fronts the
// KServe workload pods on the vLLM serving port.
func buildExternalService(c *controller.Context, spec *corev1alpha1.Service, svcType corev1.ServiceType) *corev1.Service {
	meta := c.ObjectMeta(externalServiceName(c.Name()))
	if spec != nil && len(spec.Annotations) > 0 {
		if meta.Annotations == nil {
			meta.Annotations = map[string]string{}
		}
		for k, v := range spec.Annotations {
			meta.Annotations[k] = v
		}
	}

	svc := &corev1.Service{
		ObjectMeta: meta,
		Spec: corev1.ServiceSpec{
			Type:     svcType,
			Selector: workloadPodSelector(c.Name()),
			Ports: []corev1.ServicePort{{
				Name:       "http",
				Protocol:   corev1.ProtocolTCP,
				Port:       vllmServingPort,
				TargetPort: intstr.FromInt(vllmServingPort),
			}},
		},
	}

	if svcType == corev1.ServiceTypeLoadBalancer && spec != nil && spec.LoadBalancerService != nil {
		svc.Spec.LoadBalancerSourceRanges = spec.LoadBalancerService.SourceRanges.NormalizedSourceRanges()
	}

	return svc
}

// ensureLLMConfig materializes the inline Advanced config
// (llmEngine.parameters.config) as an Instance-owned LLMInferenceServiceConfig.
func ensureLLMConfig(c *controller.Context) error {
	comp := c.Instance().Spec.Components[common.ComponentLlmEngine]

	var params components.VllmCustomSpec
	c.TryDecodeComponentParameters(comp, &params)
	if strings.TrimSpace(params.Config) == "" {
		return nil
	}

	spec, err := parseLLMConfigSpec(params.Config)
	if err != nil {
		return err
	}

	cfg := &kservev1alpha2.LLMInferenceServiceConfig{
		ObjectMeta: c.ObjectMeta(instanceConfigName(c.Name())),
		Spec:       spec,
	}
	return common.Apply(c.Context(), c.Client(), c.Instance(), cfg)
}

// modelPullerSAName is the ServiceAccount the provider creates to carry the
// HuggingFace token Secret for an Instance's llm workload.
func modelPullerSAName(instance string) string {
	return instance + "-model-puller"
}

// mirrorLimitsToRequests returns a copy of res whose Requests mirror any Limit
// that lacks an explicit request, yielding a Guaranteed-QoS container.
func mirrorLimitsToRequests(res corev1.ResourceRequirements) corev1.ResourceRequirements {
	if len(res.Limits) == 0 {
		return res
	}
	reqs := corev1.ResourceList{}
	for name, qty := range res.Requests {
		reqs[name] = qty
	}
	for name, qty := range res.Limits {
		if _, ok := reqs[name]; !ok {
			reqs[name] = qty.DeepCopy()
		}
	}
	res.Requests = reqs
	return res
}

// ensureModelPullerServiceAccount creates/updates a ServiceAccount referencing
// the configured HuggingFace token Secret.
func ensureModelPullerServiceAccount(c *controller.Context) error {
	sa := &corev1.ServiceAccount{
		ObjectMeta: c.ObjectMeta(modelPullerSAName(c.Name())),
		Secrets: []corev1.ObjectReference{
			{Name: common.HFTokenSecretName()},
		},
	}
	return common.Apply(c.Context(), c.Client(), c.Instance(), sa)
}

// statusLLM translates the LLMInferenceService status into a provider Status.
func (p *Provider) statusLLM(c *controller.Context) (controller.Status, error) {
	llmisvc := &kservev1alpha2.LLMInferenceService{}
	if err := c.Get(llmisvc, c.Name()); err != nil {
		return controller.Provisioning("Waiting for LLMInferenceService"), nil
	}

	ready := llmisvc.Status.GetCondition(apis.ConditionReady)
	if ready != nil && ready.IsTrue() {
		var topo llm.LlmTopologyParameters
		c.TryDecodeTopologyParameters(&topo)

		// When the AI Gateway is enabled, connection details come from the
		// Gateway's external address rather than the direct workload URL.
		if topo.UsesAIGateway() {
			details, waiting, err := aiGatewayConnectionDetails(c)
			if err != nil {
				return controller.Status{}, err
			}
			if waiting != "" {
				return controller.Provisioning(waiting), nil
			}
			return controller.ReadyWithConnectionDetails(details), nil
		}

		// Otherwise surface connection details from the external Service or
		// direct KServe workload URL.
		details, err := p.llmConnectionDetails(c, llmisvc)
		if err != nil {
			return controller.Provisioning(err.Error()), nil
		}
		if details != nil {
			return controller.ReadyWithConnectionDetails(*details), nil
		}
		return controller.Ready(), nil
	}

	return controller.Provisioning(conditionMessage(ready, "LLMInferenceService is being created")), nil
}

// llmConnectionDetails resolves how to reach a Ready model based on its expose
// type. It returns nil (Ready without details) when an external address is
// requested but not yet available.
func (p *Provider) llmConnectionDetails(c *controller.Context, llmisvc *kservev1alpha2.LLMInferenceService) (*controller.ConnectionDetails, error) {
	// Gateway API routing publishes an external URL directly on the status.
	if llmisvc.Status.URL != nil {
		d := connectionDetails(llmisvc.Status.URL)
		return &d, nil
	}

	comp := c.Instance().Spec.Components[common.ComponentLlmEngine]
	var topo llm.LlmTopologyParameters
	c.TryDecodeTopologyParameters(&topo)

	svcType := corev1.ServiceTypeClusterIP
	if resolved := topo.ResolvedServiceType(); resolved != "" {
		svcType = resolved
	} else if comp.Service != nil && comp.Service.ServiceType != "" {
		svcType = comp.Service.ServiceType
	}

	switch svcType {
	case corev1.ServiceTypeLoadBalancer, corev1.ServiceTypeNodePort:
		return p.externalServiceConnectionDetails(c, svcType)
	default:
		// ClusterIP: reachable in-cluster via the KServe workload Service DNS.
		host := workloadServiceHost(c, llmisvc)
		d := kserveConnectionDetails(host, strconv.Itoa(vllmServingPort))
		return &d, nil
	}
}

// externalServiceConnectionDetails derives the endpoint of the provider-owned
// external Service. Returns nil when the address is not yet assigned.
func (p *Provider) externalServiceConnectionDetails(c *controller.Context, svcType corev1.ServiceType) (*controller.ConnectionDetails, error) {
	svc := &corev1.Service{}
	if err := c.Get(svc, externalServiceName(c.Name())); err != nil {
		return nil, nil
	}

	if svcType == corev1.ServiceTypeLoadBalancer {
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			host := ing.IP
			if host == "" {
				host = ing.Hostname
			}
			if host != "" {
				d := kserveConnectionDetails(host, strconv.Itoa(vllmServingPort))
				return &d, nil
			}
		}
		// LoadBalancer provisioning still in progress.
		return nil, nil
	}

	// NodePort: pair a reachable node address with the allocated node port.
	var nodePort int32
	for _, p := range svc.Spec.Ports {
		if p.NodePort != 0 {
			nodePort = p.NodePort
			break
		}
	}
	if nodePort == 0 {
		return nil, nil
	}
	host := firstNodeAddress(c)
	if host == "" {
		return nil, nil
	}
	d := kserveConnectionDetails(host, strconv.Itoa(int(nodePort)))
	return &d, nil
}

// workloadServiceHost returns the in-cluster DNS name of the KServe workload
// Service.
func workloadServiceHost(c *controller.Context, llmisvc *kservev1alpha2.LLMInferenceService) string {
	name := c.Name() + "-kserve-workload-svc"
	if ws := llmisvc.Status.Workloads; ws != nil && ws.Service != nil && ws.Service.Name != "" {
		name = ws.Service.Name
	}
	return fmt.Sprintf("%s.%s.svc.cluster.local", name, c.Namespace())
}

// firstNodeAddress returns an address for reaching a NodePort Service.
func firstNodeAddress(c *controller.Context) string {
	nodes := &corev1.NodeList{}
	if err := c.List(nodes); err != nil {
		return ""
	}
	var internal string
	for _, node := range nodes.Items {
		for _, addr := range node.Status.Addresses {
			switch addr.Type {
			case corev1.NodeExternalIP:
				if addr.Address != "" {
					return addr.Address
				}
			case corev1.NodeInternalIP:
				if internal == "" {
					internal = addr.Address
				}
			}
		}
	}
	return internal
}

// kserveConnectionDetails builds ConnectionDetails for the vLLM HTTP endpoint.
func kserveConnectionDetails(host, port string) controller.ConnectionDetails {
	return controller.ConnectionDetails{
		Type:     "kserve",
		Provider: common.ProviderName,
		Host:     host,
		Port:     port,
		URI:      fmt.Sprintf("http://%s:%s", host, port),
	}
}
