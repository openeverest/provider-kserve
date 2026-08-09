package provider

import (
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	knativekmeta "knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/openeverest/provider-kserve/definition/topologies/llm"

	"github.com/openeverest/provider-kserve/internal/common"
)

const podMonitorSuffix = "-metrics"

var podMonitorGVK = schema.GroupVersionKind{
	Group: "monitoring.coreos.com", 
	Version: "v1", 
	Kind: "PodMonitor",
}

// buildPodMonitor builds a Prometheus Operator PodMonitor that scrapes the pods
// matched by selector on targetPort/path.
//
// This serves the per-Instance workload layers: the vLLM model pods today, and
// the predictor (InferenceService) pods later — both are created per Instance by
// this reconcile, so a new layer is just a different selector/port/path here, not
// new plumbing. Singleton infrastructure does NOT belong here: the Envoy AI
// Gateway is installed once by the chart (not per Instance) and exposes its
// gen_ai.* metrics on a differently-named port (aigw-admin), so its PodMonitor
// lives in the chart template that installs the gateway
// (charts/provider-kserve/templates/ai-gateway-podmonitor.yaml).
//
// targetPort (rather than a named port) is used so the PodMonitor does not
// depend on KServe naming the vLLM container port, which is not guaranteed
// stable across KServe versions.
func buildPodMonitor(
	instanceName, namespace string,
	selector map[string]string,
	targetPort int,
	path, interval string,
) *unstructured.Unstructured {
	labels := make(map[string]any, len(selector))
	for k, v := range selector {
		labels[k] = v
	}

	pm := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": podMonitorGVK.GroupVersion().String(),
		"kind":       podMonitorGVK.Kind,
		"metadata": map[string]any{
			"name":      knativekmeta.ChildName(instanceName, podMonitorSuffix),
			"namespace": namespace,
		},
		"spec": map[string]any{
			// No namespaceSelector: the default restricts to the PodMonitor's
			// own namespace, which is where the Instance's workload pods live.
			"selector": map[string]any{"matchLabels": labels},
			"podMetricsEndpoints": []any{map[string]any{
				"targetPort": int64(targetPort),
				"path":       path,
				"interval":   interval,
			}},
		},
	}}
	pm.SetGroupVersionKind(podMonitorGVK)
	return pm
}

// ensurePodMonitor reconciles the PodMonitor for the llm workload pods.
//
// Because metrics default on, a target cluster may not have the Prometheus
// Operator (monitoring.coreos.com) CRDs installed. That is treated as a no-op
// rather than an error, so enabling metrics never breaks a bare cluster; the
// operator/CRDs can be installed later and the next reconcile will emit it.
func ensurePodMonitor(c *controller.Context) error {
	return applyPodMonitor(c)
}

// syncPodMonitor creates or removes the per-Instance vLLM PodMonitor based on
// the chart-level and instance-level enable flags.
func syncPodMonitor(c *controller.Context, topo llm.LlmTopologyParameters) error {
	if !common.PodMonitorEnabled() || !topo.MetricsEnabled() {
		return deletePodMonitor(c)
	}
	return applyPodMonitor(c)
}

func applyPodMonitor(c *controller.Context) error {
	pm := buildPodMonitor(
		c.Name(),
		c.Instance().Namespace,
		workloadPodSelector(c.Name()),
		vllmServingPort,
		"/metrics",
		common.PodMonitorInterval(),
	)
	if err := common.Apply(c.Context(), c.Client(), c.Instance(), pm); err != nil {
		if meta.IsNoMatchError(err) {
			log.FromContext(c.Context()).V(1).Info(
				"PodMonitor CRD not installed; skipping metrics scrape config",
				"instance", c.Name(),
			)
			return nil
		}
		return err
	}
	return nil
}

func deletePodMonitor(c *controller.Context) error {
	pm := &unstructured.Unstructured{}
	pm.SetGroupVersionKind(podMonitorGVK)
	pm.SetName(knativekmeta.ChildName(c.Name(), podMonitorSuffix))
	pm.SetNamespace(c.Instance().Namespace)
	if err := c.Delete(pm); err != nil {
		if meta.IsNoMatchError(err) {
			return nil
		}
		return client.IgnoreNotFound(err)
	}
	return nil
}
