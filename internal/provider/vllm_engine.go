package provider

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kservev1alpha2 "github.com/kserve/kserve/pkg/apis/serving/v1alpha2"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/definition/components"
	"github.com/openeverest/provider-kserve/internal/common"
)

// mainContainerName is the vLLM container in every LLMInferenceService pod.
const mainContainerName = "main"

// vllmDtypes are the values vLLM accepts for --dtype.
var vllmDtypes = []string{"auto", "half", "float16", "bfloat16", "float", "float32"}

// vllmFlagValue limits name-like flag values to one shell-safe word: the
// bundled presets run `vllm serve` through bash eval, and vLLM grows these
// lists (quantization methods, parsers) faster than the provider could track.
var vllmFlagValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// validateEngine checks the first-class vLLM engine settings.
func validateEngine(e *components.VllmEngineSpec, path *field.Path) field.ErrorList {
	if e == nil {
		return nil
	}
	var errs field.ErrorList
	if e.MaxModelLen != nil && *e.MaxModelLen < 1 {
		errs = append(errs, field.Invalid(path.Child("maxModelLen"), *e.MaxModelLen, "must be at least 1"))
	}
	if e.MaxNumSeqs != nil && *e.MaxNumSeqs < 1 {
		errs = append(errs, field.Invalid(path.Child("maxNumSeqs"), *e.MaxNumSeqs, "must be at least 1"))
	}
	if u := e.GpuMemoryUtilization; u != nil && (*u <= 0 || *u > 1) {
		errs = append(errs, field.Invalid(path.Child("gpuMemoryUtilization"), *u, "must be greater than 0 and at most 1"))
	}
	if e.Dtype != "" && !slices.Contains(vllmDtypes, e.Dtype) {
		errs = append(errs, field.NotSupported(path.Child("dtype"), e.Dtype, vllmDtypes))
	}
	for _, f := range []struct{ name, value string }{
		{"quantization", e.Quantization},
		{"kvCacheDtype", e.KVCacheDtype},
		{"toolCallParser", e.ToolCallParser},
		{"reasoningParser", e.ReasoningParser},
	} {
		if f.value != "" && !vllmFlagValue.MatchString(f.value) {
			errs = append(errs, field.Invalid(path.Child(f.name), f.value, "must be a single word of letters, digits, '_', '-' or '.'"))
		}
	}
	return errs
}

// engineArgs renders the engine settings as `vllm serve` flags.
func engineArgs(e *components.VllmEngineSpec) []string {
	if e == nil {
		return nil
	}
	var args []string
	if e.MaxModelLen != nil {
		args = append(args, "--max-model-len="+strconv.Itoa(int(*e.MaxModelLen)))
	}
	if e.GpuMemoryUtilization != nil {
		args = append(args, "--gpu-memory-utilization="+strconv.FormatFloat(*e.GpuMemoryUtilization, 'f', -1, 64))
	}
	if e.MaxNumSeqs != nil {
		args = append(args, "--max-num-seqs="+strconv.Itoa(int(*e.MaxNumSeqs)))
	}
	if e.Dtype != "" {
		args = append(args, "--dtype="+e.Dtype)
	}
	if e.Quantization != "" {
		args = append(args, "--quantization="+e.Quantization)
	}
	if e.KVCacheDtype != "" {
		args = append(args, "--kv-cache-dtype="+e.KVCacheDtype)
	}
	if e.ToolCallParser != "" {
		args = append(args, "--enable-auto-tool-choice", "--tool-call-parser="+e.ToolCallParser)
	}
	if e.ReasoningParser != "" {
		args = append(args, "--reasoning-parser="+e.ReasoningParser)
	}
	return args
}

// inheritedArgs holds the "main" container args each vLLM pod template inherits
// from the LLMInferenceService's baseRefs, and which optional templates those
// configs define.
type inheritedArgs struct {
	template, worker, prefill, prefillWorker []string
	hasWorker, hasPrefill, hasPrefillWorker  bool
}

