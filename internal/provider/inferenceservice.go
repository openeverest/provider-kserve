// Package provider — InferenceService (serving.kserve.io/v1beta1) builder.
package provider

import (
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"

	kservev1beta1 "github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	kserveconstants "github.com/kserve/kserve/pkg/constants"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/definition/components"
	"github.com/openeverest/provider-kserve/definition/topologies/predictor"
	"github.com/openeverest/provider-kserve/internal/common"
)

const (
	// predictorContainerPort is the HTTP port KServe runtimes listen on.
	predictorContainerPort = 8080
	// predictorServicePort is the port KServe's in-cluster predictor Service
	// exposes (CommonDefaultHttpPort). The provider-owned external Service
	// uses the same mapping: 80 → 8080.
	predictorServicePort = 80
)

// predictorPodSelector returns the label selector KServe stamps on Standard-mode
// InferenceService predictor pods. Keep in sync with GetRawServiceLabel +
// PredictorServiceName.
func predictorPodSelector(instance string) map[string]string {
	return map[string]string{
		"app": kserveconstants.GetRawServiceLabel(kserveconstants.PredictorServiceName(instance)),
	}
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
	if comp.Service != nil {
		switch comp.Service.ServiceType {
		case "", corev1.ServiceTypeClusterIP, corev1.ServiceTypeLoadBalancer, corev1.ServiceTypeNodePort:
		default:
			return fmt.Errorf("%s.service.serviceType must be one of ClusterIP, LoadBalancer or NodePort", common.ComponentPredictor)
		}
	}

	var topo predictor.PredictorTopologyParameters
	c.TryDecodeTopologyParameters(&topo)
	switch topo.ExternalAccess {
	case "", predictor.ExternalAccessClusterIP, predictor.ExternalAccessLoadBalancer, predictor.ExternalAccessNodePort:
	default:
		return fmt.Errorf("externalAccess must be one of ClusterIP, LoadBalancer or NodePort")
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
		model.Resources = *comp.Resources
	}
	if len(params.Env) > 0 {
		model.Env = params.Env
	}
	if len(params.Args) > 0 {
		model.Args = params.Args
	}

	predictorSpec := kservev1beta1.PredictorSpec{Model: model}
	if len(params.NodeSelector) > 0 {
		predictorSpec.NodeSelector = params.NodeSelector
	}
	if len(params.Tolerations) > 0 {
		predictorSpec.Tolerations = params.Tolerations
	}
	if comp.Affinity != nil {
		predictorSpec.Affinity = comp.Affinity
	}

	switch {
	case params.MinReplicas != nil:
		predictorSpec.MinReplicas = params.MinReplicas
	case comp.Replicas != nil:
		predictorSpec.MinReplicas = comp.Replicas
	}
	if params.MaxReplicas != nil {
		predictorSpec.MaxReplicas = *params.MaxReplicas
	}

	meta := c.ObjectMeta(c.Name())
	if meta.Annotations == nil {
		meta.Annotations = map[string]string{}
	}
	meta.Annotations[common.DeploymentModeAnnotation] = common.DeploymentModeStandard

	return &kservev1beta1.InferenceService{
		ObjectMeta: meta,
		Spec: kservev1beta1.InferenceServiceSpec{
			Predictor: predictorSpec,
		},
	}, nil
}

// syncPredictor creates or updates the InferenceService and optional external Service.
func (p *Provider) syncPredictor(c *controller.Context) error {
	isvc, err := buildInferenceService(c)
	if err != nil {
		return err
	}
	if err := common.Apply(c.Context(), c.Client(), c.Instance(), isvc); err != nil {
		return err
	}
	return ensurePredictorExternalService(c)
}

func ensurePredictorExternalService(c *controller.Context) error {
	comp := c.Instance().Spec.Components[common.ComponentPredictor]
	return reconcileExternalService(
		c,
		comp.Service,
		predictorServiceType(c),
		predictorPodSelector(c.Name()),
		predictorServicePort,
		predictorContainerPort,
	)
}

func predictorServiceType(c *controller.Context) corev1.ServiceType {
	comp := c.Instance().Spec.Components[common.ComponentPredictor]
	var topo predictor.PredictorTopologyParameters
	c.TryDecodeTopologyParameters(&topo)
	return resolveServiceType(topo.ResolvedServiceType(), comp.Service)
}

// statusPredictor translates the InferenceService status into a provider Status.
func (p *Provider) statusPredictor(c *controller.Context) (controller.Status, error) {
	isvc := &kservev1beta1.InferenceService{}
	if err := c.Get(isvc, c.Name()); err != nil {
		return controller.Provisioning("Waiting for InferenceService"), nil
	}

	ready := isvc.Status.GetCondition(apis.ConditionReady)
	if ready != nil && ready.IsTrue() {
		details, err := p.predictorConnectionDetails(c, isvc)
		if err != nil {
			return controller.Provisioning(err.Error()), nil
		}
		if details != nil {
			return controller.ReadyWithConnectionDetails(*details), nil
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

// predictorConnectionDetails resolves how to reach a Ready InferenceService.
// LoadBalancer/NodePort win over KServe Status.URL so the provider-owned
// Service address is what clients see. Returns nil (Ready without details)
// when that external address is not assigned yet.
func (p *Provider) predictorConnectionDetails(c *controller.Context, isvc *kservev1beta1.InferenceService) (*controller.ConnectionDetails, error) {
	svcType := predictorServiceType(c)
	switch svcType {
	case corev1.ServiceTypeLoadBalancer, corev1.ServiceTypeNodePort:
		return p.externalServiceConnectionDetails(c, svcType, predictorServicePort)
	}
	if isvc.Status.URL != nil {
		d := connectionDetails(isvc.Status.URL)
		return &d, nil
	}
	host := fmt.Sprintf("%s.%s.svc.cluster.local", kserveconstants.PredictorServiceName(c.Name()), c.Namespace())
	d := kserveConnectionDetails(host, strconv.Itoa(predictorServicePort))
	return &d, nil
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
