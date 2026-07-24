// Package provider — LLMInferenceService (serving.kserve.io/v1alpha2) builder.
package provider

import (
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	"sigs.k8s.io/yaml"

	kservev1alpha2 "github.com/kserve/kserve/pkg/apis/serving/v1alpha2"

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
)

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
	if strings.TrimSpace(params.Config) != "" {
		if _, err := parseLLMConfigSpec(params.Config); err != nil {
			return err
		}
	}
	return nil
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
	if params.ModelName != "" {
		spec.Model.Name = ptr.To(params.ModelName)
	}

	// Static replicas from the component spec.
	if comp.Replicas != nil {
		spec.WorkloadSpec.Replicas = comp.Replicas
	}

	// Runtime parallelism.
	if params.TensorParallelSize != nil || params.PipelineParallelSize != nil {
		spec.WorkloadSpec.Parallelism = &kservev1alpha2.ParallelismSpec{
			Tensor:   params.TensorParallelSize,
			Pipeline: params.PipelineParallelSize,
		}
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
		// vLLM CPU probes *host-free* memory (not the cgroup limit) when sizing
		// its KV cache and refuses to start if too little is free. Reserving the
		// memory via requests keeps the scheduler from packing the pod onto an
		// oversubscribed node where that probe fails.
		if isCPUProfile {
			res = mirrorLimitsToRequests(res)
		}
		mainContainer.Resources = res
		haveMainOverride = true
	}

	// vLLM CPU auto-sizes its KV cache from host-free memory when
	// VLLM_CPU_KVCACHE_SPACE is unset, which adapts to the node. Only set it when
	// the user explicitly overrides it; a value derived from the container limit
	// cannot account for the (large, model-independent) CPU runtime footprint and
	// tends to exceed what is actually free at startup.
	if isCPUProfile && params.KVCacheSpaceGi != nil {
		kv := int(*params.KVCacheSpaceGi)
		if kv < 1 {
			kv = 1
		}
		mainContainer.Env = append(mainContainer.Env, corev1.EnvVar{Name: cpuKVCacheEnvVar, Value: strconv.Itoa(kv)})
		haveMainOverride = true
	}

	if haveMainOverride {
		spec.WorkloadSpec.Template = &corev1.PodSpec{
			Containers: []corev1.Container{mainContainer},
		}
	}

	// Attach the HuggingFace token ServiceAccount (created by
	// ensureModelPullerServiceAccount) so KServe's storage-initializer can
	// authenticate gated model downloads. KServe honors a user-set workload
	// ServiceAccountName only when no routing sidecar is present, so this path
	// covers the common (non-gateway-routing) gated download case.
	if common.HFTokenSecretName() != "" {
		if spec.WorkloadSpec.Template == nil {
			spec.WorkloadSpec.Template = &corev1.PodSpec{}
		}
		spec.WorkloadSpec.Template.ServiceAccountName = modelPullerSAName(c.Name())
	}

	// Config inheritance. The CPU compute profile composes the bundled CPU-only
	// LLMInferenceServiceConfig ahead of any user baseRefs, so an explicit
	// baseRef still wins on conflict (later refs override earlier ones).
	if isCPUProfile {
		spec.BaseRefs = append(spec.BaseRefs, corev1.LocalObjectReference{Name: cpuProfileConfigName})
	}
	for _, ref := range params.BaseRefs {
		spec.BaseRefs = append(spec.BaseRefs, corev1.LocalObjectReference{Name: ref})
	}
	// The inline Advanced config (materialized by ensureLLMConfig) is attached
	// last, so it overrides the CPU preset and any user baseRefs above while the
	// Instance's own structured fields (set on this spec) still win over it.
	if strings.TrimSpace(params.Config) != "" {
		spec.BaseRefs = append(spec.BaseRefs, corev1.LocalObjectReference{Name: instanceConfigName(c.Name())})
	}

	// Storage initializer toggle.
	if params.DisableStorageInitializer != nil {
		spec.StorageInitializer = &kservev1alpha2.StorageInitializerSpec{
			Enabled: ptr.To(!*params.DisableStorageInitializer),
		}
	}

	// Topology-level options (routing, disaggregation).
	var topo llm.LlmTopologyParameters
	c.TryDecodeTopologyParameters(&topo)

	if topo.EnableGatewayRouting {
		spec.Router = &kservev1alpha2.RouterSpec{
			Route:     &kservev1alpha2.GatewayRoutesSpec{},
			Gateway:   &kservev1alpha2.GatewaySpec{},
			Scheduler: &kservev1alpha2.SchedulerSpec{},
		}
	}

	if topo.EnablePrefill {
		prefill := &kservev1alpha2.WorkloadSpec{}
		if topo.PrefillReplicas != nil {
			prefill.Replicas = topo.PrefillReplicas
		}
		spec.Prefill = prefill
	}

	meta := c.ObjectMeta(c.Name())
	if meta.Annotations == nil {
		meta.Annotations = map[string]string{}
	}
	meta.Annotations[common.DeploymentModeAnnotation] = common.DeploymentModeRaw

	return &kservev1alpha2.LLMInferenceService{
		ObjectMeta: meta,
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

	// Materialize the inline Advanced config before the workload: it is
	// referenced via baseRefs and KServe errors if the config does not yet exist.
	if err := ensureLLMConfig(c); err != nil {
		return err
	}

	llmisvc, err := buildLLMInferenceService(c)
	if err != nil {
		return err
	}
	return common.Apply(c.Context(), c.Client(), c.Instance(), llmisvc)
}

// ensureLLMConfig materializes the inline Advanced config
// (llmEngine.parameters.config) as an Instance-owned LLMInferenceServiceConfig
// in the Instance namespace. KServe resolves baseRefs from the llmisvc's own
// namespace first, so a same-namespace config is inherited, and the owner
// reference garbage-collects it with the Instance. No-op when no inline config
// is set.
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
// that lacks an explicit request, yielding a Guaranteed-QoS container. The
// original ResourceRequirements maps are left untouched.
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
// the configured HuggingFace token Secret. KServe's llmisvc storage path reads
// the Secrets listed on the workload ServiceAccount and injects HF_TOKEN into
// the storage-initializer, enabling gated model downloads.
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
		// A Ready service without gateway routing exposes only the in-cluster
		// Service and KServe leaves Status.URL empty, so surface Ready with
		// connection details only when a URL is actually published.
		if llmisvc.Status.URL != nil {
			return controller.ReadyWithConnectionDetails(connectionDetails(llmisvc.Status.URL)), nil
		}
		return controller.Ready(), nil
	}

	// A not-ready LLMInferenceService is still progressing, not failed. KServe
	// drives Ready through False during normal startup (e.g.
	// MinimumReplicasUnavailable while the storage-initializer downloads the
	// model), so surface it as Provisioning and let the condition message
	// explain the current state rather than flipping the Instance to Failed.
	return controller.Provisioning(conditionMessage(ready, "LLMInferenceService is being created")), nil
}
