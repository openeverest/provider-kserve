package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller/validationtest"

	"github.com/openeverest/provider-kserve/definition/components"
	"github.com/openeverest/provider-kserve/definition/topologies/llm"
	"github.com/openeverest/provider-kserve/internal/common"
)

const (
	providerSpecPath = "../../charts/provider-kserve/generated/provider-spec.yaml"
	llmFixture       = "testdata/validate/llm.yaml"
	predictorFixture = "testdata/validate/predictor.yaml"
)

// component applies mutate to the named component of the Instance.
func component(name string, mutate func(*corev1alpha1.ComponentSpec)) func(*corev1alpha1.Instance) {
	return func(in *corev1alpha1.Instance) {
		c := in.Spec.Components[name]
		mutate(&c)
		in.Spec.Components[name] = c
	}
}

// withParameters sets the top-level keys of raw on the named component's
// parameters.
func withParameters(name, raw string) func(*corev1alpha1.Instance) {
	return component(name, func(c *corev1alpha1.ComponentSpec) { c.Parameters = mergeRaw(c.Parameters, raw) })
}

// withTopologyParameters sets the top-level keys of raw on the topology
// parameters.
func withTopologyParameters(raw string) func(*corev1alpha1.Instance) {
	return func(in *corev1alpha1.Instance) {
		in.Spec.Topology.Parameters = mergeRaw(in.Spec.Topology.Parameters, raw)
	}
}

func mergeRaw(base *runtime.RawExtension, raw string) *runtime.RawExtension {
	merged := map[string]any{}
	if base != nil {
		_ = json.Unmarshal(base.Raw, &merged)
	}
	_ = json.Unmarshal([]byte(raw), &merged)
	b, _ := json.Marshal(merged)
	return &runtime.RawExtension{Raw: b}
}

