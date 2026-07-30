package provider

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/openeverest/provider-kserve/definition/topologies/llm"
)

func TestServedModelName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		instance   string
		configured string
		expected   string
	}{
		{name: "configured name", instance: "instance", configured: "model", expected: "model"},
		{name: "instance fallback", instance: "instance", expected: "instance"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := servedModelName(tt.instance, tt.configured); got != tt.expected {
				t.Fatalf("servedModelName() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestGatewayRoutingEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		params   llm.LlmTopologyParameters
		expected bool
	}{
		{name: "disabled", params: llm.LlmTopologyParameters{}},
		{
			name: "kserve routing",
			params: llm.LlmTopologyParameters{
				EnableGatewayRouting: true,
			},
			expected: true,
		},
		{
			name: "ai gateway implies routing",
			params: llm.LlmTopologyParameters{
				EnableAIGateway: true,
			},
			expected: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := gatewayRoutingEnabled(tt.params); got != tt.expected {
				t.Fatalf("gatewayRoutingEnabled() = %t, want %t", got, tt.expected)
			}
		})
	}
}

func TestRoutingConfigRefs(t *testing.T) {
	t.Parallel()

	if got := routingConfigRefs(llm.LlmTopologyParameters{}); got != nil {
		t.Fatalf("routingConfigRefs() = %#v, want nil", got)
	}

	aiGateway := routingConfigRefs(llm.LlmTopologyParameters{EnableAIGateway: true})
	if len(aiGateway) != 1 || aiGateway[0] != "kserve-config-llm-scheduler" {
		t.Fatalf("AI Gateway refs = %#v, want scheduler only", aiGateway)
	}
	kserveGateway := routingConfigRefs(llm.LlmTopologyParameters{EnableGatewayRouting: true})
	if len(kserveGateway) != 2 ||
		kserveGateway[0] != "kserve-config-llm-scheduler" ||
		kserveGateway[1] != "kserve-config-llm-router-route" {
		t.Fatalf("KServe gateway refs = %#v, want scheduler and route", kserveGateway)
	}
}

func TestBuildAIGatewayRoute(t *testing.T) {
	t.Parallel()

	route := buildAIGatewayRoute("chat", "models", "llama", "shared", "gateway-system")
	if route.GetName() != "chat-ai-gateway" {
		t.Fatalf("route name = %q, want chat-ai-gateway", route.GetName())
	}
	gvks, _, err := runtime.NewScheme().ObjectKinds(route)
	if err != nil || len(gvks) != 1 || gvks[0] != aiGatewayRouteGVK {
		t.Fatalf("route GVKs = %v, err %v", gvks, err)
	}

	parents, found, err := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	if err != nil || !found || len(parents) != 1 {
		t.Fatalf("parentRefs = %#v, found %t, err %v", parents, found, err)
	}
	parent := parents[0].(map[string]any)
	if parent["namespace"] != "gateway-system" {
		t.Fatalf("parent namespace = %v, want gateway-system", parent["namespace"])
	}

	rules, found, err := unstructured.NestedSlice(route.Object, "spec", "rules")
	if err != nil || !found || len(rules) != 1 {
		t.Fatalf("rules = %#v, found %t, err %v", rules, found, err)
	}
	rule := rules[0].(map[string]any)
	backends := rule["backendRefs"].([]any)
	backend := backends[0].(map[string]any)
	if backend["name"] != "chat-inference-pool" {
		t.Fatalf("backend name = %v, want chat-inference-pool", backend["name"])
	}

	costs, found, err := unstructured.NestedSlice(route.Object, "spec", "llmRequestCosts")
	if err != nil || !found || len(costs) != 3 {
		t.Fatalf("llmRequestCosts = %#v, found %t, err %v", costs, found, err)
	}
}

func TestBuildTokenRateLimitPolicy(t *testing.T) {
	t.Parallel()

	limit := int32(2500)
	policy := buildTokenRateLimitPolicy("chat", "models", "llama", &limit)

	rules, found, err := unstructured.NestedSlice(
		policy.Object,
		"spec",
		"rateLimit",
		"global",
		"rules",
	)
	if err != nil || !found || len(rules) != 1 {
		t.Fatalf("rules = %#v, found %t, err %v", rules, found, err)
	}
	rule := rules[0].(map[string]any)
	gotLimit := rule["limit"].(map[string]any)["requests"]
	if gotLimit != int64(limit) {
		t.Fatalf("token limit = %v, want %d", gotLimit, limit)
	}
	requestCost := rule["cost"].(map[string]any)["request"].(map[string]any)["number"]
	if requestCost != int64(0) {
		t.Fatalf("request cost = %v, want 0", requestCost)
	}
}

func TestGatewayConnectionDetails(t *testing.T) {
	t.Setenv("AI_GATEWAY_SCHEME", "https")
	t.Setenv("AI_GATEWAY_PORT", "8443")
	t.Setenv("AI_GATEWAY_HOSTNAME", "llm.example.com")

	details := gatewayConnectionDetails("gateway.example.com")
	if details.Host != "llm.example.com" || details.Port != "8443" {
		t.Fatalf("connection address = %s:%s", details.Host, details.Port)
	}
	if details.URI != "https://llm.example.com:8443" {
		t.Fatalf("connection URI = %q, want https://llm.example.com:8443", details.URI)
	}
}

func TestGatewayConnectionDetailsUsesGatewayAddressWithoutConfiguredHostname(t *testing.T) {
	t.Setenv("AI_GATEWAY_SCHEME", "http")
	t.Setenv("AI_GATEWAY_PORT", "80")
	t.Setenv("AI_GATEWAY_HOSTNAME", "")

	details := gatewayConnectionDetails("gateway.example.com")
	if details.Host != "gateway.example.com" || details.URI != "http://gateway.example.com" {
		t.Fatalf("connection details = %#v", details)
	}
}
