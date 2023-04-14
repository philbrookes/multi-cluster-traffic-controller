package dns

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Kuadrant/multi-cluster-traffic-controller/pkg/_internal/metadata"
	"github.com/Kuadrant/multi-cluster-traffic-controller/pkg/apis/v1alpha1"
	"github.com/Kuadrant/multi-cluster-traffic-controller/pkg/dns/aws"
	"github.com/Kuadrant/multi-cluster-traffic-controller/pkg/traffic"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"
)

const (
	labelRecordID                  = "kuadrant.io/record-id"
	PATCH_ANNOTATION_PREFIX string = "MCTC_PATCH_"
)

type Patch struct {
	OP    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value"`
}

var ErrAlreadyAssigned = fmt.Errorf("managed host already assigned")

type Service struct {
	controlClient client.Client
	// this is temporary setting the tenant ns in the control plane.
	// will be removed when we have auth that can map to a given ctrl plane ns
	defaultCtrlNS string

	hostResolver HostResolver
}

func NewService(controlClient client.Client, hostResolv HostResolver, defaultCtrlNS string) *Service {
	return &Service{controlClient: controlClient, defaultCtrlNS: defaultCtrlNS, hostResolver: hostResolv}
}

func (s *Service) resolveIPS(ctx context.Context, addresses []gatewayv1beta1.GatewayAddress) ([]string, error) {
	activeDNSTargetIPs := []string{}
	for _, target := range addresses {
		if *target.Type == gatewayv1beta1.IPAddressType {
			activeDNSTargetIPs = append(activeDNSTargetIPs, target.Value)
			continue
		}
		addr, err := s.hostResolver.LookupIPAddr(ctx, target.Value)
		if err != nil {
			return activeDNSTargetIPs, fmt.Errorf("DNSLookup failed for host %s : %s", target.Value, err)
		}
		for _, add := range addr {
			activeDNSTargetIPs = append(activeDNSTargetIPs, add.IP.String())
		}
	}
	return activeDNSTargetIPs, nil
}

// GetDNSRecords returns all the existing DNSRecords for all the hosts in the traffic resource.

