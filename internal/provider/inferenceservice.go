// Package provider — InferenceService (serving.kserve.io/v1beta1) builder.
package provider

import (
	"fmt"
	"strings"

	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"

	kservev1beta1 "github.com/kserve/kserve/pkg/apis/serving/v1beta1"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/definition/components"
	"github.com/openeverest/provider-kserve/internal/common"
)

// validatePredictor checks the Instance spec for the predictor topology.
func validatePredictor(c *controller.Context) error {
	comp, ok := c.Instance().Spec.Components[common.ComponentPredictor]
	if !ok {
		return fmt.Errorf("the %q component is required for the %q topology", common.ComponentPredictor, common.TopologyPredictor)
	}

	var params components.ModelServerCustomSpec
	c.TryDecodeComponentParameters(comp, &params)
	if params.ModelFormat == "" {
		return fmt.Errorf("%s.parameters.modelFormat is required", common.ComponentPredictor)
	}
	if params.StorageURI == "" {
		return fmt.Errorf("%s.parameters.storageURI is required", common.ComponentPredictor)
	}
	if params.MinReplicas != nil && *params.MinReplicas < 0 {
		return fmt.Errorf("%s.parameters.minReplicas must not be negative", common.ComponentPredictor)
	}
	return nil
}

// buildInferenceService translates an Instance into an InferenceService.
func buildInferenceService(c *controller.Context) (*kservev1beta1.InferenceService, error) {
	comp := c.Instance().Spec.Components[common.ComponentPredictor]

	var params components.ModelServerCustomSpec
	c.TryDecodeComponentParameters(comp, &params)

	if params.ModelFormat == "" {
		return nil, fmt.Errorf("%s.parameters.modelFormat is required", common.ComponentPredictor)
	}

	model := &kservev1beta1.ModelSpec{
		ModelFormat: kservev1beta1.ModelFormat{Name: params.ModelFormat},
	}
	if params.StorageURI != "" {
		model.StorageURI = ptr.To(params.StorageURI)
	}
	if params.RuntimeVersion != "" {
		model.RuntimeVersion = ptr.To(params.RuntimeVersion)
	}
	if params.Runtime != "" {
		model.Runtime = ptr.To(params.Runtime)
	}
	if comp.Resources != nil {
		model.Container.Resources = *comp.Resources
	}

	predictor := kservev1beta1.PredictorSpec{Model: model}

	switch {
	case params.MinReplicas != nil:
		predictor.MinReplicas = params.MinReplicas
	case comp.Replicas != nil:
		predictor.MinReplicas = comp.Replicas
	}
	if params.MaxReplicas != nil {
		predictor.MaxReplicas = *params.MaxReplicas
	}

	meta := c.ObjectMeta(c.Name())
	if meta.Annotations == nil {
		meta.Annotations = map[string]string{}
	}
	meta.Annotations[common.DeploymentModeAnnotation] = common.DeploymentModeRaw

	return &kservev1beta1.InferenceService{
		ObjectMeta: meta,
		Spec: kservev1beta1.InferenceServiceSpec{
			Predictor: predictor,
		},
	}, nil
}

// syncPredictor creates or updates the InferenceService.
func (p *Provider) syncPredictor(c *controller.Context) error {
	isvc, err := buildInferenceService(c)
	if err != nil {
		return err
	}
	return c.Apply(isvc)
}

// statusPredictor translates the InferenceService status into a provider Status.
func (p *Provider) statusPredictor(c *controller.Context) (controller.Status, error) {
	isvc := &kservev1beta1.InferenceService{}
	if err := c.Get(isvc, c.Name()); err != nil {
		return controller.Provisioning("Waiting for InferenceService"), nil
	}

	ready := isvc.Status.GetCondition(apis.ConditionReady)
	if ready != nil && ready.IsTrue() && isvc.Status.URL != nil {
		return controller.ReadyWithConnectionDetails(connectionDetails(isvc.Status.URL)), nil
	}

	if ready != nil && ready.IsFalse() {
		return controller.Failed(conditionMessage(ready, "InferenceService is not ready")), nil
	}
	return controller.Provisioning(conditionMessage(ready, "InferenceService is being created")), nil
}

// connectionDetails builds ConnectionDetails from a KServe status URL.
func connectionDetails(u *apis.URL) controller.ConnectionDetails {
	host := u.Host
	port := "80"
	if u.Scheme == "https" {
		port = "443"
	}
	if h, p, ok := strings.Cut(u.Host, ":"); ok {
		host = h
		port = p
	}
	return controller.ConnectionDetails{
		Type:     "kserve",
		Provider: common.ProviderName,
		Host:     host,
		Port:     port,
		URI:      u.String(),
	}
}

// conditionMessage returns a human-readable message for a condition.
func conditionMessage(cond *apis.Condition, fallback string) string {
	if cond == nil {
		return fallback
	}
	if cond.Message != "" {
		return cond.Message
	}
	if cond.Reason != "" {
		return cond.Reason
	}
	return fallback
}
