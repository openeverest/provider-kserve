// Package provider — LLMInferenceService (serving.kserve.io/v1alpha2) builder.
package provider

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"

	kservev1alpha2 "github.com/kserve/kserve/pkg/apis/serving/v1alpha2"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/definition/components"
	"github.com/openeverest/provider-kserve/definition/topologies/llm"
	"github.com/openeverest/provider-kserve/internal/common"
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

	// Optional pod resource requirements from the component spec.
	if comp.Resources != nil {
		spec.WorkloadSpec.Template = &corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:      "main",
				Resources: *comp.Resources,
			}},
		}
	}

	// Config inheritance.
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
	llmisvc, err := buildLLMInferenceService(c)
	if err != nil {
		return err
	}
	return c.Apply(llmisvc)
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

	if ready != nil && ready.IsFalse() {
		return controller.Failed(conditionMessage(ready, "LLMInferenceService is not ready")), nil
	}
	return controller.Provisioning(conditionMessage(ready, "LLMInferenceService is being created")), nil
}
