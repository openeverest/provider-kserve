package provider

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

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

func TestBuildInferenceServiceIdentity(t *testing.T) {
	t.Parallel()

	isvc, err := buildInferenceService(predictorContext(t, components.ModelServerCustomSpec{
		ServiceAccountName: "predictor-sa",
		Labels:             map[string]string{"cost-center": "ml"},
		Annotations:        map[string]string{"azure.workload.identity/use": "true"},
		SecurityContext:    &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true)},
		ContainerSecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if isvc.Spec.Predictor.ServiceAccountName != "predictor-sa" {
		t.Fatalf("serviceAccountName = %q", isvc.Spec.Predictor.ServiceAccountName)
	}
	if isvc.Labels["cost-center"] != "ml" || isvc.Spec.Predictor.Labels["cost-center"] != "ml" {
		t.Fatalf("labels meta=%v pred=%v", isvc.Labels, isvc.Spec.Predictor.Labels)
	}
	if isvc.Annotations["azure.workload.identity/use"] != "true" || isvc.Spec.Predictor.Annotations["azure.workload.identity/use"] != "true" {
		t.Fatalf("annotations meta=%v pred=%v", isvc.Annotations, isvc.Spec.Predictor.Annotations)
	}
	if isvc.Annotations[common.DeploymentModeAnnotation] != common.DeploymentModeStandard {
		t.Fatalf("deploymentMode overwritten: %v", isvc.Annotations)
	}
	if isvc.Spec.Predictor.SecurityContext == nil || isvc.Spec.Predictor.SecurityContext.RunAsNonRoot == nil || !*isvc.Spec.Predictor.SecurityContext.RunAsNonRoot {
		t.Fatalf("pod securityContext = %#v", isvc.Spec.Predictor.SecurityContext)
	}
	if isvc.Spec.Predictor.Model.SecurityContext == nil || isvc.Spec.Predictor.Model.SecurityContext.AllowPrivilegeEscalation == nil || *isvc.Spec.Predictor.Model.SecurityContext.AllowPrivilegeEscalation {
		t.Fatalf("container securityContext = %#v", isvc.Spec.Predictor.Model.SecurityContext)
	}
}
