package provider

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	kservev1alpha2 "github.com/kserve/kserve/pkg/apis/serving/v1alpha2"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/definition/components"
	"github.com/openeverest/provider-kserve/definition/topologies/llm"
	"github.com/openeverest/provider-kserve/internal/common"
)

const testKServeNamespace = "kserve-system"

func TestEngineArgs(t *testing.T) {
	t.Parallel()
	if got := engineArgs(nil); got != nil {
		t.Fatalf("engineArgs(nil) = %v, want nil", got)
	}
	if got := engineArgs(&components.VllmEngineSpec{}); got != nil {
		t.Fatalf("engineArgs(empty) = %v, want nil", got)
	}

	got := engineArgs(&components.VllmEngineSpec{
		MaxModelLen:          ptr.To(int32(8192)),
		GpuMemoryUtilization: ptr.To(0.85),
		MaxNumSeqs:           ptr.To(int32(64)),
		Dtype:                "bfloat16",
		Quantization:         "fp8",
		KVCacheDtype:         "fp8",
		ToolCallParser:       "hermes",
		ReasoningParser:      "qwen3",
	})
	want := []string{
		"--max-model-len=8192",
		"--gpu-memory-utilization=0.85",
		"--max-num-seqs=64",
		"--dtype=bfloat16",
		"--quantization=fp8",
		"--kv-cache-dtype=fp8",
		"--enable-auto-tool-choice",
		"--tool-call-parser=hermes",
		"--reasoning-parser=qwen3",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("engineArgs() = %v, want %v", got, want)
	}
}

