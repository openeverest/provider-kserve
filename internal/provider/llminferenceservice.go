// Package provider — LLMInferenceService (serving.kserve.io/v1alpha2) builder.
package provider

import (
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	"sigs.k8s.io/yaml"

	kservev1alpha2 "github.com/kserve/kserve/pkg/apis/serving/v1alpha2"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/definition/components"
	"github.com/openeverest/provider-kserve/definition/topologies/llm"
	"github.com/openeverest/provider-kserve/internal/common"
)

const (
	// computeProfileCPU is the VllmCustomSpec.ComputeProfile value that composes
	// the CPU-only LLMInferenceServiceConfig instead of the default GPU presets.
	computeProfileCPU = "cpu"

	// cpuProfileConfigName is the name of the bundled CPU LLMInferenceServiceConfig
	// rendered by the chart (templates/llmisvcconfig-cpu.yaml). It must match the
	// chart's metadata.name.
	cpuProfileConfigName = "kserve-config-llm-cpu"

	// instanceConfigSuffix names the per-Instance LLMInferenceServiceConfig the
	// provider materializes from the inline Advanced config
	// (llmEngine.parameters.config).
	instanceConfigSuffix = "-config"

	// cpuKVCacheEnvVar is the vLLM knob for the CPU KV cache size (in GiB). Left
	// unset, vLLM auto-sizes the KV cache from the memory that is actually free at
	// startup, which adapts to the node far better than any value derived from the
	// container limit (the CPU runtime footprint dwarfs small models). The
	// provider only sets it when the user explicitly overrides it
	// (kvCacheSpaceGi), leaving the adaptive default otherwise.
	cpuKVCacheEnvVar = "VLLM_CPU_KVCACHE_SPACE"

	// externalServiceSuffix names the extra Service the provider creates to
	// publish the model externally when the user selects LoadBalancer or NodePort
	// (KServe's own workload Service is always ClusterIP and provider-unowned).
	externalServiceSuffix = "-external"

	// vllmServingPort is the port the vLLM OpenAI-compatible API listens on and
	// the port KServe's workload Service targets.
	vllmServingPort = 8000
)

// workloadPodSelector returns the label selector KServe stamps on an
// LLMInferenceService's model pods, so a provider-owned Service can front them.
// Keep in sync with KServe's GetWorkloadLabelSelector.
func workloadPodSelector(instance string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":    instance,
		"app.kubernetes.io/part-of": "llminferenceservice",
		"kserve.io/component":       "workload",
	}
}

// externalServiceName is the name of the provider-owned external Service.
func externalServiceName(instance string) string {
	return instance + externalServiceSuffix
}

// validateLLM checks the Instance spec for the llm topology.
func validateLLM(c *controller.Context) error {
	comp, ok := c.Instance().Spec.Components[common.ComponentLlmEngine]
	if !ok {
		return fmt.Errorf("the %q component is required for the %q topology", common.ComponentLlmEngine, common.TopologyLLM)
	}

	var params components.VllmCustomSpec
	c.TryDecodeComponentParameters(comp, &params)
	if params.ModelURI == "" {
		return fmt.Errorf("%s.parameters.modelURI is required", common.ComponentLlmEngine)
	}
	if _, err := apis.ParseURL(params.ModelURI); err != nil {
		return fmt.Errorf("invalid %s.parameters.modelURI %q: %w", common.ComponentLlmEngine, params.ModelURI, err)
	}
	if comp.Replicas != nil && *comp.Replicas < 0 {
		return fmt.Errorf("%s replicas must not be negative", common.ComponentLlmEngine)
	}
	if strings.TrimSpace(params.Config) != "" {
		if _, err := parseLLMConfigSpec(params.Config); err != nil {
			return err
		}
	}
	if comp.Service != nil {
		switch comp.Service.ServiceType {
		case "", corev1.ServiceTypeClusterIP, corev1.ServiceTypeLoadBalancer, corev1.ServiceTypeNodePort:
		default:
			return fmt.Errorf("%s.service.serviceType must be one of ClusterIP, LoadBalancer or NodePort", common.ComponentLlmEngine)
		}
	}
	return nil
}

// parseLLMConfigSpec decodes the inline Advanced config (YAML spec body) into an
// LLMInferenceServiceSpec. Parsing is lenient (unknown fields are ignored) so a
// config authored against a newer KServe than the one vendored here still loads.
func parseLLMConfigSpec(raw string) (kservev1alpha2.LLMInferenceServiceSpec, error) {
	var spec kservev1alpha2.LLMInferenceServiceSpec
	if err := yaml.Unmarshal([]byte(raw), &spec); err != nil {
		return spec, fmt.Errorf("invalid %s.parameters.config: %w", common.ComponentLlmEngine, err)
	}
	return spec, nil
}

