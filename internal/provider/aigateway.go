package provider

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	knativekmeta "knative.dev/pkg/kmeta"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/internal/common"
)

const (
	defaultTokenLimitPerHour int32 = 1000
	aiGatewayRouteSuffix           = "-ai-gateway"
	inferencePoolSuffix            = "-inference-pool"

	aiGatewayModelHeader = "x-ai-eg-model"
	// aiGatewayKeyIDHeader carries the authenticated key ID; the Gateway strips
	// any client-supplied value before authentication.
	aiGatewayKeyIDHeader = "x-openeverest-key-id"
	// legacyUserIDHeader is the unauthenticated quota bucket used when API keys
	// are disabled.
	legacyUserIDHeader = "x-user-id"

	openAIBasePath = "/v1"
)

var (
	aiGatewayRouteGVK = schema.GroupVersionKind{
		Group: "aigateway.envoyproxy.io", Version: "v1alpha1", Kind: "AIGatewayRoute",
	}
	backendTrafficPolicyGVK = schema.GroupVersionKind{
		Group: "gateway.envoyproxy.io", Version: "v1alpha1", Kind: "BackendTrafficPolicy",
	}
	gatewayGVK = schema.GroupVersionKind{
		Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway",
	}
)

func syncAIGateway(c *controller.Context, modelName string, tokenLimit *int32) error {
	if !common.AIGatewayEnabled() {
		return fmt.Errorf("ai gateway is enabled for the instance but disabled in the provider chart")
	}
	gatewayName := common.AIGatewayName()
	gatewayNamespace := common.AIGatewayNamespace()
	if gatewayName == "" || gatewayNamespace == "" {
		return fmt.Errorf("ai gateway is enabled for the instance but the provider gateway is not configured")
	}

	// The key must exist before the route so the model is never reachable
	// without one; the Gateway denies routes that have no key rule yet.
	if common.AIGatewayAuthEnabled() {
		if err := ensureAIGatewayKey(c); err != nil {
			return err
		}
	}

	route := buildAIGatewayRoute(c.Name(), c.Instance().Namespace, modelName, gatewayName, gatewayNamespace)
	if err := common.Apply(c.Context(), c.Client(), c.Instance(), route); err != nil {
		return fmt.Errorf("applying ai gateway route: %w", err)
	}

	if common.RateLimitRedisURL() == "" {
		return deleteAIGatewayObject(c, backendTrafficPolicyGVK, "-token-limit")
	}

	policy := buildTokenRateLimitPolicy(c.Name(), c.Instance().Namespace, modelName, tokenLimit)
	if err := common.Apply(c.Context(), c.Client(), c.Instance(), policy); err != nil {
		return fmt.Errorf("applying ai gateway token rate limit: %w", err)
	}
	return nil
}

func cleanupAIGateway(c *controller.Context) error {
	if err := deleteAIGatewayObject(c, aiGatewayRouteGVK, aiGatewayRouteSuffix); err != nil {
		return err
	}
	return deleteAIGatewayObject(c, backendTrafficPolicyGVK, "-token-limit")
}

// validateAIGatewayAccess enforces the invariants API keys rely on: keys only
// travel over HTTPS, and a model name maps to exactly one route because the
// Gateway authorizes keys by model name.
func validateAIGatewayAccess(c *controller.Context, modelName string) error {
	if common.AIGatewayAuthEnabled() && common.AIGatewayScheme() != "https" && !common.AIGatewayAuthAllowInsecureHTTP() {
		return fmt.Errorf("API keys for the Envoy AI Gateway require HTTPS: enable aiGateway.tls in the provider chart " +
			"(or set aiGateway.auth.allowInsecureHTTP for local development)")
	}

	routes := &unstructured.UnstructuredList{}
	routes.SetGroupVersionKind(aiGatewayRouteGVK.GroupVersion().WithKind(aiGatewayRouteGVK.Kind + "List"))
	if err := c.Client().List(c.Context(), routes); err != nil {
		return fmt.Errorf("listing ai gateway routes: %w", err)
	}
	ownRoute := aiGatewayRouteName(c.Name())
	for i := range routes.Items {
		route := &routes.Items[i]
		if route.GetNamespace() == c.Namespace() && route.GetName() == ownRoute {
			continue
		}
		if !routeAttachedToGateway(route, common.AIGatewayName(), common.AIGatewayNamespace()) {
			continue
		}
		if slices.Contains(aiGatewayRouteModels(route), modelName) {
			return fmt.Errorf("model name %q is already served through the Envoy AI Gateway by %s/%s; set a different %s.parameters.modelName",
				modelName, route.GetNamespace(), route.GetName(), common.ComponentLlmEngine)
		}
	}
	return nil
}

