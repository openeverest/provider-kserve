package provider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kservev1alpha2 "github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/definition/components"
	"github.com/openeverest/provider-kserve/definition/topologies/llm"
	"github.com/openeverest/provider-kserve/definition/topologies/predictor"
	"github.com/openeverest/provider-kserve/internal/common"
)

func TestMonitoringSourcesLLM(t *testing.T) {
	llmisvc := &kservev1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "llama", Namespace: "ns"}}
	cases := []struct {
		name string
		topo llm.LlmTopologyParameters
		objs []client.Object
		want []corev1alpha1.MetricsEndpoint
	}{
		{name: "nothing is published before the LLMInferenceService exists"},
		{
			name: "vLLM's endpoint on the workload pods",
			objs: []client.Object{llmisvc},
			want: []corev1alpha1.MetricsEndpoint{{
				Component: common.ComponentLlmEngine,
				PodSelector: map[string]string{
					controller.InstanceLabel:  "llama",
					controller.ComponentLabel: common.ComponentLlmEngine,
					"kserve.io/component":     "workload",
				},
				Port:   intstr.FromInt32(vllmServingPort),
				Path:   "/metrics",
				Scheme: "http",
			}},
		},
		{
			name: "enableMetrics off publishes nothing",
			topo: llm.LlmTopologyParameters{EnableMetrics: ptr.To(false)},
			objs: []client.Object{llmisvc},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := testScheme(t)
			require.NoError(t, kservev1alpha2.AddToScheme(scheme))
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc.objs...).Build()
			c := llmContext(t, nil, components.VllmCustomSpec{}, tc.topo)
			c = controller.NewContext(context.Background(), cl, c.Instance(), common.ProviderName)

			got, err := (&Provider{}).MonitoringSources(c)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Metrics)
		})
	}
}

func TestMonitoringSourcesPredictor(t *testing.T) {
	pod := func(name string, annotations map[string]string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   "ns",
			Labels:      map[string]string{controller.InstanceLabel: "iris", controller.ComponentLabel: common.ComponentPredictor},
			Annotations: annotations,
		}}
	}
	selector := map[string]string{controller.InstanceLabel: "iris", controller.ComponentLabel: common.ComponentPredictor}
	cases := []struct {
		name string
		topo predictor.PredictorTopologyParameters
		objs []client.Object
		want []corev1alpha1.MetricsEndpoint
	}{
		{name: "nothing is published before a predictor pod exists"},
		{
			name: "a pod without the runtime's annotations publishes nothing",
			objs: []client.Object{pod("iris-a", nil)},
		},
		{
			name: "the port and path the serving runtime declares",
			objs: []client.Object{pod("iris-a", map[string]string{
				metricsPortAnnotation: "8002",
				metricsPathAnnotation: "/v2/metrics",
			})},
			want: []corev1alpha1.MetricsEndpoint{{
				Component:   common.ComponentPredictor,
				PodSelector: selector,
				Port:        intstr.FromInt32(8002),
				Path:        "/v2/metrics",
				Scheme:      "http",
			}},
		},
		{
			name: "the path defaults to /metrics",
			objs: []client.Object{pod("iris-a", map[string]string{metricsPortAnnotation: "8080"})},
			want: []corev1alpha1.MetricsEndpoint{{
				Component:   common.ComponentPredictor,
				PodSelector: selector,
				Port:        intstr.FromInt32(8080),
				Path:        "/metrics",
				Scheme:      "http",
			}},
		},
		{
			name: "enableMetrics off publishes nothing",
			topo: predictor.PredictorTopologyParameters{EnableMetrics: ptr.To(false)},
			objs: []client.Object{pod("iris-a", map[string]string{metricsPortAnnotation: "8080"})},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(tc.objs...).Build()
			c := predictorContext(t, cl, corev1alpha1.ComponentSpec{})
			c.Instance().Spec.Topology.Parameters = rawJSON(t, tc.topo)

			got, err := (&Provider{}).MonitoringSources(c)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Metrics)
		})
	}
}

func TestRuntimeMetricsEndpointRejectsInvalidPorts(t *testing.T) {
	for _, port := range []string{"", "http", "0", "65536"} {
		_, _, ok := runtimeMetricsEndpoint(map[string]string{metricsPortAnnotation: port})
		assert.False(t, ok, "port %q", port)
	}
}

func TestDeleteLegacyPodMonitor(t *testing.T) {
	legacy := &unstructured.Unstructured{}
	legacy.SetGroupVersionKind(podMonitorGVK)
	legacy.SetName("iris-metrics")
	legacy.SetNamespace("ns")
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(podMonitorGVK, meta.RESTScopeNamespace)
	cl := fake.NewClientBuilder().WithScheme(testScheme(t)).WithRESTMapper(mapper).WithObjects(legacy).Build()
	c := predictorContext(t, cl, corev1alpha1.ComponentSpec{})

	require.NoError(t, deleteLegacyPodMonitor(c))
	err := cl.Get(context.Background(), client.ObjectKeyFromObject(legacy), legacy.DeepCopy())
	require.Error(t, err)
	// Deleting again, and on a cluster without the PodMonitor CRD, is a no-op.
	require.NoError(t, deleteLegacyPodMonitor(c))
	noCRD := fake.NewClientBuilder().WithScheme(testScheme(t)).WithRESTMapper(meta.NewDefaultRESTMapper(nil)).Build()
	require.NoError(t, deleteLegacyPodMonitor(predictorContext(t, noCRD, corev1alpha1.ComponentSpec{})))
}
