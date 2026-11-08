package provider

import (
	"context"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/definition/components"
	"github.com/openeverest/provider-kserve/definition/topologies/predictor"
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

func predictorPodLabels() map[string]any {
	return map[string]any{
		controller.ProviderLabel:  common.ProviderName,
		controller.InstanceLabel:  "iris",
		controller.ComponentLabel: common.ComponentPredictor,
	}
}

func TestBuildInferenceService(t *testing.T) {
	t.Run("maps parameters to KServe field paths", func(t *testing.T) {
		c := predictorContext(t, nil, corev1alpha1.ComponentSpec{
			Replicas: ptr.To(int32(2)),
			Resources: &corev1.ResourceRequirements{
				Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
			},
			Parameters: rawJSON(t, components.ModelServerCustomSpec{
				ModelFormat:      "sklearn",
				StorageURI:       "gs://bucket/iris",
				Runtime:          "kserve-sklearnserver",
				RuntimeVersion:   "1.5",
				MaxReplicas:      ptr.To(int32(4)),
				StorageContainer: "large-model-init",
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
				"minReplicas":          int64(2),
				"maxReplicas":          int64(4),
				"labels":               predictorPodLabels(),
				"storageContainerName": "large-model-init",
				"model": map[string]any{
					"modelFormat":    map[string]any{"name": "sklearn"},
					"runtime":        "kserve-sklearnserver",
					"storageUri":     "gs://bucket/iris",
					"runtimeVersion": "1.5",
					// Requests default to limits so the runtime's 2Gi request cannot exceed them.
					"resources": map[string]any{
						"limits":   map[string]any{"memory": "1Gi"},
						"requests": map[string]any{"memory": "1Gi"},
					},
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
				"labels": predictorPodLabels(),
				"model":  map[string]any{"modelFormat": map[string]any{"name": "onnx"}},
			},
		}
		if !reflect.DeepEqual(isvc.Object["spec"], want) {
			t.Fatalf("spec = %#v\nwant %#v", isvc.Object["spec"], want)
		}
	})

	t.Run("maps env, args and the scheduling policy", func(t *testing.T) {
		zone := corev1.NodeSelectorRequirement{Key: corev1.LabelTopologyZone, Operator: corev1.NodeSelectorOpIn, Values: []string{"a"}}
		policy := &commonv1alpha1.SchedulingPolicy{
			SchedulerName: "custom-scheduler",
			NodeSelector:  map[string]string{"pool": "inference"},
			Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{zone}}},
				},
			}},
			Tolerations: []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "inference", Effect: corev1.TaintEffectNoSchedule}},
			TopologySpreadConstraints: &[]corev1.TopologySpreadConstraint{{
				MaxSkew: 1, TopologyKey: corev1.LabelHostname, WhenUnsatisfiable: corev1.ScheduleAnyway,
			}},
		}
		c := predictorContext(t, nil, corev1alpha1.ComponentSpec{
			SchedulingPolicy: policy,
			Parameters: rawJSON(t, components.ModelServerCustomSpec{
				ModelFormat: "sklearn",
				Env:         []corev1.EnvVar{{Name: "OMP_NUM_THREADS", Value: "1"}},
				Args:        []string{"--workers=1"},
			}),
		})

		isvc, err := buildInferenceService(c)
		if err != nil {
			t.Fatal(err)
		}
		var spec inferenceServiceSpec
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(isvc.Object["spec"].(map[string]any), &spec); err != nil {
			t.Fatal(err)
		}
		got := spec.Predictor
		if !reflect.DeepEqual(got.Model.Env, []corev1.EnvVar{{Name: "OMP_NUM_THREADS", Value: "1"}}) || !reflect.DeepEqual(got.Model.Args, []string{"--workers=1"}) {
			t.Fatalf("env = %v, args = %v", got.Model.Env, got.Model.Args)
		}
		if got.SchedulerName != policy.SchedulerName || !reflect.DeepEqual(got.NodeSelector, policy.NodeSelector) ||
			!reflect.DeepEqual(got.Affinity, policy.Affinity) || !reflect.DeepEqual(got.Tolerations, policy.Tolerations) {
			t.Fatalf("scheduling = %+v", got)
		}
		if len(got.TopologySpreadConstraints) != 1 || got.TopologySpreadConstraints[0].LabelSelector == nil ||
			!reflect.DeepEqual(got.TopologySpreadConstraints[0].LabelSelector.MatchLabels, got.Labels) {
			t.Fatalf("topology spread constraints = %+v, want selector on the predictor pod labels %v", got.TopologySpreadConstraints, got.Labels)
		}
	})
}