// Ingress only - This is only used for traffic resources that currently expect a managed host to be generated.
func (s *Service) GetDNSRecords(ctx context.Context, traffic traffic.Interface) ([]*v1alpha1.DNSRecord, error) {
	// TODO improve this to use a label and list instead of gets
	hosts := traffic.GetHosts()
	records := []*v1alpha1.DNSRecord{}
	for _, host := range hosts {
		if host == "" {
			continue
		}
		record := &v1alpha1.DNSRecord{
			ObjectMeta: metav1.ObjectMeta{
				Name:      host,
				Namespace: s.defaultCtrlNS,
			},
		}
		if err := s.controlClient.Get(ctx, client.ObjectKeyFromObject(record), record); err != nil {
			if k8serrors.IsNotFound(err) {
				log.Log.V(10).Info("no dnsrecord found for host ", "host", record.Name)
				continue
			}
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func (s *Service) GetManagedHosts(ctx context.Context, traffic traffic.Interface) ([]v1alpha1.ManagedHost, error) {
	managed := []v1alpha1.ManagedHost{}
	for _, host := range traffic.GetHosts() {
		managedZone, subDomain, err := s.GetManagedZoneForHost(ctx, host, traffic)
		if err != nil {
			return nil, err
		}
		if managedZone == nil {
			// its ok for no managedzone to be present as this could be a CNAME or externally managed host
			continue
		}
		dnsRecord, err := s.GetDNSRecord(ctx, subDomain, managedZone)
		if err != nil && !k8serrors.IsNotFound(err) {
			return nil, err
		}
		managedHost := v1alpha1.ManagedHost{
			Host:        host,
			Subdomain:   subDomain,
			ManagedZone: managedZone,
			DnsRecord:   dnsRecord,
		}

		managed = append(managed, managedHost)
	}
	return managed, nil
}

// CreateDNSRecord creates a new DNSRecord, if one does not already exist, in the given managed zone with the given subdomain.
func (s *Service) CreateDNSRecord(ctx context.Context, subDomain string, managedZone *v1alpha1.ManagedZone) (*v1alpha1.DNSRecord, error) {
	managedHost := strings.ToLower(fmt.Sprintf("%s.%s", subDomain, managedZone.Spec.DomainName))

	dnsRecord := v1alpha1.DNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      managedHost,
			Namespace: managedZone.Namespace,
			Labels:    map[string]string{labelRecordID: subDomain},
		},
		Spec: v1alpha1.DNSRecordSpec{
			ManagedZoneRef: &v1alpha1.ManagedZoneReference{
				Name: managedZone.Name,
			},
		},
	}
	err := controllerutil.SetControllerReference(managedZone, &dnsRecord, s.controlClient.Scheme())
	if err != nil {
		return nil, err
	}

	err = s.controlClient.Create(ctx, &dnsRecord, &client.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return nil, err
	}
	//host may already be present
	if err != nil && k8serrors.IsAlreadyExists(err) {
		err = s.controlClient.Get(ctx, client.ObjectKeyFromObject(&dnsRecord), &dnsRecord)
		if err != nil {
			return nil, err
		}
	}
	return &dnsRecord, nil
}

// GetDNSRecord returns a DNSRecord, if one exists, for the given subdomain in the given managed zone.
func (s *Service) GetDNSRecord(ctx context.Context, subDomain string, managedZone *v1alpha1.ManagedZone) (*v1alpha1.DNSRecord, error) {
	managedHost := strings.ToLower(fmt.Sprintf("%s.%s", subDomain, managedZone.Spec.DomainName))

	dnsRecord := &v1alpha1.DNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      managedHost,
			Namespace: managedZone.GetNamespace(),
		},
	}
	if err := s.controlClient.Get(ctx, client.ObjectKeyFromObject(dnsRecord), dnsRecord); err != nil {
		if k8serrors.IsNotFound(err) {
			log.Log.V(10).Info("no dnsrecord found for host ", "host", dnsRecord.Name)
		}
		return nil, err
	}

	return dnsRecord, nil
}

// AddEndpoints adds endpoints to the given DNSRecord for each ip address resolvable for the given traffic resource.
func (s *Service) SetEndpoints(ctx context.Context, addresses []gatewayv1beta1.GatewayAddress, dnsRecord *v1alpha1.DNSRecord) error {

	//TODO not removing existing addresses when not in use...
	fmt.Println("setting endpoints ", addresses, " for dns record ", dnsRecord.Name)

	ips, err := s.resolveIPS(ctx, addresses)
	if err != nil {
		return err
	}
	old := dnsRecord.DeepCopy()
	host := dnsRecord.Name

	// check if endpoint already exists in the DNSRecord
	endpoints := []string{}
	for _, addr := range ips {
		endpointFound := false
		for _, endpoint := range dnsRecord.Spec.Endpoints {
			if endpoint.DNSName == host && endpoint.SetIdentifier == addr {
				log.Log.V(3).Info("address already exists in record for host", "address ", addr, "host", host)
				endpointFound = true
				continue
			}
		}
		if !endpointFound {
			endpoints = append(endpoints, addr)
		}
	}
	if len(dnsRecord.Spec.Endpoints) == 0 {
		// they are all new endpoints
		endpoints = ips
	}
	for _, ep := range endpoints {
		endpoint := &v1alpha1.Endpoint{
			DNSName:       host,
			Targets:       []string{ep},
			RecordType:    "A",
			SetIdentifier: ep,
			RecordTTL:     60,
		}

		dnsRecord.Spec.Endpoints = append(dnsRecord.Spec.Endpoints, endpoint)
	}
	totalIPs := 0
	for _, e := range dnsRecord.Spec.Endpoints {
		totalIPs += len(e.Targets)
	}
	for _, e := range dnsRecord.Spec.Endpoints {
		e.SetProviderSpecific(aws.ProviderSpecificWeight, awsEndpointWeight(totalIPs))
	}

	if equality.Semantic.DeepEqual(old.Spec, dnsRecord.Spec) {
		fmt.Println("no update required spec not changed")
		return nil
	}

	return s.controlClient.Update(ctx, dnsRecord, &client.UpdateOptions{})
}

