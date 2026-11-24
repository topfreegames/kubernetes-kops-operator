package utils

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	karpenterv1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"

	"k8s.io/apimachinery/pkg/api/resource"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	infrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"
	"github.com/topfreegames/kubernetes-kops-operator/pkg/helpers"
	kopsapi "k8s.io/kops/pkg/apis/kops"
	"k8s.io/kops/util/pkg/reflectutils"
)

func mergeCloudLabels(clusterName, machinePoolName string, clusterLabels, machinePoolLabels map[string]string) map[string]string {
	mergedLabels := make(map[string]string)

	for key, value := range clusterLabels {
		mergedLabels[key] = value
	}

	for key, value := range machinePoolLabels {
		mergedLabels[key] = value
	}

	essentialTags := map[string]string{
		"Name":                      fmt.Sprintf("%s/%s", clusterName, machinePoolName),
		"KubernetesCluster":         clusterName,
		"kops.k8s.io/instancegroup": machinePoolName,
		"k8s.io/cluster-autoscaler/node-template/label/node-role.kubernetes.io/node": "",
	}

	for key, value := range essentialTags {
		mergedLabels[key] = value
	}

	return mergedLabels
}

func BuildKarpenterVolumeConfigV1FromKops(kopsVolume *kopsapi.InstanceRootVolumeSpec) *karpenterv1.BlockDevice {

	var karpenterVolumeConfig *karpenterv1.BlockDevice

	defaultVolumeSize := resource.MustParse("60Gi")
	defaultVolumeType := "gp3"
	defaultIOPS := int64(3000)
	defaultEncrypted := true
	defaultThroughput := int64(125)

	if kopsVolume != nil {
		var volumeSize resource.Quantity
		if kopsVolume.Size != nil {
			volumeSize = resource.MustParse(fmt.Sprintf("%dGi", *kopsVolume.Size))
		} else {
			volumeSize = defaultVolumeSize
		}

		var volumeType *string
		if kopsVolume.Type != nil {
			volumeType = kopsVolume.Type
		} else {
			volumeType = &defaultVolumeType
		}

		var volumeIOPS int64
		if kopsVolume.IOPS != nil {
			volumeIOPS = int64(*kopsVolume.IOPS)
		} else {
			volumeIOPS = defaultIOPS
		}

		var volumeEncryption bool
		if kopsVolume.Encryption != nil {
			volumeEncryption = *kopsVolume.Encryption
		} else {
			volumeEncryption = defaultEncrypted
		}

		var volumeThroughput int64
		if kopsVolume.Throughput != nil {
			volumeThroughput = int64(*kopsVolume.Throughput)
		} else {
			volumeThroughput = defaultThroughput
		}

		karpenterVolumeConfig = &karpenterv1.BlockDevice{
			VolumeSize:          &volumeSize,
			VolumeType:          volumeType,
			IOPS:                &volumeIOPS,
			Encrypted:           &volumeEncryption,
			Throughput:          &volumeThroughput,
			DeleteOnTermination: helpers.BoolPtr(true),
		}
	} else {
		karpenterVolumeConfig = &karpenterv1.BlockDevice{
			VolumeSize:          &defaultVolumeSize,
			VolumeType:          &defaultVolumeType,
			IOPS:                &defaultIOPS,
			Encrypted:           &defaultEncrypted,
			Throughput:          &defaultThroughput,
			DeleteOnTermination: helpers.BoolPtr(true),
		}
	}

	return karpenterVolumeConfig
}

const karpenterRootDeviceName = "/dev/sda1"

type ImageResolver func(image string) (*ec2types.Image, error)

