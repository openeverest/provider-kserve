package provider

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/definition/components"
	"github.com/openeverest/provider-kserve/internal/common"
)

func predictorContext(t *testing.T, params components.ModelServerCustomSpec) *controller.Context {
	t.Helper()
	if params.ModelFormat == "" {
		params.ModelFormat = "sklearn"
	}
	if params.StorageURI == "" {
		params.StorageURI = "gs://bucket/model"
	}
	inst := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "sklearn", Namespace: "ns"},
		Spec: corev1alpha1.InstanceSpec{
			Topology: &corev1alpha1.TopologySpec{Type: common.TopologyPredictor},
			Components: map[string]corev1alpha1.ComponentSpec{
				common.ComponentPredictor: {
					Type:       common.ComponentTypeModelServer,
					Parameters: rawJSON(t, params),
				},
			},
		},
	}
	return controller.NewContext(context.Background(), nil, inst, common.ProviderName)
}

func TestBuildInferenceServiceStorageContainer(t *testing.T) {
	t.Parallel()

	isvc, err := buildInferenceService(predictorContext(t, components.ModelServerCustomSpec{
		StorageContainer: "custom-s3-init",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if isvc.Spec.Predictor.StorageContainerName == nil || *isvc.Spec.Predictor.StorageContainerName != "custom-s3-init" {
		t.Fatalf("storageContainerName = %v", isvc.Spec.Predictor.StorageContainerName)
	}
}

func TestBuildInferenceServiceStorageContainerUnset(t *testing.T) {
	t.Parallel()

	isvc, err := buildInferenceService(predictorContext(t, components.ModelServerCustomSpec{}))
	if err != nil {
		t.Fatal(err)
	}
	if isvc.Spec.Predictor.StorageContainerName != nil {
		t.Fatalf("storageContainerName = %v, want unset", *isvc.Spec.Predictor.StorageContainerName)
	}
}