// TestValidate runs the checks the runtime runs before every Sync: the
// schemas and catalog the provider publishes, then Validate.
func TestValidate(t *testing.T) {
	t.Parallel()
	v := validationtest.New(t, New(), providerSpecPath)

	llmParams := func(raw string) func(*corev1alpha1.Instance) { return withParameters(common.ComponentLlmEngine, raw) }
	predictorParams := func(raw string) func(*corev1alpha1.Instance) { return withParameters(common.ComponentPredictor, raw) }

	tests := []struct {
		name    string
		fixture string
		mutate  func(*corev1alpha1.Instance)
		want    []string
	}{
		{name: "valid llm", fixture: llmFixture},
		{name: "valid predictor", fixture: predictorFixture},
		{
			name:    "no topology type",
			fixture: llmFixture,
			mutate:  func(in *corev1alpha1.Instance) { in.Spec.Topology = nil },
			want:    []string{"spec.topology.type"},
		},
		{
			name:    "llm without a model",
			fixture: llmFixture,
			mutate:  llmParams(`{"modelURI":""}`),
			want:    []string{"spec.components[llmEngine].parameters.modelURI"},
		},
		{
			name:    "llm model scheme KServe cannot load",
			fixture: llmFixture,
			mutate:  llmParams(`{"modelURI":"gs://bucket/model"}`),
			want:    []string{"spec.components[llmEngine].parameters.modelURI"},
		},
		{
			name:    "oci model without the storage initializer",
			fixture: llmFixture,
			mutate:  llmParams(`{"modelURI":"oci://quay.io/org/model:v1","disableStorageInitializer":true}`),
			want:    []string{"spec.components[llmEngine].parameters.disableStorageInitializer"},
		},
		{
			name:    "invalid llm image pull secret",
			fixture: llmFixture,
			mutate:  llmParams(`{"imagePullSecret":"Quay_Pull"}`),
			want:    []string{"spec.components[llmEngine].parameters.imagePullSecret"},
		},
		{
			name:    "negative llm replicas",
			fixture: llmFixture,
			mutate:  component(common.ComponentLlmEngine, func(c *corev1alpha1.ComponentSpec) { c.Replicas = new(int32(-1)) }),
			want:    []string{"spec.components[llmEngine].replicas"},
		},
		{
			name:    "data and pipeline parallelism",
			fixture: llmFixture,
			mutate:  llmParams(`{"dataParallelSize":2,"pipelineParallelSize":2}`),
			want:    []string{"spec.components[llmEngine].parameters.dataParallelSize"},
		},
		{
			name:    "llm config that is not YAML",
			fixture: llmFixture,
			mutate:  llmParams(`{"config":"template: ["}`),
			want:    []string{"spec.components[llmEngine].parameters.config"},
		},
		{
			name:    "unknown vLLM dtype",
			fixture: llmFixture,
			mutate:  llmParams(`{"engine":{"dtype":"int4"}}`),
			want:    []string{"spec.components[llmEngine].parameters.engine.dtype"},
		},
		{
			name:    "llm ExternalName service",
			fixture: llmFixture,
			mutate: component(common.ComponentLlmEngine, func(c *corev1alpha1.ComponentSpec) {
				c.Service = &corev1alpha1.Service{ServiceType: "ExternalName"}
			}),
			want: []string{"spec.components[llmEngine].service.serviceType"},
		},
		{
			name:    "unknown llm external access",
			fixture: llmFixture,
			mutate:  withTopologyParameters(`{"externalAccess":"Ingress"}`),
			want:    []string{"spec.topology.parameters.externalAccess"},
		},
		{
			name:    "AI Gateway the chart does not enable",
			fixture: llmFixture,
			mutate:  withTopologyParameters(`{"externalAccess":"EnvoyAIGateway"}`),
			want:    []string{"spec.topology.parameters.externalAccess"},
		},
		{
			name:    "token limit without the AI Gateway",
			fixture: llmFixture,
			mutate:  withTopologyParameters(`{"tokenLimitPerHour":1000}`),
			want:    []string{"spec.topology.parameters.tokenLimitPerHour"},
		},
		{
			name:    "autoscaling without a ceiling",
			fixture: llmFixture,
			mutate:  llmParams(`{"minReplicas":1}`),
			want:    []string{"spec.components[llmEngine].parameters.maxReplicas"},
		},
		{
			name:    "autoscaling floor above the ceiling",
			fixture: llmFixture,
			mutate:  llmParams(`{"minReplicas":4,"maxReplicas":2}`),
			want:    []string{"spec.components[llmEngine].parameters.minReplicas"},
		},
		{
			name:    "prefill autoscaling without prefill",
			fixture: llmFixture,
			mutate:  withTopologyParameters(`{"prefillMaxReplicas":4}`),
			want:    []string{"spec.topology.parameters.enablePrefill"},
		},
		{
			name:    "decode and prefill on different actuators",
			fixture: llmFixture,
			mutate: func(in *corev1alpha1.Instance) {
				llmParams(`{"maxReplicas":4,"scalingActuator":"hpa"}`)(in)
				withTopologyParameters(`{"enablePrefill":true,"prefillMaxReplicas":4,"prefillScalingActuator":"keda"}`)(in)
			},
			want: []string{"spec.topology.parameters.prefillScalingActuator"},
		},
		{
			name:    "workers without pipeline parallelism",
			fixture: llmFixture,
			mutate:  llmParams(`{"workerCount":1}`),
			want:    []string{"spec.components[llmEngine].parameters.pipelineParallelSize"},
		},
		{
			name:    "GPU count on the CPU profile",
			fixture: llmFixture,
			mutate:  llmParams(`{"computeProfile":"cpu","gpuCount":1}`),
			want:    []string{"spec.components[llmEngine].parameters.gpuCount"},
		},
		{
			name:    "fewer GPUs than tensor parallelism",
			fixture: llmFixture,
			mutate:  llmParams(`{"gpuCount":1,"tensorParallelSize":2}`),
			want:    []string{"spec.components[llmEngine].parameters.gpuCount"},
		},
		{
			name:    "duplicate LoRA adapter names",
			fixture: llmFixture,
			mutate:  llmParams(`{"lora":{"adapters":[{"name":"ft","uri":"hf://org/a"},{"name":"ft","uri":"hf://org/b"}]}}`),
			want:    []string{"spec.components[llmEngine].parameters.lora.adapters[1].name"},
		},
		{
			name:    "LoRA enabled without a slot",
			fixture: llmFixture,
			mutate:  llmParams(`{"loraDeployment":"enabled"}`),
			want:    []string{"spec.components[llmEngine].parameters.loraSlot1"},
		},
		{
			name:    "predictor name KServe rejects",
			fixture: predictorFixture,
			mutate:  func(in *corev1alpha1.Instance) { in.Name = "bge-small-en-v1.5" },
			want:    []string{"metadata.name"},
		},
		{
			name:    "model not in the catalog",
			fixture: predictorFixture,
			mutate:  predictorParams(`{"model":"nope","modelFormat":"","storageURI":""}`),
			want:    []string{"spec.components[predictor].parameters.model"},
		},
		{
			name:    "serving runtime without a storage URI",
			fixture: predictorFixture,
			mutate:  predictorParams(`{"storageURI":""}`),
			want:    []string{"spec.components[predictor].parameters.storageURI"},
		},
		{
			name:    "image with a model format",
			fixture: predictorFixture,
			mutate:  predictorParams(`{"image":"ghcr.io/example/server:1","storageURI":""}`),
			want:    []string{"spec.components[predictor].parameters.modelFormat"},
		},
		{
			name:    "image field without an image",
			fixture: predictorFixture,
			mutate:  predictorParams(`{"healthPath":"/health"}`),
			want:    []string{"spec.components[predictor].parameters.healthPath"},
		},
		{
			name:    "predictor scaled to zero",
			fixture: predictorFixture,
			mutate:  predictorParams(`{"minReplicas":0}`),
			want:    []string{"spec.components[predictor].parameters.minReplicas"},
		},
		{
			name:    "predictor replicas zero",
			fixture: predictorFixture,
			mutate:  component(common.ComponentPredictor, func(c *corev1alpha1.ComponentSpec) { c.Replicas = new(int32(0)) }),
			want:    []string{"spec.components[predictor].replicas"},
		},
		{
			name:    "negative predictor GPU count",
			fixture: predictorFixture,
			mutate:  predictorParams(`{"gpuCount":-1}`),
			want:    []string{"spec.components[predictor].parameters.gpuCount"},
		},
		{
			name:    "invalid storage container name",
			fixture: predictorFixture,
			mutate:  predictorParams(`{"storageContainer":"S3_Init"}`),
			want:    []string{"spec.components[predictor].parameters.storageContainer"},
		},
		{
			name:    "predictor ExternalName service",
			fixture: predictorFixture,
			mutate: component(common.ComponentPredictor, func(c *corev1alpha1.ComponentSpec) {
				c.Service = &corev1alpha1.Service{ServiceType: "ExternalName"}
			}),
			want: []string{"spec.components[predictor].service.serviceType"},
		},
		{
			name:    "unknown predictor external access",
			fixture: predictorFixture,
			mutate:  withTopologyParameters(`{"externalAccess":"Ingress"}`),
			want:    []string{"spec.topology.parameters.externalAccess"},
		},
		{
			name:    "backend key without the AI Gateway",
			fixture: predictorFixture,
			mutate:  predictorParams(`{"image":"vllm/vllm-openai","modelFormat":"","storageURI":"","backendAPIKeySecret":"key"}`),
			want:    []string{"spec.components[predictor].parameters.backendAPIKeySecret"},
		},
		{
			name:    "label the provider manages",
			fixture: predictorFixture,
			mutate:  predictorParams(`{"labels":{"core.openeverest.io/instance":"other"}}`),
			want:    []string{"spec.components[predictor].parameters.labels[core.openeverest.io/instance]"},
		},
		{
			name:    "deployment mode annotation",
			fixture: predictorFixture,
			mutate:  predictorParams(`{"annotations":{"serving.kserve.io/deploymentMode":"Serverless"}}`),
			want:    []string{"spec.components[predictor].parameters.annotations[serving.kserve.io/deploymentMode]"},
		},
		{
			name:    "predictor config with metadata",
			fixture: predictorFixture,
			mutate:  predictorParams(`{"config":"metadata:\n  name: other"}`),
			want:    []string{"spec.components[predictor].parameters.config"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := validationtest.Load(t, tt.fixture)
			if tt.mutate != nil {
				tt.mutate(in)
			}

			res := v.Run(in, nil)

			var got []string
			for _, err := range res.Errors {
				got = append(got, err.Field)
			}
			assert.Equal(t, tt.want, got, "errors: %v", res.Errors)
		})
	}
}

