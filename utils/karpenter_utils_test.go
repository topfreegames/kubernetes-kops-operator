package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	karpenterv1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"
	karpenterv1beta1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kopsapi "k8s.io/kops/pkg/apis/kops"

	infrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"
	"github.com/topfreegames/kubernetes-kops-operator/pkg/helpers"
)

var defaultBlockDeviceV1 = BuildKarpenterVolumeConfigV1FromKops(nil)

func TestCreateEC2NodeClassFromKopsLaunchTemplateInfo(t *testing.T) {
	testCases := []struct {
		description             string
		kopsMachinePoolFunction func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool) *infrastructurev1alpha1.KopsMachinePool
		userData                string
		expectedError           error
		expectedOutputFile      string
	}{
		{
			description:        "should return the populated ec2 node class",
			expectedOutputFile: "fixtures/karpenter/test_successful_ec2_node_class.tpl",
			userData:           "dummy content",
		},
		{
			description: "should fail with a invalid image",
			kopsMachinePoolFunction: func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool) *infrastructurev1alpha1.KopsMachinePool {
				kopsMachinePool.Spec.KopsInstanceGroupSpec.Image = "invalid-image"
				return kopsMachinePool
			},
			expectedError: fmt.Errorf("invalid image format, should receive image source"),
			userData:      "dummy content",
		},
		{
			description:   "should fail with empty user data",
			expectedError: fmt.Errorf("user data file is empty"),
		},
	}
	RegisterFailHandler(Fail)
	g := NewWithT(t)

	for _, tc := range testCases {

		t.Run(tc.description, func(t *testing.T) {

			kopsCluster := helpers.NewKopsCluster("test-cluster")

			kmp := helpers.NewKopsMachinePool("test-machine-pool", "default", "test-cluster")

			terraformOutputDir := filepath.Join(os.TempDir(), kopsCluster.Name)

			err := os.RemoveAll(terraformOutputDir)
			g.Expect(err).NotTo(HaveOccurred())

			err = os.MkdirAll(filepath.Join(terraformOutputDir, "data"), os.ModePerm)
			g.Expect(err).NotTo(HaveOccurred())

			err = os.WriteFile(terraformOutputDir+"/data/aws_launch_template_"+kmp.Name+"."+kopsCluster.Name+"_user_data", []byte(tc.userData), 0644)
			g.Expect(err).NotTo(HaveOccurred())

			if tc.kopsMachinePoolFunction != nil {
				kmp = tc.kopsMachinePoolFunction(kmp)
			}

			ec2NodeClassString, err := CreateEC2NodeClassFromKopsLaunchTemplateInfo(kopsCluster, kmp, kmp.Name, terraformOutputDir)
			if tc.expectedError != nil {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err).To(Equal(tc.expectedError))
			} else {
				g.Expect(err).ToNot(HaveOccurred())
				expectedOutput, err := os.ReadFile(tc.expectedOutputFile)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(ec2NodeClassString).To(Equal(string(expectedOutput)))

			}
		})
	}

}

