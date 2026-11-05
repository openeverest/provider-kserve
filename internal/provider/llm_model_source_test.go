package provider

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	"github.com/openeverest/provider-kserve/definition/components"
	"github.com/openeverest/provider-kserve/definition/topologies/llm"
)

func TestValidateLLMModelSource(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		params  components.VllmCustomSpec
		wantErr string
	}{
		{name: "hf", params: components.VllmCustomSpec{ModelURI: "hf://org/model"}},
		{name: "s3", params: components.VllmCustomSpec{ModelURI: "s3://bucket/model"}},
		{name: "pvc", params: components.VllmCustomSpec{ModelURI: "pvc://claim/model"}},
		{name: "oci modelcar", params: components.VllmCustomSpec{ModelURI: "oci://quay.io/org/model:v1"}},
		{name: "oci native", params: components.VllmCustomSpec{ModelURI: "oci+native://quay.io/org/model:v1"}},
		{name: "gs is not supported for llm", params: components.VllmCustomSpec{ModelURI: "gs://bucket/model"}, wantErr: `scheme "gs" is not supported`},
		{name: "oci+modelcar is not supported for llm", params: components.VllmCustomSpec{ModelURI: "oci+modelcar://quay.io/org/model:v1"}, wantErr: "is not supported"},
		{name: "schemes are case sensitive", params: components.VllmCustomSpec{ModelURI: "HF://org/model"}, wantErr: "is not supported"},
		{
			name:   "disabled storage initializer accepts any scheme",
			params: components.VllmCustomSpec{ModelURI: "gs://bucket/model", DisableStorageInitializer: ptr.To(true)},
		},
		{
			name:    "oci with disabled storage initializer",
			params:  components.VllmCustomSpec{ModelURI: "oci://quay.io/org/model:v1", DisableStorageInitializer: ptr.To(true)},
			wantErr: "must be false for oci:// models",
		},
		{
			name:    "oci native with disabled storage initializer",
			params:  components.VllmCustomSpec{ModelURI: "oci+native://quay.io/org/model:v1", DisableStorageInitializer: ptr.To(true)},
			wantErr: "must be false for oci+native:// models",
		},
		{
			name:   "oci with storage initializer explicitly on",
			params: components.VllmCustomSpec{ModelURI: "oci://quay.io/org/model:v1", DisableStorageInitializer: ptr.To(false)},
		},
		{name: "pull secret", params: components.VllmCustomSpec{ModelURI: "oci://r/m:v1", ImagePullSecret: "quay-pull"}},
		{name: "invalid pull secret", params: components.VllmCustomSpec{ModelURI: "oci://r/m:v1", ImagePullSecret: "Quay_Pull"}, wantErr: "imagePullSecret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateLLMModelSource(tc.params, field.NewPath("parameters")).ToAggregate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateLLMModelSource() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateLLMModelSource() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateLLMRejectsUnsupportedModelScheme(t *testing.T) {
	t.Parallel()
	err := validateLLM(llmContext(t, nil, components.VllmCustomSpec{ModelURI: "gs://bucket/model"}, llm.LlmTopologyParameters{}).Instance()).ToAggregate()
	if err == nil || !strings.Contains(err.Error(), "modelURI") {
		t.Fatalf("validateLLM() = %v, want modelURI scheme error", err)
	}
}

func pullSecrets(pod *corev1.PodSpec) []string {
	if pod == nil {
		return nil
	}
	var names []string
	for _, s := range pod.ImagePullSecrets {
		names = append(names, s.Name)
	}
	return names
}

func TestBuildLLMOCIModel(t *testing.T) {
	t.Run("oci model gets no storage-initializer override", func(t *testing.T) {
		t.Setenv("STORAGE_INITIALIZER_RESOURCES", `{"limits":{"memory":"4Gi"}}`)
		got, err := buildLLMInferenceService(llmContext(t, nil, components.VllmCustomSpec{
			ModelURI: "oci://quay.io/org/model:v1",
		}, llm.LlmTopologyParameters{}))
		if err != nil {
			t.Fatal(err)
		}
		if got.Spec.Model.URI.String() != "oci://quay.io/org/model:v1" {
			t.Fatalf("model URI = %q", got.Spec.Model.URI.String())
		}
		// KServe adds no storage-initializer for OCI models, so an override
		// would remain as an image-less init container and break the pod.
		if inits := got.Spec.Template.InitContainers; len(inits) != 0 {
			t.Fatalf("init containers = %v, want none", inits)
		}
	})

	t.Run("no pull secret by default", func(t *testing.T) {
		got, err := buildLLMInferenceService(llmContext(t, nil, components.VllmCustomSpec{
			ModelURI: "oci://quay.io/org/model:v1",
		}, llm.LlmTopologyParameters{}))
		if err != nil {
			t.Fatal(err)
		}
		if names := pullSecrets(got.Spec.Template); names != nil {
			t.Fatalf("pull secrets = %v, want none", names)
		}
	})

	t.Run("pull secret reaches every vLLM pod", func(t *testing.T) {
		params := components.VllmCustomSpec{
			ModelURI:             "oci+native://registry.example.com/org/model:v1",
			ImagePullSecret:      "registry-pull",
			PipelineParallelSize: ptr.To(int32(2)),
			WorkerCount:          ptr.To(int32(1)),
		}
		topo := llm.LlmTopologyParameters{
			EnablePrefill:               true,
			PrefillPipelineParallelSize: ptr.To(int32(2)),
			PrefillWorkerCount:          ptr.To(int32(1)),
		}
		got, err := buildLLMInferenceService(llmContext(t, nil, params, topo))
		if err != nil {
			t.Fatal(err)
		}
		for where, pod := range map[string]*corev1.PodSpec{
			"template":       got.Spec.Template,
			"worker":         got.Spec.Worker,
			"prefill":        got.Spec.Prefill.Template,
			"prefill worker": got.Spec.Prefill.Worker,
		} {
			if names := pullSecrets(pod); len(names) != 1 || names[0] != "registry-pull" {
				t.Fatalf("%s pull secrets = %v, want [registry-pull]", where, names)
			}
		}
		if len(got.Spec.Template.Containers) == 0 || got.Spec.Template.Containers[0].Name != "main" {
			t.Fatalf("main container override lost: %v", got.Spec.Template.Containers)
		}
	})

	t.Run("pull secret without workers or prefill", func(t *testing.T) {
		got, err := buildLLMInferenceService(llmContext(t, nil, components.VllmCustomSpec{
			ModelURI:        "oci://registry.example.com/org/model:v1",
			ImagePullSecret: "registry-pull",
		}, llm.LlmTopologyParameters{}))
		if err != nil {
			t.Fatal(err)
		}
		if names := pullSecrets(got.Spec.Template); len(names) != 1 || names[0] != "registry-pull" {
			t.Fatalf("template pull secrets = %v, want [registry-pull]", names)
		}
		if got.Spec.Worker != nil || got.Spec.Prefill != nil {
			t.Fatalf("worker/prefill created without being requested: %v / %v", got.Spec.Worker, got.Spec.Prefill)
		}
	})
}
