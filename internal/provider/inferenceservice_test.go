package provider

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/definition/components"
	"github.com/openeverest/provider-kserve/internal/common"
)

func predictorContext(t *testing.T, cl client.Client, comp corev1alpha1.ComponentSpec) *controller.Context {
	t.Helper()
	inst := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "iris", Namespace: "ns"},
		Spec: corev1alpha1.InstanceSpec{
			Topology:   &corev1alpha1.TopologySpec{Type: common.TopologyPredictor},
			Components: map[string]corev1alpha1.ComponentSpec{common.ComponentPredictor: comp},
		},
	}
	return controller.NewContext(context.Background(), cl, inst, common.ProviderName)
}

func TestBuildInferenceService(t *testing.T) {
	t.Run("maps parameters to KServe field paths", func(t *testing.T) {
		c := predictorContext(t, nil, corev1alpha1.ComponentSpec{
			Replicas: ptr.To(int32(2)),
			Resources: &corev1.ResourceRequirements{
				Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
			},
			Parameters: rawJSON(t, components.ModelServerCustomSpec{
				ModelFormat:    "sklearn",
				StorageURI:     "gs://bucket/iris",
				Runtime:        "kserve-sklearnserver",
				RuntimeVersion: "1.5",
				MaxReplicas:    ptr.To(int32(4)),
			}),
		})

		isvc, err := buildInferenceService(c)
		if err != nil {
			t.Fatal(err)
		}
		if isvc.GroupVersionKind() != inferenceServiceGVK {
			t.Fatalf("gvk = %v, want %v", isvc.GroupVersionKind(), inferenceServiceGVK)
		}
		if got := isvc.GetAnnotations()[common.DeploymentModeAnnotation]; got != common.DeploymentModeStandard {
			t.Fatalf("deployment mode annotation = %q, want %q", got, common.DeploymentModeStandard)
		}

		want := map[string]any{
			"predictor": map[string]any{
				"minReplicas": int64(2),
				"maxReplicas": int64(4),
				"model": map[string]any{
					"modelFormat":    map[string]any{"name": "sklearn"},
					"runtime":        "kserve-sklearnserver",
					"storageUri":     "gs://bucket/iris",
					"runtimeVersion": "1.5",
					"resources":      map[string]any{"limits": map[string]any{"memory": "1Gi"}},
				},
			},
		}
		if !reflect.DeepEqual(isvc.Object["spec"], want) {
			t.Fatalf("spec = %#v\nwant %#v", isvc.Object["spec"], want)
		}
	})

	t.Run("omits unset optional fields", func(t *testing.T) {
		c := predictorContext(t, nil, corev1alpha1.ComponentSpec{
			Parameters: rawJSON(t, components.ModelServerCustomSpec{ModelFormat: "onnx"}),
		})

		isvc, err := buildInferenceService(c)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]any{
			"predictor": map[string]any{
				"model": map[string]any{"modelFormat": map[string]any{"name": "onnx"}},
			},
		}
		if !reflect.DeepEqual(isvc.Object["spec"], want) {
			t.Fatalf("spec = %#v\nwant %#v", isvc.Object["spec"], want)
		}
	})
}

func TestStatusPredictor(t *testing.T) {
	isvcWithStatus := func(status map[string]any) *unstructured.Unstructured {
		obj := unstructuredObject(inferenceServiceGVK)
		obj.SetName("iris")
		obj.SetNamespace("ns")
		obj.Object["status"] = status
		return obj
	}
	comp := corev1alpha1.ComponentSpec{
		Parameters: rawJSON(t, components.ModelServerCustomSpec{ModelFormat: "sklearn"}),
	}

	cases := []struct {
		name      string
		objs      []client.Object
		wantPhase corev1alpha1.InstancePhase
		wantURI   string
	}{
		{
			name:      "missing InferenceService is provisioning",
			wantPhase: corev1alpha1.InstancePhaseProvisioning,
		},
		{
			name: "not ready is provisioning",
			objs: []client.Object{isvcWithStatus(map[string]any{
				"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "MinimumReplicasUnavailable"}},
			})},
			wantPhase: corev1alpha1.InstancePhaseProvisioning,
		},
		{
			name: "ready with URL publishes connection details",
			objs: []client.Object{isvcWithStatus(map[string]any{
				"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
				"url":        "https://iris.example.com",
			})},
			wantPhase: corev1alpha1.InstancePhaseReady,
			wantURI:   "https://iris.example.com",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithObjects(tc.objs...).Build()
			got, err := (&Provider{}).statusPredictor(predictorContext(t, cl, comp))
			if err != nil {
				t.Fatal(err)
			}
			if got.Phase != tc.wantPhase {
				t.Fatalf("phase = %q, want %q", got.Phase, tc.wantPhase)
			}
			if got.ConnectionDetails.URI != tc.wantURI {
				t.Fatalf("connection details = %+v, want URI %q", got.ConnectionDetails, tc.wantURI)
			}
		})
	}
}
