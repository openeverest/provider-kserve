package provider

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildPodMonitor(t *testing.T) {
	t.Parallel()

	t.Run("llm", func(t *testing.T) {
		t.Parallel()
		assertPodMonitor(t, "chat", workloadPodSelector("chat"), vllmServingPort)
	})
	t.Run("predictor", func(t *testing.T) {
		t.Parallel()
		assertPodMonitor(t, "sklearn", predictorPodSelector("sklearn"), predictorServingPort)
	})
}

func assertPodMonitor(t *testing.T, name string, selector map[string]string, port int) {
	t.Helper()
	pm := buildPodMonitor(name, "models", selector, port, "/metrics", "30s")

	if pm.GetName() != name+"-metrics" {
		t.Fatalf("name = %q, want %s-metrics", pm.GetName(), name)
	}
	if pm.GetNamespace() != "models" {
		t.Fatalf("namespace = %q, want models", pm.GetNamespace())
	}
	if pm.GroupVersionKind() != podMonitorGVK {
		t.Fatalf("GVK = %v, want %v", pm.GroupVersionKind(), podMonitorGVK)
	}

	labels, found, err := unstructured.NestedStringMap(pm.Object, "spec", "selector", "matchLabels")
	if err != nil || !found {
		t.Fatalf("matchLabels found %t, err %v", found, err)
	}
	for k, want := range selector {
		if labels[k] != want {
			t.Fatalf("selector[%q] = %q, want %q", k, labels[k], want)
		}
	}

	eps, found, err := unstructured.NestedSlice(pm.Object, "spec", "podMetricsEndpoints")
	if err != nil || !found || len(eps) != 1 {
		t.Fatalf("podMetricsEndpoints = %#v, found %t, err %v", eps, found, err)
	}
	ep := eps[0].(map[string]any)
	if ep["targetPort"] != int64(port) {
		t.Fatalf("targetPort = %v, want %d", ep["targetPort"], port)
	}
	if ep["path"] != "/metrics" {
		t.Fatalf("path = %v, want /metrics", ep["path"])
	}
	if ep["interval"] != "30s" {
		t.Fatalf("interval = %v, want 30s", ep["interval"])
	}
}