func BuildKarpenterBlockDeviceMappingsV1FromKops(rootVolume *kopsapi.InstanceRootVolumeSpec, image *ec2types.Image) ([]*karpenterv1.BlockDeviceMapping, error) {
	root := BuildKarpenterVolumeConfigV1FromKops(rootVolume)

	var amiRoot *ec2types.BlockDeviceMapping
	var amiStripes []ec2types.BlockDeviceMapping
	for _, mapping := range image.BlockDeviceMappings {
		if mapping.Ebs == nil {
			continue
		}
		if aws.ToString(mapping.DeviceName) == aws.ToString(image.RootDeviceName) {
			amiRoot = &mapping
		} else {
			amiStripes = append(amiStripes, mapping)
		}
	}

	if len(amiStripes) == 0 {
		return []*karpenterv1.BlockDeviceMapping{
			{DeviceName: helpers.StringPtr(karpenterRootDeviceName), EBS: root, RootVolume: true},
		}, nil
	}

	if amiRoot == nil {
		return nil, fmt.Errorf("image %s has extra EBS volumes but no root EBS volume", aws.ToString(image.ImageId))
	}
	amiStripeGiB := int64(aws.ToInt32(amiStripes[0].Ebs.VolumeSize))
	for _, mapping := range amiStripes {
		if int64(aws.ToInt32(mapping.Ebs.VolumeSize)) != amiStripeGiB {
			return nil, fmt.Errorf("image %s has EBS volumes of different sizes", aws.ToString(image.ImageId))
		}
	}
	rootExtraGiB := int64(aws.ToInt32(amiRoot.Ebs.VolumeSize)) - amiStripeGiB
	if rootExtraGiB < 0 {
		return nil, fmt.Errorf("image %s has a root volume smaller than its other EBS volumes", aws.ToString(image.ImageId))
	}

	totalGiB := root.VolumeSize.Value() >> 30
	stripeCount := int64(len(amiStripes) + 1)
	stripeGiB := (totalGiB - rootExtraGiB + stripeCount - 1) / stripeCount
	stripeGiB = max(stripeGiB, amiStripeGiB)

	blockDeviceMappings := []*karpenterv1.BlockDeviceMapping{
		{DeviceName: amiRoot.DeviceName, EBS: withSize(root, stripeGiB+rootExtraGiB), RootVolume: true},
	}
	for _, mapping := range amiStripes {
		blockDeviceMappings = append(blockDeviceMappings, &karpenterv1.BlockDeviceMapping{
			DeviceName: mapping.DeviceName,
			EBS:        withSize(root, stripeGiB),
		})
	}
	return blockDeviceMappings, nil
}

func withSize(volume *karpenterv1.BlockDevice, sizeGiB int64) *karpenterv1.BlockDevice {
	sized := volume.DeepCopy()
	sized.VolumeSize = helpers.ResourceToPointer(resource.MustParse(fmt.Sprintf("%dGi", sizeGiB)))
	return sized
}

var karpenterEvictionSignals = map[string]bool{
	"memory.available":   true,
	"nodefs.available":   true,
	"nodefs.inodesFree":  true,
	"imagefs.available":  true,
	"imagefs.inodesFree": true,
	"pid.available":      true,
}

func parseEvictionThresholds(thresholds *string) (map[string]string, error) {
	if thresholds == nil || strings.TrimSpace(*thresholds) == "" {
		return nil, nil
	}

	parsed := make(map[string]string)
	for _, threshold := range strings.Split(*thresholds, ",") {
		signal, value, found := strings.Cut(strings.TrimSpace(threshold), "<")
		signal, value = strings.TrimSpace(signal), strings.TrimSpace(value)
		if !found || signal == "" || !isValidEvictionThresholdValue(value) {
			return nil, fmt.Errorf("invalid eviction threshold %q", threshold)
		}
		if karpenterEvictionSignals[signal] {
			parsed[signal] = value
		}
	}

	if len(parsed) == 0 {
		return nil, nil
	}
	return parsed, nil
}

func isValidEvictionThresholdValue(value string) bool {
	if percentage, ok := strings.CutSuffix(value, "%"); ok {
		parsed, err := strconv.ParseFloat(percentage, 64)
		return err == nil && parsed >= 0 && parsed <= 100
	}
	quantity, err := resource.ParseQuantity(value)
	return err == nil && quantity.Sign() >= 0
}

func GetKubeletConfiguration(clusterKubeletSpec, igKubeletSpec *kopsapi.KubeletConfigSpec) (karpenterv1.KubeletConfiguration, error) {
	kubeletSpec := &kopsapi.KubeletConfigSpec{}
	if clusterKubeletSpec != nil {
		kubeletSpec = clusterKubeletSpec.DeepCopy()
	}
	if igKubeletSpec != nil {
		reflectutils.JSONMergeStruct(kubeletSpec, igKubeletSpec)
	}

	evictionHard, err := parseEvictionThresholds(kubeletSpec.EvictionHard)
	if err != nil {
		return nil, err
	}

	kubeletConfiguration := karpenterv1.KubeletConfiguration{}
	if kubeletSpec.MaxPods != nil {
		kubeletConfiguration["maxPods"] = karpenterv1.JSONValue(*kubeletSpec.MaxPods)
	}
	if len(kubeletSpec.SystemReserved) > 0 {
		kubeletConfiguration["systemReserved"] = karpenterv1.JSONValue(kubeletSpec.SystemReserved)
	}
	if len(kubeletSpec.KubeReserved) > 0 {
		kubeletConfiguration["kubeReserved"] = karpenterv1.JSONValue(kubeletSpec.KubeReserved)
	}
	if len(evictionHard) > 0 {
		kubeletConfiguration["evictionHard"] = karpenterv1.JSONValue(evictionHard)
	}
	if len(kubeletConfiguration) == 0 {
		return nil, nil
	}
	return kubeletConfiguration, nil
}

