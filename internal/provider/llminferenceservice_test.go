package provider

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kserve/kserve/pkg/constants"

	kservev1alpha2 "github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"knative.dev/pkg/apis"
	duckv1 "knative.dev/pkg/apis/duck/v1"

	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
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
		ObjectMeta: metav1.ObjectMeta{Name: "llama", Namespace: "ns", UID: "instance-uid"},
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

func localModelNamespaceCacheForTest(sourceURI string) *unstructured.Unstructured {
	cache := unstructuredObject(localModelNamespaceCacheGVK)
	cache.SetName("llama")
	cache.SetNamespace("ns")
	cache.Object["spec"] = map[string]any{
		"sourceModelUri": sourceURI,
		"modelSize":      "1Gi",
		"nodeGroups":     []any{"model-workers"},
	}
	return cache
}

func controlLocalModelNamespaceCache(cache *unstructured.Unstructured, inst *corev1alpha1.Instance) {
	cache.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: "core.openeverest.io/v1alpha1",
		Kind:       "Instance",
		Name:       inst.Name,
		UID:        inst.UID,
		Controller: ptr.To(true),
	}})
}

func localModelCacheTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kservev1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

type noMatchLocalModelClient struct {
	client.Client
}

func (c noMatchLocalModelClient) Get(
	ctx context.Context,
	key client.ObjectKey,
	obj client.Object,
	opts ...client.GetOption,
) error {
	if obj.GetObjectKind().GroupVersionKind() == localModelNamespaceCacheGVK {
		return &apimeta.NoKindMatchError{GroupKind: localModelNamespaceCacheGVK.GroupKind()}
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func TestValidateLLMLocalModelCache(t *testing.T) {
	valid := components.LocalModelCacheSpec{
		Enabled:   components.LocalModelCacheEnabled,
		ModelSize: "2Gi",
		NodeGroup: "model-workers",
	}
	cases := []struct {
		name         string
		chartEnabled string
		cache        components.LocalModelCacheSpec
		disableSI    bool
		wantErr      string
	}{
		{name: "chart disabled", cache: valid, wantErr: "requires kserveResources.kserve.localmodel.enabled"},
		{name: "invalid mode", chartEnabled: "true", cache: components.LocalModelCacheSpec{Enabled: "sometimes"}, wantErr: `enabled must be "disabled" or "enabled"`},
		{name: "missing size", chartEnabled: "true", cache: components.LocalModelCacheSpec{Enabled: components.LocalModelCacheEnabled, NodeGroup: "model-workers"}, wantErr: "modelSize is required"},
		{name: "invalid size", chartEnabled: "true", cache: components.LocalModelCacheSpec{Enabled: components.LocalModelCacheEnabled, ModelSize: "large", NodeGroup: "model-workers"}, wantErr: "modelSize"},
		{name: "zero size", chartEnabled: "true", cache: components.LocalModelCacheSpec{Enabled: components.LocalModelCacheEnabled, ModelSize: "0", NodeGroup: "model-workers"}, wantErr: "greater than zero"},
		{name: "missing node group", chartEnabled: "true", cache: components.LocalModelCacheSpec{Enabled: components.LocalModelCacheEnabled, ModelSize: "2Gi"}, wantErr: "nodeGroup is required"},
		{name: "invalid node group", chartEnabled: "true", cache: components.LocalModelCacheSpec{Enabled: components.LocalModelCacheEnabled, ModelSize: "2Gi", NodeGroup: "Not_Valid"}, wantErr: "nodeGroup"},
		{name: "storage initializer disabled", chartEnabled: "true", cache: valid, disableSI: true, wantErr: "disableStorageInitializer must remain false"},
		{name: "valid", chartEnabled: "true", cache: valid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LOCAL_MODEL_ENABLED", tc.chartEnabled)
			params := components.VllmCustomSpec{LocalModelCache: &tc.cache}
			if tc.disableSI {
				params.DisableStorageInitializer = ptr.To(true)
			}
			err := validateLLM(llmContext(t, nil, params, llm.LlmTopologyParameters{}))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateLLM() error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateLLMLocalModelCacheServiceAccountAuthorization(t *testing.T) {
	t.Setenv("LOCAL_MODEL_ENABLED", "true")
	cases := []struct {
		name               string
		serviceAccountName string
		jobNamespace       string
		binding            string
		create             bool
		wantErr            string
	}{
		{name: "anonymous"},
		{name: "authorized", serviceAccountName: "model-puller", binding: "ns", create: true},
		{name: "missing", serviceAccountName: "model-puller", wantErr: "was not found"},
		{name: "unbound", serviceAccountName: "model-puller", create: true, wantErr: "is missing required annotation"},
		{name: "wrong tenant", serviceAccountName: "model-puller", binding: "other-ns", create: true, wantErr: "not Instance namespace"},
		{name: "custom job namespace", serviceAccountName: "model-puller", jobNamespace: "custom-jobs", binding: "ns", create: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(localModelJobNamespaceEnv, tc.jobNamespace)
			scheme := localModelCacheTestScheme(t)
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tc.create {
				namespace := tc.jobNamespace
				if namespace == "" {
					namespace = defaultLocalModelJobNamespace
				}
				builder = builder.WithObjects(&corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:        tc.serviceAccountName,
						Namespace:   namespace,
						Annotations: map[string]string{localModelInstanceNamespaceAnnotation: tc.binding},
					},
				})
			}

			params := components.VllmCustomSpec{
				LocalModelCache: &components.LocalModelCacheSpec{
					Enabled:            components.LocalModelCacheEnabled,
					ModelSize:          "2Gi",
					NodeGroup:          "model-workers",
					ServiceAccountName: tc.serviceAccountName,
				},
			}
			inst := llmContext(t, nil, params, llm.LlmTopologyParameters{}).Instance()
			c := controller.NewContext(context.Background(), builder.Build(), inst, common.ProviderName)
			err := validateLLM(c)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "llmEngine.parameters.localModelCache.serviceAccountName") ||
				!strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateLLM() error = %v, want field-specific error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestSyncLocalModelCache(t *testing.T) {
	t.Setenv("LOCAL_MODEL_ENABLED", "true")
	t.Setenv(localModelJobNamespaceEnv, "")
	scheme := localModelCacheTestScheme(t)

	params := components.VllmCustomSpec{
		ModelURI: "hf://org/model",
		LocalModelCache: &components.LocalModelCacheSpec{
			Enabled:            components.LocalModelCacheEnabled,
			ModelSize:          "2Gi",
			NodeGroup:          "model-workers",
			ServiceAccountName: "model-puller",
		},
	}
	inst := llmContext(t, nil, params, llm.LlmTopologyParameters{}).Instance()
	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "model-puller",
			Namespace:   defaultLocalModelJobNamespace,
			Annotations: map[string]string{localModelInstanceNamespaceAnnotation: inst.Namespace},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(serviceAccount).Build()
	c := controller.NewContext(context.Background(), cl, inst, common.ProviderName)

	ready, err := syncLocalModelCache(c, params)
	if err != nil {
		t.Fatal(err)
	}
	if !ready {
		t.Fatal("new cache should be ready for workload reconciliation")
	}
	cache := unstructuredObject(localModelNamespaceCacheGVK)
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "llama", Namespace: "ns"}, cache); err != nil {
		t.Fatal(err)
	}
	spec, found, err := unstructured.NestedMap(cache.Object, "spec")
	if err != nil || !found {
		t.Fatalf("cache spec not found: found=%v err=%v", found, err)
	}
	if got := spec["sourceModelUri"]; got != "hf://org/model" {
		t.Fatalf("sourceModelUri = %#v", got)
	}
	if got := spec["modelSize"]; got != "2Gi" {
		t.Fatalf("modelSize = %#v", got)
	}
	if got := spec["nodeGroups"]; !slices.Equal(got.([]any), []any{"model-workers"}) {
		t.Fatalf("nodeGroups = %#v", got)
	}
	if got := spec["serviceAccountName"]; got != "model-puller" {
		t.Fatalf("serviceAccountName = %#v", got)
	}
	if len(cache.GetOwnerReferences()) != 1 || cache.GetOwnerReferences()[0].Name != "llama" {
		t.Fatalf("ownerReferences = %#v", cache.GetOwnerReferences())
	}
	llmisvc := &kservev1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "llama", Namespace: "ns"}}
	if err := cl.Create(context.Background(), llmisvc); err != nil {
		t.Fatal(err)
	}

	disabled := params
	disabled.LocalModelCache = nil
	ready, err = syncLocalModelCache(c, disabled)
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		t.Fatal("workload removal should defer cache reconciliation")
	}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "llama", Namespace: "ns"}, cache); err != nil {
		t.Fatalf("cache should remain until the workload is gone: %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "llama", Namespace: "ns"}, llmisvc); !apierrors.IsNotFound(err) {
		t.Fatalf("workload after disabling cache: %v", err)
	}

	ready, err = syncLocalModelCache(c, disabled)
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		t.Fatal("cache removal should defer workload reconciliation")
	}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "llama", Namespace: "ns"}, cache); !apierrors.IsNotFound(err) {
		t.Fatalf("cache after disable: %v", err)
	}
	ready, err = syncLocalModelCache(c, disabled)
	if err != nil || !ready {
		t.Fatalf("disabled cache after cleanup: ready=%v err=%v", ready, err)
	}
}

