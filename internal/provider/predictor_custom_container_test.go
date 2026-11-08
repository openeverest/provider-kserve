package provider

import (
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"

	"github.com/openeverest/provider-kserve/definition/components"
)

func TestBuildInferenceServiceCustomContainer(t *testing.T) {
	c := predictorContext(t, nil, corev1alpha1.ComponentSpec{
		Resources: &corev1.ResourceRequirements{
			Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("8Gi")},
		},
		Parameters: rawJSON(t, components.ModelServerCustomSpec{
			// The UI always sends a catalog model; the image wins.
			Model:           "all-minilm-l6-v2",
			Image:           "vllm/vllm-openai:v0.11.0",
			Command:         []string{"vllm", "serve"},
			Args:            []string{"/mnt/models", "--served-model-name=chat"},
			Env:             []corev1.EnvVar{{Name: "VLLM_LOGGING_LEVEL", Value: "DEBUG"}},
			StorageURI:      "hf://Qwen/Qwen3-0.6B",
			Port:            ptr.To(int32(8000)),
			HealthPath:      "/health",
			GpuCount:        ptr.To(int32(1)),
			ImagePullSecret: "registry-creds",
			Annotations:     map[string]string{metricsPathAnnotation: "/custom-metrics"},
		}),
	})

	isvc, err := buildInferenceService(c)
	if err != nil {
		t.Fatal(err)
	}
	predictor, _, _ := unstructured.NestedMap(isvc.Object, "spec", "predictor")
	if _, ok := predictor["model"]; ok {
		t.Fatal("a custom container must not render predictor.model")
	}
	wantContainers := []any{map[string]any{
		"name":    "kserve-container",
		"image":   "vllm/vllm-openai:v0.11.0",
		"command": []any{"vllm", "serve"},
		"args":    []any{"/mnt/models", "--served-model-name=chat"},
		"env": []any{
			map[string]any{"name": "VLLM_LOGGING_LEVEL", "value": "DEBUG"},
			map[string]any{"name": "STORAGE_URI", "value": "hf://Qwen/Qwen3-0.6B"},
		},
		"ports": []any{map[string]any{"name": "http", "containerPort": int64(8000), "protocol": "TCP"}},
		"resources": map[string]any{
			"limits":   map[string]any{"memory": "8Gi", "nvidia.com/gpu": "1"},
			"requests": map[string]any{"memory": "8Gi", "nvidia.com/gpu": "1"},
		},
		"readinessProbe": map[string]any{
			"httpGet":       map[string]any{"path": "/health", "port": int64(8000)},
			"periodSeconds": int64(10),
		},
		"volumeMounts": []any{map[string]any{"name": "dshm", "mountPath": "/dev/shm"}},
	}}
	if !reflect.DeepEqual(predictor["containers"], wantContainers) {
		t.Fatalf("containers = %#v\nwant %#v", predictor["containers"], wantContainers)
	}
	wantVolumes := []any{map[string]any{"name": "dshm", "emptyDir": map[string]any{"medium": "Memory"}}}
	if !reflect.DeepEqual(predictor["volumes"], wantVolumes) {
		t.Fatalf("volumes = %#v", predictor["volumes"])
	}
	if want := []any{map[string]any{"name": "registry-creds"}}; !reflect.DeepEqual(predictor["imagePullSecrets"], want) {
		t.Fatalf("imagePullSecrets = %#v", predictor["imagePullSecrets"])
	}
	if predictor["deploymentStrategy"] == nil || predictor["tolerations"] == nil {
		t.Fatal("a GPU custom container must get the GPU rollout strategy and taint toleration")
	}
	annotations := isvc.GetAnnotations()
	if annotations[metricsPortAnnotation] != "8000" || annotations[metricsPathAnnotation] != "/custom-metrics" {
		t.Fatalf("metrics annotations = %v, want port 8000 and the user's path", annotations)
	}
	if got := predictorTargetPort(isvc); got != 8000 {
		t.Fatalf("predictorTargetPort = %d, want 8000", got)
	}
}

