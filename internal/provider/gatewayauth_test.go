package provider

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func keySecret(namespace, name, id, value, models string) corev1.Secret {
	return corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   namespace,
			Name:        name,
			Labels:      map[string]string{aiGatewayKeyLabel: "true"},
			Annotations: map[string]string{aiGatewayKeyIDAnnotation: id, aiGatewayModelsAnnotation: models},
		},
		Data: map[string][]byte{aiGatewayKeyDataKey: []byte(value)},
	}
}

func TestResolveGatewayKeys(t *testing.T) {
	t.Parallel()

	routes := []unstructured.Unstructured{
		*aiGatewayRouteObject("a", "chat-ai-gateway", "gw", "gw-ns", "chat"),
		*aiGatewayRouteObject("b", "code-ai-gateway", "gw", "gw-ns", "code"),
		// Two routes on the gateway serve "dup": nobody may call it.
		*aiGatewayRouteObject("c", "dup1-ai-gateway", "gw", "gw-ns", "dup"),
		*aiGatewayRouteObject("d", "dup2-ai-gateway", "gw", "gw-ns", "dup"),
		*aiGatewayRouteObject("e", "elsewhere-ai-gateway", "other-gw", "gw-ns", "elsewhere"),
	}
	secrets := []corev1.Secret{
		keySecret("a", "chat-ai-gateway-key", "k_chat", "key-chat", "a/chat"),
		keySecret("x", "multi", "k_multi", "key-multi", "b/code,a/chat"),
		keySecret("c", "dup1-ai-gateway-key", "k_dup", "key-dup", "c/dup1"),
		keySecret("e", "elsewhere-ai-gateway-key", "k_elsewhere", "key-elsewhere", "e/elsewhere"),
		keySecret("z", "off", "k_off", "key-off", "z/not-on-gateway"),
		keySecret("y", "bad-id", "BAD", "key-bad", "a/chat"),
		keySecret("y", "empty", "k_empty", "", "a/chat"),
		keySecret("zz", "copy", "k_chat", "key-copy", "a/chat"),
	}

	keys, warnings := resolveGatewayKeys(secrets, routes, "gw", "gw-ns")

	got := map[string]string{}
	for _, key := range keys {
		got[key.id] = key.value + ":" + strings.Join(key.models, ",")
	}
	want := map[string]string{
		"k_chat":  "key-chat:chat",
		"k_multi": "key-multi:chat,code",
	}
	if len(got) != len(want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	for id, value := range want {
		if got[id] != value {
			t.Fatalf("key %s = %q, want %q (all keys %v)", id, got[id], value, got)
		}
	}
	for _, expected := range []string{"invalid key id", "empty api_key", "duplicate key id", "served by several routes"} {
		if !strings.Contains(strings.Join(warnings, "\n"), expected) {
			t.Fatalf("warnings %v do not mention %q", warnings, expected)
		}
	}
}

func TestBuildGatewayAuthPolicy(t *testing.T) {
	t.Parallel()

	t.Run("denies everything without keys", func(t *testing.T) {
		policy := buildGatewayAuthPolicy("gw-api-keys", "gw", "gw-ns", nil)
		authorization, _, _ := unstructured.NestedMap(policy.Object, "spec", "authorization")
		if authorization["defaultAction"] != "Deny" || authorization["rules"] != nil {
			t.Fatalf("authorization = %v, want deny-all", authorization)
		}
	})

	t.Run("allows each key only its models", func(t *testing.T) {
		policy := buildGatewayAuthPolicy("gw-api-keys", "gw", "gw-ns", []gatewayKey{
			{id: "k_a", value: "x", models: []string{"chat", "code"}},
		})
		rules, _, _ := unstructured.NestedSlice(policy.Object, "spec", "authorization", "rules")
		if len(rules) != 1 {
			t.Fatalf("rules = %v, want one", rules)
		}
		headers := rules[0].(map[string]any)["principal"].(map[string]any)["headers"].([]any)
		keyHeader := headers[0].(map[string]any)
		modelHeader := headers[1].(map[string]any)
		if keyHeader["name"] != aiGatewayKeyIDHeader || keyHeader["values"].([]any)[0] != "k_a" {
			t.Fatalf("key header = %v", keyHeader)
		}
		if modelHeader["name"] != aiGatewayModelHeader || len(modelHeader["values"].([]any)) != 2 {
			t.Fatalf("model header = %v", modelHeader)
		}

		apiKeyAuth, _, _ := unstructured.NestedMap(policy.Object, "spec", "apiKeyAuth")
		if apiKeyAuth["forwardClientIDHeader"] != aiGatewayKeyIDHeader || apiKeyAuth["sanitize"] != true {
			t.Fatalf("apiKeyAuth = %v", apiKeyAuth)
		}
	})
}

func TestGatewayAuthReconcile(t *testing.T) {
	t.Setenv("AI_GATEWAY_ENABLED", "true")
	t.Setenv("AI_GATEWAY_AUTH_ENABLED", "true")

	gateway := unstructuredObject(gatewayGVK)
	gateway.SetNamespace("gw-ns")
	gateway.SetName("gw")
	gateway.SetUID("gw-uid")
	chatKey := keySecret("a", "chat-ai-gateway-key", "k_chat", "key-chat", "a/chat")

	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(aiGatewayRouteGVK, meta.RESTScopeNamespace)
	mapper.Add(gatewayGVK, meta.RESTScopeNamespace)
	mapper.Add(securityPolicyGVK, meta.RESTScopeNamespace)
	mapper.Add(corev1.SchemeGroupVersion.WithKind("Secret"), meta.RESTScopeNamespace)
	cl := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithRESTMapper(mapper).
		WithObjects(gateway, &chatKey, aiGatewayRouteObject("a", "chat-ai-gateway", "gw", "gw-ns", "chat")).
		Build()
	r := &gatewayAuthReconciler{client: cl, gatewayName: "gw", gatewayNamespace: "gw-ns"}

	if _, err := r.Reconcile(context.Background(), reconcile.Request{}); err != nil {
		t.Fatal(err)
	}

	credentials := &corev1.Secret{}
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: "gw-ns", Name: "gw-api-keys"}, credentials); err != nil {
		t.Fatal(err)
	}
	if string(credentials.Data["k_chat"]) != "key-chat" || len(credentials.Data[gatewayPlaceholderClientID]) == 0 {
		t.Fatalf("credentials data keys = %v", credentials.Data)
	}
	placeholder := string(credentials.Data[gatewayPlaceholderClientID])

	policy := unstructuredObject(securityPolicyGVK)
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: "gw-ns", Name: "gw-api-keys"}, policy); err != nil {
		t.Fatal(err)
	}
	if rules, _, _ := unstructured.NestedSlice(policy.Object, "spec", "authorization", "rules"); len(rules) != 1 {
		t.Fatalf("rules = %v, want one", rules)
	}

	t.Run("keeps the placeholder and drops deleted keys", func(t *testing.T) {
		if err := cl.Delete(context.Background(), &chatKey); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Reconcile(context.Background(), reconcile.Request{}); err != nil {
			t.Fatal(err)
		}
		updated := &corev1.Secret{}
		if err := cl.Get(context.Background(), client.ObjectKeyFromObject(credentials), updated); err != nil {
			t.Fatal(err)
		}
		if _, ok := updated.Data["k_chat"]; ok || string(updated.Data[gatewayPlaceholderClientID]) != placeholder {
			t.Fatalf("credentials after key deletion = %v", updated.Data)
		}
	})

	t.Run("removes its outputs when auth is disabled", func(t *testing.T) {
		t.Setenv("AI_GATEWAY_AUTH_ENABLED", "false")
		if _, err := r.Reconcile(context.Background(), reconcile.Request{}); err != nil {
			t.Fatal(err)
		}
		if err := cl.Get(context.Background(), client.ObjectKeyFromObject(credentials), &corev1.Secret{}); client.IgnoreNotFound(err) != nil || err == nil {
			t.Fatalf("credentials still present: %v", err)
		}
	})
}