// instanceConfigName is the name of the Instance-owned LLMInferenceServiceConfig
// materialized from the inline Advanced config.
func instanceConfigName(instance string) string {
	return instance + instanceConfigSuffix
}

// buildLLMInferenceService translates an Instance into an LLMInferenceService.
func buildLLMInferenceService(c *controller.Context) (*kservev1alpha2.LLMInferenceService, error) {
	comp := c.Instance().Spec.Components[common.ComponentLlmEngine]

	var params components.VllmCustomSpec
	c.TryDecodeComponentParameters(comp, &params)

	if params.ModelURI == "" {
		return nil, fmt.Errorf("%s.parameters.modelURI is required", common.ComponentLlmEngine)
	}
	uri, err := apis.ParseURL(params.ModelURI)
	if err != nil {
		return nil, fmt.Errorf("invalid modelURI %q: %w", params.ModelURI, err)
	}

	spec := kservev1alpha2.LLMInferenceServiceSpec{
		Model: kservev1alpha2.LLMModelSpec{URI: *uri},
	}
	if params.ModelName != "" {
		spec.Model.Name = ptr.To(params.ModelName)
	}

	// Static replicas from the component spec.
	if comp.Replicas != nil {
		spec.WorkloadSpec.Replicas = comp.Replicas
	}

	// Runtime parallelism.
	if params.TensorParallelSize != nil || params.PipelineParallelSize != nil {
		spec.WorkloadSpec.Parallelism = &kservev1alpha2.ParallelismSpec{
			Tensor:   params.TensorParallelSize,
			Pipeline: params.PipelineParallelSize,
		}
	}

	// Optional pod resource requirements from the component spec. KServe
	// strategic-merges the Instance spec last, so a "main" container override
	// here wins over the preset and any baseRefs.
	mainContainer := corev1.Container{Name: "main"}
	haveMainOverride := false
	isCPUProfile := strings.EqualFold(params.ComputeProfile, computeProfileCPU)
	if comp.Resources != nil {
		res := *comp.Resources
		// For the CPU profile, mirror limits into requests (Guaranteed QoS).
		// vLLM CPU probes *host-free* memory (not the cgroup limit) when sizing
		// its KV cache and refuses to start if too little is free. Reserving the
		// memory via requests keeps the scheduler from packing the pod onto an
		// oversubscribed node where that probe fails.
		if isCPUProfile {
			res = mirrorLimitsToRequests(res)
		}
		mainContainer.Resources = res
		haveMainOverride = true
	}

	// vLLM CPU auto-sizes its KV cache from host-free memory when
	// VLLM_CPU_KVCACHE_SPACE is unset, which adapts to the node. Only set it when
	// the user explicitly overrides it; a value derived from the container limit
	// cannot account for the (large, model-independent) CPU runtime footprint and
	// tends to exceed what is actually free at startup.
	if isCPUProfile && params.KVCacheSpaceGi != nil {
		kv := int(*params.KVCacheSpaceGi)
		if kv < 1 {
			kv = 1
		}
		mainContainer.Env = append(mainContainer.Env, corev1.EnvVar{Name: cpuKVCacheEnvVar, Value: strconv.Itoa(kv)})
		haveMainOverride = true
	}

	if haveMainOverride {
		spec.WorkloadSpec.Template = &corev1.PodSpec{
			Containers: []corev1.Container{mainContainer},
		}
	}

	// Attach the HuggingFace token ServiceAccount (created by
	// ensureModelPullerServiceAccount) so KServe's storage-initializer can
	// authenticate gated model downloads. KServe honors a user-set workload
	// ServiceAccountName only when no routing sidecar is present, so this path
	// covers the common (non-gateway-routing) gated download case.
	if common.HFTokenSecretName() != "" {
		if spec.WorkloadSpec.Template == nil {
			spec.WorkloadSpec.Template = &corev1.PodSpec{}
		}
		spec.WorkloadSpec.Template.ServiceAccountName = modelPullerSAName(c.Name())
	}

	// Config inheritance. The CPU compute profile composes the bundled CPU-only
	// LLMInferenceServiceConfig ahead of any user baseRefs, so an explicit
	// baseRef still wins on conflict (later refs override earlier ones).
	if isCPUProfile {
		spec.BaseRefs = append(spec.BaseRefs, corev1.LocalObjectReference{Name: cpuProfileConfigName})
	}
	for _, ref := range params.BaseRefs {
		spec.BaseRefs = append(spec.BaseRefs, corev1.LocalObjectReference{Name: ref})
	}
	// The inline Advanced config (materialized by ensureLLMConfig) is attached
	// last, so it overrides the CPU preset and any user baseRefs above while the
	// Instance's own structured fields (set on this spec) still win over it.
	if strings.TrimSpace(params.Config) != "" {
		spec.BaseRefs = append(spec.BaseRefs, corev1.LocalObjectReference{Name: instanceConfigName(c.Name())})
	}

	// Storage initializer toggle.
	if params.DisableStorageInitializer != nil {
		spec.StorageInitializer = &kservev1alpha2.StorageInitializerSpec{
			Enabled: ptr.To(!*params.DisableStorageInitializer),
		}
	}

	// Topology-level options (routing, disaggregation).
	var topo llm.LlmTopologyParameters
	c.TryDecodeTopologyParameters(&topo)

	if topo.EnableGatewayRouting {
		spec.Router = &kservev1alpha2.RouterSpec{
			Route:     &kservev1alpha2.GatewayRoutesSpec{},
			Gateway:   &kservev1alpha2.GatewaySpec{},
			Scheduler: &kservev1alpha2.SchedulerSpec{},
		}
	}

	if topo.EnablePrefill {
		prefill := &kservev1alpha2.WorkloadSpec{}
		if topo.PrefillReplicas != nil {
			prefill.Replicas = topo.PrefillReplicas
		}
		spec.Prefill = prefill
	}

	meta := c.ObjectMeta(c.Name())
	if meta.Annotations == nil {
		meta.Annotations = map[string]string{}
	}
	meta.Annotations[common.DeploymentModeAnnotation] = common.DeploymentModeRaw

	return &kservev1alpha2.LLMInferenceService{
		ObjectMeta: meta,
		Spec:       spec,
	}, nil
}

