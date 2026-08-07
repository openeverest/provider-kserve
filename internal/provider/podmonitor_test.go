package provider

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildPodMonitor(t *testing.T) {
	t.Parallel()

	pm := buildPodMonitor("chat", "models", workloadPodSelector("chat"), vllmServingPort, "/metrics", "30s")

	if pm.GetName() != "chat-metrics" {
		t.Fatalf("name = %q, want chat-metrics", pm.GetName())
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
	for k, want := range workloadPodSelector("chat") {
		if labels[k] != want {
			t.Fatalf("selector[%q] = %q, want %q", k, labels[k], want)
		}
	}

	eps, found, err := unstructured.NestedSlice(pm.Object, "spec", "podMetricsEndpoints")
	if err != nil || !found || len(eps) != 1 {
		t.Fatalf("podMetricsEndpoints = %#v, found %t, err %v", eps, found, err)
	}
	ep := eps[0].(map[string]any)
	if ep["targetPort"] != int64(vllmServingPort) {
		t.Fatalf("targetPort = %v, want %d", ep["targetPort"], vllmServingPort)
	}
	if ep["path"] != "/metrics" {
		t.Fatalf("path = %v, want /metrics", ep["path"])
	}
	if ep["interval"] != "30s" {
		t.Fatalf("interval = %v, want 30s", ep["interval"])
	}
}
