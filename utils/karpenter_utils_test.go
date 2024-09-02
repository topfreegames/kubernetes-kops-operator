package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	infrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"
	"github.com/topfreegames/kubernetes-kops-operator/pkg/helpers"
)

func TestCreateEC2NodeClassFromKops(t *testing.T) {
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

			ec2NodeClassString, err := CreateEC2NodeClassFromKops(kopsCluster, kmp, kmp.Name, terraformOutputDir)
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
