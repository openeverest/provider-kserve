{{/*
KServe objects the bundled controllers' admission webhooks validate: the LLM
presets (LLMInferenceServiceConfig), the ClusterServingRuntimes and the
ClusterStorageContainer. They are not release templates: Helm resolves every
kind before running hooks, so with them in the release a first install fails
before the CRD hook can create their CRDs, and the webhooks are not serving yet
anyway. The kserve-objects hook Job applies this document once the KServe
controllers are up, on every install and upgrade.

The LLM presets and runtimes/storage container are vendored from KServe by
`make sync-kserve-objects`. KServe hardcodes the presets to the "kserve"
namespace; the llmisvc controller resolves presets from the instance namespace
or its own POD_NAMESPACE (the release namespace), so they are rewritten to the
release namespace. Their `{{ }}` markers are KServe controller templates, so
.Files.Get emits them unevaluated. worker-pipeline-parallel.yaml is maintained
here: KServe has no multi-node pipeline-parallel preset.

The CPU profile config (kserve-config-llm-cpu) layers a CPU-only vLLM image over
kserve-config-llm-template; the llm topology adds it to baseRefs for
computeProfile: cpu. Its name must match cpuProfileConfigName in
internal/provider/llminferenceservice.go. All configs use apiVersion
serving.kserve.io/v1alpha2 (the storage version), so no conversion webhook runs.
*/}}
{{- define "provider-kserve.kserveObjects" -}}
{{- $ns := printf "namespace: %s\n" .Release.Namespace }}
{{- if .Values.llmPresets.enabled }}
{{ .Files.Get "files/llmisvcconfigs/resources.yaml" | replace "namespace: kserve\n" $ns }}
---
{{ .Files.Get "files/llmisvcconfigs/worker-pipeline-parallel.yaml" | replace "namespace: kserve\n" $ns }}
{{- if .Values.cpuProfile.enabled }}
---
apiVersion: serving.kserve.io/v1alpha2
kind: LLMInferenceServiceConfig
metadata:
  name: kserve-config-llm-cpu
  namespace: {{ .Release.Namespace }}
spec:
  template:
    containers:
    - name: main
      image: {{ .Values.cpuProfile.image | quote }}
      # On the CPU backend vLLM reuses --gpu-memory-utilization to cap the
      # fraction of host RAM reserved for KV-cache.  The default (0.92) is too
      # aggressive for small dev nodes, so we lower it here.
      env:
      - name: VLLM_ADDITIONAL_ARGS
        value: "--gpu-memory-utilization {{ .Values.cpuProfile.gpuMemoryUtilization | default 0.25 }}"
      # The published CPU vLLM images run as root, but the base preset sets
      # runAsNonRoot: true, so the kubelet refuses to start the container.
      # Override the container securityContext to allow root for this profile.
      securityContext:
        runAsNonRoot: false
        runAsUser: 0
{{- with .Values.cpuProfile.defaultArgs }}
      # Default vLLM args for CPU serving. Flow into the base command via `$@`.
      # A per-instance Advanced inline config `args` on `main` replaces this list.
      args:
{{ toYaml . | indent 8 }}
{{- end }}
{{- end }}
{{- end }}
{{- if .Values.kserveRuntimeConfigs.enabled }}
---
{{ .Files.Get "files/kserve/clusterservingruntimes.yaml" }}
{{- end }}
{{- if .Values.kserveResources.enabled }}
---
{{ .Files.Get "files/kserve/clusterstoragecontainer.yaml" }}
{{- end }}
{{- end }}
