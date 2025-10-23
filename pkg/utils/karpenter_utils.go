package utils

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	karpenterv1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"
	karpenterv1beta1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1beta1"

	"k8s.io/apimachinery/pkg/api/resource"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	infrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"
	"github.com/topfreegames/kubernetes-kops-operator/pkg/helpers"
	kopsapi "k8s.io/kops/pkg/apis/kops"
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

func BuildKarpenterVolumeConfigFromKops(kopsVolume *kopsapi.InstanceRootVolumeSpec) *karpenterv1beta1.BlockDevice {

	var karpenterVolumeConfig *karpenterv1beta1.BlockDevice

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

		karpenterVolumeConfig = &karpenterv1beta1.BlockDevice{
			VolumeSize: &volumeSize,
			VolumeType: volumeType,
			IOPS:       &volumeIOPS,
			Encrypted:  &volumeEncryption,
			Throughput: &volumeThroughput,
		}
	} else {
		karpenterVolumeConfig = &karpenterv1beta1.BlockDevice{
			VolumeSize: &defaultVolumeSize,
			VolumeType: &defaultVolumeType,
			IOPS:       &defaultIOPS,
			Encrypted:  &defaultEncrypted,
			Throughput: &defaultThroughput,
		}
	}

	return karpenterVolumeConfig
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

func GetKubeletConfiguration(kubeletSpec *kopsapi.KubeletConfigSpec) *karpenterv1.KubeletConfiguration {
	if kubeletSpec == nil {
		return &karpenterv1.KubeletConfiguration{}
	}

	return &karpenterv1.KubeletConfiguration{
		MaxPods:        kubeletSpec.MaxPods,
		SystemReserved: kubeletSpec.SystemReserved,
		KubeReserved:   kubeletSpec.KubeReserved,
	}
}

func CreateEC2NodeClassFromKopsLaunchTemplateInfo(kopsCluster *kopsapi.Cluster, kmp *infrastructurev1alpha1.KopsMachinePool, nodePoolName, terraformOutputDir string) (string, error) {
	amiName, amiAccount, err := GetAmiNameFromImageSource(kmp.Spec.KopsInstanceGroupSpec.Image)
	if err != nil {
		return "", err
	}

	userData, err := GetUserDataFromTerraformFile(kopsCluster.Name, kmp.Name, terraformOutputDir)
	if err != nil {
		return "", err
	}

	var associatePublicIP bool
	if kmp.Spec.KopsInstanceGroupSpec.AssociatePublicIP != nil {
		associatePublicIP = *kmp.Spec.KopsInstanceGroupSpec.AssociatePublicIP
	} else {
		associatePublicIP = false
	}

	mergedCloudLabels := mergeCloudLabels(kopsCluster.Name, kmp.Name, kopsCluster.Spec.CloudLabels, kmp.Spec.KopsInstanceGroupSpec.CloudLabels)

	data := struct {
		Name              string
		AmiName           string
		AmiAccount        string
		ClusterName       string
		IGName            string
		Tags              map[string]string
		RootVolume        *karpenterv1beta1.BlockDevice
		UserData          string
		AssociatePublicIP bool
	}{
		Name:              nodePoolName,
		AmiName:           amiName,
		AmiAccount:        amiAccount,
		IGName:            kmp.Name,
		ClusterName:       kopsCluster.Name,
		Tags:              mergedCloudLabels,
		RootVolume:        BuildKarpenterVolumeConfigFromKops(kmp.Spec.KopsInstanceGroupSpec.RootVolume),
		UserData:          userData,
		AssociatePublicIP: associatePublicIP,
	}

	content, err := templates.ReadFile("templates/ec2nodeclass.yaml.tpl")
	if err != nil {
		return "", err
	}

	t, err := template.New("template").Funcs(sprig.TxtFuncMap()).Parse(string(content))
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	err = t.Execute(&buf, data)
	if err != nil {
		return "", err
	}

	return buf.String(), nil
}

func CreateEC2NodeClassV1FromKopsLaunchTemplateInfo(kopsCluster *kopsapi.Cluster, kmp *infrastructurev1alpha1.KopsMachinePool, nodePoolName, terraformOutputDir string) (*karpenterv1.EC2NodeClass, error) {
	amiName, amiAccount, err := GetAmiNameFromImageSource(kmp.Spec.KopsInstanceGroupSpec.Image)
	if err != nil {
		return nil, err
	}

	userData, err := GetUserDataFromTerraformFile(kopsCluster.Name, kmp.Name, terraformOutputDir)
	if err != nil {
		return nil, err
	}

	kubeletConfiguration := GetKubeletConfiguration(kopsCluster.Spec.Kubelet)

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
			BlockDeviceMappings: []*karpenterv1.BlockDeviceMapping{
				{
					DeviceName: helpers.StringPtr("/dev/sda1"),
					EBS:        BuildKarpenterVolumeConfigV1FromKops(kmp.Spec.KopsInstanceGroupSpec.RootVolume),
					RootVolume: true,
				},
			},
			Role: fmt.Sprintf("nodes.%s", kopsCluster.Name),
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