func deleteAIGatewayObject(c *controller.Context, gvk schema.GroupVersionKind, suffix string) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	obj.SetName(knativekmeta.ChildName(c.Name(), suffix))
	obj.SetNamespace(c.Instance().Namespace)
	return c.Delete(obj)
}

func aiGatewayRouteName(instanceName string) string {
	return knativekmeta.ChildName(instanceName, aiGatewayRouteSuffix)
}

// aiGatewayRouteModels returns the model names an AIGatewayRoute matches on.
func aiGatewayRouteModels(route *unstructured.Unstructured) []string {
	rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
	var models []string
	for _, rawRule := range rules {
		rule, _ := rawRule.(map[string]any)
		matches, _, _ := unstructured.NestedSlice(rule, "matches")
		for _, rawMatch := range matches {
			match, _ := rawMatch.(map[string]any)
			headers, _, _ := unstructured.NestedSlice(match, "headers")
			for _, rawHeader := range headers {
				header, _ := rawHeader.(map[string]any)
				name, _ := header["name"].(string)
				matchType, _ := header["type"].(string)
				value, _ := header["value"].(string)
				if strings.EqualFold(name, aiGatewayModelHeader) && (matchType == "" || matchType == "Exact") && value != "" {
					models = append(models, value)
				}
			}
		}
	}
	return models
}

// routeAttachedToGateway reports whether the route references the given Gateway.
func routeAttachedToGateway(route *unstructured.Unstructured, gatewayName, gatewayNamespace string) bool {
	parents, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	for _, rawParent := range parents {
		parent, _ := rawParent.(map[string]any)
		name, _ := parent["name"].(string)
		namespace, _ := parent["namespace"].(string)
		kind, _ := parent["kind"].(string)
		if namespace == "" {
			namespace = route.GetNamespace()
		}
		if name == gatewayName && namespace == gatewayNamespace && (kind == "" || kind == gatewayGVK.Kind) {
			return true
		}
	}
	return false
}

func buildAIGatewayRoute(
	instanceName,
	namespace,
	modelName,
	gatewayName,
	gatewayNamespace string,
) *unstructured.Unstructured {
	name := knativekmeta.ChildName(instanceName, aiGatewayRouteSuffix)
	parent := map[string]any{
		"name":      gatewayName,
		"namespace": gatewayNamespace,
		"kind":      "Gateway",
		"group":     gatewayGVK.Group,
	}
	if listener := common.AIGatewayListenerName(); listener != "" {
		parent["sectionName"] = listener
	}
	route := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiGatewayRouteGVK.GroupVersion().String(),
		"kind":       aiGatewayRouteGVK.Kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]any{
			"parentRefs": []any{parent},
			"rules": []any{map[string]any{
				"matches": []any{map[string]any{
					"headers": []any{map[string]any{
						"type":  "Exact",
						"name":  aiGatewayModelHeader,
						"value": modelName,
					}},
				}},
				"backendRefs": []any{map[string]any{
					"group": "inference.networking.k8s.io",
					"kind":  "InferencePool",
					"name":  knativekmeta.ChildName(instanceName, inferencePoolSuffix),
				}},
				"timeouts": map[string]any{"request": "60s"},
			}},
			"llmRequestCosts": []any{
				map[string]any{"metadataKey": "llm_input_token", "type": "InputToken"},
				map[string]any{"metadataKey": "llm_output_token", "type": "OutputToken"},
				map[string]any{"metadataKey": "llm_total_token", "type": "TotalToken"},
			},
		},
	}}
	route.SetGroupVersionKind(aiGatewayRouteGVK)
	return route
}

func buildTokenRateLimitPolicy(
	instanceName,
	namespace,
	modelName string,
	configuredLimit *int32,
) *unstructured.Unstructured {
	limit := defaultTokenLimitPerHour
	if configuredLimit != nil {
		limit = *configuredLimit
	}

	name := knativekmeta.ChildName(instanceName, aiGatewayRouteSuffix)
	quotaBucketHeader := legacyUserIDHeader
	if common.AIGatewayAuthEnabled() {
		quotaBucketHeader = aiGatewayKeyIDHeader
	}
	policy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": backendTrafficPolicyGVK.GroupVersion().String(),
		"kind":       backendTrafficPolicyGVK.Kind,
		"metadata": map[string]any{
			"name":      knativekmeta.ChildName(instanceName, "-token-limit"),
			"namespace": namespace,
		},
		"spec": map[string]any{
			"targetRefs": []any{map[string]any{
				"name": name, "kind": "HTTPRoute", "group": gatewayGVK.Group,
			}},
			"rateLimit": map[string]any{
				"type": "Global",
				"global": map[string]any{
					"rules": []any{map[string]any{
						"clientSelectors": []any{map[string]any{
							"headers": []any{
								map[string]any{"name": quotaBucketHeader, "type": "Distinct"},
								map[string]any{
									"name": aiGatewayModelHeader, "type": "Exact", "value": modelName,
								},
							},
						}},
						"limit": map[string]any{
							"requests": int64(limit),
							"unit":     "Hour",
						},
						"cost": map[string]any{
							"request": map[string]any{"from": "Number", "number": int64(0)},
							"response": map[string]any{
								"from": "Metadata",
								"metadata": map[string]any{
									"namespace": "io.envoy.ai_gateway",
									"key":       "llm_total_token",
								},
							},
						},
					}},
				},
			},
		},
	}}
	policy.SetGroupVersionKind(backendTrafficPolicyGVK)
	return policy
}

