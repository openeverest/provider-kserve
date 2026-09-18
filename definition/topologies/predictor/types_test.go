package predictor

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestResolvedServiceType(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		p    PredictorTopologyParameters
		want corev1.ServiceType
	}{
		{name: "empty"},
		{name: "clusterip", p: PredictorTopologyParameters{ExternalAccess: ExternalAccessClusterIP}, want: corev1.ServiceTypeClusterIP},
		{name: "lb", p: PredictorTopologyParameters{ExternalAccess: ExternalAccessLoadBalancer}, want: corev1.ServiceTypeLoadBalancer},
		{name: "nodeport", p: PredictorTopologyParameters{ExternalAccess: ExternalAccessNodePort}, want: corev1.ServiceTypeNodePort},
		{name: "unknown", p: PredictorTopologyParameters{ExternalAccess: "Ingress"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.p.ResolvedServiceType(); got != tc.want {
				t.Fatalf("ResolvedServiceType()=%q want %q", got, tc.want)
			}
		})
	}
}