func TestSyncLocalModelCacheRejectsUnauthorizedServiceAccountBeforeMutation(t *testing.T) {
	t.Setenv("LOCAL_MODEL_ENABLED", "true")
	t.Setenv(localModelJobNamespaceEnv, "")
	scheme := localModelCacheTestScheme(t)
	params := components.VllmCustomSpec{
		ModelURI: "hf://org/new",
		LocalModelCache: &components.LocalModelCacheSpec{
			Enabled:            components.LocalModelCacheEnabled,
			ModelSize:          "2Gi",
			NodeGroup:          "model-workers",
			ServiceAccountName: "missing-puller",
		},
	}
	inst := llmContext(t, nil, params, llm.LlmTopologyParameters{}).Instance()
	cache := localModelNamespaceCacheForTest("hf://org/old")
	controlLocalModelNamespaceCache(cache, inst)
	llmisvc := &kservev1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Name:        inst.Name,
			Namespace:   inst.Namespace,
			Annotations: map[string]string{"test": "unchanged"},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cache, llmisvc).Build()
	c := controller.NewContext(context.Background(), cl, inst, common.ProviderName)

	ready, err := syncLocalModelCache(c, params)
	if err == nil || !strings.Contains(err.Error(), "llmEngine.parameters.localModelCache.serviceAccountName") ||
		!strings.Contains(err.Error(), "was not found") {
		t.Fatalf("syncLocalModelCache() error = %v, want missing ServiceAccount error", err)
	}
	if ready {
		t.Fatal("unauthorized cache request should not be ready")
	}

	gotCache := unstructuredObject(localModelNamespaceCacheGVK)
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(cache), gotCache); err != nil {
		t.Fatalf("owned cache was removed: %v", err)
	}
	sourceURI, _, err := unstructured.NestedString(gotCache.Object, "spec", "sourceModelUri")
	if err != nil || sourceURI != "hf://org/old" {
		t.Fatalf("owned cache changed: sourceModelUri=%q err=%v", sourceURI, err)
	}
	gotLLM := &kservev1alpha2.LLMInferenceService{}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(llmisvc), gotLLM); err != nil {
		t.Fatalf("LLMInferenceService was removed: %v", err)
	}
	if gotLLM.Annotations["test"] != "unchanged" {
		t.Fatalf("LLMInferenceService changed: annotations=%v", gotLLM.Annotations)
	}
}

func TestSyncLocalModelCacheReplacesImmutableURI(t *testing.T) {
	t.Setenv("LOCAL_MODEL_ENABLED", "true")
	scheme := runtime.NewScheme()
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kservev1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	cache := unstructuredObject(localModelNamespaceCacheGVK)
	cache.SetName("llama")
	cache.SetNamespace("ns")
	cache.Object["spec"] = map[string]any{
		"sourceModelUri": "hf://org/old",
		"modelSize":      "1Gi",
		"nodeGroups":     []any{"model-workers"},
	}
	params := components.VllmCustomSpec{
		ModelURI: "hf://org/new",
		LocalModelCache: &components.LocalModelCacheSpec{
			Enabled:   components.LocalModelCacheEnabled,
			ModelSize: "2Gi",
			NodeGroup: "model-workers",
		},
	}
	inst := llmContext(t, nil, params, llm.LlmTopologyParameters{}).Instance()
	controlLocalModelNamespaceCache(cache, inst)
	llmisvc := &kservev1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "llama", Namespace: "ns"}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cache, llmisvc).Build()
	c := controller.NewContext(context.Background(), cl, inst, common.ProviderName)

	ready, err := syncLocalModelCache(c, params)
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		t.Fatal("immutable URI replacement should wait for the next reconcile")
	}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "llama", Namespace: "ns"}, cache); err != nil {
		t.Fatalf("cache should remain until the workload is gone: %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "llama", Namespace: "ns"}, llmisvc); !apierrors.IsNotFound(err) {
		t.Fatalf("workload after URI change: %v", err)
	}
	ready, err = syncLocalModelCache(c, params)
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		t.Fatal("cache removal should defer workload reconciliation")
	}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "llama", Namespace: "ns"}, cache); !apierrors.IsNotFound(err) {
		t.Fatalf("cache after URI change: %v", err)
	}
	ready, err = syncLocalModelCache(c, params)
	if err != nil || !ready {
		t.Fatalf("replacement cache: ready=%v err=%v", ready, err)
	}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "llama", Namespace: "ns"}, cache); err != nil {
		t.Fatal(err)
	}
	sourceURI, _, err := unstructured.NestedString(cache.Object, "spec", "sourceModelUri")
	if err != nil || sourceURI != "hf://org/new" {
		t.Fatalf("replacement sourceModelUri=%q err=%v", sourceURI, err)
	}
}

