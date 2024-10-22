package kops

import (
	"context"
	"fmt"
	"net"

	kopsapi "k8s.io/kops/pkg/apis/kops"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/pkg/errors"
	kcontrolplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	kinfrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"
)

var (
	// ErrLabelKeyEmpty is returned when the label key is empty
	ErrLabelKeyEmpty = errors.New("label key is empty")
	// ErrLabelValueEmpty is returned when the label value is empty
	ErrLabelValueEmpty = errors.New("label value is empty")
)

func GetSubnetFromKopsControlPlane(kcp *kcontrolplanev1alpha1.KopsControlPlane) (*kopsapi.ClusterSubnetSpec, error) {
	if kcp.Spec.KopsClusterSpec.Networking.Subnets == nil {
		return nil, errors.Wrap(errors.Errorf("SubnetNotFound"), "subnet not found in KopsControlPlane")
	}
	subnet := kcp.Spec.KopsClusterSpec.Networking.Subnets[0]
	return &subnet, nil
}

func GetRegionFromKopsSubnet(subnet kopsapi.ClusterSubnetSpec) (*string, error) {
	if subnet.Region != "" {
		return &subnet.Region, nil
	}

	if subnet.Zone != "" {
		zone := subnet.Zone
		region := zone[:len(zone)-1]
		return &region, nil
	}

	return nil, errors.Wrap(errors.Errorf("RegionNotFound"), "couldn't get region from KopsControlPlane")
}

// GetKopsMachinePoolsWithLabel retrieve all KopsMachinePool with the given label
func GetKopsMachinePoolsWithLabel(ctx context.Context, c client.Reader, key, value string) ([]kinfrastructurev1alpha1.KopsMachinePool, error) {
	var kmps []kinfrastructurev1alpha1.KopsMachinePool

	if key == "" {
		return kmps, ErrLabelKeyEmpty
	}
	if value == "" {
		return kmps, ErrLabelValueEmpty
	}

	selectors := []client.ListOption{
		client.MatchingLabels{
			key: value,
		},
	}

	kmpsList := &kinfrastructurev1alpha1.KopsMachinePoolList{}
	err := c.List(ctx, kmpsList, selectors...)
	if err != nil {
		return kmps, fmt.Errorf("error while trying to retrieve KopsMachinePool list: %w", err)
	}

	return kmpsList.Items, nil
}

func GetCloudResourceNameFromKopsMachinePool(kmp kinfrastructurev1alpha1.KopsMachinePool) (string, error) {

	if _, ok := kmp.Spec.KopsInstanceGroupSpec.NodeLabels["kops.k8s.io/instance-group-role"]; !ok {
		return "", fmt.Errorf("failed to retrieve role from KopsMachinePool %s", kmp.GetName())
	}

	if kmp.Spec.ClusterName == "" {
		return "", fmt.Errorf("failed to retrieve clusterName from KopsMachinePool %s", kmp.GetName())
	}

	var cloudName string
	if kmp.Spec.KopsInstanceGroupSpec.NodeLabels["kops.k8s.io/instance-group-role"] == "Master" {
		cloudName = fmt.Sprintf("%s.masters.%s", kmp.Name, kmp.Spec.ClusterName)
	} else {
		cloudName = fmt.Sprintf("%s.%s", kmp.Name, kmp.Spec.ClusterName)
	}

	return cloudName, nil
}

// CalculateServiceClusterIPRange calculates the service cluster IP range based on the nonMasqueradeCIDR
// it returns at most a /20 network
func CalculateServiceClusterIPRange(nonMasqueradeCIDRString string) (string, error) {

	_, nonMasqueradeCIDR, err := net.ParseCIDR(nonMasqueradeCIDRString)
	if err != nil {
		return "", err
	}

	nmOnes, nmBits := nonMasqueradeCIDR.Mask.Size()
	// Allocate from the '0' subnet; but only carve off 1/4 of that (i.e. add 1 + 2 bits to the netmask)
	serviceOnes := nmOnes + 3
	// Max size of network is /20 (4096 addresses)
	if serviceOnes < 20 {
		serviceOnes = 20
	}

	cidr := net.IPNet{IP: nonMasqueradeCIDR.IP.Mask(nonMasqueradeCIDR.Mask), Mask: net.CIDRMask(serviceOnes, nmBits)}

	return cidr.String(), nil
}
