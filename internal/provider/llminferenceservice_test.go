package provider

import (
	"context"
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/definition/components"
	"github.com/openeverest/provider-kserve/definition/topologies/llm"
	"github.com/openeverest/provider-kserve/internal/common"
)

func loraCatalogEnv(t *testing.T) {
	t.Helper()
	t.Setenv("LORA_ADAPTER_CATALOG", `[{"label":"SQL","name":"sql-lora","uri":"hf://org/sql"},{"label":"Code","name":"code-style","uri":"s3://bucket/code"}]`)
	common.ResetLoRACatalogForTest()
}

func TestBuildModelLoRA(t *testing.T) {
	t.Run("nil when unset", func(t *testing.T) {
		got, err := buildModelLoRA(components.VllmCustomSpec{})
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Fatalf("buildModelLoRA() = %#v, want nil", got)
		}
	})

	t.Run("maps adapters and tuning", func(t *testing.T) {
		got, err := buildModelLoRA(components.VllmCustomSpec{
			LoRA: &components.LoRASpec{
				MaxRank:        ptr.To(int32(32)),
				MaxAdapters:    ptr.To(int32(4)),
				MaxCpuAdapters: ptr.To(int32(8)),
				Adapters: []components.LoRAAdapterSpec{
					{Name: "ft-v1", URI: "hf://org/adapter-v1"},
					{Name: "ft-v2", URI: "s3://bucket/adapters/v2"},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			t.Fatal("expected non-nil LoRA spec")
		}
		if len(got.Adapters) != 2 {
			t.Fatalf("adapters = %d, want 2", len(got.Adapters))
		}
		if got.MaxRank == nil || *got.MaxRank != 32 {
			t.Fatalf("maxRank = %v, want 32", got.MaxRank)
		}
		if *got.Adapters[0].Name != "ft-v1" || got.Adapters[0].URI.String() != "hf://org/adapter-v1" {
			t.Fatalf("adapter[0] = %+v", got.Adapters[0])
		}
	})

	t.Run("maps catalog slots when deployment enabled", func(t *testing.T) {
		loraCatalogEnv(t)
		got, err := buildModelLoRA(components.VllmCustomSpec{
			LoraDeployment: components.LoraDeploymentEnabled,
			LoraSlot1:      "sql-lora",
			LoraSlot2:      "code-style",
		})
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || len(got.Adapters) != 2 {
			t.Fatalf("adapters = %d, want 2", len(got.Adapters))
		}
		if *got.Adapters[0].Name != "sql-lora" || got.Adapters[0].URI.String() != "hf://org/sql" {
			t.Fatalf("adapter[0] = %+v", got.Adapters[0])
		}
		if *got.Adapters[1].Name != "code-style" || got.Adapters[1].URI.String() != "s3://bucket/code" {
			t.Fatalf("adapter[1] = %+v", got.Adapters[1])
		}
	})
}

func TestValidateLoRAParams(t *testing.T) {
	base := components.VllmCustomSpec{
		ModelURI:  "hf://org/base",
		ModelName: "base-model",
	}

	t.Run("duplicate adapter names", func(t *testing.T) {
		err := validateLoRAParams("inst", components.VllmCustomSpec{
			ModelURI: base.ModelURI,
			LoRA: &components.LoRASpec{
				Adapters: []components.LoRAAdapterSpec{
					{Name: "dup", URI: "hf://org/a"},
					{Name: "dup", URI: "hf://org/b"},
				},
			},
		})
		if err == nil {
			t.Fatal("expected error for duplicate adapter name")
		}
	})

	t.Run("adapter name matches base model", func(t *testing.T) {
		err := validateLoRAParams("inst", components.VllmCustomSpec{
			ModelURI:  base.ModelURI,
			ModelName: "base-model",
			LoRA: &components.LoRASpec{
				Adapters: []components.LoRAAdapterSpec{
					{Name: "base-model", URI: "hf://org/a"},
				},
			},
		})
		if err == nil {
			t.Fatal("expected error when adapter name equals base model name")
		}
	})

	t.Run("hf adapter with storage initializer disabled", func(t *testing.T) {
		err := validateLoRAParams("inst", components.VllmCustomSpec{
			ModelURI:                  base.ModelURI,
			DisableStorageInitializer: ptr.To(true),
			LoRA: &components.LoRASpec{
				Adapters: []components.LoRAAdapterSpec{
					{Name: "ft", URI: "hf://org/a"},
				},
			},
		})
		if err == nil {
			t.Fatal("expected error when hf adapter needs storage initializer")
		}
	})

	t.Run("valid", func(t *testing.T) {
		if err := validateLoRAParams("inst", components.VllmCustomSpec{
			ModelURI: base.ModelURI,
			LoRA: &components.LoRASpec{
				Adapters: []components.LoRAAdapterSpec{
					{Name: "ft-v1", URI: "hf://org/a"},
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("enabled deployment requires slot", func(t *testing.T) {
		loraCatalogEnv(t)
		err := validateLoRAParams("inst", components.VllmCustomSpec{
			ModelURI:       base.ModelURI,
			LoraDeployment: components.LoraDeploymentEnabled,
		})
		if err == nil {
			t.Fatal("expected error when enabled with no slots")
		}
	})

	t.Run("unknown catalog adapter in slot", func(t *testing.T) {
		loraCatalogEnv(t)
		err := validateLoRAParams("inst", components.VllmCustomSpec{
			ModelURI:       base.ModelURI,
			LoraDeployment: components.LoraDeploymentEnabled,
			LoraSlot1:      "missing",
		})
		if err == nil {
			t.Fatal("expected error for unknown catalog adapter")
		}
	})

	t.Run("duplicate slots", func(t *testing.T) {
		loraCatalogEnv(t)
		err := validateLoRAParams("inst", components.VllmCustomSpec{
			ModelURI:       base.ModelURI,
			LoraDeployment: components.LoraDeploymentEnabled,
			LoraSlot1:      "sql-lora",
			LoraSlot2:      "sql-lora",
		})
		if err == nil {
			t.Fatal("expected error for duplicate slot selection")
		}
	})

	t.Run("valid catalog slots", func(t *testing.T) {
		loraCatalogEnv(t)
		if err := validateLoRAParams("inst", components.VllmCustomSpec{
			ModelURI:       base.ModelURI,
			LoraDeployment: components.LoraDeploymentEnabled,
			LoraSlot1:      "sql-lora",
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func rawJSON(t *testing.T, v any) *runtime.RawExtension {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return &runtime.RawExtension{Raw: b}
}

func llmContext(t *testing.T, replicas *int32, params components.VllmCustomSpec, topo llm.LlmTopologyParameters) *controller.Context {
	t.Helper()
	if params.ModelURI == "" {
		params.ModelURI = "hf://org/model"
	}
	inst := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "llama", Namespace: "ns"},
		Spec: corev1alpha1.InstanceSpec{
			Topology: &corev1alpha1.TopologySpec{
				Type:       common.TopologyLLM,
				Parameters: rawJSON(t, topo),
			},
			Components: map[string]corev1alpha1.ComponentSpec{
				common.ComponentLlmEngine: {
					Type:       common.ComponentTypeVllm,
					Replicas:   replicas,
					Parameters: rawJSON(t, params),
				},
			},
		},
	}
	return controller.NewContext(context.Background(), nil, inst, common.ProviderName)
}

func TestValidateWorkloadScaling(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		s       workloadScaling
		wantErr bool
	}{
		{name: "unset", s: workloadScaling{}},
		{name: "max only", s: workloadScaling{max: ptr.To(int32(4))}},
		{name: "min and max", s: workloadScaling{min: ptr.To(int32(1)), max: ptr.To(int32(4))}},
		{name: "missing max", s: workloadScaling{min: ptr.To(int32(1))}, wantErr: true},
		{name: "min zero", s: workloadScaling{min: ptr.To(int32(0)), max: ptr.To(int32(4))}, wantErr: true},
		{name: "min exceeds max", s: workloadScaling{min: ptr.To(int32(8)), max: ptr.To(int32(4))}, wantErr: true},
		{name: "bad actuator", s: workloadScaling{max: ptr.To(int32(4)), actuator: "knative"}, wantErr: true},
		{name: "hpa", s: workloadScaling{max: ptr.To(int32(4)), actuator: components.ScalingActuatorHPA}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateWorkloadScaling(tc.s, "minReplicas", "maxReplicas", "scalingActuator")
			if tc.wantErr != (err != nil) {
				t.Fatalf("validateWorkloadScaling() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestBuildScalingSpec(t *testing.T) {
	t.Parallel()

	if got := buildScalingSpec(workloadScaling{}); got != nil {
		t.Fatalf("unset = %#v, want nil", got)
	}

	keda := buildScalingSpec(workloadScaling{min: ptr.To(int32(1)), max: ptr.To(int32(4))})
	if keda == nil || keda.MaxReplicas != 4 || keda.MinReplicas == nil || *keda.MinReplicas != 1 {
		t.Fatalf("keda bounds = %#v", keda)
	}
	if keda.WVA == nil || keda.WVA.KEDA == nil || keda.WVA.HPA != nil {
		t.Fatalf("default actuator should be keda: %#v", keda.WVA)
	}

	hpa := buildScalingSpec(workloadScaling{max: ptr.To(int32(2)), actuator: "HPA"})
	if hpa == nil || hpa.WVA == nil || hpa.WVA.HPA == nil || hpa.WVA.KEDA != nil {
		t.Fatalf("hpa actuator = %#v", hpa)
	}
}

func TestBuildLLMInferenceServiceScaling(t *testing.T) {
	t.Parallel()

	t.Run("static replicas", func(t *testing.T) {
		t.Parallel()
		got, err := buildLLMInferenceService(llmContext(t, ptr.To(int32(2)), components.VllmCustomSpec{}, llm.LlmTopologyParameters{}))
		if err != nil {
			t.Fatal(err)
		}
		if got.Spec.Replicas == nil || *got.Spec.Replicas != 2 {
			t.Fatalf("replicas = %v, want 2", got.Spec.Replicas)
		}
		if got.Spec.Scaling != nil {
			t.Fatalf("scaling = %#v, want nil", got.Spec.Scaling)
		}
	})

	t.Run("scaling wins over replicas", func(t *testing.T) {
		t.Parallel()
		got, err := buildLLMInferenceService(llmContext(t, ptr.To(int32(2)), components.VllmCustomSpec{
			MaxReplicas: ptr.To(int32(8)),
			MinReplicas: ptr.To(int32(1)),
		}, llm.LlmTopologyParameters{}))
		if err != nil {
			t.Fatal(err)
		}
		if got.Spec.Replicas != nil {
			t.Fatalf("replicas = %v, want nil when scaling is set", got.Spec.Replicas)
		}
		if got.Spec.Scaling == nil || got.Spec.Scaling.MaxReplicas != 8 || got.Spec.Scaling.WVA == nil || got.Spec.Scaling.WVA.KEDA == nil {
			t.Fatalf("scaling = %#v", got.Spec.Scaling)
		}
	})

	t.Run("prefill scaling", func(t *testing.T) {
		t.Parallel()
		got, err := buildLLMInferenceService(llmContext(t, nil, components.VllmCustomSpec{}, llm.LlmTopologyParameters{
			EnablePrefill:      true,
			PrefillReplicas:    ptr.To(int32(3)),
			PrefillMaxReplicas: ptr.To(int32(6)),
			PrefillMinReplicas: ptr.To(int32(2)),
		}))
		if err != nil {
			t.Fatal(err)
		}
		if got.Spec.Prefill == nil || got.Spec.Prefill.Replicas != nil {
			t.Fatalf("prefill replicas should be omitted: %#v", got.Spec.Prefill)
		}
		if got.Spec.Prefill.Scaling == nil || got.Spec.Prefill.Scaling.MaxReplicas != 6 {
			t.Fatalf("prefill scaling = %#v", got.Spec.Prefill.Scaling)
		}
	})
}

func TestValidateLLMScaling(t *testing.T) {
	t.Parallel()

	t.Run("valid decode scaling", func(t *testing.T) {
		t.Parallel()
		if err := validateLLM(llmContext(t, ptr.To(int32(1)), components.VllmCustomSpec{
			MaxReplicas: ptr.To(int32(4)),
		}, llm.LlmTopologyParameters{})); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("prefill scaling without enablePrefill", func(t *testing.T) {
		t.Parallel()
		err := validateLLM(llmContext(t, nil, components.VllmCustomSpec{}, llm.LlmTopologyParameters{
			PrefillMaxReplicas: ptr.To(int32(4)),
		}))
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("actuator mismatch", func(t *testing.T) {
		t.Parallel()
		err := validateLLM(llmContext(t, nil, components.VllmCustomSpec{
			MaxReplicas:     ptr.To(int32(4)),
			ScalingActuator: components.ScalingActuatorHPA,
		}, llm.LlmTopologyParameters{
			EnablePrefill:          true,
			PrefillMaxReplicas:     ptr.To(int32(4)),
			PrefillScalingActuator: components.ScalingActuatorKEDA,
		}))
		if err == nil {
			t.Fatal("expected actuator mismatch error")
		}
	})
}
