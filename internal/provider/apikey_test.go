package provider

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/definition/components"
	"github.com/openeverest/provider-kserve/definition/topologies/llm"
	"github.com/openeverest/provider-kserve/internal/common"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func keyContext(t *testing.T, cl client.Client) *controller.Context {
	t.Helper()
	inst := &corev1alpha1.Instance{ObjectMeta: metav1.ObjectMeta{Name: "llama", Namespace: "ns", UID: "uid-1"}}
	return controller.NewContext(context.Background(), cl, inst, common.ProviderName)
}

func getKeySecret(t *testing.T, cl client.Client) *corev1.Secret {
	t.Helper()
	secret := &corev1.Secret{}
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "llama-ai-gateway-key"}, secret); err != nil {
		t.Fatal(err)
	}
	return secret
}

func TestNewAPIKeyFormats(t *testing.T) {
	t.Parallel()

	id1, err := newAPIKeyID()
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := newAPIKeyID()
	if !validAPIKeyID.MatchString(id1) || len(id1) != len(apiKeyIDPrefix)+16 || id1 == id2 {
		t.Fatalf("key ids %q, %q are not random k_ + 16 base32 chars", id1, id2)
	}

	key1, err := newAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	key2, _ := newAPIKey()
	if !strings.HasPrefix(key1, apiKeyPrefix) || len(key1) != len(apiKeyPrefix)+43 || key1 == key2 {
		t.Fatalf("keys %q, %q are not random oe_sk_ + 32 bytes", key1, key2)
	}
}

func TestParseModelRefs(t *testing.T) {
	t.Parallel()

	got := parseModelRefs(" b/two, a/one,,a/one ")
	if strings.Join(got, ",") != "a/one,b/two" {
		t.Fatalf("parseModelRefs() = %v, want canonical [a/one b/two]", got)
	}
}

func TestEnsureAIGatewayKey(t *testing.T) {
	t.Parallel()

	cl := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	c := keyContext(t, cl)

	if err := ensureAIGatewayKey(c); err != nil {
		t.Fatal(err)
	}
	created := getKeySecret(t, cl)
	id := created.Annotations[aiGatewayKeyIDAnnotation]
	key := string(created.Data[aiGatewayKeyDataKey])
	if !validAPIKeyID.MatchString(id) || !strings.HasPrefix(key, apiKeyPrefix) {
		t.Fatalf("generated key id %q / key %q", id, key)
	}
	if created.Labels[aiGatewayKeyLabel] != "true" ||
		created.Labels[aiGatewayKeyOwnerKindLabel] != aiGatewayKeyOwnerKindInstance ||
		created.Labels[aiGatewayKeyOwnerNameLabel] != "llama" ||
		created.Annotations[aiGatewayModelsAnnotation] != "ns/llama" {
		t.Fatalf("key metadata = %v / %v", created.Labels, created.Annotations)
	}
	if len(created.OwnerReferences) != 1 || created.OwnerReferences[0].UID != "uid-1" {
		t.Fatalf("owner references = %v, want the Instance", created.OwnerReferences)
	}

	t.Run("is never regenerated", func(t *testing.T) {
		if err := ensureAIGatewayKey(c); err != nil {
			t.Fatal(err)
		}
		again := getKeySecret(t, cl)
		if again.Annotations[aiGatewayKeyIDAnnotation] != id || string(again.Data[aiGatewayKeyDataKey]) != key {
			t.Fatal("existing key was rewritten")
		}
	})

	t.Run("restores metadata and missing value", func(t *testing.T) {
		tampered := getKeySecret(t, cl)
		delete(tampered.Labels, aiGatewayKeyLabel)
		tampered.Annotations[aiGatewayModelsAnnotation] = "other/model"
		delete(tampered.Data, aiGatewayKeyDataKey)
		if err := cl.Update(context.Background(), tampered); err != nil {
			t.Fatal(err)
		}

		if err := ensureAIGatewayKey(c); err != nil {
			t.Fatal(err)
		}
		fixed := getKeySecret(t, cl)
		if fixed.Labels[aiGatewayKeyLabel] != "true" || fixed.Annotations[aiGatewayModelsAnnotation] != "ns/llama" {
			t.Fatalf("metadata not restored: %v / %v", fixed.Labels, fixed.Annotations)
		}
		if fixed.Annotations[aiGatewayKeyIDAnnotation] != id {
			t.Fatal("key id changed")
		}
		if newKey := string(fixed.Data[aiGatewayKeyDataKey]); newKey == "" || newKey == key {
			t.Fatalf("missing key value not regenerated: %q", newKey)
		}
	})
}

