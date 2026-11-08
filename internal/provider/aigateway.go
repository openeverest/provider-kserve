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
	"k8s.io/apimachinery/pkg/util/validation/field"
	knativekmeta "knative.dev/pkg/kmeta"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/definition/components"
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
	// typeSafeBasePath is where the Envoy AI Gateway serves the TypeSafe
	// System One API: clients POST {base}/v1/systemone.
	typeSafeBasePath = "/typesafe"
	// typeSafeAPIVersion is the TypeSafe API version the gateway forwards to:
	// POST /v1/systemone on the backend.
	typeSafeAPIVersion = "v1"
)

var (
	aiGatewayRouteGVK = schema.GroupVersionKind{
		Group: "aigateway.envoyproxy.io", Version: "v1beta1", Kind: "AIGatewayRoute",
	}
	backendTrafficPolicyGVK = schema.GroupVersionKind{
		Group: "gateway.envoyproxy.io", Version: "v1alpha1", Kind: "BackendTrafficPolicy",
	}
	aiServiceBackendGVK = schema.GroupVersionKind{
		Group: "aigateway.envoyproxy.io", Version: "v1beta1", Kind: "AIServiceBackend",
	}
	backendSecurityPolicyGVK = schema.GroupVersionKind{
		Group: "aigateway.envoyproxy.io", Version: "v1beta1", Kind: "BackendSecurityPolicy",
	}
	envoyBackendGVK = schema.GroupVersionKind{
		Group: "gateway.envoyproxy.io", Version: "v1alpha1", Kind: "Backend",
	}
	gatewayGVK = schema.GroupVersionKind{
		Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway",
	}
)

// syncAIGateway registers the Instance on the shared AI Gateway: its API key,
// any backend objects the route needs, the route to backendRef, and the token
// rate limit.
func syncAIGateway(c *controller.Context, modelName string, tokenLimit *int32, backendRef map[string]any, backends ...*unstructured.Unstructured) error {
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

	for _, backend := range backends {
		if err := common.Apply(c.Context(), c.Client(), c.Instance(), backend); err != nil {
			return fmt.Errorf("applying ai gateway %s: %w", backend.GetKind(), err)
		}
	}

	route := buildAIGatewayRoute(c.Name(), c.Instance().Namespace, modelName, gatewayName, gatewayNamespace, backendRef)
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

// validateAIGatewayChart checks the provider chart can serve externalAccess
// EnvoyAIGateway. API keys only travel over HTTPS.
func validateAIGatewayChart(path *field.Path, externalAccess string) field.ErrorList {
	if !common.AIGatewayEnabled() {
		return field.ErrorList{field.Invalid(path, externalAccess, "requires aiGateway.enabled in the provider chart")}
	}
	if common.AIGatewayAuthEnabled() && common.AIGatewayScheme() != "https" && !common.AIGatewayAuthAllowInsecureHTTP() {
		return field.ErrorList{field.Invalid(path, externalAccess, "API keys for the Envoy AI Gateway require HTTPS: enable aiGateway.tls in the provider chart "+
			"(or set aiGateway.auth.allowInsecureHTTP for local development)")}
	}
	return nil
}

// aiGatewayRouteServing returns the namespace/name of another route on the
// provider's Gateway that serves modelName, or "" when there is none. A model
// name maps to exactly one route because the Gateway authorizes keys by model
// name.
func aiGatewayRouteServing(c *controller.Context, modelName string) (string, error) {
	routes := &unstructured.UnstructuredList{}
	routes.SetGroupVersionKind(aiGatewayRouteGVK.GroupVersion().WithKind(aiGatewayRouteGVK.Kind + "List"))
	if err := c.Client().List(c.Context(), routes); err != nil {
		return "", fmt.Errorf("listing ai gateway routes: %w", err)
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
			return route.GetNamespace() + "/" + route.GetName(), nil
		}
	}
	return "", nil
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

// inferencePoolBackendRef routes to the llm topology's InferencePool.
func inferencePoolBackendRef(instanceName string) map[string]any {
	return map[string]any{
		"group": "inference.networking.k8s.io",
		"kind":  "InferencePool",
		"name":  knativekmeta.ChildName(instanceName, inferencePoolSuffix),
	}
}

// aiGatewaySchema is the API a backend serves to the Envoy AI Gateway.
type aiGatewaySchema struct {
	// name is the Envoy AI Gateway schema name: OpenAI or TypeSafe.
	name string
	// prefix is where an OpenAI backend serves the API, e.g. /v1.
	prefix string
}

// spec is the AIServiceBackend spec.schema.
func (s aiGatewaySchema) spec() map[string]any {
	if s.name == components.APISchemaTypeSafe {
		return map[string]any{"name": s.name, "version": typeSafeAPIVersion}
	}
	return map[string]any{"name": s.name, "prefix": s.prefix}
}

// clientBasePath is the gateway path clients use as their SDK base URL.
func (s aiGatewaySchema) clientBasePath() string {
	if s.name == components.APISchemaTypeSafe {
		return typeSafeBasePath
	}
	return openAIBasePath
}

// buildServiceBackends returns the Envoy Gateway Backend for an in-cluster
// Service and the AIServiceBackend that speaks apiSchema to it; an
// AIServiceBackend cannot reference a Service directly. Both share the
// route's name.
func buildServiceBackends(instanceName, namespace, host string, port int32, apiSchema aiGatewaySchema) (backend, aiBackend *unstructured.Unstructured) {
	name := aiGatewayRouteName(instanceName)
	backend = &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": envoyBackendGVK.GroupVersion().String(),
		"kind":       envoyBackendGVK.Kind,
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"spec": map[string]any{
			"endpoints": []any{map[string]any{
				"fqdn": map[string]any{"hostname": host, "port": int64(port)},
			}},
		},
	}}
	backend.SetGroupVersionKind(envoyBackendGVK)
	aiBackend = &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiServiceBackendGVK.GroupVersion().String(),
		"kind":       aiServiceBackendGVK.Kind,
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"spec": map[string]any{
			"schema": apiSchema.spec(),
			"backendRef": map[string]any{
				"group": envoyBackendGVK.Group,
				"kind":  envoyBackendGVK.Kind,
				"name":  name,
			},
		},
	}}
	aiBackend.SetGroupVersionKind(aiServiceBackendGVK)
	return backend, aiBackend
}

