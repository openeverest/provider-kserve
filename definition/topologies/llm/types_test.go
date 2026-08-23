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
