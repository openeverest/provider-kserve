package predictor

import (
	"encoding/json"
	"testing"
)

func TestMetricsEnabled(t *testing.T) {
	t.Parallel()
	if !(PredictorTopologyParameters{}).MetricsEnabled() {
		t.Fatal("nil enableMetrics should default true")
	}
	falseVal := false
	if (PredictorTopologyParameters{EnableMetrics: &falseVal}).MetricsEnabled() {
		t.Fatal("explicit false should disable")
	}
}

func TestUnmarshalEnableMetricsString(t *testing.T) {
	t.Parallel()
	var p PredictorTopologyParameters
	if err := json.Unmarshal([]byte(`{"enableMetrics":"false"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.MetricsEnabled() {
		t.Fatal("string false should disable")
	}
}
