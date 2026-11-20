package provider

import (
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	knativekmeta "knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kservev1alpha2 "github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/definition/topologies/llm"
	"github.com/openeverest/provider-kserve/definition/topologies/predictor"
	"github.com/openeverest/provider-kserve/internal/common"
)

const (
	defaultMetricsPath = "/metrics"
	// kserveComponentLabel tells KServe's LLM workload pods (the vLLM servers)
	// from the router and scheduler pods of the same LLMInferenceService.
	kserveComponentLabel  = "kserve.io/component"
	kserveWorkloadComponent = "workload"
	legacyPodMonitorSuffix = "-metrics"
)

var _ controller.MonitoringSourcesProvider = (*Provider)(nil)

// MonitoringSources implements controller.MonitoringSourcesProvider. vLLM and
// the KServe model servers serve OpenMetrics themselves (metrics: Native in
// definition/monitoring.yaml), so nothing is switched on or off for a bound
// class. Monitoring extensions turn the endpoints into scrape objects; other
// scrapers can read them from the Instance status. An Instance with
// enableMetrics off publishes none, for model servers without a metrics
// endpoint.
func (p *Provider) MonitoringSources(c *controller.Context) (*corev1alpha1.MonitoringSources, error) {
	sources := &corev1alpha1.MonitoringSources{}
	var (
		endpoint *corev1alpha1.MetricsEndpoint
		err      error
	)
	switch c.Instance().GetTopologyType() {
	case common.TopologyLLM:
		endpoint, err = llmMetricsEndpoint(c)
	case common.TopologyPredictor:
		endpoint, err = predictorMetricsEndpoint(c)
	}
	if err != nil {
		return nil, err
	}
	if endpoint != nil {
		sources.Metrics = []corev1alpha1.MetricsEndpoint{*endpoint}
	}
	return sources, nil
}

// llmMetricsEndpoint is vLLM's /metrics on its serving port, on the decode
// and prefill workload pods, once the LLMInferenceService exists. KServe
// serves it over plain http unless enableLLMInferenceServiceTLS is set in
// its ingress config, which the chart does not set.
func llmMetricsEndpoint(c *controller.Context) (*corev1alpha1.MetricsEndpoint, error) {
	var topo llm.LlmTopologyParameters
	c.TryDecodeTopologyParameters(&topo)
	if !topo.MetricsEnabled() {
		return nil, nil
	}
	found, err := c.Exists(&kservev1alpha2.LLMInferenceService{}, c.Name())
	if err != nil {
		return nil, fmt.Errorf("get LLMInferenceService: %w", err)
	}
	if !found {
		return nil, nil
	}
	selector := componentPodSelector(c.Name(), common.ComponentLlmEngine)
	selector[kserveComponentLabel] = kserveWorkloadComponent
	return &corev1alpha1.MetricsEndpoint{
		Component:   common.ComponentLlmEngine,
		PodSelector: selector,
		Port:        intstr.FromInt32(vllmServingPort),
		Path:        defaultMetricsPath,
		Scheme:      "http",
	}, nil
}

// predictorMetricsEndpoint is the endpoint the serving runtime declares in its
// prometheus.kserve.io/port and /path annotations, which KServe copies onto
// the predictor pods (Triton 8002, TorchServe 8082, most others 8080; a custom
// container gets them from the provider). It is read from a predictor pod, so
// it is published once one exists.
func predictorMetricsEndpoint(c *controller.Context) (*corev1alpha1.MetricsEndpoint, error) {
	var topo predictor.PredictorTopologyParameters
	c.TryDecodeTopologyParameters(&topo)
	if !topo.MetricsEnabled() {
		return nil, nil
	}
	selector := componentPodSelector(c.Name(), common.ComponentPredictor)
	pods := &corev1.PodList{}
	if err := c.List(pods, client.MatchingLabels(selector)); err != nil {
		return nil, fmt.Errorf("list predictor pods: %w", err)
	}
	for i := range pods.Items {
		port, path, ok := runtimeMetricsEndpoint(pods.Items[i].Annotations)
		if !ok {
			continue
		}
		return &corev1alpha1.MetricsEndpoint{
			Component:   common.ComponentPredictor,
			PodSelector: selector,
			Port:        intstr.FromInt32(port),
			Path:        path,
			Scheme:      "http",
		}, nil
	}
	return nil, nil
}

// runtimeMetricsEndpoint reads the metrics port and path a serving runtime
// declares on a predictor pod. The path defaults to /metrics; a pod without a
// valid port declares none.
func runtimeMetricsEndpoint(annotations map[string]string) (int32, string, bool) {
	port, err := strconv.ParseInt(annotations[metricsPortAnnotation], 10, 32)
	if err != nil || port < 1 || port > 65535 {
		return 0, "", false
	}
	path := annotations[metricsPathAnnotation]
	if path == "" {
		path = defaultMetricsPath
	}
	return int32(port), path, true
}

// componentPodSelector selects the pods of one component of an Instance by the
// labels the runtime requires on a published endpoint.
func componentPodSelector(instance, component string) map[string]string {
	return map[string]string{
		controller.InstanceLabel:  instance,
		controller.ComponentLabel: component,
	}
}

// podMonitorGVK is the Prometheus Operator PodMonitor, deleted only as
// metadata so the provider needs neither its types nor its CRD.
var podMonitorGVK = schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "PodMonitor"}

// legacyPodMonitorName is the name of the PodMonitor earlier releases created.
func legacyPodMonitorName(instance string) string {
	return knativekmeta.ChildName(instance, legacyPodMonitorSuffix)
}

// deleteLegacyPodMonitor removes the PodMonitor that releases before the
// monitoring contract created per Instance; a monitoring extension now creates
// the scrape objects from the published sources. Clusters without the
// Prometheus Operator CRDs are skipped.
// TODO: drop once no supported release created PodMonitors.
func deleteLegacyPodMonitor(c *controller.Context) error {
	pm := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{
		Name:      legacyPodMonitorName(c.Name()),
		Namespace: c.Namespace(),
	}}
	pm.SetGroupVersionKind(podMonitorGVK)
	if err := c.Delete(pm); err != nil && !meta.IsNoMatchError(err) {
		return fmt.Errorf("delete legacy PodMonitor: %w", err)
	}
	return nil
}