func TestBuildInferenceServiceIdentity(t *testing.T) {
	c := predictorContext(t, nil, corev1alpha1.ComponentSpec{
		Parameters: rawJSON(t, components.ModelServerCustomSpec{
			ModelFormat:              "sklearn",
			ServiceAccountName:       "model-reader",
			Labels:                   map[string]string{"cost-center": "ml"},
			Annotations:              map[string]string{"sidecar.istio.io/inject": "false"},
			SecurityContext:          &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true)},
			ContainerSecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false)},
		}),
	})

	isvc, err := buildInferenceService(c)
	if err != nil {
		t.Fatal(err)
	}
	labels := isvc.GetLabels()
	if labels["cost-center"] != "ml" || labels[controller.ProviderLabel] != common.ProviderName || labels["app.kubernetes.io/managed-by"] != "everest" {
		t.Fatalf("labels = %v, want the user label next to the provider labels", labels)
	}
	annotations := isvc.GetAnnotations()
	if annotations["sidecar.istio.io/inject"] != "false" || annotations[common.DeploymentModeAnnotation] != common.DeploymentModeStandard {
		t.Fatalf("annotations = %v", annotations)
	}

	var spec inferenceServiceSpec
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(isvc.Object["spec"].(map[string]any), &spec); err != nil {
		t.Fatal(err)
	}
	p := spec.Predictor
	if p.ServiceAccountName != "model-reader" || p.SecurityContext == nil || !*p.SecurityContext.RunAsNonRoot ||
		p.Model.SecurityContext == nil || *p.Model.SecurityContext.AllowPrivilegeEscalation {
		t.Fatalf("predictor = %+v, model security context = %+v", p, p.Model.SecurityContext)
	}
}

