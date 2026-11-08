package provider

import (
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"

	"github.com/openeverest/provider-kserve/definition/components"
)

func TestBuildInferenceServiceConfig(t *testing.T) {
	params := components.ModelServerCustomSpec{
		ModelFormat: "sklearn", StorageURI: "gs://bucket/iris",
		MinReplicas: ptr.To(int32(0)), MaxReplicas: ptr.To(int32(0)),
		Args:                     []string{"--workers=2"},
		ContainerSecurityContext: &corev1.SecurityContext{RunAsNonRoot: ptr.To(false)},
		Config: `predictor:
  minReplicas: 9
  maxReplicas: 10
  batcher:
    maxBatchSize: 8
    maxLatency: 100
  logger:
    mode: all
    url: http://logger.default.svc.cluster.local
  model:
    modelFormat:
      name: xgboost
      version: "1"
    storageUri: gs://wrong/model
    args: [--workers=9, --raw-only]
    securityContext:
      runAsNonRoot: true
      readOnlyRootFilesystem: true
    livenessProbe:
      httpGet:
        path: /v1/models/iris
        port: 8080
      initialDelaySeconds: 30
  containers:
    - name: sidecar
      image: busybox:1.37
      command: [sleep, infinity]
`,
	}
	c := predictorContext(t, nil, corev1alpha1.ComponentSpec{Parameters: rawJSON(t, params)})
	isvc, err := buildInferenceService(c)
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		path []string
		want any
	}{
		{[]string{"predictor", "minReplicas"}, int64(0)},
		{[]string{"predictor", "maxReplicas"}, int64(0)},
		{[]string{"predictor", "model", "modelFormat", "name"}, "sklearn"},
		{[]string{"predictor", "model", "modelFormat", "version"}, "1"},
		{[]string{"predictor", "model", "storageUri"}, "gs://bucket/iris"},
		{[]string{"predictor", "model", "args"}, []any{"--workers=2"}},
		{[]string{"predictor", "model", "securityContext", "runAsNonRoot"}, false},
		{[]string{"predictor", "model", "securityContext", "readOnlyRootFilesystem"}, true},
		{[]string{"predictor", "batcher", "maxBatchSize"}, int64(8)},
		{[]string{"predictor", "logger", "mode"}, "all"},
		{[]string{"predictor", "model", "livenessProbe", "initialDelaySeconds"}, int64(30)},
		{[]string{"predictor", "containers"}, []any{map[string]any{"name": "sidecar", "image": "busybox:1.37", "command": []any{"sleep", "infinity"}}}},
	}
	for _, check := range checks {
		t.Run(strings.Join(check.path, "."), func(t *testing.T) {
			got, _, err := unstructured.NestedFieldNoCopy(isvc.Object, append([]string{"spec"}, check.path...)...)
			if err != nil || !reflect.DeepEqual(got, check.want) {
				t.Fatalf("got %#v, error %v; want %#v", got, err, check.want)
			}
		})
	}
}

func TestBuildInferenceServiceInvalidConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"malformed YAML", "predictor: ["},
		{"duplicate keys", "predictor: {}\npredictor: {}"},
		{"multiple documents", "predictor: {}\n---\npredictor: {}"},
		{"null", "null"},
		{"scalar", "hello"},
		{"list", "- predictor"},
		{"whitespace", " \n"},
		{"resource envelope", "apiVersion: serving.kserve.io/v1beta1\nkind: InferenceService\nspec: {}"},
		{"metadata", "metadata:\n  name: another-instance"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := predictorContext(t, nil, corev1alpha1.ComponentSpec{Parameters: rawJSON(t, components.ModelServerCustomSpec{
				ModelFormat: "sklearn", StorageURI: "gs://bucket/iris", Config: tc.raw,
			})})
			if err := validatePredictor(c.Instance()).ToAggregate(); err == nil || !strings.Contains(err.Error(), "[predictor].parameters.config") {
				t.Fatalf("expected config validation error, got %v", err)
			}
		})
	}
}
