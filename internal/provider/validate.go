package provider

import (
	"encoding/json"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/definition/components"
	"github.com/openeverest/provider-kserve/definition/topologies/llm"
	"github.com/openeverest/provider-kserve/definition/topologies/predictor"
	"github.com/openeverest/provider-kserve/internal/common"
)

var _ controller.ReferenceValidator = (*Provider)(nil)

var (
	topologyParametersPath = field.NewPath("spec", "topology", "parameters")
	llmEnginePath          = field.NewPath("spec", "components").Key(common.ComponentLlmEngine)
	predictorPath          = field.NewPath("spec", "components").Key(common.ComponentPredictor)
)

// Validate checks the rules about the Instance alone. The runtime runs it once
// the Instance matches the topologies, components, versions and parameter
// schemas the Provider declares.
func (p *Provider) Validate(in controller.ValidationInput) controller.ValidationResult {
	switch topology := in.Instance.GetTopologyType(); topology {
	case common.TopologyLLM:
		return controller.ValidationResult{Errors: validateLLM(in.Instance)}
	case common.TopologyPredictor:
		return controller.ValidationResult{Errors: validatePredictor(in.Instance)}
	default:
		return controller.ValidationResult{Errors: field.ErrorList{field.NotSupported(
			field.NewPath("spec", "topology", "type"), topology, []string{common.TopologyLLM, common.TopologyPredictor},
		)}}
	}
}

// ValidateReferences checks the rules that read the cluster: the model names
// other Envoy AI Gateway routes serve, the ClusterStorageContainer a predictor
// names and the InferenceService a predictor's config merges into.
func (p *Provider) ValidateReferences(c *controller.Context) (field.ErrorList, error) {
	switch c.Instance().GetTopologyType() {
	case common.TopologyLLM:
		return llmReferences(c)
	case common.TopologyPredictor:
		return predictorReferences(c)
	default:
		return nil, nil
	}
}

func llmReferences(c *controller.Context) (field.ErrorList, error) {
	var topo llm.LlmTopologyParameters
	c.TryDecodeTopologyParameters(&topo)
	if !topo.UsesAIGateway() {
		return nil, nil
	}
	var params components.VllmCustomSpec
	c.TryDecodeComponentParameters(c.Instance().Spec.Components[common.ComponentLlmEngine], &params)
	model := servedModelName(c.Name(), params.ModelName)
	route, err := aiGatewayRouteServing(c, model)
	if err != nil || route == "" {
		return nil, err
	}
	return field.ErrorList{field.Invalid(llmEnginePath.Child("parameters", "modelName"), model,
		"is already served through the Envoy AI Gateway by "+route+"; set a different modelName")}, nil
}

func predictorReferences(c *controller.Context) (field.ErrorList, error) {
	var params components.ModelServerCustomSpec
	c.TryDecodeComponentParameters(c.Instance().Spec.Components[common.ComponentPredictor], &params)
	params, err := resolveCatalogModel(params)
	if err != nil {
		return nil, err
	}
	paramsPath := predictorPath.Child("parameters")

	var errs field.ErrorList
	if params.StorageContainer != "" {
		storageErrs, err := validateStorageContainer(c, paramsPath.Child("storageContainer"), params.StorageContainer, params.StorageURI)
		if err != nil {
			return nil, err
		}
		errs = append(errs, storageErrs...)
	}
	var topo predictor.PredictorTopologyParameters
	c.TryDecodeTopologyParameters(&topo)
	if topo.UsesAIGateway() {
		route, err := aiGatewayRouteServing(c, c.Name())
		if err != nil {
			return nil, err
		}
		if route != "" {
			errs = append(errs, field.Invalid(field.NewPath("metadata", "name"), c.Name(),
				"is already served through the Envoy AI Gateway by "+route+"; the predictor serves the model under the Instance name, so use another name"))
		}
	}
	if params.Config != "" {
		configErrs, err := validatePredictorConfig(c, paramsPath.Child("config"))
		if err != nil {
			return nil, err
		}
		errs = append(errs, configErrs...)
	}
	return errs, nil
}

// decodeParameters decodes raw into target, leaving target unset when raw is;
// the runtime has checked raw against the parameters schema.
func decodeParameters(raw *runtime.RawExtension, target any) {
	if raw != nil && raw.Raw != nil {
		_ = json.Unmarshal(raw.Raw, target)
	}
}
