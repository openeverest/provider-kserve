package provider

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kservev1beta1 "github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	kserveconstants "github.com/kserve/kserve/pkg/constants"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/definition/components"
	"github.com/openeverest/provider-kserve/definition/topologies/predictor"
	"github.com/openeverest/provider-kserve/internal/common"
)

func predictorContext(t *testing.T, params components.ModelServerCustomSpec, topo predictor.PredictorTopologyParameters, mut ...func(*corev1alpha1.ComponentSpec)) *controller.Context {
	t.Helper()
	return predictorContextWithClient(t, params, topo, nil, mut...)
}

func predictorContextWithClient(t *testing.T, params components.ModelServerCustomSpec, topo predictor.PredictorTopologyParameters, cl client.Client, mut ...func(*corev1alpha1.ComponentSpec)) *controller.Context {
	t.Helper()
	if params.ModelFormat == "" {
		params.ModelFormat = "sklearn"
	}
	if params.StorageURI == "" {
		params.StorageURI = "gs://kfserving-examples/models/sklearn/1.0/model"
	}
	comp := corev1alpha1.ComponentSpec{
		Type:       common.ComponentTypeModelServer,
		Replicas:   ptr.To(int32(1)),
		Parameters: rawJSON(t, params),
	}
	for _, fn := range mut {
		fn(&comp)
	}
	inst := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "iris", Namespace: "ns"},
		Spec: corev1alpha1.InstanceSpec{
			Topology: &corev1alpha1.TopologySpec{
				Type:       common.TopologyPredictor,
				Parameters: rawJSON(t, topo),
			},
			Components: map[string]corev1alpha1.ComponentSpec{
				common.ComponentPredictor: comp,
			},
		},
	}
	return controller.NewContext(context.Background(), cl, inst, common.ProviderName)
}