func aiGatewayRouteObject(namespace, name, gatewayName, gatewayNamespace string, models ...string) *unstructured.Unstructured {
	route := buildAIGatewayRoute(strings.TrimSuffix(name, aiGatewayRouteSuffix), namespace, "", gatewayName, gatewayNamespace)
	var matches []any
	for _, model := range models {
		matches = append(matches, map[string]any{
			"headers": []any{map[string]any{"type": "Exact", "name": aiGatewayModelHeader, "value": model}},
		})
	}
	rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
	rules[0].(map[string]any)["matches"] = matches
	_ = unstructured.SetNestedSlice(route.Object, rules, "spec", "rules")
	return route
}

func TestValidateAIGatewayAccess(t *testing.T) {
	newContext := func(t *testing.T, routes ...client.Object) *controller.Context {
		t.Helper()
		mapper := meta.NewDefaultRESTMapper(nil)
		mapper.Add(aiGatewayRouteGVK, meta.RESTScopeNamespace)
		cl := fake.NewClientBuilder().WithScheme(testScheme(t)).WithRESTMapper(mapper).WithObjects(routes...).Build()
		return keyContext(t, cl)
	}
	gatewayEnv := func(t *testing.T, scheme, allowInsecure string) {
		t.Helper()
		t.Setenv("AI_GATEWAY_ENABLED", "true")
		t.Setenv("AI_GATEWAY_AUTH_ENABLED", "true")
		t.Setenv("AI_GATEWAY_NAME", "gw")
		t.Setenv("AI_GATEWAY_NAMESPACE", "gw-ns")
		t.Setenv("AI_GATEWAY_SCHEME", scheme)
		t.Setenv("AI_GATEWAY_AUTH_ALLOW_INSECURE_HTTP", allowInsecure)
	}

	t.Run("rejects plain HTTP", func(t *testing.T) {
		gatewayEnv(t, "http", "false")
		if err := validateAIGatewayAccess(newContext(t), "llama"); err == nil || !strings.Contains(err.Error(), "HTTPS") {
			t.Fatalf("err = %v, want HTTPS requirement", err)
		}
	})

	t.Run("allows plain HTTP when explicitly allowed", func(t *testing.T) {
		gatewayEnv(t, "http", "true")
		if err := validateAIGatewayAccess(newContext(t), "llama"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("rejects a model name served by another route", func(t *testing.T) {
		gatewayEnv(t, "https", "false")
		other := aiGatewayRouteObject("other", "chat-ai-gateway", "gw", "gw-ns", "llama")
		if err := validateAIGatewayAccess(newContext(t, other), "llama"); err == nil || !strings.Contains(err.Error(), "other/chat-ai-gateway") {
			t.Fatalf("err = %v, want conflict with other/chat-ai-gateway", err)
		}
	})

	t.Run("ignores its own route and other gateways", func(t *testing.T) {
		gatewayEnv(t, "https", "false")
		own := aiGatewayRouteObject("ns", "llama-ai-gateway", "gw", "gw-ns", "llama")
		elsewhere := aiGatewayRouteObject("other", "chat-ai-gateway", "another-gw", "gw-ns", "llama")
		if err := validateAIGatewayAccess(newContext(t, own, elsewhere), "llama"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestValidateLLMChecksAIGatewayAccess(t *testing.T) {
	t.Setenv("AI_GATEWAY_ENABLED", "true")
	t.Setenv("AI_GATEWAY_AUTH_ENABLED", "true")
	t.Setenv("AI_GATEWAY_SCHEME", "http")
	t.Setenv("AI_GATEWAY_AUTH_ALLOW_INSECURE_HTTP", "false")

	c := llmContext(t, nil, components.VllmCustomSpec{}, llm.LlmTopologyParameters{
		ExternalAccess: llm.ExternalAccessEnvoyAIGateway,
	})
	if err := validateLLM(c); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("err = %v, want HTTPS requirement", err)
	}
}