func TestSyncLocalModelCacheChartDisabledCleanup(t *testing.T) {
	t.Setenv("LOCAL_MODEL_ENABLED", "false")

	t.Run("stages workload before cache", func(t *testing.T) {
		scheme := localModelCacheTestScheme(t)
		params := components.VllmCustomSpec{ModelURI: "hf://org/model"}
		inst := llmContext(t, nil, params, llm.LlmTopologyParameters{}).Instance()
		cache := localModelNamespaceCacheForTest(params.ModelURI)
		controlLocalModelNamespaceCache(cache, inst)
		llmisvc := &kservev1alpha2.LLMInferenceService{
			ObjectMeta: metav1.ObjectMeta{Name: inst.Name, Namespace: inst.Namespace},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cache, llmisvc).Build()
		c := controller.NewContext(context.Background(), cl, inst, common.ProviderName)

		ready, err := syncLocalModelCache(c, params)
		if err != nil {
			t.Fatal(err)
		}
		if ready {
			t.Fatal("workload deletion should defer cache cleanup")
		}
		if err := cl.Get(context.Background(), client.ObjectKeyFromObject(cache), cache); err != nil {
			t.Fatalf("cache should remain during workload deletion: %v", err)
		}
		if err := cl.Get(context.Background(), client.ObjectKeyFromObject(llmisvc), llmisvc); !apierrors.IsNotFound(err) {
			t.Fatalf("workload after first cleanup reconcile: %v", err)
		}

		ready, err = syncLocalModelCache(c, params)
		if err != nil {
			t.Fatal(err)
		}
		if ready {
			t.Fatal("cache deletion should defer workload reconciliation")
		}
		if err := cl.Get(context.Background(), client.ObjectKeyFromObject(cache), cache); !apierrors.IsNotFound(err) {
			t.Fatalf("cache after second cleanup reconcile: %v", err)
		}

		ready, err = syncLocalModelCache(c, params)
		if err != nil || !ready {
			t.Fatalf("cleanup completion: ready=%v err=%v", ready, err)
		}
	})

	t.Run("absent CRD is harmless", func(t *testing.T) {
		scheme := localModelCacheTestScheme(t)
		params := components.VllmCustomSpec{ModelURI: "hf://org/model"}
		inst := llmContext(t, nil, params, llm.LlmTopologyParameters{}).Instance()
		cl := noMatchLocalModelClient{
			Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		}
		c := controller.NewContext(context.Background(), cl, inst, common.ProviderName)

		ready, err := syncLocalModelCache(c, params)
		if err != nil || !ready {
			t.Fatalf("cleanup without CRD: ready=%v err=%v", ready, err)
		}
	})

	t.Run("enable request still fails", func(t *testing.T) {
		scheme := localModelCacheTestScheme(t)
		params := components.VllmCustomSpec{
			ModelURI: "hf://org/model",
			LocalModelCache: &components.LocalModelCacheSpec{
				Enabled:   components.LocalModelCacheEnabled,
				ModelSize: "2Gi",
				NodeGroup: "model-workers",
			},
		}
		inst := llmContext(t, nil, params, llm.LlmTopologyParameters{}).Instance()
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		c := controller.NewContext(context.Background(), cl, inst, common.ProviderName)

		ready, err := syncLocalModelCache(c, params)
		if err == nil || !strings.Contains(err.Error(), "local model cache requires") {
			t.Fatalf("syncLocalModelCache() error = %v, want chart support error", err)
		}
		if ready {
			t.Fatal("unsupported cache request should not be ready")
		}
	})
}