func TestCreateEC2NodeClassV1FromKopsLaunchTemplateInfo(t *testing.T) {
	testCases := []struct {
		description             string
		kopsMachinePoolFunction func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool) *infrastructurev1alpha1.KopsMachinePool
		userData                string
		expectedError           error
		expectedEC2NodeClass    *karpenterv1.EC2NodeClass
	}{
		{
			description: "should return the populated ec2 node class v1",
			expectedEC2NodeClass: &karpenterv1.EC2NodeClass{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "karpenter.k8s.aws/v1",
					Kind:       "EC2NodeClass",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-machine-pool",
					Labels: map[string]string{
						"kops.k8s.io/managed-by": "kops-controller",
					},
				},
				Spec: karpenterv1.EC2NodeClassSpec{
					AMIFamily: &karpenterv1.AMIFamilyCustom,
					AMISelectorTerms: []karpenterv1.AMISelectorTerm{
						{
							Name: "ubuntu-v1",
						},
					},
					MetadataOptions: &karpenterv1.MetadataOptions{
						HTTPEndpoint:            helpers.StringPtr("enabled"),
						HTTPProtocolIPv6:        helpers.StringPtr("disabled"),
						HTTPPutResponseHopLimit: helpers.Int64Ptr(3),
						HTTPTokens:              helpers.StringPtr("required"),
					},
					AssociatePublicIPAddress: helpers.BoolPtr(false),
					BlockDeviceMappings: []*karpenterv1.BlockDeviceMapping{
						{
							DeviceName: helpers.StringPtr("/dev/sda1"),
							EBS:        defaultBlockDeviceV1,
							RootVolume: true,
						},
					},
					Role: "nodes.test-cluster.test.k8s.cluster",
					SecurityGroupSelectorTerms: []karpenterv1.SecurityGroupSelectorTerm{
						{
							Name: "nodes.test-cluster.test.k8s.cluster",
						},
						{
							Tags: map[string]string{
								"karpenter/test-cluster.test.k8s.cluster/test-machine-pool": "true",
							},
						},
					},
					SubnetSelectorTerms: []karpenterv1.SubnetSelectorTerm{
						{
							Tags: map[string]string{
								"kops.k8s.io/instance-group/test-machine-pool":        "*",
								"kubernetes.io/cluster/test-cluster.test.k8s.cluster": "*",
							},
						},
					},
					Tags: map[string]string{
						"k8s.io/cluster-autoscaler/node-template/label/node-role.kubernetes.io/node": "",
						"Name":                      "test-cluster.test.k8s.cluster/test-machine-pool",
						"kops.k8s.io/instancegroup": "test-machine-pool",
						"KubernetesCluster":         "test-cluster.test.k8s.cluster",
					},
					Kubelet:  &karpenterv1.KubeletConfiguration{},
					UserData: helpers.StringPtr("dummy content"),
				},
			},
			userData: "dummy content",
		},
		{
			description: "should fail with a invalid image",
			kopsMachinePoolFunction: func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool) *infrastructurev1alpha1.KopsMachinePool {
				kopsMachinePool.Spec.KopsInstanceGroupSpec.Image = "invalid-image"
				return kopsMachinePool
			},
			expectedError: fmt.Errorf("invalid image format, should receive image source"),
			userData:      "dummy content",
		},
		{
			description:   "should fail with empty user data",
			expectedError: fmt.Errorf("user data file is empty"),
		},
	}
	RegisterFailHandler(Fail)
	g := NewWithT(t)

	for _, tc := range testCases {

		t.Run(tc.description, func(t *testing.T) {

			kopsCluster := helpers.NewKopsCluster("test-cluster")

			kmp := helpers.NewKopsMachinePool("test-machine-pool", "default", "test-cluster")

			terraformOutputDir := filepath.Join(os.TempDir(), kopsCluster.Name)

			err := os.RemoveAll(terraformOutputDir)
			g.Expect(err).NotTo(HaveOccurred())

			err = os.MkdirAll(filepath.Join(terraformOutputDir, "data"), os.ModePerm)
			g.Expect(err).NotTo(HaveOccurred())

			err = os.WriteFile(terraformOutputDir+"/data/aws_launch_template_"+kmp.Name+"."+kopsCluster.Name+"_user_data", []byte(tc.userData), 0644)
			g.Expect(err).NotTo(HaveOccurred())

			if tc.kopsMachinePoolFunction != nil {
				kmp = tc.kopsMachinePoolFunction(kmp)
			}

			ec2NodeClass, err := CreateEC2NodeClassV1FromKopsLaunchTemplateInfo(kopsCluster, kmp, kmp.Name, terraformOutputDir)
			if tc.expectedError != nil {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err).To(Equal(tc.expectedError))
			} else {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(ec2NodeClass).To(Equal(tc.expectedEC2NodeClass))

			}
		})
	}

}

func TestBuildKarpenterVolumeConfigFromKops(t *testing.T) {
	testCases := []struct {
		description    string
		input          *kopsapi.InstanceRootVolumeSpec
		expectedOutput *karpenterv1beta1.BlockDevice
	}{
		{
			description: "should populate the karpenter volume config with the default values when the kops volume is nil",
			input:       nil,
			expectedOutput: &karpenterv1beta1.BlockDevice{
				VolumeSize: helpers.ResourceToPointer(resource.MustParse("60Gi")),
				VolumeType: helpers.StringPtr("gp3"),
				IOPS:       helpers.Int64Ptr(3000),
				Encrypted:  helpers.BoolPtr(true),
				Throughput: helpers.Int64Ptr(125),
			},
		},
		{
			description: "should populate the karpenter volume config with a 100Gb",
			input: &kopsapi.InstanceRootVolumeSpec{
				Size: helpers.Int32Ptr(100),
			},
			expectedOutput: &karpenterv1beta1.BlockDevice{
				VolumeSize: helpers.ResourceToPointer(resource.MustParse("100Gi")),
				VolumeType: helpers.StringPtr("gp3"),
				IOPS:       helpers.Int64Ptr(3000),
				Encrypted:  helpers.BoolPtr(true),
				Throughput: helpers.Int64Ptr(125),
			},
		},
		{
			description: "should populate the karpenter volume config with type gp2 and iops custom",
			input: &kopsapi.InstanceRootVolumeSpec{
				Type: helpers.StringPtr("gp2"),
				IOPS: helpers.Int32Ptr(1000),
			},
			expectedOutput: &karpenterv1beta1.BlockDevice{
				VolumeSize: helpers.ResourceToPointer(resource.MustParse("60Gi")),
				VolumeType: helpers.StringPtr("gp2"),
				IOPS:       helpers.Int64Ptr(1000),
				Encrypted:  helpers.BoolPtr(true),
				Throughput: helpers.Int64Ptr(125),
			},
		},
		{
			description: "should populate the karpenter volume config without encryption and throughput custom",
			input: &kopsapi.InstanceRootVolumeSpec{
				Encryption: helpers.BoolPtr(false),
				Throughput: helpers.Int32Ptr(500),
			},
			expectedOutput: &karpenterv1beta1.BlockDevice{
				VolumeSize: helpers.ResourceToPointer(resource.MustParse("60Gi")),
				VolumeType: helpers.StringPtr("gp3"),
				IOPS:       helpers.Int64Ptr(3000),
				Encrypted:  helpers.BoolPtr(false),
				Throughput: helpers.Int64Ptr(500),
			},
		},
	}
	RegisterFailHandler(Fail)
	g := NewWithT(t)

	for _, tc := range testCases {

		t.Run(tc.description, func(t *testing.T) {
			output := BuildKarpenterVolumeConfigFromKops(tc.input)
			g.Expect(output).To(Equal(tc.expectedOutput))
		})
	}
}