func TestBuildInferenceServiceCustomContainerConfig(t *testing.T) {
	params := components.ModelServerCustomSpec{
		Image: "ghcr.io/example/server:1",
		Config: `predictor:
  containers:
    - name: transformer-container
      image: ghcr.io/example/shim:1
      ports:
        - containerPort: 3000
    - name: kserve-container
      image: ignored
      args: [--max-model-len=4096]
      volumeMounts:
        - name: cache
          mountPath: /root/.cache
  volumes:
    - name: cache
      persistentVolumeClaim:
        claimName: model-cache
`,
	}
	c := predictorContext(t, nil, corev1alpha1.ComponentSpec{Parameters: rawJSON(t, params)})
	isvc, err := buildInferenceService(c)
	if err != nil {
		t.Fatal(err)
	}
	containers, _, _ := unstructured.NestedSlice(isvc.Object, "spec", "predictor", "containers")
	if len(containers) != 2 {
		t.Fatalf("containers = %#v, want the model server and the sidecar", containers)
	}
	server := containers[0].(map[string]any)
	checks := map[string]any{
		"name":  "kserve-container",
		"image": "ghcr.io/example/server:1",
		"args":  []any{"--max-model-len=4096"},
		"volumeMounts": []any{
			map[string]any{"name": "dshm", "mountPath": "/dev/shm"},
			map[string]any{"name": "cache", "mountPath": "/root/.cache"},
		},
	}
	for field, want := range checks {
		if !reflect.DeepEqual(server[field], want) {
			t.Errorf("model server %s = %#v, want %#v", field, server[field], want)
		}
	}
	if name := containers[1].(map[string]any)["name"]; name != "transformer-container" {
		t.Fatalf("second container = %v, want the sidecar", name)
	}
	volumes, _, _ := unstructured.NestedSlice(isvc.Object, "spec", "predictor", "volumes")
	if len(volumes) != 2 {
		t.Fatalf("volumes = %#v, want dshm and cache", volumes)
	}
	if got := predictorTargetPort(isvc); got != 3000 {
		t.Fatalf("predictorTargetPort = %d, want the sidecar's 3000", got)
	}
}