// syncLLM creates or updates the LLMInferenceService.
func (p *Provider) syncLLM(c *controller.Context) error {
	// Provision the HuggingFace token ServiceAccount before the workload so
	// KServe finds it when it reconciles the storage-initializer.
	if common.HFTokenSecretName() != "" {
		if err := ensureModelPullerServiceAccount(c); err != nil {
			return err
		}
	}

	// Materialize the inline Advanced config before the workload: it is
	// referenced via baseRefs and KServe errors if the config does not yet exist.
	if err := ensureLLMConfig(c); err != nil {
		return err
	}

	llmisvc, err := buildLLMInferenceService(c)
	if err != nil {
		return err
	}
	if err := common.Apply(c.Context(), c.Client(), c.Instance(), llmisvc); err != nil {
		return err
	}

	// Publish the model externally when requested (LoadBalancer/NodePort). This
	// is a provider-owned Service fronting the KServe workload pods; ClusterIP
	// needs none (KServe's own workload Service already covers in-cluster).
	return ensureExternalService(c)
}

// ensureExternalService reconciles the provider-owned external Service for the
// llm workload. It creates a LoadBalancer/NodePort Service selecting the KServe
// workload pods when the user picks that expose type, and deletes any stale one
// when the expose type is ClusterIP (the default) so switching back cleans up.
func ensureExternalService(c *controller.Context) error {
	comp := c.Instance().Spec.Components[common.ComponentLlmEngine]

	svcType := corev1.ServiceTypeClusterIP
	if comp.Service != nil && comp.Service.ServiceType != "" {
		svcType = comp.Service.ServiceType
	}

	if svcType == corev1.ServiceTypeClusterIP {
		// No external Service needed; remove a previously created one if the
		// user switched away from LoadBalancer/NodePort.
		stale := &corev1.Service{ObjectMeta: c.ObjectMeta(externalServiceName(c.Name()))}
		return c.Delete(stale)
	}

	svc := buildExternalService(c, comp.Service, svcType)
	return common.Apply(c.Context(), c.Client(), c.Instance(), svc)
}

