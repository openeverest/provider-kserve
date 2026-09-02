package llm

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestUsesAIGateway(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		p    LlmTopologyParameters
		want bool
	}{
		{name: "empty", want: false},
		{name: "legacy flag", p: LlmTopologyParameters{EnableAIGateway: true}, want: true},
		{name: "envoy", p: LlmTopologyParameters{ExternalAccess: ExternalAccessEnvoyAIGateway}, want: true},
		{name: "clusterip wins over legacy", p: LlmTopologyParameters{ExternalAccess: ExternalAccessClusterIP, EnableAIGateway: true}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.p.UsesAIGateway(); got != tc.want {
				t.Fatalf("UsesAIGateway()=%v want %v", got, tc.want)
			}
		})
	}
}

func TestMetricsEnabled(t *testing.T) {
	t.Parallel()
	if !(LlmTopologyParameters{}).MetricsEnabled() {
		t.Fatal("nil enableMetrics should default true")
	}
	falseVal := false
	if (LlmTopologyParameters{EnableMetrics: &falseVal}).MetricsEnabled() {
		t.Fatal("explicit false should disable")
	}
}

func TestResolvedServiceType(t *testing.T) {
	t.Parallel()
	if got := (LlmTopologyParameters{ExternalAccess: ExternalAccessLoadBalancer}).ResolvedServiceType(); got != corev1.ServiceTypeLoadBalancer {
		t.Fatalf("got %q", got)
	}
	if got := (LlmTopologyParameters{ExternalAccess: ExternalAccessEnvoyAIGateway}).ResolvedServiceType(); got != corev1.ServiceTypeClusterIP {
		t.Fatalf("got %q", got)
	}
	if got := (LlmTopologyParameters{}).ResolvedServiceType(); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestUnmarshalTracing(t *testing.T) {
	t.Parallel()
	var def LlmTopologyParameters
	if err := json.Unmarshal([]byte(`{}`), &def); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if def.EnableTracing {
		t.Fatal("tracing should default disabled")
	}

	var on LlmTopologyParameters
	if err := json.Unmarshal([]byte(`{"enableTracing":"true","tracingEndpoint":"http://otel:4317"}`), &on); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !on.EnableTracing {
		t.Fatal("enableTracing=\"true\" should enable")
	}
	if on.TracingEndpoint != "http://otel:4317" {
		t.Fatalf("tracingEndpoint=%q", on.TracingEndpoint)
	}
}

func TestUnmarshalPrefillScaling(t *testing.T) {
	t.Parallel()
	var got LlmTopologyParameters
	if err := json.Unmarshal([]byte(`{"prefillMinReplicas":1,"prefillMaxReplicas":4,"prefillScalingActuator":"hpa"}`), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.PrefillMinReplicas == nil || *got.PrefillMinReplicas != 1 {
		t.Fatalf("prefillMinReplicas=%v", got.PrefillMinReplicas)
	}
	if got.PrefillMaxReplicas == nil || *got.PrefillMaxReplicas != 4 {
		t.Fatalf("prefillMaxReplicas=%v", got.PrefillMaxReplicas)
	}
	if got.PrefillScalingActuator != "hpa" {
		t.Fatalf("prefillScalingActuator=%q", got.PrefillScalingActuator)
	}
}

func TestUnmarshalPrefillWorkers(t *testing.T) {
	t.Parallel()
	var got LlmTopologyParameters
	if err := json.Unmarshal([]byte(`{"prefillWorkerCount":1,"prefillPipelineParallelSize":2,"prefillWorkerResources":{"limits":{"cpu":"4"}}}`), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.PrefillWorkerCount == nil || *got.PrefillWorkerCount != 1 {
		t.Fatalf("prefillWorkerCount=%v", got.PrefillWorkerCount)
	}
	if got.PrefillPipelineParallelSize == nil || *got.PrefillPipelineParallelSize != 2 {
		t.Fatalf("prefillPipelineParallelSize=%v", got.PrefillPipelineParallelSize)
	}
	if got.PrefillWorkerResources == nil {
		t.Fatal("prefillWorkerResources unset")
	}
}
