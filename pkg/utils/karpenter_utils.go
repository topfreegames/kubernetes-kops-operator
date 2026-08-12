package utils

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"
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

// validEvictionSignals mirrors the signals Karpenter accepts in
// EC2NodeClass.spec.kubelet.evictionHard. Kept so parseEvictionHard can reject typos rather than
// silently dropping them.
var validEvictionSignals = []string{
	"memory.available",
	"nodefs.available",
	"nodefs.inodesFree",
	"imagefs.available",
	"imagefs.inodesFree",
	"pid.available",
}

// parseEvictionHard converts kops' comma-delimited hard eviction expression list
// ("memory.available<1Gi,nodefs.available<10%") into the signal-to-quantity map that Karpenter's
// KubeletConfiguration expects ({"memory.available": "1Gi", "nodefs.available": "10%"}).
//
// Only the "<" operator is handled, because that is the only operator kubelet accepts for hard
// eviction thresholds. Unknown signals and unparseable quantities are rejected rather than skipped:
// a silently dropped threshold makes Karpenter compute a larger allocatable than the node actually
// honours, which is the failure this mapping exists to prevent.
func parseEvictionHard(evictionHard string) (map[string]string, error) {
	thresholds := map[string]string{}

	for _, expression := range strings.Split(evictionHard, ",") {
		expression = strings.TrimSpace(expression)
		if expression == "" {
			continue
		}

		signal, quantity, found := strings.Cut(expression, "<")
		if !found {
			return nil, fmt.Errorf("invalid hard eviction expression %q, expected format signal<quantity, e.g. memory.available<1Gi", expression)
		}

		signal = strings.TrimSpace(signal)
		quantity = strings.TrimSpace(quantity)

		if !slices.Contains(validEvictionSignals, signal) {
			return nil, fmt.Errorf("invalid hard eviction signal %q, valid signals are %s", signal, strings.Join(validEvictionSignals, ", "))
		}

		if err := validateEvictionQuantity(quantity); err != nil {
			return nil, fmt.Errorf("invalid hard eviction expression %q: %w", expression, err)
		}

		thresholds[signal] = quantity
	}

	if len(thresholds) == 0 {
		return nil, nil
	}

	return thresholds, nil
}

// validateEvictionQuantity accepts either a resource quantity ("1Gi", "300Mi") or a percentage
// ("10%"), the two forms both kubelet and Karpenter support.
func validateEvictionQuantity(quantity string) error {
	if quantity == "" {
		return fmt.Errorf("missing quantity")
	}

	if percentage, isPercentage := strings.CutSuffix(quantity, "%"); isPercentage {
		value, err := strconv.ParseFloat(percentage, 64)
		if err != nil {
			return fmt.Errorf("quantity %q is not a valid percentage", quantity)
		}
		if value < 0 || value > 100 {
			return fmt.Errorf("percentage %q must be between 0%% and 100%%", quantity)
		}
		return nil
	}

	if _, err := resource.ParseQuantity(quantity); err != nil {
		return fmt.Errorf("quantity %q is neither a valid resource quantity nor a percentage", quantity)
	}

	return nil
}

// GetKubeletConfiguration builds the KubeletConfiguration attached to an EC2NodeClass. Karpenter
// uses it to compute a node's allocatable capacity, which drives bin-packing, instance type
// selection and consolidation simulation.
//
// Instance group values override cluster-wide ones per field, mirroring kops' own precedence, since
// every instance group gets its own EC2NodeClass.
//
// This only shapes Karpenter's *scheduling* model. AMIFamily is Custom and UserData comes from the
// kops-generated nodeup script, so Karpenter never configures kubelet on the node — kops does, from
// these same specs. Any field omitted here therefore makes Karpenter over-estimate allocatable
// relative to what the node enforces, and lets it over-pack nodes.
func GetKubeletConfiguration(clusterKubeletSpec, instanceGroupKubeletSpec *kopsapi.KubeletConfigSpec) (*karpenterv1.KubeletConfiguration, error) {
	kubeletConfiguration := &karpenterv1.KubeletConfiguration{}

	for _, kubeletSpec := range []*kopsapi.KubeletConfigSpec{clusterKubeletSpec, instanceGroupKubeletSpec} {
		if kubeletSpec == nil {
			continue
		}

		if kubeletSpec.MaxPods != nil {
			kubeletConfiguration.MaxPods = kubeletSpec.MaxPods
		}

		if kubeletSpec.KubeReserved != nil {
			kubeletConfiguration.KubeReserved = kubeletSpec.KubeReserved
		}

		if kubeletSpec.SystemReserved != nil {
			kubeletConfiguration.SystemReserved = kubeletSpec.SystemReserved
		}

		if kubeletSpec.EvictionHard != nil {
			evictionHard, err := parseEvictionHard(*kubeletSpec.EvictionHard)
			if err != nil {
				return nil, err
			}
			kubeletConfiguration.EvictionHard = evictionHard
		}
	}

	return kubeletConfiguration, nil
}

func CreateEC2NodeClass(kopsCluster *kopsapi.Cluster, kmp *infrastructurev1alpha1.KopsMachinePool, nodePoolName, terraformOutputDir string) (string, error) {
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

func CreateEC2NodeClassV1(kopsCluster *kopsapi.Cluster, kmp *infrastructurev1alpha1.KopsMachinePool, nodePoolName, terraformOutputDir string) (*karpenterv1.EC2NodeClass, error) {
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
