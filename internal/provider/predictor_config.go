package provider

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/validation/field"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/yaml"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/internal/common"
)

func mergePredictorConfig(raw string, generated map[string]any) (map[string]any, error) {
	reader := utilyaml.NewYAMLReader(bufio.NewReader(strings.NewReader(raw)))
	doc, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("reading YAML: %w", err)
	}
	if _, err := reader.Read(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config must contain exactly one YAML document")
	}
	data, err := yaml.YAMLToJSONStrict(doc)
	if err != nil {
		return nil, fmt.Errorf("parsing YAML: %w", err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("config must be a spec object: %w", err)
	}
	if config == nil {
		return nil, fmt.Errorf("config must be a spec object")
	}
	for _, key := range []string{"apiVersion", "kind", "metadata", "spec", "status"} {
		if _, ok := config[key]; ok {
			return nil, fmt.Errorf("provide only the InferenceService spec body, not %q", key)
		}
	}
	mergeSpecMaps(config, generated)
	return config, nil
}

// mergeSpecMaps overlays generated fields in place. Lists are atomic, except
// those in namedLists, whose items are merged by name.
func mergeSpecMaps(config, generated map[string]any) {
	for key, value := range generated {
		switch base := config[key].(type) {
		case map[string]any:
			if override, ok := value.(map[string]any); ok {
				mergeSpecMaps(base, override)
				continue
			}
		case []any:
			if override, ok := value.([]any); ok && namedLists[key] {
				config[key] = mergeNamedLists(base, override)
				continue
			}
		}
		config[key] = value
	}
}

// namedLists lets config add sidecars, volumes and pull secrets next to a
// custom container, and extend the generated container, without restating it.
var namedLists = map[string]bool{
	"containers":       true,
	"volumes":          true,
	"volumeMounts":     true,
	"imagePullSecrets": true,
}

// mergeNamedLists merges generated items into the config item of the same
// name, generated fields winning. Generated items come first: KServe treats
// the first container as the model server.
func mergeNamedLists(config, generated []any) []any {
	out := make([]any, 0, len(config)+len(generated))
	used := make([]bool, len(config))
	for _, item := range generated {
		override, _ := item.(map[string]any)
		if i := indexByName(config, used, override["name"]); i >= 0 {
			used[i] = true
			base := config[i].(map[string]any)
			mergeSpecMaps(base, override)
			item = base
		}
		out = append(out, item)
	}
	for i, item := range config {
		if !used[i] {
			out = append(out, item)
		}
	}
	return out
}

func indexByName(items []any, used []bool, name any) int {
	if name, _ := name.(string); name != "" {
		for i, item := range items {
			if m, ok := item.(map[string]any); ok && !used[i] && m["name"] == name {
				return i
			}
		}
	}
	return -1
}

// validatePredictorConfig runs the same apply as Sync without persisting it.
// The installed CRD and admission webhooks validate fields absent from our Go types.
func validatePredictorConfig(c *controller.Context, path *field.Path) (field.ErrorList, error) {
	isvc, err := buildInferenceService(c)
	if err != nil {
		return nil, err
	}
	// The Instance has no UID yet when it is validated before it is created.
	if c.Instance().UID != "" {
		if err := controllerutil.SetControllerReference(c.Instance(), isvc, c.Client().Scheme()); err != nil {
			return nil, fmt.Errorf("setting InferenceService owner: %w", err)
		}
	}
	//nolint:staticcheck // KServe's unstructured resources lack apply configurations.
	err = c.Client().Patch(c.Context(), isvc, client.Apply,
		client.FieldOwner(common.FieldOwner), client.ForceOwnership,
		client.DryRunAll, client.FieldValidation(metav1.FieldValidationStrict))
	var status apierrors.APIStatus
	switch {
	case err == nil:
		return nil, nil
	// Webhook denials come back as Forbidden, and fields the CRD does not declare as a Status without a reason.
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err), apierrors.IsForbidden(err),
		errors.As(err, &status) && status.Status().Reason == metav1.StatusReasonUnknown:
		return field.ErrorList{field.Invalid(path, field.OmitValueType{}, "the merged InferenceService is rejected: "+err.Error())}, nil
	default:
		return nil, fmt.Errorf("validating the merged InferenceService: %w", err)
	}
}
