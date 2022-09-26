package kops

import (
	"context"
	"fmt"

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
	if kcp.Spec.KopsClusterSpec.Subnets == nil {
		return nil, errors.Wrap(errors.Errorf("SubnetNotFound"), "subnet not found in KopsControlPlane")
	}
	subnet := kcp.Spec.KopsClusterSpec.Subnets[0]
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
func GetKopsMachinePoolsWithLabel(ctx context.Context, c client.Client, key, value string) ([]kinfrastructurev1alpha1.KopsMachinePool, error) {
	var kmps []kinfrastructurev1alpha1.KopsMachinePool

	if key == "" {
		return kmps, ErrLabelKeyEmpty
	}
	if value == "" {
		return kmps, ErrLabelValueEmpty
	}

	kmpsList := &kinfrastructurev1alpha1.KopsMachinePoolList{}
	err := c.List(ctx, kmpsList)
	if err != nil {
		return kmps, fmt.Errorf("error while trying to retrieve KopsMachinePool list: %w", err)
	}

	// Todo: use label selector to avoid iterate over the items
	for _, kmp := range kmpsList.Items {
		if _, ok := kmp.Labels[key]; ok {
			kmps = append(kmps, kmp)
		}
	}

	return kmps, nil
}

func GetAutoScalingGroupNameFromKopsMachinePool(kmp kinfrastructurev1alpha1.KopsMachinePool) (*string, error) {

	if _, ok := kmp.Spec.KopsInstanceGroupSpec.NodeLabels["kops.k8s.io/instance-group-name"]; !ok {
		return nil, fmt.Errorf("failed to retrieve igName from KopsMachinePool %s", kmp.GetName())
	}

	if _, ok := kmp.Spec.KopsInstanceGroupSpec.NodeLabels["kops.k8s.io/instance-group-role"]; !ok {
		return nil, fmt.Errorf("failed to retrieve role from KopsMachinePool %s", kmp.GetName())
	}

	if kmp.Spec.ClusterName == "" {
		return nil, fmt.Errorf("failed to retrieve clusterName from KopsMachinePool %s", kmp.GetName())
	}

	var asgName string
	if kmp.Spec.KopsInstanceGroupSpec.NodeLabels["kops.k8s.io/instance-group-role"] == "Master" {
		asgName = fmt.Sprintf("%s.masters.%s", kmp.Spec.KopsInstanceGroupSpec.NodeLabels["kops.k8s.io/instance-group-name"], kmp.Spec.ClusterName)
	} else {
		asgName = fmt.Sprintf("%s.%s", kmp.Spec.KopsInstanceGroupSpec.NodeLabels["kops.k8s.io/instance-group-name"], kmp.Spec.ClusterName)
	}

	return &asgName, nil
}
