package llm

import (
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
