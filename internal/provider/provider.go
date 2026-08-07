package provider

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kservev1alpha2 "github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	kservev1beta1 "github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/internal/common"
)

// Compile-time check that Provider implements the required interface.
var _ controller.ProviderInterface = (*Provider)(nil)

// Provider implements controller.ProviderInterface for the provider-kserve provider.
type Provider struct {
	controller.BaseProvider
}

// New creates a new Provider instance.
func New() *Provider {
	watches := []controller.WatchConfig{
		controller.WatchOwned(&kservev1alpha2.LLMInferenceService{}),
		controller.WatchOwned(&kservev1alpha2.LLMInferenceServiceConfig{}),
		controller.WatchOwned(&kservev1beta1.InferenceService{}),
		controller.WatchOwned(&corev1.Service{}),
	}
	if common.AIGatewayEnabled() {
		watches = append(watches, controller.WatchOwned(unstructuredObject(aiGatewayRouteGVK)))
	}
	// Note: the PodMonitor is intentionally NOT watched. Owning a watch on
	// monitoring.coreos.com/PodMonitor would fail the manager at startup on any
	// cluster without the Prometheus Operator CRDs. Since metrics default on, we
	// keep the emit-only path and let the periodic resync re-apply on drift.

	return &Provider{
		BaseProvider: controller.BaseProvider{
			ProviderName: common.ProviderName,
			SchemeFuncs: []func(*runtime.Scheme) error{
				kservev1beta1.AddToScheme,
				kservev1alpha2.AddToScheme,
			},
			WatchConfigs: watches,
		},
	}
}

func unstructuredObject(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	return obj
}

// Validate checks if the Instance spec is valid.
func (p *Provider) Validate(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Validating instance", "name", c.Name())

	switch c.Instance().GetTopologyType() {
	case common.TopologyLLM:
		return validateLLM(c)
	case common.TopologyPredictor:
		return validatePredictor(c)
	default:
		return fmt.Errorf("unsupported topology %q", c.Instance().GetTopologyType())
	}
}

// Sync ensures the KServe resource for the Instance exists and is up to date.
func (p *Provider) Sync(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Syncing instance", "name", c.Name())

	switch c.Instance().GetTopologyType() {
	case common.TopologyLLM:
		return p.syncLLM(c)
	case common.TopologyPredictor:
		return p.syncPredictor(c)
	default:
		return fmt.Errorf("unsupported topology %q", c.Instance().GetTopologyType())
	}
}

// Status translates the KServe resource status into the provider-runtime Status.
func (p *Provider) Status(c *controller.Context) (controller.Status, error) {
	l := log.FromContext(c.Context())
	l.Info("Computing status", "name", c.Name())

	switch c.Instance().GetTopologyType() {
	case common.TopologyLLM:
		return p.statusLLM(c)
	case common.TopologyPredictor:
		return p.statusPredictor(c)
	default:
		return controller.Failed(fmt.Sprintf("unsupported topology %q", c.Instance().GetTopologyType())), nil
	}
}

// Cleanup deletes the KServe resource for the Instance. Owner references
// normally handle garbage collection; the delete is issued explicitly to be safe.
func (p *Provider) Cleanup(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Cleaning up instance", "name", c.Name())

	switch c.Instance().GetTopologyType() {
	case common.TopologyLLM:
		if err := c.Delete(&kservev1alpha2.LLMInferenceService{ObjectMeta: c.ObjectMeta(c.Name())}); err != nil {
			return err
		}
		// The inline Advanced config is owner-ref garbage-collected; delete it
		// explicitly too (no-op when absent).
		return c.Delete(&kservev1alpha2.LLMInferenceServiceConfig{ObjectMeta: c.ObjectMeta(instanceConfigName(c.Name()))})
	case common.TopologyPredictor:
		return c.Delete(&kservev1beta1.InferenceService{ObjectMeta: c.ObjectMeta(c.Name())})
	default:
		return nil
	}
}
