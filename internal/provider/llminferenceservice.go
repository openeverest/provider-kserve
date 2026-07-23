// Package provider — LLMInferenceService (serving.kserve.io/v1alpha2) builder.
package provider

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"

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
	return nil
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
	if comp.Resources != nil {
		spec.WorkloadSpec.Template = &corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:      "main",
				Resources: *comp.Resources,
			}},
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
	if strings.EqualFold(params.ComputeProfile, computeProfileCPU) {
		spec.BaseRefs = append(spec.BaseRefs, corev1.LocalObjectReference{Name: cpuProfileConfigName})
	}
	for _, ref := range params.BaseRefs {
		spec.BaseRefs = append(spec.BaseRefs, corev1.LocalObjectReference{Name: ref})
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

	llmisvc, err := buildLLMInferenceService(c)
	if err != nil {
		return err
	}
	return common.Apply(c.Context(), c.Client(), c.Instance(), llmisvc)
}

// modelPullerSAName is the ServiceAccount the provider creates to carry the
// HuggingFace token Secret for an Instance's llm workload.
func modelPullerSAName(instance string) string {
	return instance + "-model-puller"
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
	if ready != nil && ready.IsTrue() && llmisvc.Status.URL != nil {
		return controller.ReadyWithConnectionDetails(connectionDetails(llmisvc.Status.URL)), nil
	}

	// A not-ready LLMInferenceService is still progressing, not failed. KServe
	// drives Ready through False during normal startup (e.g.
	// MinimumReplicasUnavailable while the storage-initializer downloads the
	// model), so surface it as Provisioning and let the condition message
	// explain the current state rather than flipping the Instance to Failed.
	return controller.Provisioning(conditionMessage(ready, "LLMInferenceService is being created")), nil
}
