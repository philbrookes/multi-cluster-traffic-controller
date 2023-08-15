package dnspolicy

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kuadrant/kuadrant-operator/pkg/common"

	"github.com/Kuadrant/multicluster-gateway-controller/pkg/_internal/metadata"
	"github.com/Kuadrant/multicluster-gateway-controller/pkg/apis/v1alpha1"
)

// ProbeEventMapper is an EventHandler that maps DNSHealthCheckProbe object events to policy events.
type ProbeEventMapper struct {
	Logger logr.Logger
	client client.Client
}

func (p *ProbeEventMapper) MapToDNSPolicy(obj client.Object) []reconcile.Request {
	return p.mapToPolicyRequest(obj, "dnspolicy", &DNSPolicyRefsConfig{})
}

func (p *ProbeEventMapper) mapToPolicyRequest(obj client.Object, _ string, _ common.PolicyRefsConfig) []reconcile.Request {
	logger := p.Logger.V(3).WithValues("object", client.ObjectKeyFromObject(obj))
	probe, ok := obj.(*v1alpha1.DNSHealthCheckProbe)
	if !ok {
		logger.Info("mapToPolicyRequest:", "error", fmt.Sprintf("%T is not a *v1alpha1.DNSHealthCheckProbe", obj))
		return []reconcile.Request{}
	}

	allDNSPolicy := &v1alpha1.DNSPolicyList{}
	err := p.client.List(context.TODO(), allDNSPolicy)
	if err != nil {
		logger.Info("mapToPolicyRequest:", "error", "failed to get dnspolices")
		return []reconcile.Request{}
	}
	requests := make([]reconcile.Request, 0)

	probeRef := metadata.GetLabel(probe, DNSPolicyBackRefAnnotation)
	if probeRef == "" {
		return requests
	}
	for _, policy := range allDNSPolicy.Items {
		if strings.Compare(probeRef, policy.Name) == 0 {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      policy.Name,
					Namespace: policy.Namespace,
				}})
		}
	}
	return requests
}