func CreateEC2NodeClassV1(kopsCluster *kopsapi.Cluster, kmp *infrastructurev1alpha1.KopsMachinePool, nodePoolName, terraformOutputDir string, resolveImage ImageResolver) (*karpenterv1.EC2NodeClass, error) {
	amiName, amiAccount, err := GetAmiNameFromImageSource(kmp.Spec.KopsInstanceGroupSpec.Image)
	if err != nil {
		return nil, err
	}

	userData, err := GetUserDataFromTerraformFile(kopsCluster.Name, kmp.Name, terraformOutputDir)
	if err != nil {
		return nil, err
	}

	kubeletConfiguration, err := GetKubeletConfiguration(kopsCluster.Spec.Kubelet, kmp.Spec.KopsInstanceGroupSpec.Kubelet)
	if err != nil {
		return nil, err
	}

	if resolveImage == nil {
		return nil, fmt.Errorf("no image resolver: karpenter node classes need an AWS cloud")
	}
	image, err := resolveImage(kmp.Spec.KopsInstanceGroupSpec.Image)
	if err != nil {
		return nil, fmt.Errorf("resolving image %q: %w", kmp.Spec.KopsInstanceGroupSpec.Image, err)
	}

	blockDeviceMappings, err := BuildKarpenterBlockDeviceMappingsV1FromKops(kmp.Spec.KopsInstanceGroupSpec.RootVolume, image)
	if err != nil {
		return nil, err
	}

	var associatePublicIP bool
	if kmp.Spec.KopsInstanceGroupSpec.AssociatePublicIP != nil {
		associatePublicIP = *kmp.Spec.KopsInstanceGroupSpec.AssociatePublicIP
	} else {
		associatePublicIP = false
	}

	mergedCloudLabels := mergeCloudLabels(kopsCluster.Name, kmp.Name, kopsCluster.Spec.CloudLabels, kmp.Spec.KopsInstanceGroupSpec.CloudLabels)

	ec2NodeClass := karpenterv1.EC2NodeClass{
		TypeMeta: metav1.TypeMeta{
			Kind:       "EC2NodeClass",
			APIVersion: "karpenter.k8s.aws/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: nodePoolName,
			Labels: map[string]string{
				"kops.k8s.io/managed-by": "kops-controller",
			},
		},
		Spec: karpenterv1.EC2NodeClassSpec{
			Kubelet:   kubeletConfiguration,
			AMIFamily: &karpenterv1.AMIFamilyCustom,
			AMISelectorTerms: []karpenterv1.AMISelectorTerm{
				{
					Name:  amiName,
					Owner: amiAccount,
				},
			},
			MetadataOptions: &karpenterv1.MetadataOptions{
				HTTPEndpoint:            helpers.StringPtr("enabled"),
				HTTPProtocolIPv6:        helpers.StringPtr("disabled"),
				HTTPPutResponseHopLimit: helpers.Int64Ptr(3),
				HTTPTokens:              helpers.StringPtr("required"),
			},
			AssociatePublicIPAddress: &associatePublicIP,
			BlockDeviceMappings:      blockDeviceMappings,
			Role:                     fmt.Sprintf("nodes.%s", kopsCluster.Name),
			SecurityGroupSelectorTerms: []karpenterv1.SecurityGroupSelectorTerm{
				{
					Name: fmt.Sprintf("nodes.%s", kopsCluster.Name),
				},
				{
					Tags: map[string]string{
						fmt.Sprintf("karpenter/%s/%s", kopsCluster.Name, kmp.Name): "true",
					},
				},
			},
			SubnetSelectorTerms: []karpenterv1.SubnetSelectorTerm{
				{
					Tags: map[string]string{
						fmt.Sprintf("kops.k8s.io/instance-group/%s", kmp.Name):    "*",
						fmt.Sprintf("kubernetes.io/cluster/%s", kopsCluster.Name): "*",
					},
				},
			},
			Tags:     mergedCloudLabels,
			UserData: helpers.StringPtr(userData),
		},
	}

	return &ec2NodeClass, nil
}