func TestValidateReferencesLLMModelName(t *testing.T) {
	t.Setenv("AI_GATEWAY_ENABLED", "true")
	t.Setenv("AI_GATEWAY_NAME", "gw")
	t.Setenv("AI_GATEWAY_NAMESPACE", "gw-ns")
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(aiGatewayRouteGVK, meta.RESTScopeNamespace)
	other := aiGatewayRouteObject("other", "chat-ai-gateway", "gw", "gw-ns", "shared")
	cl := fake.NewClientBuilder().WithScheme(testScheme(t)).WithRESTMapper(mapper).WithObjects(other).Build()
	in := llmContext(t, nil, components.VllmCustomSpec{ModelName: "shared"}, llm.LlmTopologyParameters{
		ExternalAccess: llm.ExternalAccessEnvoyAIGateway,
	}).Instance()

	errs, err := (&Provider{}).ValidateReferences(controller.NewContext(t.Context(), cl, in, common.ProviderName))
	require.NoError(t, err)
	require.Len(t, errs, 1)
	assert.Equal(t, "spec.components[llmEngine].parameters.modelName", errs[0].Field)
	assert.Contains(t, errs[0].Detail, "other/chat-ai-gateway")
}

func TestValidateReferencesPredictorConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		patchErr error
		wantErrs bool
		wantErr  bool
	}{
		{name: "accepted"},
		{
			name:     "invalid field value",
			patchErr: apierrors.NewInvalid(schema.GroupKind{Group: "serving.kserve.io", Kind: "InferenceService"}, "iris", nil),
			wantErrs: true,
		},
		{
			name: "field the CRD does not declare",
			patchErr: &apierrors.StatusError{ErrStatus: metav1.Status{
				Status: metav1.StatusFailure, Code: http.StatusInternalServerError,
				Message: "failed to create typed patch object: .spec.predictor.model.unknownField: field not declared in schema",
			}},
			wantErrs: true,
		},
		{name: "webhook unreachable", patchErr: apierrors.NewInternalError(errors.New("failed calling webhook")), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cl := fake.NewClientBuilder().WithScheme(testScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
				Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
					return tt.patchErr
				},
			}).Build()
			c := predictorContext(t, cl, corev1alpha1.ComponentSpec{Parameters: rawJSON(t, components.ModelServerCustomSpec{
				ModelFormat: "sklearn", StorageURI: "gs://bucket/iris", Config: "predictor:\n  minReplicas: 2",
			})})

			errs, err := (&Provider{}).ValidateReferences(c)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if !tt.wantErrs {
				assert.Empty(t, errs)
				return
			}
			require.Len(t, errs, 1)
			assert.Equal(t, "spec.components[predictor].parameters.config", errs[0].Field)
		})
	}
}