func TestBuildKarpenterVolumeConfigV1FromKops(t *testing.T) {
	testCases := []struct {
		description    string
		input          *kopsapi.InstanceRootVolumeSpec
		expectedOutput *karpenterv1.BlockDevice
	}{
		{
			description: "should populate the karpenter volume config with the default values when the kops volume is nil",
			input:       nil,
			expectedOutput: &karpenterv1.BlockDevice{
				VolumeSize:          helpers.ResourceToPointer(resource.MustParse("60Gi")),
				VolumeType:          helpers.StringPtr("gp3"),
				IOPS:                helpers.Int64Ptr(3000),
				Encrypted:           helpers.BoolPtr(true),
				Throughput:          helpers.Int64Ptr(125),
				DeleteOnTermination: helpers.BoolPtr(true),
			},
		},
		{
			description: "should populate the karpenter volume config with a 100Gb",
			input: &kopsapi.InstanceRootVolumeSpec{
				Size: helpers.Int32Ptr(100),
			},
			expectedOutput: &karpenterv1.BlockDevice{
				VolumeSize:          helpers.ResourceToPointer(resource.MustParse("100Gi")),
				VolumeType:          helpers.StringPtr("gp3"),
				IOPS:                helpers.Int64Ptr(3000),
				Encrypted:           helpers.BoolPtr(true),
				Throughput:          helpers.Int64Ptr(125),
				DeleteOnTermination: helpers.BoolPtr(true),
			},
		},
		{
			description: "should populate the karpenter volume config with type gp2 and iops custom",
			input: &kopsapi.InstanceRootVolumeSpec{
				Type: helpers.StringPtr("gp2"),
				IOPS: helpers.Int32Ptr(1000),
			},
			expectedOutput: &karpenterv1.BlockDevice{
				VolumeSize:          helpers.ResourceToPointer(resource.MustParse("60Gi")),
				VolumeType:          helpers.StringPtr("gp2"),
				IOPS:                helpers.Int64Ptr(1000),
				Encrypted:           helpers.BoolPtr(true),
				Throughput:          helpers.Int64Ptr(125),
				DeleteOnTermination: helpers.BoolPtr(true),
			},
		},
		{
			description: "should populate the karpenter volume config without encryption and throughput custom",
			input: &kopsapi.InstanceRootVolumeSpec{
				Encryption: helpers.BoolPtr(false),
				Throughput: helpers.Int32Ptr(500),
			},
			expectedOutput: &karpenterv1.BlockDevice{
				VolumeSize:          helpers.ResourceToPointer(resource.MustParse("60Gi")),
				VolumeType:          helpers.StringPtr("gp3"),
				IOPS:                helpers.Int64Ptr(3000),
				Encrypted:           helpers.BoolPtr(false),
				Throughput:          helpers.Int64Ptr(500),
				DeleteOnTermination: helpers.BoolPtr(true),
			},
		},
	}
	RegisterFailHandler(Fail)
	g := NewWithT(t)

	for _, tc := range testCases {

		t.Run(tc.description, func(t *testing.T) {
			output := BuildKarpenterVolumeConfigV1FromKops(tc.input)
			g.Expect(output).To(Equal(tc.expectedOutput))
		})
	}
}