func TestValidatePredictor(t *testing.T) {
	t.Parallel()

	t.Run("ok", func(t *testing.T) {
		t.Parallel()
		if err := validatePredictor(predictorContext(t, components.ModelServerCustomSpec{}, predictor.PredictorTopologyParameters{})); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("missing format", func(t *testing.T) {
		t.Parallel()
		err := validatePredictor(predictorContext(t, components.ModelServerCustomSpec{StorageURI: "gs://x"}, predictor.PredictorTopologyParameters{}, func(c *corev1alpha1.ComponentSpec) {
			c.Parameters = rawJSON(t, components.ModelServerCustomSpec{StorageURI: "gs://x"})
		}))
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("bad service type", func(t *testing.T) {
		t.Parallel()
		err := validatePredictor(predictorContext(t, components.ModelServerCustomSpec{}, predictor.PredictorTopologyParameters{}, func(c *corev1alpha1.ComponentSpec) {
			c.Service = &corev1alpha1.Service{ServiceType: corev1.ServiceTypeExternalName}
		}))
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("bad externalAccess", func(t *testing.T) {
		t.Parallel()
		err := validatePredictor(predictorContext(t, components.ModelServerCustomSpec{}, predictor.PredictorTopologyParameters{ExternalAccess: "Ingress"}))
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("loadbalancer", func(t *testing.T) {
		t.Parallel()
		if err := validatePredictor(predictorContext(t, components.ModelServerCustomSpec{}, predictor.PredictorTopologyParameters{ExternalAccess: predictor.ExternalAccessLoadBalancer})); err != nil {
			t.Fatal(err)
		}
	})
}

func TestBuildInferenceService(t *testing.T) {
	t.Parallel()

	t.Run("maps custom spec and affinity", func(t *testing.T) {
		t.Parallel()
		params := components.ModelServerCustomSpec{
			ModelFormat:    "sklearn",
			StorageURI:     "gs://bucket/model",
			Runtime:        "kserve-sklearnserver",
			RuntimeVersion: "v2",
			MinReplicas:    ptr.To(int32(1)),
			MaxReplicas:    ptr.To(int32(3)),
			Env:            []corev1.EnvVar{{Name: "OMP_NUM_THREADS", Value: "1"}},
			Args:           []string{"--strict-model-config=false"},
			NodeSelector:   map[string]string{"node.kubernetes.io/instance-type": "g4dn.xlarge"},
			Tolerations:    []corev1.Toleration{{Key: "nvidia.com/gpu", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}},
		}
		affinity := &corev1.Affinity{
			NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{{
						MatchExpressions: []corev1.NodeSelectorRequirement{{
							Key: "topology.kubernetes.io/zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"us-east-1a"},
						}},
					}},
				},
			},
		}
		got, err := buildInferenceService(predictorContext(t, params, predictor.PredictorTopologyParameters{}, func(c *corev1alpha1.ComponentSpec) {
			c.Affinity = affinity
		}))
		if err != nil {
			t.Fatal(err)
		}
		if got.Annotations[common.DeploymentModeAnnotation] != common.DeploymentModeStandard {
			t.Fatalf("deploymentMode = %q", got.Annotations[common.DeploymentModeAnnotation])
		}
		model := got.Spec.Predictor.Model
		if model == nil || model.ModelFormat.Name != "sklearn" || model.StorageURI == nil || *model.StorageURI != "gs://bucket/model" {
			t.Fatalf("model = %#v", model)
		}
		if model.Runtime == nil || *model.Runtime != "kserve-sklearnserver" {
			t.Fatalf("runtime = %v", model.Runtime)
		}
		if len(model.Env) != 1 || model.Env[0].Name != "OMP_NUM_THREADS" {
			t.Fatalf("env = %#v", model.Env)
		}
		if len(model.Args) != 1 || model.Args[0] != "--strict-model-config=false" {
			t.Fatalf("args = %#v", model.Args)
		}
		if got.Spec.Predictor.NodeSelector["node.kubernetes.io/instance-type"] != "g4dn.xlarge" {
			t.Fatalf("nodeSelector = %#v", got.Spec.Predictor.NodeSelector)
		}
		if len(got.Spec.Predictor.Tolerations) != 1 {
			t.Fatalf("tolerations = %#v", got.Spec.Predictor.Tolerations)
		}
		if got.Spec.Predictor.Affinity == nil || got.Spec.Predictor.Affinity.NodeAffinity == nil {
			t.Fatalf("affinity = %#v", got.Spec.Predictor.Affinity)
		}
		if got.Spec.Predictor.MinReplicas == nil || *got.Spec.Predictor.MinReplicas != 1 || got.Spec.Predictor.MaxReplicas != 3 {
			t.Fatalf("replicas min=%v max=%d", got.Spec.Predictor.MinReplicas, got.Spec.Predictor.MaxReplicas)
		}
	})
}

func TestPredictorServiceType(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		topo predictor.PredictorTopologyParameters
		mut  func(*corev1alpha1.ComponentSpec)
		want corev1.ServiceType
	}{
		{name: "default", want: corev1.ServiceTypeClusterIP},
		{name: "topo lb", topo: predictor.PredictorTopologyParameters{ExternalAccess: predictor.ExternalAccessLoadBalancer}, want: corev1.ServiceTypeLoadBalancer},
		{name: "component nodeport", mut: func(c *corev1alpha1.ComponentSpec) {
			c.Service = &corev1alpha1.Service{ServiceType: corev1.ServiceTypeNodePort}
		}, want: corev1.ServiceTypeNodePort},
		{name: "topo wins", topo: predictor.PredictorTopologyParameters{ExternalAccess: predictor.ExternalAccessClusterIP}, mut: func(c *corev1alpha1.ComponentSpec) {
			c.Service = &corev1alpha1.Service{ServiceType: corev1.ServiceTypeLoadBalancer}
		}, want: corev1.ServiceTypeClusterIP},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var mut []func(*corev1alpha1.ComponentSpec)
			if tc.mut != nil {
				mut = append(mut, tc.mut)
			}
			if got := predictorServiceType(predictorContext(t, components.ModelServerCustomSpec{}, tc.topo, mut...)); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestBuildExternalServicePredictor(t *testing.T) {
	t.Parallel()
	c := predictorContext(t, components.ModelServerCustomSpec{}, predictor.PredictorTopologyParameters{})
	svc := buildExternalService(c, &corev1alpha1.Service{
		Annotations: map[string]string{"service.beta.kubernetes.io/aws-load-balancer-type": "nlb"},
	}, corev1.ServiceTypeLoadBalancer, predictorPodSelector(c.Name()), predictorServicePort, predictorContainerPort)

	if svc.Name != "iris-external" {
		t.Fatalf("name = %q", svc.Name)
	}
	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		t.Fatalf("type = %q", svc.Spec.Type)
	}
	wantSel := kserveconstants.GetRawServiceLabel(kserveconstants.PredictorServiceName("iris"))
	if svc.Spec.Selector["app"] != wantSel {
		t.Fatalf("selector = %#v want app=%s", svc.Spec.Selector, wantSel)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 80 || svc.Spec.Ports[0].TargetPort.IntVal != 8080 {
		t.Fatalf("ports = %#v", svc.Spec.Ports)
	}
	if svc.Annotations["service.beta.kubernetes.io/aws-load-balancer-type"] != "nlb" {
		t.Fatalf("annotations = %#v", svc.Annotations)
	}
}

func TestPredictorConnectionDetails(t *testing.T) {
	t.Parallel()
	isvcURL := &kservev1beta1.InferenceService{}
	isvcURL.Status.URL = &apis.URL{Scheme: "http", Host: "iris.example.svc"}

	t.Run("clusterip uses kserve url", func(t *testing.T) {
		t.Parallel()
		c := predictorContext(t, components.ModelServerCustomSpec{}, predictor.PredictorTopologyParameters{})
		got, err := (&Provider{}).predictorConnectionDetails(c, isvcURL)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || got.Host != "iris.example.svc" {
			t.Fatalf("got %#v", got)
		}
	})

	t.Run("clusterip without url uses in-cluster host", func(t *testing.T) {
		t.Parallel()
		c := predictorContext(t, components.ModelServerCustomSpec{}, predictor.PredictorTopologyParameters{})
		got, err := (&Provider{}).predictorConnectionDetails(c, &kservev1beta1.InferenceService{})
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || got.Host != "iris-predictor.ns.svc.cluster.local" || got.Port != "80" {
			t.Fatalf("got %#v", got)
		}
	})

	t.Run("loadbalancer wins over kserve url", func(t *testing.T) {
		t.Parallel()
		scheme := runtime.NewScheme()
		if err := corev1.AddToScheme(scheme); err != nil {
			t.Fatal(err)
		}
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "iris-external", Namespace: "ns"},
			Status: corev1.ServiceStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{{IP: "203.0.113.10"}},
				},
			},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()
		c := predictorContextWithClient(t, components.ModelServerCustomSpec{}, predictor.PredictorTopologyParameters{
			ExternalAccess: predictor.ExternalAccessLoadBalancer,
		}, cl)
		got, err := (&Provider{}).predictorConnectionDetails(c, isvcURL)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || got.Host != "203.0.113.10" || got.Port != "80" || got.URI != "http://203.0.113.10:80" {
			t.Fatalf("got %#v, want LB address not Status.URL", got)
		}
	})
}