func TestValidateEngine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		e       *components.VllmEngineSpec
		wantErr string
	}{
		{name: "nil"},
		{name: "valid", e: &components.VllmEngineSpec{
			MaxModelLen: ptr.To(int32(4096)), GpuMemoryUtilization: ptr.To(1.0), MaxNumSeqs: ptr.To(int32(1)),
			Dtype: "half", Quantization: "compressed-tensors", KVCacheDtype: "fp8_e4m3",
			ToolCallParser: "granite-20b-fc", ReasoningParser: "deepseek_r1",
		}},
		{name: "max model len zero", e: &components.VllmEngineSpec{MaxModelLen: ptr.To(int32(0))}, wantErr: "engine.maxModelLen"},
		{name: "max num seqs zero", e: &components.VllmEngineSpec{MaxNumSeqs: ptr.To(int32(0))}, wantErr: "engine.maxNumSeqs"},
		{name: "gpu memory zero", e: &components.VllmEngineSpec{GpuMemoryUtilization: ptr.To(0.0)}, wantErr: "engine.gpuMemoryUtilization"},
		{name: "gpu memory above one", e: &components.VllmEngineSpec{GpuMemoryUtilization: ptr.To(1.5)}, wantErr: "engine.gpuMemoryUtilization"},
		{name: "unknown dtype", e: &components.VllmEngineSpec{Dtype: "int4"}, wantErr: "engine.dtype"},
		{name: "quantization with space", e: &components.VllmEngineSpec{Quantization: "fp8 --foo"}, wantErr: "engine.quantization"},
		{name: "parser with shell", e: &components.VllmEngineSpec{ToolCallParser: "hermes;id"}, wantErr: "engine.toolCallParser"},
		{name: "reasoning parser leading dash", e: &components.VllmEngineSpec{ReasoningParser: "-x"}, wantErr: "engine.reasoningParser"},
		{name: "kv cache dtype with quote", e: &components.VllmEngineSpec{KVCacheDtype: `fp8"`}, wantErr: "engine.kvCacheDtype"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateEngine(tc.e, field.NewPath("engine")).ToAggregate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateEngine() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateEngine() = %v, want error mentioning %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateLLMRejectsInvalidEngine(t *testing.T) {
	t.Parallel()
	err := validateLLM(llmContext(t, nil, components.VllmCustomSpec{
		Engine: &components.VllmEngineSpec{Dtype: "int4"},
	}, llm.LlmTopologyParameters{}).Instance()).ToAggregate()
	if err == nil || !strings.Contains(err.Error(), "engine.dtype") {
		t.Fatalf("validateLLM() = %v, want engine.dtype error", err)
	}
}

func engineTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kservev1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

// llmContextWithConfigs is llmContext backed by a fake client holding objs.
func llmContextWithConfigs(t *testing.T, params components.VllmCustomSpec, topo llm.LlmTopologyParameters, objs ...client.Object) *controller.Context {
	t.Helper()
	cl := fake.NewClientBuilder().WithScheme(engineTestScheme(t)).WithObjects(objs...).Build()
	return controller.NewContext(context.Background(), cl, llmContext(t, nil, params, topo).Instance(), common.ProviderName)
}

func llmConfig(namespace, name string, spec kservev1alpha2.LLMInferenceServiceSpec) *kservev1alpha2.LLMInferenceServiceConfig {
	return &kservev1alpha2.LLMInferenceServiceConfig{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       spec,
	}
}

func podWithMainArgs(args ...string) *corev1.PodSpec {
	return &corev1.PodSpec{Containers: []corev1.Container{{Name: mainContainerName, Args: args}}}
}

func mustMainArgs(t *testing.T, pod *corev1.PodSpec, where string) []string {
	t.Helper()
	args, ok := mainArgs(pod)
	if !ok {
		t.Fatalf("%s: main container has no args", where)
	}
	return args
}

func TestBuildLLMEngineArgs(t *testing.T) {
	engine := &components.VllmEngineSpec{MaxModelLen: ptr.To(int32(4096)), ToolCallParser: "hermes"}
	flags := []string{"--max-model-len=4096", "--enable-auto-tool-choice", "--tool-call-parser=hermes"}
	cpuDefaults := []string{"--gpu-memory-utilization=0.3", "--enforce-eager", "--max-model-len=2048"}
	cpuConfig := llmConfig(testKServeNamespace, cpuProfileConfigName, kservev1alpha2.LLMInferenceServiceSpec{
		WorkloadSpec: kservev1alpha2.WorkloadSpec{Template: podWithMainArgs(cpuDefaults...)},
	})

	t.Run("no engine settings leave args alone", func(t *testing.T) {
		got, err := buildLLMInferenceService(llmContext(t, nil, components.VllmCustomSpec{}, llm.LlmTopologyParameters{}))
		if err != nil {
			t.Fatal(err)
		}
		if args, ok := mainArgs(got.Spec.Template); ok {
			t.Fatalf("main args = %v, want unset", args)
		}
	})

	t.Run("GPU profile keeps env and adds flags", func(t *testing.T) {
		got, err := buildLLMInferenceService(llmContext(t, nil, components.VllmCustomSpec{Engine: engine}, llm.LlmTopologyParameters{}))
		if err != nil {
			t.Fatal(err)
		}
		if args := mustMainArgs(t, got.Spec.Template, "template"); !slices.Equal(args, flags) {
			t.Fatalf("main args = %v, want %v", args, flags)
		}
		main := got.Spec.Template.Containers[0]
		if len(main.Env) == 0 || main.Env[0].Name != gpuLibraryPathEnvVar {
			t.Fatalf("main env = %v, want %s kept", main.Env, gpuLibraryPathEnvVar)
		}
		if got.Spec.Worker != nil || got.Spec.Prefill != nil {
			t.Fatalf("worker/prefill created without being requested: %v / %v", got.Spec.Worker, got.Spec.Prefill)
		}
	})

	t.Run("CPU profile keeps preset defaults", func(t *testing.T) {
		t.Setenv("KSERVE_NAMESPACE", testKServeNamespace)
		c := llmContextWithConfigs(t, components.VllmCustomSpec{ComputeProfile: computeProfileCPU, Engine: engine}, llm.LlmTopologyParameters{}, cpuConfig)
		got, err := buildLLMInferenceService(c)
		if err != nil {
			t.Fatal(err)
		}
		want := append(slices.Clone(cpuDefaults), flags...)
		if args := mustMainArgs(t, got.Spec.Template, "template"); !slices.Equal(args, want) {
			t.Fatalf("main args = %v, want %v", args, want)
		}
	})

	t.Run("inline config replaces preset args", func(t *testing.T) {
		t.Setenv("KSERVE_NAMESPACE", testKServeNamespace)
		params := components.VllmCustomSpec{
			ComputeProfile: computeProfileCPU,
			Engine:         engine,
			Config:         "template:\n  containers:\n    - name: main\n      args: [--enforce-eager]\n",
		}
		got, err := buildLLMInferenceService(llmContextWithConfigs(t, params, llm.LlmTopologyParameters{}, cpuConfig))
		if err != nil {
			t.Fatal(err)
		}
		want := append([]string{"--enforce-eager"}, flags...)
		if args := mustMainArgs(t, got.Spec.Template, "template"); !slices.Equal(args, want) {
			t.Fatalf("main args = %v, want %v", args, want)
		}
	})

	t.Run("namespace config shadows the system one", func(t *testing.T) {
		t.Setenv("KSERVE_NAMESPACE", testKServeNamespace)
		params := components.VllmCustomSpec{Engine: engine, BaseRefs: []string{"shared"}}
		got, err := buildLLMInferenceService(llmContextWithConfigs(t, params, llm.LlmTopologyParameters{},
			llmConfig(testKServeNamespace, "shared", kservev1alpha2.LLMInferenceServiceSpec{
				WorkloadSpec: kservev1alpha2.WorkloadSpec{Template: podWithMainArgs("--system")},
			}),
			llmConfig("ns", "shared", kservev1alpha2.LLMInferenceServiceSpec{
				WorkloadSpec: kservev1alpha2.WorkloadSpec{Template: podWithMainArgs("--local")},
			}),
		))
		if err != nil {
			t.Fatal(err)
		}
		want := append([]string{"--local"}, flags...)
		if args := mustMainArgs(t, got.Spec.Template, "template"); !slices.Equal(args, want) {
			t.Fatalf("main args = %v, want %v", args, want)
		}
	})

	t.Run("missing baseRef is skipped", func(t *testing.T) {
		params := components.VllmCustomSpec{Engine: engine, BaseRefs: []string{"absent"}}
		got, err := buildLLMInferenceService(llmContextWithConfigs(t, params, llm.LlmTopologyParameters{}))
		if err != nil {
			t.Fatal(err)
		}
		if args := mustMainArgs(t, got.Spec.Template, "template"); !slices.Equal(args, flags) {
			t.Fatalf("main args = %v, want %v", args, flags)
		}
	})

	t.Run("workers and prefill get the flags", func(t *testing.T) {
		params := components.VllmCustomSpec{
			Engine:               engine,
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
			if args := mustMainArgs(t, pod, where); !slices.Equal(args, flags) {
				t.Fatalf("%s main args = %v, want %v", where, args, flags)
			}
		}
		if res := got.Spec.Worker.Containers[0].Resources; res.Limits == nil {
			t.Fatal("worker resources were dropped")
		}
	})

	t.Run("worker defined by a baseRef gets the flags", func(t *testing.T) {
		params := components.VllmCustomSpec{Engine: engine, BaseRefs: []string{"dp"}}
		got, err := buildLLMInferenceService(llmContextWithConfigs(t, params, llm.LlmTopologyParameters{},
			llmConfig("ns", "dp", kservev1alpha2.LLMInferenceServiceSpec{
				WorkloadSpec: kservev1alpha2.WorkloadSpec{Worker: podWithMainArgs("--headless")},
			}),
		))
		if err != nil {
			t.Fatal(err)
		}
		want := append([]string{"--headless"}, flags...)
		if args := mustMainArgs(t, got.Spec.Worker, "worker"); !slices.Equal(args, want) {
			t.Fatalf("worker main args = %v, want %v", args, want)
		}
		if got.Spec.Prefill != nil {
			t.Fatalf("prefill created without being requested: %v", got.Spec.Prefill)
		}
	})
}

// TestVendoredPresetsSetNoMainArgs guards an assumption of applyEngineArgs: the
// presets KServe applies implicitly (not listed in baseRefs) set no "main"
// args, so the provider has nothing to carry over from them. Re-check
// applyEngineArgs if a KServe bump breaks this.
func TestVendoredPresetsSetNoMainArgs(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("../../charts/provider-kserve/files/llmisvcconfigs/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no vendored presets found: %v", err)
	}
	parsed := 0
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, doc := range strings.Split(string(raw), "\n---") {
			var cfg kservev1alpha2.LLMInferenceServiceConfig
			if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			if cfg.Name == "" {
				continue
			}
			parsed++
			var in inheritedArgs
			in.add(&cfg.Spec)
			for where, args := range map[string][]string{
				"template": in.template, "worker": in.worker, "prefill": in.prefill, "prefill worker": in.prefillWorker,
			} {
				if args != nil {
					t.Errorf("%s: %s sets %s main args %v", filepath.Base(file), cfg.Name, where, args)
				}
			}
		}
	}
	// resources.yaml alone vendors well over ten presets.
	if parsed < 10 {
		t.Fatalf("parsed %d presets, want the full vendored set", parsed)
	}
}