// buildBackendAPIKeyPolicy makes the Envoy AI Gateway authenticate to the
// route's AIServiceBackend with the apiKey entry of secretName, sent as a
// bearer token. The client's own gateway key never reaches the backend.
func buildBackendAPIKeyPolicy(instanceName, namespace, secretName string) *unstructured.Unstructured {
	name := aiGatewayRouteName(instanceName)
	policy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": backendSecurityPolicyGVK.GroupVersion().String(),
		"kind":       backendSecurityPolicyGVK.Kind,
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"spec": map[string]any{
			"targetRefs": []any{map[string]any{
				"group": aiServiceBackendGVK.Group,
				"kind":  aiServiceBackendGVK.Kind,
				"name":  name,
			}},
			"type":   "APIKey",
			"apiKey": map[string]any{"secretRef": map[string]any{"name": secretName}},
		},
	}}
	policy.SetGroupVersionKind(backendSecurityPolicyGVK)
	return policy
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
	backendRef map[string]any,
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
				"backendRefs": []any{backendRef},
				"timeouts":    map[string]any{"request": "60s"},
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

// aiGatewayConnectionDetails returns the gateway address, model and API key
// once the route is accepted; basePath is the API's path on the gateway.
func aiGatewayConnectionDetails(c *controller.Context, modelName, basePath string) (controller.ConnectionDetails, string, error) {
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

	details := gatewayConnectionDetails(address, basePath)
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

func gatewayConnectionDetails(address, basePath string) controller.ConnectionDetails {
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
		URI:      (&url.URL{Scheme: scheme, Host: uriHost, Path: basePath}).String(),
	}
}

func defaultPortForScheme(scheme string) string {
	if scheme == "https" {
		return strconv.Itoa(443)
	}
	return strconv.Itoa(80)
}