// GetManagedZoneForHost returns a ManagedZone and subDomain for the given host if one exists.
//
// Currently, this returns the first matching ManagedZone found in the traffic resources own namespace, or if none is found,
// it looks for the first matching one listed in the default ctrl namespace.
func (s *Service) GetManagedZoneForHost(ctx context.Context, host string, t metav1.Object) (*v1alpha1.ManagedZone, string, error) {
	hostParts := strings.SplitN(host, ".", 2)
	if len(hostParts) < 2 {
		return nil, "", fmt.Errorf("unable to parse host : %s on traffic resource : %s", host, t.GetName())
	}
	subDomain := hostParts[0]
	parentDomain := hostParts[1]

	var managedZones v1alpha1.ManagedZoneList

	if err := s.controlClient.List(ctx, &managedZones, client.InNamespace(t.GetNamespace()), client.MatchingFields{"spec.domainName": parentDomain}); err != nil {
		log.FromContext(ctx).Error(err, "unable to list managed zones in traffic resource NS")
		return nil, "", err
	}

	if len(managedZones.Items) > 0 {
		return &managedZones.Items[0], subDomain, nil
	}

	if err := s.controlClient.List(ctx, &managedZones, client.InNamespace(s.defaultCtrlNS), client.MatchingFields{"spec.domainName": parentDomain}); err != nil {
		log.FromContext(ctx).Error(err, "unable to list managed zones in default Ctrl NS")
		return nil, "", err
	}

	if len(managedZones.Items) > 0 {
		return &managedZones.Items[0], subDomain, nil
	}

	return nil, "", fmt.Errorf("no managed zone found for host : %s on traffic resource : %s", host, t.GetName())
}

func (s *Service) PatchTargets(ctx context.Context, targets, hosts []string, clusterID string, remove bool) error {
	//build patches to add dns targets to all matched DNSRecords
	patches := []*Patch{}
	for _, target := range targets {
		patch := &Patch{
			OP:    "add",
			Path:  "/spec/endpoints/0/targets/-",
			Value: target,
		}
		patches = append(patches, patch)
	}
	patchAnnotation, err := json.Marshal(patches)
	if err != nil {
		return fmt.Errorf("could not convert patches to string. Patches: %+v, error: %v", patches, err)
	}
	for _, host := range hosts {
		if host == "" {
			continue
		}
		dnsRecord := &v1alpha1.DNSRecord{}
		err := s.controlClient.Get(ctx, client.ObjectKey{Name: host, Namespace: s.defaultCtrlNS}, dnsRecord)
		if err != nil {
			return err
		}

		if !remove {
			metadata.AddAnnotation(dnsRecord, PATCH_ANNOTATION_PREFIX+clusterID, string(patchAnnotation))
		} else {
			metadata.RemoveAnnotation(dnsRecord, PATCH_ANNOTATION_PREFIX+clusterID)
		}

		err = s.controlClient.Update(ctx, dnsRecord)
		if err != nil {
			return err
		}
	}
	return nil
}

// awsEndpointWeight returns the weight Value for a single AWS record in a set of records where the traffic is split
// evenly between a number of clusters/ingresses, each splitting traffic evenly to a number of IPs (numIPs)
//
// Divides the number of IPs by a known weight allowance for a cluster/ingress, note that this means:
// * Will always return 1 after a certain number of ips is reached, 60 in the current case (maxWeight / 2)
// * Will return values that don't add up to the total maxWeight when the number of ingresses is not divisible by numIPs
//
// The aws weight value must be an integer between 0 and 255.
// https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/resource-record-sets-values-weighted.html#rrsets-values-weighted-weight
func awsEndpointWeight(numIPs int) string {
	maxWeight := 120
	if numIPs > maxWeight {
		numIPs = maxWeight
	}
	return strconv.Itoa(maxWeight / numIPs)
}
