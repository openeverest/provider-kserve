package provider

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openeverest/provider-kserve/internal/common"
)

const (
	gatewayAuthControllerName = "ai-gateway-auth"
	gatewayAuthSuffix         = "-api-keys"
	gatewayCredentialsLabel   = "openeverest.io/ai-gateway-credentials"
	// gatewayPlaceholderClientID keeps apiKeyAuth valid when no keys exist. It
	// has no authorization rule, so it can never reach a model.
	gatewayPlaceholderClientID = "placeholder"
)

var (
	securityPolicyGVK = schema.GroupVersionKind{
		Group: "gateway.envoyproxy.io", Version: "v1alpha1", Kind: "SecurityPolicy",
	}
	validAPIKeyID = regexp.MustCompile(`^k_[a-z0-9]+$`)
)

// gatewayKey is one API key and the served model names it may call.
type gatewayKey struct {
	id     string
	value  string
	models []string
}

// gatewayAuthReconciler renders every API key Secret in the cluster into one
// credentials Secret and one SecurityPolicy on the shared AI Gateway. Envoy
// checks the key before the AI Gateway reads the model from the body, so
// authentication and the key-to-model authorization must both live at the
// Gateway level rather than on individual routes.
type gatewayAuthReconciler struct {
	client           client.Client
	gatewayName      string
	gatewayNamespace string
}

// SetupGatewayAuth registers the AI Gateway auth renderer on the manager.
func SetupGatewayAuth(mgr ctrl.Manager) error {
	if !common.AIGatewayEnabled() {
		return nil
	}
	r := &gatewayAuthReconciler{
		client:           mgr.GetClient(),
		gatewayName:      common.AIGatewayName(),
		gatewayNamespace: common.AIGatewayNamespace(),
	}
	enqueue := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: gatewayAuthControllerName}}}
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named(gatewayAuthControllerName).
		Watches(&corev1.Secret{}, enqueue, builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
			return isAIGatewayKeySecret(obj) || r.isOutput(obj)
		}))).
		Watches(unstructuredObject(aiGatewayRouteGVK), enqueue).
		Watches(unstructuredObject(securityPolicyGVK), enqueue, builder.WithPredicates(predicate.NewPredicateFuncs(r.isOutput))).
		// The Gateway's create event renders the deny-all policy at startup,
		// before any key or route exists.
		Watches(unstructuredObject(gatewayGVK), enqueue, builder.WithPredicates(predicate.NewPredicateFuncs(r.isGateway))).
		Complete(r)
}

func (r *gatewayAuthReconciler) isGateway(obj client.Object) bool {
	return obj.GetNamespace() == r.gatewayNamespace && obj.GetName() == r.gatewayName
}

func (r *gatewayAuthReconciler) outputName() string {
	return r.gatewayName + gatewayAuthSuffix
}

func (r *gatewayAuthReconciler) isOutput(obj client.Object) bool {
	return obj.GetNamespace() == r.gatewayNamespace && obj.GetName() == r.outputName()
}

func (r *gatewayAuthReconciler) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	if !common.AIGatewayAuthEnabled() {
		return reconcile.Result{}, r.deleteOutputs(ctx)
	}

	gateway := unstructuredObject(gatewayGVK)
	if err := r.client.Get(ctx, types.NamespacedName{Name: r.gatewayName, Namespace: r.gatewayNamespace}, gateway); err != nil {
		return reconcile.Result{}, fmt.Errorf("reading ai gateway: %w", err)
	}

	secrets := &corev1.SecretList{}
	if err := r.client.List(ctx, secrets, client.MatchingLabels{aiGatewayKeyLabel: "true"}); err != nil {
		return reconcile.Result{}, fmt.Errorf("listing api key secrets: %w", err)
	}
	routes := &unstructured.UnstructuredList{}
	routes.SetGroupVersionKind(aiGatewayRouteGVK.GroupVersion().WithKind(aiGatewayRouteGVK.Kind + "List"))
	if err := r.client.List(ctx, routes); err != nil {
		return reconcile.Result{}, fmt.Errorf("listing ai gateway routes: %w", err)
	}

	keys, warnings := resolveGatewayKeys(secrets.Items, routes.Items, r.gatewayName, r.gatewayNamespace)
	for _, warning := range warnings {
		log.FromContext(ctx).Info(warning)
	}

	placeholder, err := r.placeholderKey(ctx)
	if err != nil {
		return reconcile.Result{}, err
	}
	credentials := buildGatewayCredentials(r.outputName(), r.gatewayNamespace, placeholder, keys)
	if err := common.Apply(ctx, r.client, gateway, credentials); err != nil {
		return reconcile.Result{}, fmt.Errorf("applying ai gateway credentials: %w", err)
	}
	policy := buildGatewayAuthPolicy(r.outputName(), r.gatewayName, r.gatewayNamespace, keys)
	if err := common.Apply(ctx, r.client, gateway, policy); err != nil {
		return reconcile.Result{}, fmt.Errorf("applying ai gateway security policy: %w", err)
	}
	return reconcile.Result{}, nil
}

// placeholderKey reuses the stored placeholder value so re-renders do not
// rewrite the Secret.
func (r *gatewayAuthReconciler) placeholderKey(ctx context.Context) (string, error) {
	existing := &corev1.Secret{}
	err := r.client.Get(ctx, types.NamespacedName{Name: r.outputName(), Namespace: r.gatewayNamespace}, existing)
	if client.IgnoreNotFound(err) != nil {
		return "", fmt.Errorf("reading ai gateway credentials: %w", err)
	}
	if value := existing.Data[gatewayPlaceholderClientID]; len(value) > 0 {
		return string(value), nil
	}
	return newAPIKey()
}

