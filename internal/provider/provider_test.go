package provider

import (
	"context"
	"testing"

	kservev1alpha2 "github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	kservev1beta1 "github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/openeverest/provider-kserve/internal/common"
)

func TestCleanupIgnoresMissingOwnedResources(t *testing.T) {
	t.Run("llm", func(t *testing.T) {
		c := newCleanupContext(t, common.TopologyLLM)
		if err := New().Cleanup(c); err != nil {
			t.Fatalf("Cleanup() error = %v, want nil", err)
		}
	})

	t.Run("predictor", func(t *testing.T) {
		c := newCleanupContext(t, common.TopologyPredictor)
		if err := New().Cleanup(c); err != nil {
			t.Fatalf("Cleanup() error = %v, want nil", err)
		}
	})
}

func newCleanupContext(t *testing.T, topology string) *controller.Context {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kservev1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kservev1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	instance := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instance",
			Namespace: "test-namespace",
		},
		Spec: corev1alpha1.InstanceSpec{
			Topology: &corev1alpha1.TopologySpec{
				Type: topology,
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(instance).
		Build()

	return controller.NewContext(context.Background(), client, instance, common.ProviderName)
}