// buildExternalService builds the LoadBalancer/NodePort Service that fronts the
// KServe workload pods on the vLLM serving port.
func buildExternalService(c *controller.Context, spec *corev1alpha1.Service, svcType corev1.ServiceType) *corev1.Service {
	meta := c.ObjectMeta(externalServiceName(c.Name()))
	if spec != nil && len(spec.Annotations) > 0 {
		if meta.Annotations == nil {
			meta.Annotations = map[string]string{}
		}
		for k, v := range spec.Annotations {
			meta.Annotations[k] = v
		}
	}

	svc := &corev1.Service{
		ObjectMeta: meta,
		Spec: corev1.ServiceSpec{
			Type:     svcType,
			Selector: workloadPodSelector(c.Name()),
			Ports: []corev1.ServicePort{{
				Name:       "http",
				Protocol:   corev1.ProtocolTCP,
				Port:       vllmServingPort,
				TargetPort: intstr.FromInt(vllmServingPort),
			}},
		},
	}

	if svcType == corev1.ServiceTypeLoadBalancer && spec != nil && spec.LoadBalancerService != nil {
		svc.Spec.LoadBalancerSourceRanges = spec.LoadBalancerService.SourceRanges.NormalizedSourceRanges()
	}

	return svc
}

// ensureLLMConfig materializes the inline Advanced config
// (llmEngine.parameters.config) as an Instance-owned LLMInferenceServiceConfig
// in the Instance namespace. KServe resolves baseRefs from the llmisvc's own
// namespace first, so a same-namespace config is inherited, and the owner
// reference garbage-collects it with the Instance. No-op when no inline config
// is set.
func ensureLLMConfig(c *controller.Context) error {
	comp := c.Instance().Spec.Components[common.ComponentLlmEngine]

	var params components.VllmCustomSpec
	c.TryDecodeComponentParameters(comp, &params)
	if strings.TrimSpace(params.Config) == "" {
		return nil
	}

	spec, err := parseLLMConfigSpec(params.Config)
	if err != nil {
		return err
	}

	cfg := &kservev1alpha2.LLMInferenceServiceConfig{
		ObjectMeta: c.ObjectMeta(instanceConfigName(c.Name())),
		Spec:       spec,
	}
	return common.Apply(c.Context(), c.Client(), c.Instance(), cfg)
}

// modelPullerSAName is the ServiceAccount the provider creates to carry the
// HuggingFace token Secret for an Instance's llm workload.
func modelPullerSAName(instance string) string {
	return instance + "-model-puller"
}

// mirrorLimitsToRequests returns a copy of res whose Requests mirror any Limit
// that lacks an explicit request, yielding a Guaranteed-QoS container. The
// original ResourceRequirements maps are left untouched.
func mirrorLimitsToRequests(res corev1.ResourceRequirements) corev1.ResourceRequirements {
	if len(res.Limits) == 0 {
		return res
	}
	reqs := corev1.ResourceList{}
	for name, qty := range res.Requests {
		reqs[name] = qty
	}
	for name, qty := range res.Limits {
		if _, ok := reqs[name]; !ok {
			reqs[name] = qty.DeepCopy()
		}
	}
	res.Requests = reqs
	return res
}

// ensureModelPullerServiceAccount creates/updates a ServiceAccount referencing
// the configured HuggingFace token Secret. KServe's llmisvc storage path reads
// the Secrets listed on the workload ServiceAccount and injects HF_TOKEN into
// the storage-initializer, enabling gated model downloads.
func ensureModelPullerServiceAccount(c *controller.Context) error {
	sa := &corev1.ServiceAccount{
		ObjectMeta: c.ObjectMeta(modelPullerSAName(c.Name())),
		Secrets: []corev1.ObjectReference{
			{Name: common.HFTokenSecretName()},
		},
	}
	return common.Apply(c.Context(), c.Client(), c.Instance(), sa)
}

// statusLLM translates the LLMInferenceService status into a provider Status.
func (p *Provider) statusLLM(c *controller.Context) (controller.Status, error) {
	llmisvc := &kservev1alpha2.LLMInferenceService{}
	if err := c.Get(llmisvc, c.Name()); err != nil {
		return controller.Provisioning("Waiting for LLMInferenceService"), nil
	}

	ready := llmisvc.Status.GetCondition(apis.ConditionReady)
	if ready != nil && ready.IsTrue() {
		// Surface connection details derived from however the model is exposed
		// (gateway URL, external Service, or the in-cluster workload Service).
		// While an external address is still settling (e.g. a LoadBalancer with
		// no ingress IP yet), report Ready without details rather than blocking.
		details, err := p.llmConnectionDetails(c, llmisvc)
		if err != nil {
			return controller.Provisioning(err.Error()), nil
		}
		if details != nil {
			return controller.ReadyWithConnectionDetails(*details), nil
		}
		return controller.Ready(), nil
	}

	// A not-ready LLMInferenceService is still progressing, not failed. KServe
	// drives Ready through False during normal startup (e.g.
	// MinimumReplicasUnavailable while the storage-initializer downloads the
	// model), so surface it as Provisioning and let the condition message
	// explain the current state rather than flipping the Instance to Failed.
	return controller.Provisioning(conditionMessage(ready, "LLMInferenceService is being created")), nil
}

