package utils

import (
	"bytes"
	"fmt"
	"text/template"

	karpenterv1beta1 "github.com/aws/karpenter/pkg/apis/v1beta1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/Masterminds/sprig/v3"
	infrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"
	kopsapi "k8s.io/kops/pkg/apis/kops"
)

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

func CreateEC2NodeClassFromKopsLaunchTemplateInfo(kopsCluster *kopsapi.Cluster, kmp *infrastructurev1alpha1.KopsMachinePool, nodePoolName, terraformOutputDir string) (string, error) {
	amiName, err := GetAmiNameFromImageSource(kmp.Spec.KopsInstanceGroupSpec.Image)
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

	data := struct {
		Name              string
		AmiName           string
		ClusterName       string
		IGName            string
		Tags              map[string]string
		RootVolume        *karpenterv1beta1.BlockDevice
		UserData          string
		AssociatePublicIP bool
	}{
		Name:              nodePoolName,
		AmiName:           amiName,
		IGName:            kmp.Name,
		ClusterName:       kopsCluster.Name,
		Tags:              kopsCluster.Spec.CloudLabels,
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
