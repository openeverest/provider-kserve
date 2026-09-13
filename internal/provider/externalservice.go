package provider

import (
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-kserve/internal/common"
)

const (
	// externalServiceSuffix names the extra Service the provider creates to
	// publish a model externally when the user selects LoadBalancer or NodePort
	// (KServe's own workload Service is always ClusterIP and provider-unowned).
	externalServiceSuffix = "-external"
)

// externalServiceName is the name of the provider-owned external Service.
func externalServiceName(instance string) string {
	return instance + externalServiceSuffix
}

// reconcileExternalService creates a LoadBalancer/NodePort Service selecting the
// given pods, or deletes a leftover one when the expose type is ClusterIP.
func reconcileExternalService(c *controller.Context, spec *corev1alpha1.Service, svcType corev1.ServiceType, selector map[string]string, port, targetPort int32) error {
	if svcType == "" || svcType == corev1.ServiceTypeClusterIP {
		stale := &corev1.Service{ObjectMeta: c.ObjectMeta(externalServiceName(c.Name()))}
		return c.Delete(stale)
	}

	svc := buildExternalService(c, spec, svcType, selector, port, targetPort)
	return common.Apply(c.Context(), c.Client(), c.Instance(), svc)
}

// resolveServiceType prefers a topology-level ExternalAccess mapping, then the
// component Service type, then ClusterIP.
func resolveServiceType(topoType corev1.ServiceType, spec *corev1alpha1.Service) corev1.ServiceType {
	if topoType != "" {
		return topoType
	}
	if spec != nil && spec.ServiceType != "" {
		return spec.ServiceType
	}
	return corev1.ServiceTypeClusterIP
}

// buildExternalService builds the LoadBalancer/NodePort Service that fronts the
// given pods on port/targetPort.
func buildExternalService(c *controller.Context, spec *corev1alpha1.Service, svcType corev1.ServiceType, selector map[string]string, port, targetPort int32) *corev1.Service {
	meta := c.ObjectMeta(externalServiceName(c.Name()))
	if spec != nil && len(spec.Annotations) > 0 {
		if meta.Annotations == nil {
			meta.Annotations = map[string]string{}
		}
		for k, v := range spec.Annotations {
			meta.Annotations[k] = v
		}
	}

	svc := &corev1.Service{
		ObjectMeta: meta,
		Spec: corev1.ServiceSpec{
			Type:     svcType,
			Selector: selector,
			Ports: []corev1.ServicePort{{
				Name:       "http",
				Protocol:   corev1.ProtocolTCP,
				Port:       port,
				TargetPort: intstr.FromInt32(targetPort),
			}},
		},
	}

	if svcType == corev1.ServiceTypeLoadBalancer && spec != nil && spec.LoadBalancerService != nil {
		svc.Spec.LoadBalancerSourceRanges = spec.LoadBalancerService.SourceRanges.NormalizedSourceRanges()
	}

	return svc
}

// externalServiceConnectionDetails derives the endpoint of the provider-owned
// external Service. Returns nil when the address is not yet assigned.
func (p *Provider) externalServiceConnectionDetails(c *controller.Context, svcType corev1.ServiceType, port int32) (*controller.ConnectionDetails, error) {
	svc := &corev1.Service{}
	if err := c.Get(svc, externalServiceName(c.Name())); err != nil {
		return nil, nil
	}

	if svcType == corev1.ServiceTypeLoadBalancer {
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			host := ing.IP
			if host == "" {
				host = ing.Hostname
			}
			if host != "" {
				d := kserveConnectionDetails(host, strconv.Itoa(int(port)))
				return &d, nil
			}
		}
		return nil, nil
	}

	var nodePort int32
	for _, p := range svc.Spec.Ports {
		if p.NodePort != 0 {
			nodePort = p.NodePort
			break
		}
	}
	if nodePort == 0 {
		return nil, nil
	}
	host := firstNodeAddress(c)
	if host == "" {
		return nil, nil
	}
	d := kserveConnectionDetails(host, strconv.Itoa(int(nodePort)))
	return &d, nil
}

// firstNodeAddress returns an address for reaching a NodePort Service.
func firstNodeAddress(c *controller.Context) string {
	nodes := &corev1.NodeList{}
	if err := c.List(nodes); err != nil {
		return ""
	}
	var internal string
	for _, node := range nodes.Items {
		for _, addr := range node.Status.Addresses {
			switch addr.Type {
			case corev1.NodeExternalIP:
				if addr.Address != "" {
					return addr.Address
				}
			case corev1.NodeInternalIP:
				if internal == "" {
					internal = addr.Address
				}
			}
		}
	}
	return internal
}

// kserveConnectionDetails builds ConnectionDetails for an HTTP endpoint.
func kserveConnectionDetails(host, port string) controller.ConnectionDetails {
	return controller.ConnectionDetails{
		Type:     "kserve",
		Provider: common.ProviderName,
		Host:     host,
		Port:     port,
		URI:      "http://" + host + ":" + port,
	}
}