func TestValidatePredictorMetadata(t *testing.T) {
	cases := []struct {
		name        string
		labels      map[string]string
		annotations map[string]string
		wantErr     string
	}{
		{name: "valid", labels: map[string]string{"team": "ml"}, annotations: map[string]string{"example.com/owner": "ml"}},
		{name: "provider label", labels: map[string]string{controller.InstanceLabel: "other"}, wantErr: "labels[core.openeverest.io/instance]: Forbidden: is managed by the provider"},
		{name: "ownership label", labels: map[string]string{"app.kubernetes.io/managed-by": "me"}, wantErr: "labels[app.kubernetes.io/managed-by]: Forbidden: is managed by the provider"},
		{name: "invalid label value", labels: map[string]string{"team": "has spaces"}, wantErr: `labels[team]: Invalid value: "has spaces"`},
		{name: "deployment mode annotation", annotations: map[string]string{common.DeploymentModeAnnotation: "Serverless"}, wantErr: "annotations[serving.kserve.io/deploymentMode]: Forbidden: is managed by the provider"},
		{name: "invalid annotation key", annotations: map[string]string{"bad key": "x"}, wantErr: `annotations[bad key]: Invalid value: "bad key"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := predictorContext(t, nil, corev1alpha1.ComponentSpec{
				Parameters: rawJSON(t, components.ModelServerCustomSpec{
					ModelFormat: "sklearn", StorageURI: "gs://bucket/iris",
					Labels: tc.labels, Annotations: tc.annotations,
				}),
			})
			err := validatePredictor(c.Instance()).ToAggregate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidatePredictorExternalAccess(t *testing.T) {
	cases := []struct {
		name    string
		topo    predictor.PredictorTopologyParameters
		service *corev1alpha1.Service
		wantErr bool
	}{
		{name: "defaults"},
		{name: "load balancer", topo: predictor.PredictorTopologyParameters{ExternalAccess: predictor.ExternalAccessLoadBalancer}},
		{name: "node port service type", service: &corev1alpha1.Service{ServiceType: corev1.ServiceTypeNodePort}},
		{name: "unknown externalAccess", topo: predictor.PredictorTopologyParameters{ExternalAccess: "Ingress"}, wantErr: true},
		{name: "external name service type", service: &corev1alpha1.Service{ServiceType: corev1.ServiceTypeExternalName}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := predictorContext(t, nil, corev1alpha1.ComponentSpec{
				Service:    tc.service,
				Parameters: rawJSON(t, components.ModelServerCustomSpec{ModelFormat: "sklearn", StorageURI: "gs://bucket/iris"}),
			})
			c.Instance().Spec.Topology.Parameters = rawJSON(t, tc.topo)
			if err := validatePredictor(c.Instance()).ToAggregate(); (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %t", err, tc.wantErr)
			}
		})
	}
}

func TestPredictorServiceType(t *testing.T) {
	cases := []struct {
		name    string
		topo    predictor.PredictorTopologyParameters
		service *corev1alpha1.Service
		want    corev1.ServiceType
	}{
		{name: "defaults to ClusterIP", want: corev1.ServiceTypeClusterIP},
		{name: "component service type", service: &corev1alpha1.Service{ServiceType: corev1.ServiceTypeNodePort}, want: corev1.ServiceTypeNodePort},
		{
			name:    "externalAccess wins over the component service type",
			topo:    predictor.PredictorTopologyParameters{ExternalAccess: predictor.ExternalAccessClusterIP},
			service: &corev1alpha1.Service{ServiceType: corev1.ServiceTypeLoadBalancer},
			want:    corev1.ServiceTypeClusterIP,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := predictorContext(t, nil, corev1alpha1.ComponentSpec{Service: tc.service})
			c.Instance().Spec.Topology.Parameters = rawJSON(t, tc.topo)
			if got := predictorServiceType(c); got != tc.want {
				t.Fatalf("service type = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildPredictorExternalService(t *testing.T) {
	c := predictorContext(t, nil, corev1alpha1.ComponentSpec{})
	spec := &corev1alpha1.Service{Annotations: map[string]string{"service.beta.kubernetes.io/linode-loadbalancer-throttle": "5"}}
	svc := buildExternalService(c, spec, corev1.ServiceTypeLoadBalancer, c.PodLabels(common.ComponentPredictor), predictorServicePort, predictorContainerPort)

	if svc.Name != "iris-external" || svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		t.Fatalf("service %s type %s", svc.Name, svc.Spec.Type)
	}
	if !reflect.DeepEqual(svc.Spec.Selector, c.PodLabels(common.ComponentPredictor)) {
		t.Fatalf("selector = %v", svc.Spec.Selector)
	}
	if p := svc.Spec.Ports; len(p) != 1 || p[0].Port != 80 || p[0].TargetPort.IntVal != 8080 {
		t.Fatalf("ports = %+v, want 80 -> 8080", p)
	}
	if svc.Annotations["service.beta.kubernetes.io/linode-loadbalancer-throttle"] != "5" {
		t.Fatalf("annotations = %v", svc.Annotations)
	}
}

func TestValidatePredictorStorageContainer(t *testing.T) {
	storageContainer := func(name string, mut func(map[string]any)) client.Object {
		obj := unstructuredObject(clusterStorageContainerGVK)
		obj.SetName(name)
		spec := map[string]any{
			"container": map[string]any{"name": "storage-initializer", "image": "kserve/storage-initializer"},
			"supportedUriFormats": []any{
				map[string]any{"prefix": "s3://"},
				map[string]any{"regex": `https://(.+?)\.blob\.core\.windows\.net/(.+)`},
			},
			"workloadType": "initContainer",
		}
		obj.Object["spec"] = spec
		if mut != nil {
			mut(obj.Object)
		}
		return obj
	}
	setWorkloadType := func(wt string) func(map[string]any) {
		return func(o map[string]any) { o["spec"].(map[string]any)["workloadType"] = wt }
	}

	cases := []struct {
		name             string
		storageContainer string
		storageURI       string
		objs             []client.Object
		wantErr          string
	}{
		{
			name:       "unset skips the lookup",
			storageURI: "gs://bucket/model",
		},
		{
			name:             "prefix match",
			storageContainer: "s3-init",
			storageURI:       "s3://bucket/model",
			objs:             []client.Object{storageContainer("s3-init", nil)},
		},
		{
			name:             "regex match",
			storageContainer: "s3-init",
			storageURI:       "https://acct.blob.core.windows.net/models/iris",
			objs:             []client.Object{storageContainer("s3-init", nil)},
		},
		{
			name:             "empty workloadType defaults to initContainer",
			storageContainer: "s3-init",
			storageURI:       "s3://bucket/model",
			objs:             []client.Object{storageContainer("s3-init", setWorkloadType(""))},
		},
		{
			name:             "invalid name",
			storageContainer: "S3_Init",
			storageURI:       "s3://bucket/model",
			wantErr:          `spec.components[predictor].parameters.storageContainer: Invalid value: "S3_Init"`,
		},
		{
			name:             "not found",
			storageContainer: "missing",
			storageURI:       "s3://bucket/model",
			wantErr:          `spec.components[predictor].parameters.storageContainer: Not found: "missing"`,
		},
		{
			name:             "disabled",
			storageContainer: "s3-init",
			storageURI:       "s3://bucket/model",
			objs: []client.Object{storageContainer("s3-init", func(o map[string]any) {
				o["disabled"] = true
			})},
			wantErr: `spec.components[predictor].parameters.storageContainer: Invalid value: "s3-init": the ClusterStorageContainer is disabled`,
		},
		{
			name:             "local model download job",
			storageContainer: "s3-init",
			storageURI:       "s3://bucket/model",
			objs:             []client.Object{storageContainer("s3-init", setWorkloadType("localModelDownloadJob"))},
			wantErr:          `spec.components[predictor].parameters.storageContainer: Invalid value: "s3-init": the ClusterStorageContainer has workloadType "localModelDownloadJob", want "initContainer"`,
		},
		{
			name:             "unsupported URI",
			storageContainer: "s3-init",
			storageURI:       "gs://bucket/model",
			objs:             []client.Object{storageContainer("s3-init", nil)},
			wantErr:          `spec.components[predictor].parameters.storageContainer: Invalid value: "s3-init": the ClusterStorageContainer does not support storageURI "gs://bucket/model"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithObjects(tc.objs...).Build()
			c := predictorContext(t, cl, corev1alpha1.ComponentSpec{
				Parameters: rawJSON(t, components.ModelServerCustomSpec{
					ModelFormat:      "sklearn",
					StorageURI:       tc.storageURI,
					StorageContainer: tc.storageContainer,
				}),
			})

			errs := validatePredictor(c.Instance())
			if len(errs) == 0 {
				refErrs, err := (&Provider{}).ValidateReferences(c)
				if err != nil {
					t.Fatal(err)
				}
				errs = refErrs
			}
			err := errs.ToAggregate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.HasPrefix(err.Error(), tc.wantErr)):
				t.Fatalf("error = %v, want prefix %q", err, tc.wantErr)
			}
		})
	}
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
	readyStatus := map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
	externalService := func(mut func(*corev1.Service)) *corev1.Service {
		svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "iris-external", Namespace: "ns"}}
		mut(svc)
		return svc
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-a"},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{Type: corev1.NodeInternalIP, Address: "10.0.0.5"},
			{Type: corev1.NodeExternalIP, Address: "203.0.113.5"},
		}},
	}

	cases := []struct {
		name      string
		topo      predictor.PredictorTopologyParameters
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
			name: "ready publishes the in-cluster address, not the ingress-domain URL",
			objs: []client.Object{isvcWithStatus(map[string]any{
				"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
				"url":        "http://iris-ns.example.com",
				"address":    map[string]any{"url": "http://iris-predictor.ns.svc.cluster.local"},
			})},
			wantPhase: corev1alpha1.InstancePhaseReady,
			wantURI:   "http://iris-predictor.ns.svc.cluster.local",
		},
		{
			name:      "ready without URL falls back to the in-cluster predictor Service",
			objs:      []client.Object{isvcWithStatus(readyStatus)},
			wantPhase: corev1alpha1.InstancePhaseReady,
			wantURI:   "http://iris-predictor.ns.svc.cluster.local:80",
		},
		{
			name:      "load balancer publishes its ingress address",
			topo:      predictor.PredictorTopologyParameters{ExternalAccess: predictor.ExternalAccessLoadBalancer},
			wantPhase: corev1alpha1.InstancePhaseReady,
			objs: []client.Object{
				isvcWithStatus(map[string]any{
					"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
					"url":        "http://iris-ns.example.com",
				}),
				externalService(func(s *corev1.Service) {
					s.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "198.51.100.7"}}
				}),
			},
			wantURI: "http://198.51.100.7:80",
		},
		{
			name:      "pending load balancer is ready without connection details",
			topo:      predictor.PredictorTopologyParameters{ExternalAccess: predictor.ExternalAccessLoadBalancer},
			objs:      []client.Object{isvcWithStatus(readyStatus), externalService(func(*corev1.Service) {})},
			wantPhase: corev1alpha1.InstancePhaseReady,
		},
		{
			name:      "node port pairs a node address with the allocated port",
			topo:      predictor.PredictorTopologyParameters{ExternalAccess: predictor.ExternalAccessNodePort},
			wantPhase: corev1alpha1.InstancePhaseReady,
			objs: []client.Object{
				isvcWithStatus(readyStatus),
				node,
				externalService(func(s *corev1.Service) {
					s.Spec.Ports = []corev1.ServicePort{{Port: 80, NodePort: 31080}}
				}),
			},
			wantURI: "http://203.0.113.5:31080",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithObjects(tc.objs...).Build()
			c := predictorContext(t, cl, comp)
			c.Instance().Spec.Topology.Parameters = rawJSON(t, tc.topo)
			got, err := (&Provider{}).statusPredictor(c)
			if err != nil {
				t.Fatal(err)
			}
			if got.Phase != tc.wantPhase {
				t.Fatalf("phase = %q, want %q", got.Phase, tc.wantPhase)
			}
			if got.ConnectionDetails.URI != tc.wantURI {
				t.Fatalf("connection details = %+v, want URI %q", got.ConnectionDetails, tc.wantURI)
			}
			if tc.wantURI != "" && got.ConnectionDetails.AdditionalProperties["model"] != "iris" {
				t.Fatalf("model = %q, want the Instance name", got.ConnectionDetails.AdditionalProperties["model"])
			}
		})
	}
}

func TestPredictorArgs(t *testing.T) {
	gpu := &corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")}}
	cpu := &corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}}
	cases := []struct {
		name   string
		format string
		task   string
		args   []string
		res    *corev1.ResourceRequirements
		want   []string
	}{
		{name: "huggingface on CPU gets the transformers backend", format: "huggingface", args: []string{"--task=text_embedding"}, res: cpu, want: []string{"--task=text_embedding", "--backend=huggingface"}},
		{name: "no resources counts as CPU", format: "huggingface", want: []string{"--backend=huggingface"}},
		{name: "huggingface on GPU keeps the runtime default", format: "huggingface", res: gpu},
		{name: "explicit backend is kept", format: "huggingface", args: []string{"--backend=vllm"}, res: cpu, want: []string{"--backend=vllm"}},
		{name: "task parameter becomes the first flag", format: "huggingface", task: "text_embedding", args: []string{"--max_length=256"}, res: gpu, want: []string{"--task=text_embedding", "--max_length=256", "--runner=pooling"}},
		{name: "embeddings on CPU need no runner", format: "huggingface", task: "text_embedding", res: cpu, want: []string{"--task=text_embedding", "--backend=huggingface"}},
		{name: "explicit runner is kept", format: "huggingface", task: "text_embedding", args: []string{"--runner=generate"}, res: gpu, want: []string{"--task=text_embedding", "--runner=generate"}},
		{name: "other GPU tasks need no runner", format: "huggingface", task: "sequence_classification", res: gpu, want: []string{"--task=sequence_classification"}},
		{name: "task in args wins over the parameter", format: "huggingface", task: "text_embedding", args: []string{"--task", "fill_mask"}, res: gpu, want: []string{"--task", "fill_mask"}},
		{name: "other formats are untouched", format: "sklearn", args: []string{"--workers=1"}, res: cpu, want: []string{"--workers=1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := components.ModelServerCustomSpec{ModelFormat: tc.format, Task: tc.task, Args: tc.args}
			if got := predictorArgs(params, tc.res); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("args = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidatePredictorTask(t *testing.T) {
	for format, wantErr := range map[string]bool{"huggingface": false, "sklearn": true} {
		c := predictorContext(t, nil, corev1alpha1.ComponentSpec{
			Parameters: rawJSON(t, components.ModelServerCustomSpec{ModelFormat: format, StorageURI: "hf://org/model", Task: "text_embedding"}),
		})
		if err := validatePredictor(c.Instance()).ToAggregate(); (err != nil) != wantErr {
			t.Fatalf("%s: err = %v, wantErr %t", format, err, wantErr)
		}
	}
}

func predictorCatalogEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PREDICTOR_MODEL_CATALOG", `[
		{"label":"MiniLM","name":"minilm","modelFormat":"huggingface","storageURI":"hf://sentence-transformers/all-MiniLM-L6-v2","task":"text_embedding"},
		{"label":"Qwen3 Embedding","name":"qwen3-embedding","modelFormat":"huggingface","storageURI":"hf://Qwen/Qwen3-Embedding-0.6B","task":"text_embedding","gpuCount":1},
		{"label":"Iris","name":"iris","modelFormat":"sklearn","storageURI":"gs://bucket/iris"}
	]`)
	common.ResetPredictorCatalogForTest()
	t.Cleanup(common.ResetPredictorCatalogForTest)
}

func TestResolveCatalogModel(t *testing.T) {
	predictorCatalogEnv(t)
	cases := []struct {
		name    string
		in      components.ModelServerCustomSpec
		want    components.ModelServerCustomSpec
		wantErr bool
	}{
		{
			name: "catalog entry fills format, URI and task",
			in:   components.ModelServerCustomSpec{Model: "minilm"},
			want: components.ModelServerCustomSpec{Model: "minilm", ModelFormat: "huggingface", StorageURI: "hf://sentence-transformers/all-MiniLM-L6-v2", Task: "text_embedding"},
		},
		{
			name: "explicit task wins",
			in:   components.ModelServerCustomSpec{Model: "minilm", Task: "fill_mask"},
			want: components.ModelServerCustomSpec{Model: "minilm", ModelFormat: "huggingface", StorageURI: "hf://sentence-transformers/all-MiniLM-L6-v2", Task: "fill_mask"},
		},
		{
			name: "GPU catalog entry sets gpuCount",
			in:   components.ModelServerCustomSpec{Model: "qwen3-embedding"},
			want: components.ModelServerCustomSpec{Model: "qwen3-embedding", ModelFormat: "huggingface", StorageURI: "hf://Qwen/Qwen3-Embedding-0.6B", Task: "text_embedding", GpuCount: ptr.To(int32(1))},
		},
		{
			name: "explicit gpuCount wins",
			in:   components.ModelServerCustomSpec{Model: "qwen3-embedding", GpuCount: ptr.To(int32(0))},
			want: components.ModelServerCustomSpec{Model: "qwen3-embedding", ModelFormat: "huggingface", StorageURI: "hf://Qwen/Qwen3-Embedding-0.6B", Task: "text_embedding", GpuCount: ptr.To(int32(0))},
		},
		{
			name: "custom format and URI ignore the catalog",
			in:   components.ModelServerCustomSpec{Model: "minilm", ModelFormat: "sklearn", StorageURI: "gs://bucket/mine"},
			want: components.ModelServerCustomSpec{Model: "minilm", ModelFormat: "sklearn", StorageURI: "gs://bucket/mine"},
		},
		{
			name: "no model leaves params alone",
			in:   components.ModelServerCustomSpec{ModelFormat: "onnx"},
			want: components.ModelServerCustomSpec{ModelFormat: "onnx"},
		},
		{name: "unknown model", in: components.ModelServerCustomSpec{Model: "nope"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveCatalogModel(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %t", err, tc.wantErr)
			}
			if !tc.wantErr && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("params = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestValidatePredictorInstanceName(t *testing.T) {
	for name, wantErr := range map[string]bool{"iris": false, "bge-small-en-v1.5": true, "1st-model": true} {
		c := predictorContext(t, nil, corev1alpha1.ComponentSpec{
			Parameters: rawJSON(t, components.ModelServerCustomSpec{ModelFormat: "sklearn", StorageURI: "gs://bucket/iris"}),
		})
		c.Instance().Name = name
		if err := validatePredictor(c.Instance()).ToAggregate(); (err != nil) != wantErr {
			t.Fatalf("%s: err = %v, wantErr %t", name, err, wantErr)
		}
	}
}

func TestBuildInferenceServiceFromCatalog(t *testing.T) {
	predictorCatalogEnv(t)
	c := predictorContext(t, nil, corev1alpha1.ComponentSpec{
		Parameters: rawJSON(t, components.ModelServerCustomSpec{Model: "minilm"}),
	})
	if err := validatePredictor(c.Instance()).ToAggregate(); err != nil {
		t.Fatal(err)
	}
	isvc, err := buildInferenceService(c)
	if err != nil {
		t.Fatal(err)
	}
	model, _, _ := unstructured.NestedMap(isvc.Object, "spec", "predictor", "model")
	want := map[string]any{
		"modelFormat": map[string]any{"name": "huggingface"},
		"storageUri":  "hf://sentence-transformers/all-MiniLM-L6-v2",
		"args":        []any{"--task=text_embedding", "--backend=huggingface"},
	}
	if !reflect.DeepEqual(model, want) {
		t.Fatalf("model = %#v\nwant %#v", model, want)
	}
}

func TestBuildInferenceServiceGPUCatalogModel(t *testing.T) {
	predictorCatalogEnv(t)
	c := predictorContext(t, nil, corev1alpha1.ComponentSpec{
		Resources:  &corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("16Gi")}},
		Parameters: rawJSON(t, components.ModelServerCustomSpec{Model: "qwen3-embedding"}),
	})
	isvc, err := buildInferenceService(c)
	if err != nil {
		t.Fatal(err)
	}
	model, _, _ := unstructured.NestedMap(isvc.Object, "spec", "predictor", "model")
	want := map[string]any{
		"modelFormat": map[string]any{"name": "huggingface"},
		"storageUri":  "hf://Qwen/Qwen3-Embedding-0.6B",
		"resources": map[string]any{
			"limits":   map[string]any{"memory": "16Gi", "nvidia.com/gpu": "1"},
			"requests": map[string]any{"memory": "16Gi", "nvidia.com/gpu": "1"},
		},
		"args": []any{"--task=text_embedding", "--runner=pooling"},
	}
	if !reflect.DeepEqual(model, want) {
		t.Fatalf("model = %#v\nwant %#v", model, want)
	}
}

func TestValidatePredictorGPUCount(t *testing.T) {
	for count, wantErr := range map[int32]bool{0: false, 2: false, -1: true} {
		c := predictorContext(t, nil, corev1alpha1.ComponentSpec{
			Parameters: rawJSON(t, components.ModelServerCustomSpec{ModelFormat: "sklearn", StorageURI: "gs://bucket/iris", GpuCount: ptr.To(count)}),
		})
		if err := validatePredictor(c.Instance()).ToAggregate(); (err != nil) != wantErr {
			t.Fatalf("%d: err = %v, wantErr %t", count, err, wantErr)
		}
	}
}

func TestValidatePredictorReplicaFloor(t *testing.T) {
	cases := []struct {
		name        string
		replicas    *int32
		minReplicas *int32
		wantErr     bool
	}{
		{name: "minReplicas 0", minReplicas: ptr.To(int32(0)), wantErr: true},
		{name: "minReplicas 1", minReplicas: ptr.To(int32(1))},
		{name: "replicas 0 without minReplicas", replicas: ptr.To(int32(0)), wantErr: true},
		{name: "minReplicas overrides replicas", replicas: ptr.To(int32(0)), minReplicas: ptr.To(int32(2))},
		{name: "neither set"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := predictorContext(t, nil, corev1alpha1.ComponentSpec{
				Replicas:   tc.replicas,
				Parameters: rawJSON(t, components.ModelServerCustomSpec{ModelFormat: "sklearn", StorageURI: "gs://bucket/iris", MinReplicas: tc.minReplicas}),
			})
			if err := validatePredictor(c.Instance()).ToAggregate(); (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %t", err, tc.wantErr)
			}
		})
	}
}

func TestBuildInferenceServiceGPUScheduling(t *testing.T) {
	gpuTaint := corev1.Toleration{Key: "nvidia.com/gpu", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}
	userToleration := corev1.Toleration{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "ml", Effect: corev1.TaintEffectNoSchedule}
	cases := []struct {
		name            string
		gpuCount        *int32
		tolerations     []corev1.Toleration
		wantTolerations []corev1.Toleration
		wantStrategy    bool
	}{
		{name: "CPU predictor keeps defaults", tolerations: []corev1.Toleration{userToleration}, wantTolerations: []corev1.Toleration{userToleration}},
		{name: "GPU predictor tolerates the GPU taint", gpuCount: ptr.To(int32(1)), wantTolerations: []corev1.Toleration{gpuTaint}, wantStrategy: true},
		{name: "user tolerations are kept", gpuCount: ptr.To(int32(1)), tolerations: []corev1.Toleration{userToleration}, wantTolerations: []corev1.Toleration{userToleration, gpuTaint}, wantStrategy: true},
		{name: "an existing GPU toleration is not duplicated", gpuCount: ptr.To(int32(1)), tolerations: []corev1.Toleration{{Key: "nvidia.com/gpu", Operator: corev1.TolerationOpExists}}, wantTolerations: []corev1.Toleration{{Key: "nvidia.com/gpu", Operator: corev1.TolerationOpExists}}, wantStrategy: true},
		{name: "a tolerate-everything toleration covers it", gpuCount: ptr.To(int32(1)), tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}}, wantTolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}}, wantStrategy: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			comp := corev1alpha1.ComponentSpec{
				Parameters: rawJSON(t, components.ModelServerCustomSpec{ModelFormat: "huggingface", StorageURI: "hf://org/model", GpuCount: tc.gpuCount}),
			}
			if tc.tolerations != nil {
				comp.SchedulingPolicy = &commonv1alpha1.SchedulingPolicy{Tolerations: tc.tolerations}
			}
			isvc, err := buildInferenceService(predictorContext(t, nil, comp))
			if err != nil {
				t.Fatal(err)
			}
			var spec inferenceServiceSpec
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(isvc.Object["spec"].(map[string]any), &spec); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(spec.Predictor.Tolerations, tc.wantTolerations) {
				t.Fatalf("tolerations = %+v\nwant %+v", spec.Predictor.Tolerations, tc.wantTolerations)
			}
			want := (*appsv1.DeploymentStrategy)(nil)
			if tc.wantStrategy {
				want = gpuDeploymentStrategy()
			}
			if !reflect.DeepEqual(spec.Predictor.DeploymentStrategy, want) {
				t.Fatalf("deploymentStrategy = %+v, want %+v", spec.Predictor.DeploymentStrategy, want)
			}
		})
	}
}

func TestBuildInferenceServiceHFTokenServiceAccount(t *testing.T) {
	t.Setenv("HF_TOKEN_SECRET_NAME", "hf-token")
	for _, tc := range []struct {
		uri, serviceAccount, want string
	}{
		{uri: "hf://sentence-transformers/all-MiniLM-L6-v2", want: "iris-model-puller"},
		{uri: "gs://bucket/iris"},
		{uri: "hf://sentence-transformers/all-MiniLM-L6-v2", serviceAccount: "models-reader", want: "models-reader"},
	} {
		c := predictorContext(t, nil, corev1alpha1.ComponentSpec{
			Parameters: rawJSON(t, components.ModelServerCustomSpec{ModelFormat: "huggingface", StorageURI: tc.uri, ServiceAccountName: tc.serviceAccount}),
		})
		isvc, err := buildInferenceService(c)
		if err != nil {
			t.Fatal(err)
		}
		got, _, _ := unstructured.NestedString(isvc.Object, "spec", "predictor", "serviceAccountName")
		if got != tc.want {
			t.Fatalf("%s with %q: serviceAccountName = %q, want %q", tc.uri, tc.serviceAccount, got, tc.want)
		}
	}
}
