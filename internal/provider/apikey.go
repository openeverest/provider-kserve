package provider

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/internal/common"
)

// The credential Secret layout follows Authorino's API key format (api_key
// data key, label selector, metadata annotations) so Kuadrant can consume the
// same Secrets later. Consumers must select keys by label, never by name.
const (
	aiGatewayKeySecretSuffix = "-ai-gateway-key"

	aiGatewayKeyLabel          = "openeverest.io/ai-gateway-key"
	aiGatewayKeyOwnerKindLabel = "openeverest.io/ai-gateway-key-owner-kind"
	aiGatewayKeyOwnerNameLabel = "openeverest.io/ai-gateway-key-owner-name"
	aiGatewayKeyIDAnnotation   = "openeverest.io/ai-gateway-key-id"
	// Comma-separated <namespace>/<instance> refs the key may call.
	aiGatewayModelsAnnotation = "openeverest.io/ai-gateway-models"
	aiGatewayKeyDataKey       = "api_key"

	aiGatewayKeyOwnerKindInstance = "instance"
	managedByLabel                = "app.kubernetes.io/managed-by"

	apiKeyPrefix   = "oe_sk_"
	apiKeyIDPrefix = "k_"
)

var lowerBase32 = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func aiGatewayKeySecretName(instance string) string {
	return instance + aiGatewayKeySecretSuffix
}

func isAIGatewayKeySecret(obj client.Object) bool {
	return obj.GetLabels()[aiGatewayKeyLabel] == "true"
}

// instanceModelRef identifies an Instance in the ai-gateway-models annotation.
func instanceModelRef(namespace, name string) string {
	return namespace + "/" + name
}

// newAPIKeyID returns a random, non-secret key identifier (k_ + 16 base32 chars).
func newAPIKeyID() (string, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating api key id: %w", err)
	}
	return apiKeyIDPrefix + lowerBase32.EncodeToString(b), nil
}

// newAPIKey returns a random API key with a scanner-friendly prefix.
func newAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating api key: %w", err)
	}
	return apiKeyPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// parseModelRefs splits the ai-gateway-models annotation into Instance refs.
func parseModelRefs(value string) []string {
	var refs []string
	for _, ref := range strings.Split(value, ",") {
		if ref = strings.TrimSpace(ref); ref != "" {
			refs = append(refs, ref)
		}
	}
	slices.Sort(refs)
	return slices.Compact(refs)
}

// ensureAIGatewayKey makes sure the Instance has its API key Secret. The key
// is generated once and never rewritten, like a database password.
func ensureAIGatewayKey(c *controller.Context) error {
	existing := &corev1.Secret{}
	err := c.Get(existing, aiGatewayKeySecretName(c.Name()))
	if apierrors.IsNotFound(err) {
		return createAIGatewayKey(c)
	}
	if err != nil {
		return fmt.Errorf("reading api key secret: %w", err)
	}

	desired := existing.DeepCopy()
	if err := setAIGatewayKeyFields(c, desired); err != nil {
		return err
	}
	if equality.Semantic.DeepEqual(existing, desired) {
		return nil
	}
	if err := c.Client().Patch(c.Context(), desired, client.MergeFrom(existing)); err != nil {
		return fmt.Errorf("updating api key secret: %w", err)
	}
	return nil
}

func createAIGatewayKey(c *controller.Context) error {
	secret := &corev1.Secret{
		ObjectMeta: c.ObjectMeta(aiGatewayKeySecretName(c.Name())),
		Type:       corev1.SecretTypeOpaque,
	}
	if err := setAIGatewayKeyFields(c, secret); err != nil {
		return err
	}
	if err := controllerutil.SetControllerReference(c.Instance(), secret, c.Client().Scheme()); err != nil {
		return fmt.Errorf("setting api key secret owner: %w", err)
	}
	err := c.Client().Create(c.Context(), secret)
	if apierrors.IsAlreadyExists(err) {
		// Never regenerate: retry once the cache sees the existing key.
		return fmt.Errorf("api key secret %s exists but is not cached yet", secret.Name)
	}
	if err != nil {
		return fmt.Errorf("creating api key secret: %w", err)
	}
	return nil
}

// setAIGatewayKeyFields sets the labels and annotations and fills in the key
// ID and value only when they are missing.
func setAIGatewayKeyFields(c *controller.Context, secret *corev1.Secret) error {
	if secret.Labels == nil {
		secret.Labels = map[string]string{}
	}
	secret.Labels[aiGatewayKeyLabel] = "true"
	secret.Labels[aiGatewayKeyOwnerKindLabel] = aiGatewayKeyOwnerKindInstance
	secret.Labels[aiGatewayKeyOwnerNameLabel] = c.Name()
	secret.Labels[managedByLabel] = common.ProviderName

	if secret.Annotations == nil {
		secret.Annotations = map[string]string{}
	}
	secret.Annotations[aiGatewayModelsAnnotation] = instanceModelRef(c.Namespace(), c.Name())
	if secret.Annotations[aiGatewayKeyIDAnnotation] == "" {
		id, err := newAPIKeyID()
		if err != nil {
			return err
		}
		secret.Annotations[aiGatewayKeyIDAnnotation] = id
	}

	if len(secret.Data[aiGatewayKeyDataKey]) == 0 {
		key, err := newAPIKey()
		if err != nil {
			return err
		}
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		secret.Data[aiGatewayKeyDataKey] = []byte(key)
	}
	return nil
}

// readAIGatewayKey returns the Instance's key ID and API key, or empty strings
// when the Secret does not exist yet.
func readAIGatewayKey(c *controller.Context) (id, key string, err error) {
	secret := &corev1.Secret{}
	if err := c.Get(secret, aiGatewayKeySecretName(c.Name())); err != nil {
		return "", "", client.IgnoreNotFound(err)
	}
	return secret.Annotations[aiGatewayKeyIDAnnotation], string(secret.Data[aiGatewayKeyDataKey]), nil
}
