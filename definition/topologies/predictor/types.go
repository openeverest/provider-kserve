// Package predictor contains parameter types for the predictor topology.
//
// Add fields to PredictorTopologyParameters and reference it via
// parametersSchema in topology.yaml when this topology needs parameters.
//
// +k8s:openapi-gen=true
package predictor

import (
	"encoding/json"
	"fmt"
	"strings"
)

// PredictorTopologyParameters defines topology-level parameters for the
// predictor topology. Per-model fields (format, storage URI) live on the
// predictor component.
type PredictorTopologyParameters struct {
	// EnableMetrics emits a Prometheus Operator PodMonitor for this instance's
	// InferenceService pods (/metrics on the Standard-mode HTTP port).
	// Nil/unset defaults to true, matching the llm topology.
	EnableMetrics *bool `json:"enableMetrics,omitempty"`
}

// MetricsEnabled reports whether this instance should emit a PodMonitor.
func (t PredictorTopologyParameters) MetricsEnabled() bool {
	if t.EnableMetrics == nil {
		return true
	}
	return *t.EnableMetrics
}

// UnmarshalJSON accepts JSON booleans and UI select strings ("true"/"false")
// for flag fields. Everest's form generator has no switch uiType, so flags are
// rendered as Enabled/Disabled selects.
func (t *PredictorTopologyParameters) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var err error
	if t.EnableMetrics, err = unmarshalOptionalBoolFlag(raw, "enableMetrics"); err != nil {
		return err
	}
	return nil
}

func unmarshalOptionalBoolFlag(raw map[string]json.RawMessage, key string) (*bool, error) {
	v, ok := raw[key]
	if !ok || string(v) == "null" {
		return nil, nil
	}
	b, err := unmarshalBoolFlag(raw, key)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func unmarshalBoolFlag(raw map[string]json.RawMessage, key string) (bool, error) {
	v, ok := raw[key]
	if !ok || string(v) == "null" {
		return false, nil
	}
	var b bool
	if err := json.Unmarshal(v, &b); err == nil {
		return b, nil
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "enabled", "yes":
		return true, nil
	case "false", "0", "disabled", "no", "":
		return false, nil
	default:
		return false, fmt.Errorf("%s: invalid boolean value %q", key, s)
	}
}