func TestValidateCustomContainer(t *testing.T) {
	image := "ghcr.io/example/server:1"
	cases := []struct {
		name    string
		params  components.ModelServerCustomSpec
		wantErr string
	}{
		{name: "image only", params: components.ModelServerCustomSpec{Image: image}},
		{name: "catalog model is ignored", params: components.ModelServerCustomSpec{Image: image, Model: "all-minilm-l6-v2"}},
		{
			name: "all container fields",
			params: components.ModelServerCustomSpec{
				Image: image, Port: ptr.To(int32(80)), HealthPath: "/health", OpenAICompatible: true,
				ServedModelName: "chat", ImagePullSecret: "creds", StorageURI: "s3://bucket/model",
			},
		},
		{name: "model format", params: components.ModelServerCustomSpec{Image: image, ModelFormat: "sklearn"}, wantErr: "parameters.modelFormat: Forbidden: selects a KServe serving runtime"},
		{name: "runtime", params: components.ModelServerCustomSpec{Image: image, Runtime: "kserve-sklearnserver"}, wantErr: "parameters.runtime: Forbidden: selects"},
		{name: "task", params: components.ModelServerCustomSpec{Image: image, Task: "text_embedding"}, wantErr: "parameters.task: Forbidden: selects"},
		{name: "port out of range", params: components.ModelServerCustomSpec{Image: image, Port: ptr.To(int32(70000))}, wantErr: "between 1 and 65535"},
		{name: "relative health path", params: components.ModelServerCustomSpec{Image: image, HealthPath: "health"}, wantErr: "must start with /"},
		{name: "invalid pull secret", params: components.ModelServerCustomSpec{Image: image, ImagePullSecret: "Bad_Name"}, wantErr: "parameters.imagePullSecret"},
		{name: "storage container without URI", params: components.ModelServerCustomSpec{Image: image, StorageContainer: "large"}, wantErr: "requires storageURI"},
		{
			name:    "container field without image",
			params:  components.ModelServerCustomSpec{ModelFormat: "sklearn", StorageURI: "gs://bucket/iris", HealthPath: "/health"},
			wantErr: "parameters.healthPath: Forbidden: requires image",
		},
		{
			name:    "openAICompatible without image",
			params:  components.ModelServerCustomSpec{ModelFormat: "sklearn", StorageURI: "gs://bucket/iris", OpenAICompatible: true},
			wantErr: "parameters.openAICompatible: Forbidden: requires image",
		},
		{
			name:    "apiSchema without image",
			params:  components.ModelServerCustomSpec{ModelFormat: "sklearn", StorageURI: "gs://bucket/iris", APISchema: "TypeSafe"},
			wantErr: "parameters.apiSchema: Forbidden: requires image",
		},
		{
			name:    "backendAPIKeySecret without image",
			params:  components.ModelServerCustomSpec{ModelFormat: "sklearn", StorageURI: "gs://bucket/iris", BackendAPIKeySecret: "key"},
			wantErr: "parameters.backendAPIKeySecret: Forbidden: requires image",
		},
		{name: "apiSchema TypeSafe", params: components.ModelServerCustomSpec{Image: image, APISchema: "TypeSafe"}},
		{name: "apiSchema OpenAI with openAICompatible", params: components.ModelServerCustomSpec{Image: image, APISchema: "OpenAI", OpenAICompatible: true}},
		{name: "unknown apiSchema", params: components.ModelServerCustomSpec{Image: image, APISchema: "Anthropic"}, wantErr: `parameters.apiSchema: Unsupported value: "Anthropic"`},
		{
			name:    "TypeSafe with openAICompatible",
			params:  components.ModelServerCustomSpec{Image: image, APISchema: "TypeSafe", OpenAICompatible: true},
			wantErr: "parameters.openAICompatible: Invalid value: true: conflicts with apiSchema",
		},
		{name: "invalid backend key secret", params: components.ModelServerCustomSpec{Image: image, BackendAPIKeySecret: "Bad_Name"}, wantErr: "parameters.backendAPIKeySecret:"},
		{name: "backend key secret without the gateway", params: components.ModelServerCustomSpec{Image: image, BackendAPIKeySecret: "key"}, wantErr: "requires topology.parameters.externalAccess EnvoyAIGateway"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := predictorContext(t, nil, corev1alpha1.ComponentSpec{Parameters: rawJSON(t, tc.params)})
			err := validatePredictor(c.Instance()).ToAggregate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestPredictorGatewaySchema(t *testing.T) {
	cases := []struct {
		name     string
		params   components.ModelServerCustomSpec
		want     map[string]any
		wantBase string
	}{
		{name: "huggingface embeddings", params: components.ModelServerCustomSpec{ModelFormat: "huggingface", Task: "text_embedding"},
			want: map[string]any{"name": "OpenAI", "prefix": "/openai/v1"}, wantBase: "/v1"},
		{name: "other runtime", params: components.ModelServerCustomSpec{ModelFormat: "sklearn"}},
		{name: "OpenAI-compatible image", params: components.ModelServerCustomSpec{Image: "vllm/vllm-openai", OpenAICompatible: true},
			want: map[string]any{"name": "OpenAI", "prefix": "/v1"}, wantBase: "/v1"},
		{name: "apiSchema OpenAI", params: components.ModelServerCustomSpec{Image: "vllm/vllm-openai", APISchema: "OpenAI"},
			want: map[string]any{"name": "OpenAI", "prefix": "/v1"}, wantBase: "/v1"},
		{name: "apiSchema TypeSafe", params: components.ModelServerCustomSpec{Image: "vllm/vllm-openai", APISchema: "TypeSafe"},
			want: map[string]any{"name": "TypeSafe", "version": "v1"}, wantBase: "/typesafe"},
		{name: "other image", params: components.ModelServerCustomSpec{Image: "ghcr.io/example/server:1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := predictorGatewaySchema(tc.params)
			if ok != (tc.want != nil) {
				t.Fatalf("predictorGatewaySchema ok = %v, want %v", ok, tc.want != nil)
			}
			if !ok {
				return
			}
			if !reflect.DeepEqual(got.spec(), tc.want) {
				t.Fatalf("schema = %#v, want %#v", got.spec(), tc.want)
			}
			if got.clientBasePath() != tc.wantBase {
				t.Fatalf("clientBasePath = %q, want %q", got.clientBasePath(), tc.wantBase)
			}
		})
	}
}

func TestPredictorAIBackendRef(t *testing.T) {
	cases := []struct {
		name            string
		servedModelName string
		want            map[string]any
	}{
		{name: "unset", want: map[string]any{"name": "chat-ai-gateway"}},
		{name: "same as the Instance", servedModelName: "chat", want: map[string]any{"name": "chat-ai-gateway"}},
		{name: "server name", servedModelName: "Qwen/Qwen3-0.6B", want: map[string]any{"name": "chat-ai-gateway", "modelNameOverride": "Qwen/Qwen3-0.6B"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := predictorAIBackendRef("chat-ai-gateway", "chat", components.ModelServerCustomSpec{Image: "x", ServedModelName: tc.servedModelName})
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("backendRef = %#v, want %#v", got, tc.want)
			}
		})
	}
}