func TestProviderCleanupStagesLocalModelCacheAfterWorkload(t *testing.T) {
	t.Setenv("LOCAL_MODEL_ENABLED", "false")
	scheme := localModelCacheTestScheme(t)
	params := components.VllmCustomSpec{ModelURI: "hf://org/model"}
	inst := llmContext(t, nil, params, llm.LlmTopologyParameters{}).Instance()
	cache := localModelNamespaceCacheForTest(params.ModelURI)
	controlLocalModelNamespaceCache(cache, inst)
	llmisvc := &kservev1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: inst.Name, Namespace: inst.Namespace},
	}
	config := &kservev1alpha2.LLMInferenceServiceConfig{
		ObjectMeta: metav1.ObjectMeta{Name: instanceConfigName(inst.Name), Namespace: inst.Namespace},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: aiGatewayKeySecretName(inst.Name), Namespace: inst.Namespace},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cache, llmisvc, config, secret).Build()
	c := controller.NewContext(context.Background(), cl, inst, common.ProviderName)

	if err := (&Provider{}).Cleanup(c); !controller.IsWaitError(err) {
		t.Fatalf("first Cleanup error = %v, want wait error", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(llmisvc), llmisvc); !apierrors.IsNotFound(err) {
		t.Fatalf("workload after first Cleanup: %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(cache), cache); err != nil {
		t.Fatalf("cache should remain during workload deletion: %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(config), config); !apierrors.IsNotFound(err) {
		t.Fatalf("LLM config after first Cleanup: %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(secret), secret); !apierrors.IsNotFound(err) {
		t.Fatalf("API key secret after first Cleanup: %v", err)
	}

	if err := (&Provider{}).Cleanup(c); err != nil {
		t.Fatal(err)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(cache), cache); !apierrors.IsNotFound(err) {
		t.Fatalf("cache after second Cleanup: %v", err)
	}
}

func TestLocalModelCacheOwnershipProtection(t *testing.T) {
	t.Setenv("LOCAL_MODEL_ENABLED", "true")
	operations := []struct {
		name         string
		cacheURI     string
		desiredURI   string
		withWorkload bool
		cleanup      bool
	}{
		{name: "apply", cacheURI: "hf://org/model", desiredURI: "hf://org/model"},
		{name: "replace", cacheURI: "hf://org/old", desiredURI: "hf://org/new", withWorkload: true},
		{name: "delete", cacheURI: "hf://org/model", desiredURI: "hf://org/model", cleanup: true},
	}
	owners := []struct {
		name    string
		foreign bool
		wantErr string
	}{
		{name: "unowned", wantErr: "is not controller-owned"},
		{name: "foreign", foreign: true, wantErr: "not Instance"},
	}

	for _, operation := range operations {
		for _, owner := range owners {
			t.Run(operation.name+"/"+owner.name, func(t *testing.T) {
				scheme := localModelCacheTestScheme(t)
				params := components.VllmCustomSpec{
					ModelURI: operation.desiredURI,
					LocalModelCache: &components.LocalModelCacheSpec{
						Enabled:   components.LocalModelCacheEnabled,
						ModelSize: "2Gi",
						NodeGroup: "model-workers",
					},
				}
				inst := llmContext(t, nil, params, llm.LlmTopologyParameters{}).Instance()
				cache := localModelNamespaceCacheForTest(operation.cacheURI)
				if owner.foreign {
					cache.SetOwnerReferences([]metav1.OwnerReference{{
						APIVersion: "core.openeverest.io/v1alpha1",
						Kind:       "Instance",
						Name:       inst.Name,
						UID:        "other-instance-uid",
						Controller: ptr.To(true),
					}})
				}
				objects := []client.Object{cache}
				llmisvc := &kservev1alpha2.LLMInferenceService{
					ObjectMeta: metav1.ObjectMeta{Name: inst.Name, Namespace: inst.Namespace},
				}
				if operation.withWorkload {
					objects = append(objects, llmisvc)
				}
				cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
				c := controller.NewContext(context.Background(), cl, inst, common.ProviderName)

				var err error
				if operation.cleanup {
					err = (&Provider{}).Cleanup(c)
				} else {
					_, err = syncLocalModelCache(c, params)
				}
				if err == nil || !strings.Contains(err.Error(), owner.wantErr) {
					t.Fatalf("operation error = %v, want containing %q", err, owner.wantErr)
				}

				stored := unstructuredObject(localModelNamespaceCacheGVK)
				if err := cl.Get(context.Background(), client.ObjectKeyFromObject(cache), stored); err != nil {
					t.Fatalf("foreign cache should remain: %v", err)
				}
				sourceURI, _, err := unstructured.NestedString(stored.Object, "spec", "sourceModelUri")
				if err != nil || sourceURI != operation.cacheURI {
					t.Fatalf("sourceModelUri=%q err=%v, want %q", sourceURI, err, operation.cacheURI)
				}
				modelSize, _, err := unstructured.NestedString(stored.Object, "spec", "modelSize")
				if err != nil || modelSize != "1Gi" {
					t.Fatalf("modelSize=%q err=%v, want unchanged", modelSize, err)
				}
				if owner.foreign {
					refs := stored.GetOwnerReferences()
					if len(refs) != 1 || refs[0].UID != "other-instance-uid" {
						t.Fatalf("ownerReferences=%v, want foreign owner unchanged", refs)
					}
				} else if len(stored.GetOwnerReferences()) != 0 {
					t.Fatalf("ownerReferences=%v, want unowned cache unchanged", stored.GetOwnerReferences())
				}
				if operation.withWorkload {
					if err := cl.Get(context.Background(), client.ObjectKeyFromObject(llmisvc), llmisvc); err != nil {
						t.Fatalf("workload should remain after rejected replacement: %v", err)
					}
				}
			})
		}
	}
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

func TestBuildLLMInferenceServicePodLabels(t *testing.T) {
	t.Parallel()
	c := llmContext(t, nil, components.VllmCustomSpec{}, llm.LlmTopologyParameters{EnablePrefill: true})
	got, err := buildLLMInferenceService(c)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		controller.ProviderLabel:  common.ProviderName,
		controller.InstanceLabel:  "llama",
		controller.ComponentLabel: common.ComponentLlmEngine,
	}
	if !maps.Equal(got.Spec.Labels, want) {
		t.Fatalf("decode pod labels = %v, want %v", got.Spec.Labels, want)
	}
	if got.Spec.Prefill == nil || !maps.Equal(got.Spec.Prefill.Labels, want) {
		t.Fatalf("prefill pod labels = %#v, want %v", got.Spec.Prefill, want)
	}
	if !slices.Equal(c.LabelledComponents(), []string{common.ComponentLlmEngine}) {
		t.Fatalf("labelled components = %v, want [%s]", c.LabelledComponents(), common.ComponentLlmEngine)
	}
}

func TestValidateLLMScaling(t *testing.T) {
	t.Parallel()

	t.Run("valid decode scaling", func(t *testing.T) {
		t.Parallel()
		if err := validateLLM(llmContext(t, nil, components.VllmCustomSpec{
			MaxReplicas: ptr.To(int32(4)),
		}, llm.LlmTopologyParameters{})); err != nil {
			t.Fatal(err)
		}
	})

	// The UI always sends a replica count; autoscaling ignores it.
	t.Run("decode replicas with scaling are accepted", func(t *testing.T) {
		t.Parallel()
		if err := validateLLM(llmContext(t, ptr.To(int32(2)), components.VllmCustomSpec{
			MaxReplicas: ptr.To(int32(4)),
		}, llm.LlmTopologyParameters{})); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("prefill replicas with scaling are accepted", func(t *testing.T) {
		t.Parallel()
		if err := validateLLM(llmContext(t, nil, components.VllmCustomSpec{}, llm.LlmTopologyParameters{
			EnablePrefill:      true,
			PrefillReplicas:    ptr.To(int32(3)),
			PrefillMaxReplicas: ptr.To(int32(6)),
		})); err != nil {
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

func llmContextWithResources(t *testing.T, replicas *int32, res *corev1.ResourceRequirements, params components.VllmCustomSpec, topo llm.LlmTopologyParameters) *controller.Context {
	t.Helper()
	c := llmContext(t, replicas, params, topo)
	comp := c.Instance().Spec.Components[common.ComponentLlmEngine]
	comp.Resources = res
	c.Instance().Spec.Components[common.ComponentLlmEngine] = comp
	return c
}

func gpuResources(n string) *corev1.ResourceRequirements {
	return &corev1.ResourceRequirements{
		Limits: corev1.ResourceList{nvidiaGPUResource: resource.MustParse(n)},
	}
}

func TestValidateWorkerGroup(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		count        *int32
		pp           *int32
		resourcesSet bool
		wantErr      bool
	}{
		{name: "unset"},
		{name: "valid", count: ptr.To(int32(1)), pp: ptr.To(int32(2))},
		{name: "resources without count", resourcesSet: true, wantErr: true},
		{name: "missing pp", count: ptr.To(int32(1)), wantErr: true},
		{name: "mismatch", count: ptr.To(int32(1)), pp: ptr.To(int32(4)), wantErr: true},
		{name: "zero count", count: ptr.To(int32(0)), pp: ptr.To(int32(1)), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateWorkerGroup(tc.count, tc.pp, tc.resourcesSet, "workerCount", "pipelineParallelSize", "workerResources")
			if tc.wantErr != (err != nil) {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateTensorGPU(t *testing.T) {
	t.Parallel()
	if err := validateTensorGPU(nil, ptr.To(int32(8)), false, "res"); err != nil {
		t.Fatal(err)
	}
	if err := validateTensorGPU(gpuResources("8"), ptr.To(int32(8)), false, "res"); err != nil {
		t.Fatal(err)
	}
	if err := validateTensorGPU(gpuResources("1"), ptr.To(int32(8)), false, "res"); err == nil {
		t.Fatal("expected GPU < TP error")
	}
	if err := validateTensorGPU(gpuResources("1"), ptr.To(int32(8)), true, "res"); err != nil {
		t.Fatal(err)
	}
	// Limits govern scheduling; requests-only GPU is not compared to tensorParallelSize.
	if err := validateTensorGPU(&corev1.ResourceRequirements{
		Requests: corev1.ResourceList{nvidiaGPUResource: resource.MustParse("1")},
	}, ptr.To(int32(8)), false, "res"); err != nil {
		t.Fatal(err)
	}
}

func TestValidateLLMWorkers(t *testing.T) {
	t.Parallel()

	t.Run("valid decode workers", func(t *testing.T) {
		t.Parallel()
		if err := validateLLM(llmContext(t, nil, components.VllmCustomSpec{
			WorkerCount:          ptr.To(int32(1)),
			PipelineParallelSize: ptr.To(int32(2)),
		}, llm.LlmTopologyParameters{})); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("pp alone ok", func(t *testing.T) {
		t.Parallel()
		if err := validateLLM(llmContext(t, nil, components.VllmCustomSpec{
			PipelineParallelSize: ptr.To(int32(2)),
		}, llm.LlmTopologyParameters{})); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("gpu vs tp", func(t *testing.T) {
		t.Parallel()
		err := validateLLM(llmContextWithResources(t, nil, gpuResources("1"), components.VllmCustomSpec{
			TensorParallelSize: ptr.To(int32(8)),
		}, llm.LlmTopologyParameters{}))
		if err == nil {
			t.Fatal("expected GPU/TP error")
		}
	})

	t.Run("prefill workers need enablePrefill", func(t *testing.T) {
		t.Parallel()
		err := validateLLM(llmContext(t, nil, components.VllmCustomSpec{}, llm.LlmTopologyParameters{
			PrefillWorkerCount:          ptr.To(int32(1)),
			PrefillPipelineParallelSize: ptr.To(int32(2)),
		}))
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("prefill PP-only without enablePrefill is ignored", func(t *testing.T) {
		t.Parallel()
		if err := validateLLM(llmContext(t, nil, components.VllmCustomSpec{}, llm.LlmTopologyParameters{
			PrefillPipelineParallelSize: ptr.To(int32(2)),
		})); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("cpu profile skips GPU vs TP", func(t *testing.T) {
		t.Parallel()
		if err := validateLLM(llmContextWithResources(t, nil, gpuResources("1"), components.VllmCustomSpec{
			ComputeProfile:     "cpu",
			TensorParallelSize: ptr.To(int32(8)),
		}, llm.LlmTopologyParameters{})); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("UI worker CPU keeps head GPU for TP check", func(t *testing.T) {
		t.Parallel()
		err := validateLLM(llmContextWithResources(t, nil, gpuResources("1"), components.VllmCustomSpec{
			TensorParallelSize:   ptr.To(int32(8)),
			WorkerCount:          ptr.To(int32(1)),
			PipelineParallelSize: ptr.To(int32(2)),
			WorkerResources: &corev1.ResourceRequirements{
				Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
			},
		}, llm.LlmTopologyParameters{}))
		if err == nil {
			t.Fatal("expected GPU/TP error on overlaid worker resources")
		}
	})
}

func TestBuildLLMInferenceServiceWorkers(t *testing.T) {
	t.Parallel()

	t.Run("pp alone does not set worker", func(t *testing.T) {
		t.Parallel()
		got, err := buildLLMInferenceService(llmContext(t, nil, components.VllmCustomSpec{
			PipelineParallelSize: ptr.To(int32(2)),
		}, llm.LlmTopologyParameters{}))
		if err != nil {
			t.Fatal(err)
		}
		if got.Spec.Worker != nil {
			t.Fatalf("worker = %#v, want nil", got.Spec.Worker)
		}
	})

	t.Run("workerCount sets spec.worker and copies head resources", func(t *testing.T) {
		t.Parallel()
		got, err := buildLLMInferenceService(llmContextWithResources(t, nil, gpuResources("2"), components.VllmCustomSpec{
			WorkerCount:          ptr.To(int32(1)),
			PipelineParallelSize: ptr.To(int32(2)),
		}, llm.LlmTopologyParameters{}))
		if err != nil {
			t.Fatal(err)
		}
		if got.Spec.Worker == nil || len(got.Spec.Worker.Containers) != 1 {
			t.Fatalf("worker = %#v", got.Spec.Worker)
		}
		q := got.Spec.Worker.Containers[0].Resources.Limits[nvidiaGPUResource]
		if q.Value() != 2 {
			t.Fatalf("copied GPU = %s", q.String())
		}
	})

	t.Run("prefill workers", func(t *testing.T) {
		t.Parallel()
		got, err := buildLLMInferenceService(llmContextWithResources(t, nil, gpuResources("2"), components.VllmCustomSpec{
			TensorParallelSize: ptr.To(int32(2)),
		}, llm.LlmTopologyParameters{
			EnablePrefill:               true,
			PrefillWorkerCount:          ptr.To(int32(1)),
			PrefillPipelineParallelSize: ptr.To(int32(2)),
		}))
		if err != nil {
			t.Fatal(err)
		}
		if got.Spec.Prefill == nil || got.Spec.Prefill.Worker == nil {
			t.Fatalf("prefill.worker = %#v", got.Spec.Prefill)
		}
		if got.Spec.Prefill.Parallelism == nil || got.Spec.Prefill.Parallelism.Pipeline == nil || *got.Spec.Prefill.Parallelism.Pipeline != 2 {
			t.Fatalf("prefill parallelism = %#v", got.Spec.Prefill.Parallelism)
		}
		if got.Spec.Prefill.Parallelism.Tensor != nil {
			t.Fatal("prefill must not copy decode tensorParallelSize")
		}
		if got.Spec.Prefill.Template == nil || len(got.Spec.Prefill.Template.Containers) != 1 {
			t.Fatalf("prefill template = %#v", got.Spec.Prefill.Template)
		}
	})

	t.Run("worker overlay keeps head GPU", func(t *testing.T) {
		t.Parallel()
		got, err := buildLLMInferenceService(llmContextWithResources(t, nil, gpuResources("2"), components.VllmCustomSpec{
			WorkerCount:          ptr.To(int32(1)),
			PipelineParallelSize: ptr.To(int32(2)),
			WorkerResources: &corev1.ResourceRequirements{
				Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
			},
		}, llm.LlmTopologyParameters{}))
		if err != nil {
			t.Fatal(err)
		}
		res := got.Spec.Worker.Containers[0].Resources
		gpu := res.Limits[nvidiaGPUResource]
		cpu := res.Limits[corev1.ResourceCPU]
		if gpu.Value() != 2 {
			t.Fatalf("GPU = %s, want 2 from head", gpu.String())
		}
		if cpu.Cmp(resource.MustParse("4")) != 0 {
			t.Fatalf("CPU = %s", cpu.String())
		}
	})
}

func TestBuildLLMInferenceServiceGPULibraryPath(t *testing.T) {
	t.Parallel()

	hasLibraryPath := func(c corev1.Container) bool {
		for _, e := range c.Env {
			if e.Name == "LIBRARY_PATH" && e.Value == "/usr/lib/x86_64-linux-gnu:/usr/local/cuda/lib64" {
				return true
			}
		}
		return false
	}

	t.Run("gpu decode sets LIBRARY_PATH", func(t *testing.T) {
		t.Parallel()
		got, err := buildLLMInferenceService(llmContext(t, nil, components.VllmCustomSpec{}, llm.LlmTopologyParameters{}))
		if err != nil {
			t.Fatal(err)
		}
		if got.Spec.Template == nil || len(got.Spec.Template.Containers) != 1 {
			t.Fatalf("template = %#v", got.Spec.Template)
		}
		if !hasLibraryPath(got.Spec.Template.Containers[0]) {
			t.Fatalf("gpu main missing LIBRARY_PATH: %#v", got.Spec.Template.Containers[0].Env)
		}
	})

	t.Run("cpu profile omits LIBRARY_PATH", func(t *testing.T) {
		t.Parallel()
		got, err := buildLLMInferenceService(llmContext(t, nil, components.VllmCustomSpec{
			ComputeProfile: "cpu",
		}, llm.LlmTopologyParameters{}))
		if err != nil {
			t.Fatal(err)
		}
		if got.Spec.Template != nil {
			for _, c := range got.Spec.Template.Containers {
				if hasLibraryPath(c) {
					t.Fatalf("cpu profile should not set LIBRARY_PATH: %#v", c.Env)
				}
			}
		}
	})

	t.Run("gpu worker and prefill set LIBRARY_PATH", func(t *testing.T) {
		t.Parallel()
		got, err := buildLLMInferenceService(llmContextWithResources(t, nil, gpuResources("2"), components.VllmCustomSpec{
			WorkerCount:          ptr.To(int32(1)),
			PipelineParallelSize: ptr.To(int32(2)),
		}, llm.LlmTopologyParameters{
			EnablePrefill:               true,
			PrefillWorkerCount:          ptr.To(int32(1)),
			PrefillPipelineParallelSize: ptr.To(int32(2)),
		}))
		if err != nil {
			t.Fatal(err)
		}
		if got.Spec.Worker == nil || !hasLibraryPath(got.Spec.Worker.Containers[0]) {
			t.Fatalf("worker missing LIBRARY_PATH: %#v", got.Spec.Worker)
		}
		if got.Spec.Prefill == nil || got.Spec.Prefill.Template == nil || !hasLibraryPath(got.Spec.Prefill.Template.Containers[0]) {
			t.Fatalf("prefill main missing LIBRARY_PATH: %#v", got.Spec.Prefill)
		}
		if got.Spec.Prefill.Worker == nil || !hasLibraryPath(got.Spec.Prefill.Worker.Containers[0]) {
			t.Fatalf("prefill worker missing LIBRARY_PATH: %#v", got.Spec.Prefill.Worker)
		}
	})
}

func TestBuildLLMInferenceServiceScheduling(t *testing.T) {
	t.Parallel()
	gpuTaint := corev1.Toleration{Key: "nvidia.com/gpu", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}
	userToleration := corev1.Toleration{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "llm", Effect: corev1.TaintEffectNoSchedule}
	nodeSelector := map[string]string{"pool": "gpu"}
	withPolicy := func(c *controller.Context, policy *commonv1alpha1.SchedulingPolicy) *controller.Context {
		comp := c.Instance().Spec.Components[common.ComponentLlmEngine]
		comp.SchedulingPolicy = policy
		c.Instance().Spec.Components[common.ComponentLlmEngine] = comp
		return c
	}
	allPods := func(got *kservev1alpha2.LLMInferenceService) map[string]*corev1.PodSpec {
		pods := map[string]*corev1.PodSpec{"template": got.Spec.Template, "worker": got.Spec.Worker}
		if got.Spec.Prefill != nil {
			pods["prefill"] = got.Spec.Prefill.Template
			pods["prefill worker"] = got.Spec.Prefill.Worker
		}
		return pods
	}
	multiNode := components.VllmCustomSpec{WorkerCount: ptr.To(int32(1)), PipelineParallelSize: ptr.To(int32(2))}
	prefillWorkers := llm.LlmTopologyParameters{EnablePrefill: true, PrefillWorkerCount: ptr.To(int32(1)), PrefillPipelineParallelSize: ptr.To(int32(2))}

	t.Run("GPU pods tolerate the GPU taint", func(t *testing.T) {
		t.Parallel()
		got, err := buildLLMInferenceService(llmContext(t, nil, multiNode, prefillWorkers))
		if err != nil {
			t.Fatal(err)
		}
		for name, pod := range allPods(got) {
			if pod == nil || !slices.Equal(pod.Tolerations, []corev1.Toleration{gpuTaint}) {
				t.Fatalf("%s tolerations = %#v, want [%v]", name, pod, gpuTaint)
			}
		}
	})

	t.Run("CPU profile adds no GPU toleration", func(t *testing.T) {
		t.Parallel()
		got, err := buildLLMInferenceService(llmContext(t, nil, components.VllmCustomSpec{ComputeProfile: "cpu"}, llm.LlmTopologyParameters{}))
		if err != nil {
			t.Fatal(err)
		}
		if got.Spec.Template != nil && len(got.Spec.Template.Tolerations) > 0 {
			t.Fatalf("tolerations = %#v, want none", got.Spec.Template.Tolerations)
		}
	})

	t.Run("schedulingPolicy reaches every pod and keeps user tolerations", func(t *testing.T) {
		t.Parallel()
		affinity := &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "gpu", Operator: corev1.NodeSelectorOpExists}},
			}}},
		}}
		c := withPolicy(llmContext(t, nil, multiNode, prefillWorkers), &commonv1alpha1.SchedulingPolicy{
			SchedulerName: "custom",
			NodeSelector:  nodeSelector,
			Affinity:      affinity,
			Tolerations:   []corev1.Toleration{userToleration},
		})
		got, err := buildLLMInferenceService(c)
		if err != nil {
			t.Fatal(err)
		}
		for name, pod := range allPods(got) {
			if pod == nil {
				t.Fatalf("%s pod spec is nil", name)
			}
			if pod.SchedulerName != "custom" || !maps.Equal(pod.NodeSelector, nodeSelector) || pod.Affinity != affinity {
				t.Fatalf("%s placement = scheduler %q, nodeSelector %v, affinity %v", name, pod.SchedulerName, pod.NodeSelector, pod.Affinity)
			}
			if !slices.Equal(pod.Tolerations, []corev1.Toleration{userToleration, gpuTaint}) {
				t.Fatalf("%s tolerations = %#v", name, pod.Tolerations)
			}
		}
	})

	t.Run("CPU profile honours schedulingPolicy", func(t *testing.T) {
		t.Parallel()
		c := withPolicy(llmContext(t, nil, components.VllmCustomSpec{ComputeProfile: "cpu"}, llm.LlmTopologyParameters{}),
			&commonv1alpha1.SchedulingPolicy{NodeSelector: nodeSelector})
		got, err := buildLLMInferenceService(c)
		if err != nil {
			t.Fatal(err)
		}
		if got.Spec.Template == nil || !maps.Equal(got.Spec.Template.NodeSelector, nodeSelector) || len(got.Spec.Template.Tolerations) > 0 {
			t.Fatalf("template = %#v", got.Spec.Template)
		}
	})
}

func TestBuildLLMInferenceServiceGPUCount(t *testing.T) {
	t.Parallel()

	gpuLimit := func(c corev1.Container) int64 {
		q := c.Resources.Limits[nvidiaGPUResource]
		return q.Value()
	}

	t.Run("gpu profile defaults to 1 gpu", func(t *testing.T) {
		t.Parallel()
		got, err := buildLLMInferenceService(llmContext(t, nil, components.VllmCustomSpec{}, llm.LlmTopologyParameters{}))
		if err != nil {
			t.Fatal(err)
		}
		if got.Spec.Template == nil || gpuLimit(got.Spec.Template.Containers[0]) != 1 {
			t.Fatalf("main gpu = %#v, want 1", got.Spec.Template)
		}
	})

	t.Run("gpuCount sets head, worker and prefill", func(t *testing.T) {
		t.Parallel()
		got, err := buildLLMInferenceService(llmContext(t, nil, components.VllmCustomSpec{
			GpuCount:             ptr.To(int32(4)),
			WorkerCount:          ptr.To(int32(1)),
			PipelineParallelSize: ptr.To(int32(2)),
		}, llm.LlmTopologyParameters{
			EnablePrefill:               true,
			PrefillWorkerCount:          ptr.To(int32(1)),
			PrefillPipelineParallelSize: ptr.To(int32(2)),
		}))
		if err != nil {
			t.Fatal(err)
		}
		if gpuLimit(got.Spec.Template.Containers[0]) != 4 {
			t.Fatalf("head gpu = %d, want 4", gpuLimit(got.Spec.Template.Containers[0]))
		}
		if got.Spec.Worker == nil || gpuLimit(got.Spec.Worker.Containers[0]) != 4 {
			t.Fatalf("worker gpu = %#v, want 4", got.Spec.Worker)
		}
		if got.Spec.Prefill == nil || got.Spec.Prefill.Template == nil || gpuLimit(got.Spec.Prefill.Template.Containers[0]) != 4 {
			t.Fatalf("prefill gpu = %#v, want 4", got.Spec.Prefill)
		}
		if got.Spec.Prefill.Worker == nil || gpuLimit(got.Spec.Prefill.Worker.Containers[0]) != 4 {
			t.Fatalf("prefill worker gpu = %#v, want 4", got.Spec.Prefill.Worker)
		}
	})

	t.Run("cpu profile sets no gpu", func(t *testing.T) {
		t.Parallel()
		got, err := buildLLMInferenceService(llmContext(t, nil, components.VllmCustomSpec{
			ComputeProfile: "cpu",
		}, llm.LlmTopologyParameters{}))
		if err != nil {
			t.Fatal(err)
		}
		if got.Spec.Template != nil {
			if _, ok := got.Spec.Template.Containers[0].Resources.Limits[nvidiaGPUResource]; ok {
				t.Fatal("cpu profile must not request a GPU")
			}
		}
	})
}

func TestValidateLLMGpuCount(t *testing.T) {
	t.Parallel()

	t.Run("gpuCount on cpu profile errors", func(t *testing.T) {
		t.Parallel()
		err := validateLLM(llmContext(t, nil, components.VllmCustomSpec{
			ComputeProfile: "cpu",
			GpuCount:       ptr.To(int32(1)),
		}, llm.LlmTopologyParameters{}))
		if err == nil {
			t.Fatal("expected error for gpuCount on cpu profile")
		}
	})

	t.Run("gpuCount below tensorParallelSize errors", func(t *testing.T) {
		t.Parallel()
		err := validateLLM(llmContext(t, nil, components.VllmCustomSpec{
			GpuCount:           ptr.To(int32(1)),
			TensorParallelSize: ptr.To(int32(2)),
		}, llm.LlmTopologyParameters{}))
		if err == nil {
			t.Fatal("expected GPU/TP error")
		}
	})

	t.Run("gpuCount meets tensorParallelSize", func(t *testing.T) {
		t.Parallel()
		if err := validateLLM(llmContext(t, nil, components.VllmCustomSpec{
			GpuCount:           ptr.To(int32(2)),
			TensorParallelSize: ptr.To(int32(2)),
		}, llm.LlmTopologyParameters{})); err != nil {
			t.Fatal(err)
		}
	})
}

func TestApplyStorageInitializerResources(t *testing.T) {
	t.Parallel()

	res := &corev1.ResourceRequirements{
		Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("4Gi")},
	}
	storageInit := func(pod *corev1.PodSpec) *corev1.Container {
		if pod == nil {
			return nil
		}
		for i := range pod.InitContainers {
			if pod.InitContainers[i].Name == "storage-initializer" {
				return &pod.InitContainers[i]
			}
		}
		return nil
	}

	tests := []struct {
		name     string
		params   components.VllmCustomSpec
		res      *corev1.ResourceRequirements
		prefill  bool
		wantInit bool
	}{
		{name: "hf model", params: components.VllmCustomSpec{ModelURI: "hf://org/model"}, res: res, wantInit: true},
		{name: "s3 model with prefill", params: components.VllmCustomSpec{ModelURI: "s3://bucket/model"}, res: res, prefill: true, wantInit: true},
		{name: "not configured", params: components.VllmCustomSpec{ModelURI: "hf://org/model"}},
		{name: "pvc model", params: components.VllmCustomSpec{ModelURI: "pvc://claim/model"}, res: res},
		{name: "oci model", params: components.VllmCustomSpec{ModelURI: "oci://registry/model"}, res: res},
		{name: "storage initializer disabled", params: components.VllmCustomSpec{ModelURI: "hf://org/model", DisableStorageInitializer: ptr.To(true)}, res: res},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			spec := kservev1alpha2.LLMInferenceServiceSpec{}
			spec.Template = &corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}}}
			spec.Worker = &corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}}}
			if tt.prefill {
				spec.Prefill = &kservev1alpha2.WorkloadSpec{}
			}
			applyStorageInitializerResources(&spec, tt.params, tt.res)

			pods := []*corev1.PodSpec{spec.Template, spec.Worker}
			if tt.prefill {
				pods = append(pods, spec.Prefill.Template)
			}
			for _, pod := range pods {
				got := storageInit(pod)
				if !tt.wantInit {
					if got != nil {
						t.Fatalf("unexpected storage-initializer override: %#v", got)
					}
					continue
				}
				if got == nil {
					t.Fatalf("missing storage-initializer override in %#v", pod)
				}
				if mem := got.Resources.Limits[corev1.ResourceMemory]; mem.Cmp(resource.MustParse("4Gi")) != 0 {
					t.Fatalf("memory limit = %s, want 4Gi", mem.String())
				}
			}
		})
	}
}

func TestBuildLLMInferenceServiceRolloutStrategy(t *testing.T) {
	t.Parallel()

	noSurge := func(t *testing.T, rs *kservev1alpha2.RolloutStrategy) {
		t.Helper()
		if rs == nil || rs.MaxSurge == nil || rs.MaxSurge.IntValue() != 0 ||
			rs.MaxUnavailable == nil || rs.MaxUnavailable.IntValue() != 1 {
			t.Fatalf("rolloutStrategy = %#v, want maxSurge 0, maxUnavailable 1", rs)
		}
	}

	t.Run("gpu decode and prefill never surge", func(t *testing.T) {
		t.Parallel()
		got, err := buildLLMInferenceService(llmContext(t, nil, components.VllmCustomSpec{}, llm.LlmTopologyParameters{EnablePrefill: true}))
		if err != nil {
			t.Fatal(err)
		}
		noSurge(t, got.Spec.RolloutStrategy)
		noSurge(t, got.Spec.Prefill.RolloutStrategy)
	})

	t.Run("cpu profile keeps the default strategy", func(t *testing.T) {
		t.Parallel()
		got, err := buildLLMInferenceService(llmContext(t, nil, components.VllmCustomSpec{ComputeProfile: "cpu"}, llm.LlmTopologyParameters{}))
		if err != nil {
			t.Fatal(err)
		}
		if got.Spec.RolloutStrategy != nil {
			t.Fatalf("cpu rolloutStrategy = %#v, want nil", got.Spec.RolloutStrategy)
		}
	})
}

func TestUnservedWorkload(t *testing.T) {
	t.Parallel()
	observed := func(kind, name string, ready *int32) *kservev1alpha2.ObservedWorkloadStatus {
		return &kservev1alpha2.ObservedWorkloadStatus{
			TypedLocalObjectReference: corev1.TypedLocalObjectReference{Kind: kind, Name: name},
			ReadyReplicas:             ready,
		}
	}
	tests := []struct {
		name string
		ws   *kservev1alpha2.WorkloadStatus
		want string
	}{
		{name: "no workload status trusts Ready", ws: nil, want: ""},
		{name: "primary has a ready pod", ws: &kservev1alpha2.WorkloadStatus{
			Primary: observed("Deployment", "m-kserve", ptr.To[int32](1)),
		}, want: ""},
		{name: "primary has no ready pod", ws: &kservev1alpha2.WorkloadStatus{
			Primary: observed("Deployment", "m-kserve", ptr.To[int32](0)),
		}, want: "Waiting for Deployment m-kserve to have a ready pod"},
		{name: "primary not observed yet", ws: &kservev1alpha2.WorkloadStatus{
			Primary: observed("LeaderWorkerSet", "m-kserve-mn", nil),
		}, want: "Waiting for LeaderWorkerSet m-kserve-mn to have a ready pod"},
		{name: "prefill has no ready pod", ws: &kservev1alpha2.WorkloadStatus{
			Primary: observed("Deployment", "m-kserve", ptr.To[int32](1)),
			Prefill: observed("Deployment", "m-kserve-prefill", ptr.To[int32](0)),
		}, want: "Waiting for Deployment m-kserve-prefill to have a ready pod"},
		{name: "scheduler is not a model workload", ws: &kservev1alpha2.WorkloadStatus{
			Primary:   observed("Deployment", "m-kserve", ptr.To[int32](1)),
			Scheduler: observed("Deployment", "m-kserve-router-scheduler", ptr.To[int32](0)),
		}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := unservedWorkload(tt.ws); got != tt.want {
				t.Fatalf("unservedWorkload() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStatusLLMLocalModelCache(t *testing.T) {
	t.Parallel()

	readyURL, err := apis.ParseURL("https://llama.example.com/v1")
	if err != nil {
		t.Fatal(err)
	}
	newLLM := func(labels map[string]string) *kservev1alpha2.LLMInferenceService {
		return &kservev1alpha2.LLMInferenceService{
			ObjectMeta: metav1.ObjectMeta{Name: "llama", Namespace: "ns", Labels: labels},
			Status: kservev1alpha2.LLMInferenceServiceStatus{
				URL: readyURL,
				Status: duckv1.Status{Conditions: duckv1.Conditions{{
					Type:   apis.ConditionReady,
					Status: corev1.ConditionTrue,
				}}},
				Workloads: &kservev1alpha2.WorkloadStatus{
					Primary: &kservev1alpha2.ObservedWorkloadStatus{
						TypedLocalObjectReference: corev1.TypedLocalObjectReference{Kind: "Deployment", Name: "llama-kserve"},
						ReadyReplicas:             ptr.To[int32](1),
					},
				},
			},
		}
	}
	copies := func(available, total, failed int64) map[string]any {
		return map[string]any{"available": available, "total": total, "failed": failed}
	}
	newCache := func(kind, name, namespace string, modelCopies map[string]any) client.Object {
		cache := unstructuredObject(schema.GroupVersionKind{
			Group: "serving.kserve.io", Version: "v1alpha1", Kind: kind,
		})
		cache.SetName(name)
		cache.SetNamespace(namespace)
		if modelCopies != nil {
			cache.Object["status"] = map[string]any{"copies": modelCopies}
		}
		return cache
	}

	tests := []struct {
		name    string
		labels  map[string]string
		cache   client.Object
		message string
	}{
		{
			name:    "cluster cache ready",
			labels:  map[string]string{constants.LocalModelLabel: "qwen"},
			cache:   newCache("LocalModelCache", "qwen", "", copies(3, 3, 0)),
			message: `Model cache "qwen": 3/3 nodes ready`,
		},
		{
			name: "namespace cache downloading",
			labels: map[string]string{
				constants.LocalModelLabel:          "qwen",
				constants.LocalModelNamespaceLabel: "ns",
			},
			cache:   newCache("LocalModelNamespaceCache", "qwen", "ns", copies(1, 3, 0)),
			message: `Model cache "qwen": 1/3 nodes ready`,
		},
		{
			name:    "cache download failures",
			labels:  map[string]string{constants.LocalModelLabel: "qwen"},
			cache:   newCache("LocalModelCache", "qwen", "", copies(1, 3, 2)),
			message: `Model cache "qwen": 1/3 nodes ready, 2 failed`,
		},
		{
			name:    "cache preparing",
			labels:  map[string]string{constants.LocalModelLabel: "qwen"},
			cache:   newCache("LocalModelCache", "qwen", "", nil),
			message: `Model cache "qwen" is preparing`,
		},
		{
			name:    "stale cache reference",
			labels:  map[string]string{constants.LocalModelLabel: "missing"},
			message: `Model cache "missing" not found`,
		},
		{
			name: "no cache",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			for _, addToScheme := range []func(*runtime.Scheme) error{
				corev1alpha1.AddToScheme,
				kservev1alpha2.AddToScheme,
			} {
				if err := addToScheme(scheme); err != nil {
					t.Fatal(err)
				}
			}

			objects := []client.Object{newLLM(tt.labels)}
			if tt.cache != nil {
				objects = append(objects, tt.cache)
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			inst := llmContext(t, nil, components.VllmCustomSpec{}, llm.LlmTopologyParameters{}).Instance()
			c := controller.NewContext(context.Background(), cl, inst, common.ProviderName)

			got, err := (&Provider{}).statusLLM(c)
			if err != nil {
				t.Fatal(err)
			}
			if got.Phase != corev1alpha1.InstancePhaseReady {
				t.Fatalf("phase = %q, want Ready", got.Phase)
			}
			if got.Message != tt.message {
				t.Fatalf("message = %q, want %q", got.Message, tt.message)
			}
			if got.ConnectionDetails.Host != "llama.example.com" || got.ConnectionDetails.Port != "443" {
				t.Fatalf("connection details = %#v, want original HTTPS endpoint", got.ConnectionDetails)
			}
		})
	}
}