// llmConnectionDetails resolves how to reach a Ready model based on its expose
// type. It returns nil (Ready without details) when an external address is
// requested but not yet available, so the Instance does not stall waiting on a
// pending LoadBalancer.
func (p *Provider) llmConnectionDetails(c *controller.Context, llmisvc *kservev1alpha2.LLMInferenceService) (*controller.ConnectionDetails, error) {
	// Gateway API routing publishes an external URL directly on the status.
	if llmisvc.Status.URL != nil {
		d := connectionDetails(llmisvc.Status.URL)
		return &d, nil
	}

	comp := c.Instance().Spec.Components[common.ComponentLlmEngine]
	svcType := corev1.ServiceTypeClusterIP
	if comp.Service != nil && comp.Service.ServiceType != "" {
		svcType = comp.Service.ServiceType
	}

	switch svcType {
	case corev1.ServiceTypeLoadBalancer, corev1.ServiceTypeNodePort:
		return p.externalServiceConnectionDetails(c, svcType)
	default:
		// ClusterIP: reachable in-cluster via the KServe workload Service DNS.
		host := workloadServiceHost(c, llmisvc)
		d := kserveConnectionDetails(host, strconv.Itoa(vllmServingPort))
		return &d, nil
	}
}

// externalServiceConnectionDetails derives the endpoint of the provider-owned
// external Service. Returns nil when the address is not yet assigned.
func (p *Provider) externalServiceConnectionDetails(c *controller.Context, svcType corev1.ServiceType) (*controller.ConnectionDetails, error) {
	svc := &corev1.Service{}
	if err := c.Get(svc, externalServiceName(c.Name())); err != nil {
		return nil, nil
	}

	if svcType == corev1.ServiceTypeLoadBalancer {
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			host := ing.IP
			if host == "" {
				host = ing.Hostname
			}
			if host != "" {
				d := kserveConnectionDetails(host, strconv.Itoa(vllmServingPort))
				return &d, nil
			}
		}
		// LoadBalancer provisioning still in progress.
		return nil, nil
	}

	// NodePort: pair a reachable node address with the allocated node port.
	var nodePort int32
	for _, p := range svc.Spec.Ports {
		if p.NodePort != 0 {
			nodePort = p.NodePort
			break
		}
	}
	if nodePort == 0 {
		return nil, nil
	}
	host := firstNodeAddress(c)
	if host == "" {
		return nil, nil
	}
	d := kserveConnectionDetails(host, strconv.Itoa(int(nodePort)))
	return &d, nil
}

// workloadServiceHost returns the in-cluster DNS name of the KServe workload
// Service. It prefers the name KServe records in status, falling back to the
// well-known naming pattern before the status is populated.
func workloadServiceHost(c *controller.Context, llmisvc *kservev1alpha2.LLMInferenceService) string {
	name := c.Name() + "-kserve-workload-svc"
	if ws := llmisvc.Status.Workloads; ws != nil && ws.Service != nil && ws.Service.Name != "" {
		name = ws.Service.Name
	}
	return fmt.Sprintf("%s.%s.svc.cluster.local", name, c.Namespace())
}

// firstNodeAddress returns an address for reaching a NodePort Service, preferring
// an external (routable) address and falling back to the internal one. Returns
// empty when nodes cannot be listed or have no usable address.
func firstNodeAddress(c *controller.Context) string {
	nodes := &corev1.NodeList{}
	if err := c.List(nodes); err != nil {
		return ""
	}
	var internal string
	for _, node := range nodes.Items {
		for _, addr := range node.Status.Addresses {
			switch addr.Type {
			case corev1.NodeExternalIP:
				if addr.Address != "" {
					return addr.Address
				}
			case corev1.NodeInternalIP:
				if internal == "" {
					internal = addr.Address
				}
			}
		}
	}
	return internal
}

// kserveConnectionDetails builds ConnectionDetails for the vLLM HTTP endpoint.
func kserveConnectionDetails(host, port string) controller.ConnectionDetails {
	return controller.ConnectionDetails{
		Type:     "kserve",
		Provider: common.ProviderName,
		Host:     host,
		Port:     port,
		URI:      fmt.Sprintf("http://%s:%s", host, port),
	}
}