// add layers one config over the ones before it. KServe strategic-merges
// configs in baseRefs order, and container args is a plain list, so the last
// config that sets it wins whole.
func (in *inheritedArgs) add(spec *kservev1alpha2.LLMInferenceServiceSpec) {
	if args, ok := mainArgs(spec.Template); ok {
		in.template = args
	}
	if spec.Worker != nil {
		in.hasWorker = true
		if args, ok := mainArgs(spec.Worker); ok {
			in.worker = args
		}
	}
	if spec.Prefill != nil {
		in.hasPrefill = true
		if args, ok := mainArgs(spec.Prefill.Template); ok {
			in.prefill = args
		}
		if spec.Prefill.Worker != nil {
			in.hasPrefillWorker = true
			if args, ok := mainArgs(spec.Prefill.Worker); ok {
				in.prefillWorker = args
			}
		}
	}
}

func mainArgs(pod *corev1.PodSpec) ([]string, bool) {
	if pod == nil {
		return nil, false
	}
	for _, c := range pod.Containers {
		if c.Name == mainContainerName && c.Args != nil {
			return c.Args, true
		}
	}
	return nil, false
}

// resolveInheritedArgs walks spec.BaseRefs the way KServe does. The inline
// config is taken from the Instance rather than the cluster, since it may not
// be applied yet. A missing config is skipped: KServe reports it on the
// LLMInferenceService.
func resolveInheritedArgs(c *controller.Context, refs []corev1.LocalObjectReference, inline *kservev1alpha2.LLMInferenceServiceSpec) (inheritedArgs, error) {
	var in inheritedArgs
	for _, ref := range refs {
		if inline != nil && ref.Name == instanceConfigName(c.Name()) {
			in.add(inline)
			continue
		}
		cfg, err := getLLMConfig(c, ref.Name)
		if err != nil {
			return in, err
		}
		if cfg != nil {
			in.add(&cfg.Spec)
		}
	}
	return in, nil
}

// getLLMConfig looks a baseRef up in the Instance's namespace, then in KServe's
// system namespace, matching KServe's own lookup. It returns nil when neither
// has it.
func getLLMConfig(c *controller.Context, name string) (*kservev1alpha2.LLMInferenceServiceConfig, error) {
	namespaces := []string{c.Instance().Namespace}
	if ns := common.KServeNamespace(); ns != "" && ns != c.Instance().Namespace {
		namespaces = append(namespaces, ns)
	}
	for _, ns := range namespaces {
		cfg := &kservev1alpha2.LLMInferenceServiceConfig{}
		err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: ns, Name: name}, cfg)
		if err == nil {
			return cfg, nil
		}
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("getting LLMInferenceServiceConfig %s/%s: %w", ns, name, err)
		}
	}
	return nil, nil
}

// applyEngineArgs appends the engine flags to the inherited args of every vLLM
// pod template the LLMInferenceService ends up with. Setting args on the
// Instance replaces the inherited list, so it is carried over; vLLM takes the
// last value of a repeated flag, so the engine settings win.
func applyEngineArgs(spec *kservev1alpha2.LLMInferenceServiceSpec, in inheritedArgs, flags []string) {
	if spec.Template == nil {
		spec.Template = &corev1.PodSpec{}
	}
	setMainArgs(spec.Template, in.template, flags)
	if spec.Worker != nil || in.hasWorker {
		if spec.Worker == nil {
			spec.Worker = &corev1.PodSpec{}
		}
		setMainArgs(spec.Worker, in.worker, flags)
	}
	if spec.Prefill == nil && !in.hasPrefill {
		return
	}
	if spec.Prefill == nil {
		spec.Prefill = &kservev1alpha2.WorkloadSpec{}
	}
	if spec.Prefill.Template == nil {
		spec.Prefill.Template = &corev1.PodSpec{}
	}
	setMainArgs(spec.Prefill.Template, in.prefill, flags)
	if spec.Prefill.Worker != nil || in.hasPrefillWorker {
		if spec.Prefill.Worker == nil {
			spec.Prefill.Worker = &corev1.PodSpec{}
		}
		setMainArgs(spec.Prefill.Worker, in.prefillWorker, flags)
	}
}

func setMainArgs(pod *corev1.PodSpec, inherited, flags []string) {
	args := append(slices.Clone(inherited), flags...)
	for i := range pod.Containers {
		if pod.Containers[i].Name == mainContainerName {
			pod.Containers[i].Args = args
			return
		}
	}
	pod.Containers = append(pod.Containers, corev1.Container{Name: mainContainerName, Args: args})
}