func (r *gatewayAuthReconciler) deleteOutputs(ctx context.Context) error {
	policy := unstructuredObject(securityPolicyGVK)
	policy.SetName(r.outputName())
	policy.SetNamespace(r.gatewayNamespace)
	if err := r.client.Delete(ctx, policy); client.IgnoreNotFound(err) != nil && !meta.IsNoMatchError(err) {
		return fmt.Errorf("deleting ai gateway security policy: %w", err)
	}
	credentials := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: r.outputName(), Namespace: r.gatewayNamespace}}
	if err := r.client.Delete(ctx, credentials); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("deleting ai gateway credentials: %w", err)
	}
	return nil
}

// resolveGatewayKeys turns API key Secrets into keys with the served model
// names they may call. It fails closed: a model name served by more than one
// route is granted to nobody, and keys without any resolvable model are left
// out entirely.
func resolveGatewayKeys(
	secrets []corev1.Secret,
	routes []unstructured.Unstructured,
	gatewayName, gatewayNamespace string,
) ([]gatewayKey, []string) {
	routeModels := map[string][]string{}
	servedBy := map[string]int{}
	for i := range routes {
		route := &routes[i]
		if !routeAttachedToGateway(route, gatewayName, gatewayNamespace) {
			continue
		}
		models := aiGatewayRouteModels(route)
		slices.Sort(models)
		models = slices.Compact(models)
		routeModels[route.GetNamespace()+"/"+route.GetName()] = models
		for _, model := range models {
			servedBy[model]++
		}
	}

	sort.Slice(secrets, func(i, j int) bool {
		return secrets[i].Namespace+"/"+secrets[i].Name < secrets[j].Namespace+"/"+secrets[j].Name
	})

	var keys []gatewayKey
	var warnings []string
	seen := map[string]bool{}
	for i := range secrets {
		secret := &secrets[i]
		ref := secret.Namespace + "/" + secret.Name
		id := secret.Annotations[aiGatewayKeyIDAnnotation]
		value := string(secret.Data[aiGatewayKeyDataKey])
		switch {
		case !validAPIKeyID.MatchString(id):
			warnings = append(warnings, fmt.Sprintf("skipping api key secret %s: invalid key id %q", ref, id))
			continue
		case value == "":
			warnings = append(warnings, fmt.Sprintf("skipping api key secret %s: empty %s", ref, aiGatewayKeyDataKey))
			continue
		case seen[id]:
			warnings = append(warnings, fmt.Sprintf("skipping api key secret %s: duplicate key id %q", ref, id))
			continue
		}
		seen[id] = true

		var models []string
		for _, modelRef := range parseModelRefs(secret.Annotations[aiGatewayModelsAnnotation]) {
			namespace, instance, ok := strings.Cut(modelRef, "/")
			if !ok || namespace == "" || instance == "" {
				warnings = append(warnings, fmt.Sprintf("api key secret %s: invalid model ref %q", ref, modelRef))
				continue
			}
			for _, model := range routeModels[namespace+"/"+aiGatewayRouteName(instance)] {
				if servedBy[model] > 1 {
					warnings = append(warnings, fmt.Sprintf("api key secret %s: model %q is served by several routes; access denied", ref, model))
					continue
				}
				models = append(models, model)
			}
		}
		if len(models) == 0 {
			continue
		}
		slices.Sort(models)
		keys = append(keys, gatewayKey{id: id, value: value, models: slices.Compact(models)})
	}
	return keys, warnings
}

func buildGatewayCredentials(name, namespace, placeholder string, keys []gatewayKey) *corev1.Secret {
	data := map[string][]byte{gatewayPlaceholderClientID: []byte(placeholder)}
	for _, key := range keys {
		data[key.id] = []byte(key.value)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				managedByLabel:          common.ProviderName,
				gatewayCredentialsLabel: "true",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
}

func buildGatewayAuthPolicy(name, gatewayName, namespace string, keys []gatewayKey) *unstructured.Unstructured {
	rules := make([]any, 0, len(keys))
	for _, key := range keys {
		models := make([]any, 0, len(key.models))
		for _, model := range key.models {
			models = append(models, model)
		}
		rules = append(rules, map[string]any{
			"action": "Allow",
			"principal": map[string]any{
				"headers": []any{
					map[string]any{"name": aiGatewayKeyIDHeader, "values": []any{key.id}},
					map[string]any{"name": aiGatewayModelHeader, "values": models},
				},
			},
		})
	}
	authorization := map[string]any{"defaultAction": "Deny"}
	if len(rules) > 0 {
		authorization["rules"] = rules
	}
	target := map[string]any{"group": gatewayGVK.Group, "kind": gatewayGVK.Kind, "name": gatewayName}
	if listener := common.AIGatewayListenerName(); listener != "" {
		target["sectionName"] = listener
	}

	policy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": securityPolicyGVK.GroupVersion().String(),
		"kind":       securityPolicyGVK.Kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]any{
			"targetRefs": []any{target},
			"apiKeyAuth": map[string]any{
				"credentialRefs": []any{map[string]any{"name": name}},
				// Envoy strips a "Bearer " prefix; x-api-key serves Anthropic-style clients.
				"extractFrom":           []any{map[string]any{"headers": []any{"Authorization", "x-api-key"}}},
				"forwardClientIDHeader": aiGatewayKeyIDHeader,
				"sanitize":              true,
			},
			// Runs after the AI Gateway has set x-ai-eg-model from the request body.
			"authorization": authorization,
		},
	}}
	policy.SetGroupVersionKind(securityPolicyGVK)
	return policy
}