func aiGatewayConnectionDetails(c *controller.Context, modelName string) (controller.ConnectionDetails, string, error) {
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(aiGatewayRouteGVK)
	routeName := aiGatewayRouteName(c.Name())
	if err := c.Client().Get(
		c.Context(),
		types.NamespacedName{Name: routeName, Namespace: c.Instance().Namespace},
		route,
	); err != nil {
		return controller.ConnectionDetails{}, "Waiting for Envoy AI Gateway route", nil
	}

	if accepted, message := routeAcceptance(route); !accepted {
		return controller.ConnectionDetails{}, message, nil
	}

	gateway := &unstructured.Unstructured{}
	gateway.SetGroupVersionKind(gatewayGVK)
	if err := c.Client().Get(
		c.Context(),
		types.NamespacedName{Name: common.AIGatewayName(), Namespace: common.AIGatewayNamespace()},
		gateway,
	); err != nil {
		return controller.ConnectionDetails{}, "Waiting for Envoy AI Gateway", nil
	}

	addresses, _, err := unstructured.NestedSlice(gateway.Object, "status", "addresses")
	if err != nil {
		return controller.ConnectionDetails{}, "", fmt.Errorf("reading ai gateway addresses: %w", err)
	}
	if len(addresses) == 0 {
		return controller.ConnectionDetails{}, "Waiting for Envoy AI Gateway address", nil
	}
	first, ok := addresses[0].(map[string]any)
	if !ok {
		return controller.ConnectionDetails{}, "", fmt.Errorf("invalid ai gateway address status")
	}
	address, _ := first["value"].(string)
	if address == "" {
		return controller.ConnectionDetails{}, "Waiting for Envoy AI Gateway address", nil
	}

	details := gatewayConnectionDetails(address)
	details.AdditionalProperties = map[string]string{"model": modelName}
	if common.AIGatewayAuthEnabled() {
		keyID, key, err := readAIGatewayKey(c)
		if err != nil {
			return controller.ConnectionDetails{}, "", fmt.Errorf("reading ai gateway api key: %w", err)
		}
		if key == "" {
			return controller.ConnectionDetails{}, "Waiting for Envoy AI Gateway API key", nil
		}
		details.Username = keyID
		details.Password = key
	}
	return details, "", nil
}

func routeAcceptance(route *unstructured.Unstructured) (bool, string) {
	conditions, _, err := unstructured.NestedSlice(route.Object, "status", "conditions")
	if err != nil || len(conditions) == 0 {
		return false, "Waiting for Envoy AI Gateway route"
	}
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok || condition["type"] != "Accepted" {
			continue
		}
		if condition["status"] == string(metav1.ConditionTrue) {
			return true, ""
		}
		if message, ok := condition["message"].(string); ok && message != "" {
			return false, message
		}
	}
	return false, "Waiting for Envoy AI Gateway route acceptance"
}

func gatewayConnectionDetails(address string) controller.ConnectionDetails {
	scheme := common.AIGatewayScheme()
	port := common.AIGatewayPort()
	host := address
	if parsed, err := url.Parse(address); err == nil && parsed.Host != "" {
		scheme = parsed.Scheme
		host = parsed.Hostname()
		if parsed.Port() != "" {
			port = parsed.Port()
		}
	}
	if parsedHost, parsedPort, err := net.SplitHostPort(address); err == nil {
		host = parsedHost
		port = parsedPort
	}
	if configuredHost := common.AIGatewayHostname(); configuredHost != "" {
		host = configuredHost
	}

	uriHost := host
	if net.ParseIP(host) != nil && strings.Contains(host, ":") {
		uriHost = "[" + host + "]"
	}
	if port != defaultPortForScheme(scheme) {
		uriHost = net.JoinHostPort(host, port)
	}
	return controller.ConnectionDetails{
		Type:     "kserve-ai-gateway",
		Provider: common.ProviderName,
		Host:     host,
		Port:     port,
		URI:      (&url.URL{Scheme: scheme, Host: uriHost, Path: openAIBasePath}).String(),
	}
}

func defaultPortForScheme(scheme string) string {
	if scheme == "https" {
		return strconv.Itoa(443)
	}
	return strconv.Itoa(80)
}
