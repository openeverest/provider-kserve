// Package provider — InferenceService (serving.kserve.io/v1beta1) builder.
package provider

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"knative.dev/pkg/apis"
	duckv1 "knative.dev/pkg/apis/duck/v1"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/definition/components"
	"github.com/openeverest/provider-kserve/internal/common"
)

// The KServe v1beta1 Go package does not build against the k8s.io/api version
// OpenEverest requires, so the InferenceService fields the provider sets are
// mirrored here and applied as an unstructured object.
var inferenceServiceGVK = schema.GroupVersionKind{
	Group: "serving.kserve.io", Version: "v1beta1", Kind: "InferenceService",
}

type inferenceServiceSpec struct {
	Predictor predictorSpec `json:"predictor"`
}

type predictorSpec struct {
	MinReplicas *int32            `json:"minReplicas,omitempty"`
	MaxReplicas int32             `json:"maxReplicas,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Model       *modelSpec        `json:"model,omitempty"`
}

type modelSpec struct {
	ModelFormat    modelFormat                  `json:"modelFormat"`
	Runtime        string                       `json:"runtime,omitempty"`
	StorageURI     string                       `json:"storageUri,omitempty"`
	RuntimeVersion string                       `json:"runtimeVersion,omitempty"`
	Resources      *corev1.ResourceRequirements `json:"resources,omitempty"`
}

type modelFormat struct {
	Name string `json:"name"`
}

type inferenceServiceStatus struct {
	duckv1.Status `json:",inline"`
	URL           *apis.URL `json:"url,omitempty"`
}

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
func buildInferenceService(c *controller.Context) (*unstructured.Unstructured, error) {
	comp := c.Instance().Spec.Components[common.ComponentPredictor]

	var params components.ModelServerCustomSpec
	c.TryDecodeComponentParameters(comp, &params)

	if params.ModelFormat == "" {
		return nil, fmt.Errorf("%s.parameters.modelFormat is required", common.ComponentPredictor)
	}

	predictor := predictorSpec{Model: &modelSpec{
		ModelFormat:    modelFormat{Name: params.ModelFormat},
		Runtime:        params.Runtime,
		StorageURI:     params.StorageURI,
		RuntimeVersion: params.RuntimeVersion,
		Resources:      comp.Resources,
	}}
	predictor.Labels = c.PodLabels(common.ComponentPredictor)

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
	meta.Annotations[common.DeploymentModeAnnotation] = common.DeploymentModeStandard

	spec, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&inferenceServiceSpec{Predictor: predictor})
	if err != nil {
		return nil, fmt.Errorf("building InferenceService spec: %w", err)
	}
	isvc := unstructuredObject(inferenceServiceGVK)
	isvc.SetName(meta.Name)
	isvc.SetNamespace(meta.Namespace)
	isvc.SetLabels(meta.Labels)
	isvc.SetAnnotations(meta.Annotations)
	isvc.Object["spec"] = spec
	return isvc, nil
}

// syncPredictor creates or updates the InferenceService.
func (p *Provider) syncPredictor(c *controller.Context) error {
	isvc, err := buildInferenceService(c)
	if err != nil {
		return err
	}
	return common.Apply(c.Context(), c.Client(), c.Instance(), isvc)
}

// statusPredictor translates the InferenceService status into a provider Status.
func (p *Provider) statusPredictor(c *controller.Context) (controller.Status, error) {
	isvc := unstructuredObject(inferenceServiceGVK)
	if err := c.Get(isvc, c.Name()); err != nil {
		return controller.Provisioning("Waiting for InferenceService"), nil
	}

	var status inferenceServiceStatus
	if raw, ok := isvc.Object["status"].(map[string]any); ok {
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &status); err != nil {
			return controller.Status{}, fmt.Errorf("decoding InferenceService status: %w", err)
		}
	}

	ready := status.GetCondition(apis.ConditionReady)
	if ready != nil && ready.IsTrue() {
		// A Ready service without external ingress exposes only the in-cluster
		// Service and KServe leaves Status.URL empty, so surface Ready with
		// connection details only when a URL is actually published.
		if status.URL != nil {
			return controller.ReadyWithConnectionDetails(connectionDetails(status.URL)), nil
		}
		return controller.Ready(), nil
	}

	// A not-ready InferenceService is still progressing, not failed. KServe
	// drives Ready through False during normal startup (e.g.
	// MinimumReplicasUnavailable while the storage-initializer downloads the
	// model), so surface it as Provisioning and let the condition message
	// explain the current state rather than flipping the Instance to Failed.
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
