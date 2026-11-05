package provider

import (
	"context"
	"reflect"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"

	"github.com/openeverest/provider-kserve/definition/components"
	"github.com/openeverest/provider-kserve/definition/topologies/predictor"
)

func TestValidatePredictorAIGateway(t *testing.T) {
	embeddings := components.ModelServerCustomSpec{ModelFormat: "huggingface", StorageURI: "hf://org/model", Task: "text_embedding"}
	gateway := predictor.PredictorTopologyParameters{ExternalAccess: predictor.ExternalAccessEnvoyAIGateway}
	cases := []struct {
		name    string
		enabled bool
		params  components.ModelServerCustomSpec
		topo    predictor.PredictorTopologyParameters
		routes  []client.Object
		wantErr string
	}{
		{name: "embedding model", enabled: true, params: embeddings, topo: gateway},
		{name: "chart gateway disabled", params: embeddings, topo: gateway, wantErr: "aiGateway.enabled"},
		{name: "not an embedding model", enabled: true, params: components.ModelServerCustomSpec{ModelFormat: "sklearn", StorageURI: "gs://bucket/iris"}, topo: gateway, wantErr: "an API the gateway routes"},
		{name: "huggingface without the embedding task", enabled: true, params: components.ModelServerCustomSpec{ModelFormat: "huggingface", StorageURI: "hf://org/model"}, topo: gateway, wantErr: "an API the gateway routes"},
		{name: "OpenAI-compatible image", enabled: true, params: components.ModelServerCustomSpec{Image: "vllm/vllm-openai", OpenAICompatible: true}, topo: gateway},
		{name: "image without an API schema", enabled: true, params: components.ModelServerCustomSpec{Image: "ghcr.io/example/server:1"}, topo: gateway, wantErr: "apiSchema"},
		{name: "TypeSafe image", enabled: true, params: components.ModelServerCustomSpec{Image: "vllm/vllm-openai", APISchema: "TypeSafe"}, topo: gateway},
		{
			name: "TypeSafe image with a backend key", enabled: true, topo: gateway,
			params: components.ModelServerCustomSpec{Image: "vllm/vllm-openai", APISchema: "TypeSafe", BackendAPIKeySecret: "openjev-shim"},
		},
		{
			name: "model name served by another route", enabled: true, params: embeddings, topo: gateway,
			routes:  []client.Object{aiGatewayRouteObject("other", "iris-ai-gateway", "gw", "gw-ns", "iris")},
			wantErr: "other/iris-ai-gateway",
		},
		{
			name: "token limit with the gateway", enabled: true, params: embeddings,
			topo: predictor.PredictorTopologyParameters{ExternalAccess: predictor.ExternalAccessEnvoyAIGateway, TokenLimitPerHour: ptr.To(int32(5000))},
		},
		{name: "token limit without the gateway", params: embeddings, topo: predictor.PredictorTopologyParameters{TokenLimitPerHour: ptr.To(int32(5000))}, wantErr: "requires externalAccess EnvoyAIGateway"},
		{
			name: "zero token limit", enabled: true, params: embeddings,
			topo:    predictor.PredictorTopologyParameters{ExternalAccess: predictor.ExternalAccessEnvoyAIGateway, TokenLimitPerHour: ptr.To(int32(0))},
			wantErr: "greater than zero",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.enabled {
				t.Setenv("AI_GATEWAY_ENABLED", "true")
				t.Setenv("AI_GATEWAY_AUTH_ENABLED", "true")
				t.Setenv("AI_GATEWAY_AUTH_ALLOW_INSECURE_HTTP", "true")
				t.Setenv("AI_GATEWAY_NAME", "gw")
				t.Setenv("AI_GATEWAY_NAMESPACE", "gw-ns")
			}
			mapper := meta.NewDefaultRESTMapper(nil)
			mapper.Add(aiGatewayRouteGVK, meta.RESTScopeNamespace)
			cl := fake.NewClientBuilder().WithScheme(testScheme(t)).WithRESTMapper(mapper).WithObjects(tc.routes...).Build()
			c := predictorContext(t, cl, corev1alpha1.ComponentSpec{Parameters: rawJSON(t, tc.params)})
			c.Instance().Spec.Topology.Parameters = rawJSON(t, tc.topo)
			errs := validatePredictor(c.Instance())
			if len(errs) == 0 {
				refErrs, err := (&Provider{}).ValidateReferences(c)
				if err != nil {
					t.Fatal(err)
				}
				errs = refErrs
			}
			err := errs.ToAggregate()
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

func TestBuildServiceBackends(t *testing.T) {
	backend, aiBackend := buildServiceBackends("minilm", "models", "minilm-predictor.models.svc.cluster.local", 80, aiGatewaySchema{name: "OpenAI", prefix: "/openai/v1"})

	if backend.GroupVersionKind() != envoyBackendGVK || backend.GetName() != "minilm-ai-gateway" || backend.GetNamespace() != "models" {
		t.Fatalf("backend = %s %s/%s", backend.GroupVersionKind(), backend.GetNamespace(), backend.GetName())
	}
	endpoints, _, _ := unstructured.NestedSlice(backend.Object, "spec", "endpoints")
	wantEndpoints := []any{map[string]any{"fqdn": map[string]any{"hostname": "minilm-predictor.models.svc.cluster.local", "port": int64(80)}}}
	if !reflect.DeepEqual(endpoints, wantEndpoints) {
		t.Fatalf("endpoints = %#v", endpoints)
	}

	if aiBackend.GroupVersionKind() != aiServiceBackendGVK || aiBackend.GetName() != "minilm-ai-gateway" {
		t.Fatalf("aiBackend = %s %s", aiBackend.GroupVersionKind(), aiBackend.GetName())
	}
	spec, _, _ := unstructured.NestedMap(aiBackend.Object, "spec")
	wantSpec := map[string]any{
		"schema":     map[string]any{"name": "OpenAI", "prefix": "/openai/v1"},
		"backendRef": map[string]any{"group": "gateway.envoyproxy.io", "kind": "Backend", "name": "minilm-ai-gateway"},
	}
	if !reflect.DeepEqual(spec, wantSpec) {
		t.Fatalf("aiBackend spec = %#v", spec)
	}
}

func TestBuildServiceBackendsTypeSafe(t *testing.T) {
	_, aiBackend := buildServiceBackends("openjev", "decisions", "openjev-predictor.decisions.svc.cluster.local", 80, aiGatewaySchema{name: "TypeSafe"})
	schema, _, _ := unstructured.NestedMap(aiBackend.Object, "spec", "schema")
	if want := map[string]any{"name": "TypeSafe", "version": "v1"}; !reflect.DeepEqual(schema, want) {
		t.Fatalf("schema = %#v, want %#v", schema, want)
	}
}

func TestBuildBackendAPIKeyPolicy(t *testing.T) {
	policy := buildBackendAPIKeyPolicy("openjev", "decisions", "openjev-shim")
	if policy.GroupVersionKind() != backendSecurityPolicyGVK || policy.GetName() != "openjev-ai-gateway" || policy.GetNamespace() != "decisions" {
		t.Fatalf("policy = %s %s/%s", policy.GroupVersionKind(), policy.GetNamespace(), policy.GetName())
	}
	spec, _, _ := unstructured.NestedMap(policy.Object, "spec")
	want := map[string]any{
		"targetRefs": []any{map[string]any{"group": "aigateway.envoyproxy.io", "kind": "AIServiceBackend", "name": "openjev-ai-gateway"}},
		"type":       "APIKey",
		"apiKey":     map[string]any{"secretRef": map[string]any{"name": "openjev-shim"}},
	}
	if !reflect.DeepEqual(spec, want) {
		t.Fatalf("policy spec = %#v, want %#v", spec, want)
	}
}

func TestPredictorAIGatewayServiceType(t *testing.T) {
	topo := predictor.PredictorTopologyParameters{ExternalAccess: predictor.ExternalAccessEnvoyAIGateway}
	if got := topo.ResolvedServiceType(); got != "ClusterIP" {
		t.Fatalf("ResolvedServiceType() = %q, want ClusterIP: the gateway needs no external Service", got)
	}
}

func TestSyncPredictorAIGatewayBackendAPIKey(t *testing.T) {
	t.Setenv("AI_GATEWAY_ENABLED", "true")
	t.Setenv("AI_GATEWAY_NAME", "gw")
	t.Setenv("AI_GATEWAY_NAMESPACE", "gw-ns")

	mapper := meta.NewDefaultRESTMapper(nil)
	for _, gvk := range []schema.GroupVersionKind{envoyBackendGVK, aiServiceBackendGVK, backendSecurityPolicyGVK, aiGatewayRouteGVK, backendTrafficPolicyGVK} {
		mapper.Add(gvk, meta.RESTScopeNamespace)
	}
	cl := fake.NewClientBuilder().WithScheme(testScheme(t)).WithRESTMapper(mapper).
		WithInterceptorFuncs(interceptor.Funcs{
			// The fake client cannot server-side apply CRD kinds it has no schema for.
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				err := cl.Create(ctx, obj)
				if !apierrors.IsAlreadyExists(err) {
					return err
				}
				existing := &unstructured.Unstructured{}
				existing.SetGroupVersionKind(obj.GetObjectKind().GroupVersionKind())
				if err := cl.Get(ctx, client.ObjectKeyFromObject(obj), existing); err != nil {
					return err
				}
				obj.SetResourceVersion(existing.GetResourceVersion())
				return cl.Update(ctx, obj)
			},
		}).Build()
	topo := predictor.PredictorTopologyParameters{ExternalAccess: predictor.ExternalAccessEnvoyAIGateway}
	params := components.ModelServerCustomSpec{Image: "vllm/vllm-openai", APISchema: "TypeSafe", BackendAPIKeySecret: "openjev-shim"}
	c := predictorContext(t, cl, corev1alpha1.ComponentSpec{Parameters: rawJSON(t, params)})

	if err := syncPredictorAIGateway(c, topo, params); err != nil {
		t.Fatal(err)
	}
	aiBackend := &unstructured.Unstructured{}
	aiBackend.SetGroupVersionKind(aiServiceBackendGVK)
	if err := cl.Get(t.Context(), client.ObjectKey{Namespace: "ns", Name: "iris-ai-gateway"}, aiBackend); err != nil {
		t.Fatal(err)
	}
	if name, _, _ := unstructured.NestedString(aiBackend.Object, "spec", "schema", "name"); name != "TypeSafe" {
		t.Fatalf("AIServiceBackend schema = %q, want TypeSafe", name)
	}
	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(backendSecurityPolicyGVK)
	key := client.ObjectKey{Namespace: "ns", Name: "iris-ai-gateway"}
	if err := cl.Get(t.Context(), key, policy); err != nil {
		t.Fatalf("BackendSecurityPolicy not created: %v", err)
	}

	params.BackendAPIKeySecret = ""
	if err := syncPredictorAIGateway(c, topo, params); err != nil {
		t.Fatal(err)
	}
	if err := cl.Get(t.Context(), key, policy); !apierrors.IsNotFound(err) {
		t.Fatalf("BackendSecurityPolicy after removing the secret: err = %v, want NotFound", err)
	}
}
