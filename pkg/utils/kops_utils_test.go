package utils

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/pkg/errors"

	infrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"

	"github.com/aws/aws-sdk-go-v2/aws"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/topfreegames/kubernetes-kops-operator/pkg/helpers"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	controlplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kops/pkg/featureflag"
	"k8s.io/kops/upup/pkg/fi"
	clusterv1betav1 "sigs.k8s.io/cluster-api/api/v1beta1"

	"k8s.io/kubectl/pkg/scheme"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stretchr/testify/assert"
	kopsapi "k8s.io/kops/pkg/apis/kops"

	"k8s.io/kops/util/pkg/vfs"
)

func TestParseSpotinstFeatureflags(t *testing.T) {
	testCases := []struct {
		description          string
		input                *controlplanev1alpha1.KopsControlPlane
		environmentVariables map[string]string
		expectedError        bool
		expectedResult       map[string]bool
	}{
		{
			description: "should enable Spotinst, SpotinstOcean and SpotinstHybrid",
			input: &controlplanev1alpha1.KopsControlPlane{
				Spec: controlplanev1alpha1.KopsControlPlaneSpec{
					SpotInst: controlplanev1alpha1.SpotInstSpec{
						Enabled:      true,
						FeatureFlags: "+SpotinstOcean,SpotinstHybrid",
					},
				},
			},
			environmentVariables: map[string]string{
				"SPOTINST_TOKEN":   "token",
				"SPOTINST_ACCOUNT": "account",
			},
			expectedResult: map[string]bool{
				"Spotinst":       true,
				"SpotinstOcean":  true,
				"SpotinstHybrid": true,
			},
		},
		{
			description: "should enable Spotinst",
			input: &controlplanev1alpha1.KopsControlPlane{
				Spec: controlplanev1alpha1.KopsControlPlaneSpec{
					SpotInst: controlplanev1alpha1.SpotInstSpec{
						Enabled: true,
					},
				},
			},
			environmentVariables: map[string]string{
				"SPOTINST_TOKEN":   "token",
				"SPOTINST_ACCOUNT": "account",
			},
			expectedResult: map[string]bool{
				"Spotinst":       true,
				"SpotinstOcean":  false,
				"SpotinstHybrid": false,
			},
		},

		{
			description: "should not enable any feature flag",
			input:       helpers.NewKopsControlPlane("testKopsControlPlane", metav1.NamespaceDefault),
			environmentVariables: map[string]string{
				"SPOTINST_TOKEN":   "token",
				"SPOTINST_ACCOUNT": "account",
			},
			expectedResult: map[string]bool{
				"Spotinst":       false,
				"SpotinstOcean":  false,
				"SpotinstHybrid": false,
			},
		},
		{
			description: "should return error if credentials environment variables aren't defined",
			input: &controlplanev1alpha1.KopsControlPlane{
				Spec: controlplanev1alpha1.KopsControlPlaneSpec{
					SpotInst: controlplanev1alpha1.SpotInstSpec{
						Enabled:      true,
						FeatureFlags: "+SpotinstOcean,SpotinstHybrid",
					},
				},
			},
			expectedError: true,
			expectedResult: map[string]bool{
				"Spotinst":       false,
				"SpotinstOcean":  false,
				"SpotinstHybrid": false,
			},
		},
	}

	RegisterFailHandler(Fail)
	g := NewWithT(t)

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			_ = os.Unsetenv("SPOTINST_TOKEN")
			_ = os.Unsetenv("SPOTINST_ACCOUNT")
			for key, value := range tc.environmentVariables {
				_ = os.Setenv(key, value)
			}

			err := ParseSpotinstFeatureflags(tc.input)
			if tc.expectedError {
				g.Expect(err).ToNot(BeNil())
			} else {
				g.Expect(err).To(BeNil())
			}
			g.Expect(featureflag.Spotinst.Enabled()).To(BeEquivalentTo(tc.expectedResult["Spotinst"]))
			g.Expect(featureflag.SpotinstOcean.Enabled()).To(BeEquivalentTo(tc.expectedResult["SpotinstOcean"]))
			g.Expect(featureflag.SpotinstHybrid.Enabled()).To(BeEquivalentTo(tc.expectedResult["SpotinstHybrid"]))
		})
	}
}

func Test_GetBucketName(t *testing.T) {

	testCases := []map[string]interface{}{
		{
			"description":    "Should return bucket name",
			"expectedResult": "bucket-test",
			"expectedError":  false,
			"input":          "s3://bucket-test/cluster.general.test.wildlife.io",
		},
		{
			"description":    "Should return err",
			"expectedResult": "bucket-test",
			"expectedError":  true,
			"input":          "",
		},
	}
	for _, tc := range testCases {

		t.Run(tc["description"].(string), func(t *testing.T) {
			bucketName, err := GetBucketName(tc["input"].(string))
			if err == nil && tc["expectedError"] == true {
				assert.Fail(t, "expected error")
			}
			if tc["expectedError"] == false {
				assert.Equal(t, bucketName, tc["expectedResult"])
			}
		})
	}

}

func Test_GetKopsClientset(t *testing.T) {

	testCases := []map[string]interface{}{
		{
			"description":        "Should return clientset for s3",
			"expectedPathPrefix": "s3://",
			"expectedError":      false,
			"input":              "s3://bucket-test/cluster.general.test.wildlife.io",
		},
	}
	vfs.Context.ResetMemfsContext(true)
	for _, tc := range testCases {
		t.Run(tc["description"].(string), func(t *testing.T) {
			configBase := tc["input"].(string)
			clientset, err := GetKopsClientset(configBase)
			if tc["expectedError"].(bool) == false && err != nil {
				assert.Fail(t, "expected error")
			}
			kopsCluster := &kopsapi.Cluster{
				Spec: kopsapi.ClusterSpec{
					ConfigStore: kopsapi.ConfigStoreSpec{
						Base: configBase,
					},
				},
			}
			path, _ := clientset.ConfigBaseFor(kopsCluster)
			assert.True(t, strings.HasPrefix(path.Path(), tc["expectedPathPrefix"].(string)))
		})
	}

}

func TestReconcileKopsSecretsDelete(t *testing.T) {
	testCases := []struct {
		description               string
		statusKopsSecret          []string
		actualKopsSecrets         []string
		desiredKopsSecrets        map[string][]byte
		expectedKopsSecrets       []string
		expectedStatusKopsSecrets []string
	}{
		{
			description: "shouldn't remove anything",
			statusKopsSecret: []string{
				"dockerconfig",
				"customSecret",
			},
			actualKopsSecrets: []string{
				"dockerconfig",
				"customSecret",
				"admin",
				"kube-proxy",
			},
			desiredKopsSecrets: map[string][]byte{
				"dockerconfig": []byte(""),
				"customSecret": []byte(""),
			},
			expectedKopsSecrets: []string{
				"dockerconfig",
				"customSecret",
				"admin",
				"kube-proxy",
			},
			expectedStatusKopsSecrets: []string{
				"dockerconfig",
				"customSecret",
			},
		},
		{
			description: "should remove customSecret",
			statusKopsSecret: []string{
				"dockerconfig",
				"customSecret",
			},
			actualKopsSecrets: []string{
				"dockerconfig",
				"customSecret",
				"admin",
				"kube-proxy",
			},
			desiredKopsSecrets: map[string][]byte{
				"dockerconfig": []byte(""),
			},
			expectedKopsSecrets: []string{
				"dockerconfig",
				"admin",
				"kube-proxy",
			},
			expectedStatusKopsSecrets: []string{
				"dockerconfig",
			},
		},
		{
			description: "should only remove secret from status when it was already deleted",
			statusKopsSecret: []string{
				"dockerconfig",
				"customSecret",
			},
			actualKopsSecrets: []string{
				"dockerconfig",
				"admin",
				"kube-proxy",
			},
			desiredKopsSecrets: map[string][]byte{
				"dockerconfig": []byte(""),
			},
			expectedKopsSecrets: []string{
				"dockerconfig",
				"admin",
				"kube-proxy",
			},
			expectedStatusKopsSecrets: []string{
				"dockerconfig",
			},
		},
	}

	ctx := context.TODO()
	vfs.Context.ResetMemfsContext(true)
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {

			fakeKopsClientset := helpers.NewFakeKopsClientset()
			kopsCluster := &kopsapi.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testCluster",
				},
				Spec: kopsapi.ClusterSpec{
					ConfigStore: kopsapi.ConfigStoreSpec{
						Base: "memfs://bucket-test/testCluster",
					},
				},
			}
			_, _ = fakeKopsClientset.CreateCluster(ctx, kopsCluster)
			fakeSecretStore, _ := fakeKopsClientset.SecretStore(kopsCluster)
			kopsSecret := &fi.Secret{}
			for _, kopsSecretName := range tc.actualKopsSecrets {
				_, _, _ = fakeSecretStore.GetOrCreateSecret(ctx, kopsSecretName, kopsSecret)
			}

			kopsControlPlane := &controlplanev1alpha1.KopsControlPlane{
				Status: controlplanev1alpha1.KopsControlPlaneStatus{
					Secrets: tc.statusKopsSecret,
				},
			}

			err := reconcileKopsSecretsDelete(fakeSecretStore, kopsControlPlane, tc.desiredKopsSecrets)
			assert.Nil(t, err)
			for _, secretName := range tc.expectedKopsSecrets {
				secret, _ := fakeSecretStore.FindSecret(secretName)
				assert.NotNil(t, secret)
			}
			assert.Equal(t, tc.expectedStatusKopsSecrets, kopsControlPlane.Status.Secrets)

		})
	}
}

func TestReconcileKopsSecrets(t *testing.T) {
	testCases := []struct {
		description               string
		statusKopsSecret          []string
		actualKopsSecrets         map[string][]byte
		desiredKopsSecrets        map[string][]byte
		expectedKopsSecrets       map[string][]byte
		expectedStatusKopsSecrets []string
	}{
		{
			description: "should create desired secrets",
			actualKopsSecrets: map[string][]byte{
				"admin":      []byte("admin"),
				"kube-proxy": []byte("kube-proxy"),
			},
			desiredKopsSecrets: map[string][]byte{
				"dockerconfig": []byte("{}"),
				"customSecret": []byte("testSecretContent"),
			},
			expectedKopsSecrets: map[string][]byte{
				"admin":        []byte("admin"),
				"kube-proxy":   []byte("kube-proxy"),
				"dockerconfig": []byte("{}"),
				"customSecret": []byte("testSecretContent"),
			},
			expectedStatusKopsSecrets: []string{
				"dockerconfig",
				"customSecret",
			},
		},
		{
			description: "should update dockerconfig secret",
			statusKopsSecret: []string{
				"dockerconfig",
				"customSecret",
			},
			actualKopsSecrets: map[string][]byte{
				"admin":        []byte("admin"),
				"kube-proxy":   []byte("kube-proxy"),
				"dockerconfig": []byte("{}"),
				"customSecret": []byte("testSecretContent"),
			},
			desiredKopsSecrets: map[string][]byte{
				"dockerconfig": []byte("dockerconfig updated"),
				"customSecret": []byte("testSecretContent"),
			},
			expectedKopsSecrets: map[string][]byte{
				"admin":        []byte("admin"),
				"kube-proxy":   []byte("kube-proxy"),
				"dockerconfig": []byte("dockerconfig updated"),
				"customSecret": []byte("testSecretContent"),
			},
			expectedStatusKopsSecrets: []string{
				"dockerconfig",
				"customSecret",
			},
		},
		{
			description: "should just update status",
			statusKopsSecret: []string{
				"dockerconfig",
			},
			actualKopsSecrets: map[string][]byte{
				"admin":        []byte("admin"),
				"kube-proxy":   []byte("kube-proxy"),
				"dockerconfig": []byte("{}"),
				"customSecret": []byte("testSecretContent"),
			},
			desiredKopsSecrets: map[string][]byte{
				"dockerconfig": []byte("{}"),
				"customSecret": []byte("testSecretContent"),
			},
			expectedKopsSecrets: map[string][]byte{
				"admin":        []byte("admin"),
				"kube-proxy":   []byte("kube-proxy"),
				"dockerconfig": []byte("{}"),
				"customSecret": []byte("testSecretContent"),
			},
			expectedStatusKopsSecrets: []string{
				"dockerconfig",
				"customSecret",
			},
		},
	}

	ctx := context.TODO()
	vfs.Context.ResetMemfsContext(true)
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {

			fakeKopsClientset := helpers.NewFakeKopsClientset()
			kopsCluster := &kopsapi.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testCluster",
				},
				Spec: kopsapi.ClusterSpec{
					ConfigStore: kopsapi.ConfigStoreSpec{
						Base: "memfs://bucket-test/testCluster",
					},
				},
			}
			_, _ = fakeKopsClientset.CreateCluster(ctx, kopsCluster)
			fakeSecretStore, _ := fakeKopsClientset.SecretStore(kopsCluster)
			for kopsSecretName, data := range tc.actualKopsSecrets {
				kopsSecret := &fi.Secret{
					Data: data,
				}
				_, _, _ = fakeSecretStore.GetOrCreateSecret(ctx, kopsSecretName, kopsSecret)
			}

			kopsControlPlane := &controlplanev1alpha1.KopsControlPlane{
				Status: controlplanev1alpha1.KopsControlPlaneStatus{
					Secrets: tc.statusKopsSecret,
				},
			}

			err := reconcileKopsSecretsNormal(ctx, fakeSecretStore, kopsControlPlane, tc.desiredKopsSecrets)
			assert.Nil(t, err)
			for secretName, data := range tc.expectedKopsSecrets {
				secret, _ := fakeSecretStore.FindSecret(secretName)
				assert.NotNil(t, secret)
				assert.True(t, bytes.Equal(data, secret.Data))
			}
			assert.ElementsMatch(t, tc.expectedStatusKopsSecrets, kopsControlPlane.Status.Secrets)
		})
	}
}

func TestGetAmiNameFromImageSource(t *testing.T) {
	testCases := []struct {
		description   string
		input         string
		output        string
		output2       string
		expectedError error
	}{
		{
			description: "should return the ami name from the image source",
			input:       "000000000000/ubuntu-v1.0.0",
			output:      "ubuntu-v1.0.0",
			output2:     "000000000000",
		},
		{
			description:   "should fail when receiving ami id",
			input:         "ami-000000000000",
			expectedError: errors.New("invalid image format, should receive image source"),
		},
		{
			description:   "should fail when receiving ami name",
			input:         "ubuntu-v1.0.0",
			expectedError: errors.New("invalid image format, should receive image source"),
		},
	}
	RegisterFailHandler(Fail)
	g := NewWithT(t)

	for _, tc := range testCases {

		t.Run(tc.description, func(t *testing.T) {

			amiName, amiAccount, err := GetAmiNameFromImageSource(tc.input)
			if tc.expectedError != nil {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(BeEquivalentTo(tc.expectedError.Error()))

			} else {
				g.Expect(err).To(BeNil())
				g.Expect(amiName).To(Equal(tc.output))
				g.Expect(amiAccount).To(Equal(tc.output2))
			}

		})
	}

}

func TestGetUserDataFromTerraformFile(t *testing.T) {
	testCases := []struct {
		description        string
		userData           string
		terraformFile      string
		expectedError      error
		writeTerraformFile bool
	}{
		{
			description:        "should return the user data from the terraform file",
			userData:           "dummy content",
			writeTerraformFile: true,
		},
		{
			description:   "should fail if the user data is not found",
			expectedError: &fs.PathError{Op: "open", Path: "/tmp/test-cluster.test.k8s.cluster/data/aws_launch_template_test-machine-pool.test-cluster.test.k8s.cluster_user_data", Err: syscall.Errno(0x2)},
		},
		{
			description:        "should fail if the user data is empty",
			writeTerraformFile: true,
			expectedError:      errors.New("user data file is empty"),
		},
	}
	RegisterFailHandler(Fail)
	g := NewWithT(t)

	for _, tc := range testCases {

		t.Run(tc.description, func(t *testing.T) {

			kopsCluster := helpers.NewKopsCluster("test-cluster")

			terraformOutputDir := fmt.Sprintf("/tmp/%s", kopsCluster.Name)

			err := os.RemoveAll(terraformOutputDir)
			g.Expect(err).NotTo(HaveOccurred())

			err = os.MkdirAll(filepath.Join(terraformOutputDir, "data"), os.ModePerm)
			g.Expect(err).NotTo(HaveOccurred())

			kmp := helpers.NewKopsMachinePool("test-machine-pool", "default", "test-cluster")

			if tc.writeTerraformFile {
				err := os.WriteFile(terraformOutputDir+"/data/aws_launch_template_"+kmp.Name+"."+kopsCluster.Name+"_user_data", []byte(tc.userData), 0644)
				g.Expect(err).NotTo(HaveOccurred())
			}
			userDataString, err := GetUserDataFromTerraformFile(kopsCluster.Name, kmp.Name, terraformOutputDir)
			if tc.expectedError != nil {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(BeEquivalentTo(tc.expectedError.Error()))
			} else {
				g.Expect(err).To(BeNil())
				g.Expect(userDataString).To(Equal(tc.userData))
			}

		})
	}

}

func TestDeleteOwnerResources(t *testing.T) {
	RegisterFailHandler(Fail)
	g := NewWithT(t)
	testCases := []struct {
		description      string
		input            client.Object
		k8sObjects       []client.Object
		validateFunction func(kubeClient client.Client) bool
	}{
		{
			description: "should delete kcp owner reference",
			input:       helpers.NewKopsControlPlane("test-controlplane", metav1.NamespaceDefault),
			k8sObjects: []client.Object{
				newCluster("test-controlplane", "test-controlplane", metav1.NamespaceDefault),
				helpers.NewKopsControlPlane("test-controlplane", metav1.NamespaceDefault),
			},
			validateFunction: func(kubeClient client.Client) bool {
				cluster := &clusterv1betav1.Cluster{}
				err := kubeClient.Get(context.TODO(), client.ObjectKey{Name: "test-controlplane", Namespace: metav1.NamespaceDefault}, cluster)
				return apierrors.IsNotFound(err)
			},
		},
		{
			description: "should delete kmp owner reference",
			input:       helpers.NewKopsMachinePool("test-kops-machine-pool", metav1.NamespaceDefault, "test-controlplane"),
			k8sObjects: []client.Object{
				helpers.NewKopsMachinePool("test-kops-machine-pool", metav1.NamespaceDefault, "test-controlplane"),
				helpers.NewKopsControlPlane("test-controlplane", metav1.NamespaceDefault),
				&clusterv1betav1.Machine{
					TypeMeta: metav1.TypeMeta{
						Kind:       "Machine",
						APIVersion: "cluster.x-k8s.io/v1beta1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-machine",
						UID:       "1",
						Namespace: metav1.NamespaceDefault,
					},
				},
			},
			validateFunction: func(kubeClient client.Client) bool {
				machine := &clusterv1betav1.Machine{}
				err := kubeClient.Get(context.TODO(), client.ObjectKey{Name: "test-machine", Namespace: metav1.NamespaceDefault}, machine)
				return apierrors.IsNotFound(err)
			},
		},
	}

	err := clusterv1betav1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = controlplanev1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = infrastructurev1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(tc.k8sObjects...).Build()
			err := clusterv1betav1.AddToScheme(scheme.Scheme)
			Expect(err).NotTo(HaveOccurred())
			err = DeleteOwnerResources(context.TODO(), fakeClient, tc.input)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(tc.validateFunction(fakeClient)).To(BeTrue())
		})
	}
}

func TestGetClusterByName(t *testing.T) {
	testCases := []struct {
		description   string
		clusters      []client.Object
		expectedError bool
	}{
		{
			description:   "Should successfully return cluster",
			expectedError: false,
			clusters: []client.Object{
				newCluster("test-cluster", "", metav1.NamespaceDefault),
			},
		},
		{
			description:   "Cluster don't exist, should return error",
			expectedError: true,
			clusters:      nil,
		},
	}

	RegisterFailHandler(Fail)
	g := NewWithT(t)
	ctx := context.TODO()

	err := clusterv1betav1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(tc.clusters...).Build()
			cluster, err := GetClusterByName(ctx, fakeClient, metav1.NamespaceDefault, "test-cluster.k8s.cluster")
			if !tc.expectedError {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(cluster).NotTo(BeNil())
			} else {
				g.Expect(err).To(HaveOccurred())
			}

		})
	}
}

func TestSetAWSEnvFromKopsControlPlaneSecret(t *testing.T) {
	testCases := []struct {
		description   string
		k8sObjects    []client.Object
		expectedError bool
	}{
		{
			description:   "Should successfully set AWS envs",
			expectedError: false,
			k8sObjects: []client.Object{
				newAWSCredentialSecret("11111111-credential", "kubernetes-kops-operator-system"),
			},
		},
	}

	RegisterFailHandler(Fail)
	g := NewWithT(t)

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			awsCredentials := aws.Credentials{
				AccessKeyID:     "11111111-credential",
				SecretAccessKey: "kubernetes-kops-operator-system",
			}
			err := SetEnvVarsFromAWSCredentials(awsCredentials)
			if !tc.expectedError {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(os.Getenv("AWS_ACCESS_KEY_ID")).To(Equal("11111111-credential"))
				g.Expect(os.Getenv("AWS_SECRET_ACCESS_KEY")).To(Equal("kubernetes-kops-operator-system"))
			} else {
				g.Expect(err).To(HaveOccurred())
			}

		})
	}
}

func TestGetAwsCredentialsFromKopsControlPlaneSecret(t *testing.T) {
	testCases := []struct {
		description           string
		k8sObjects            []client.Object
		expectedAwsCredential *aws.Credentials
		expectedError         bool
	}{
		{
			description:   "Should successfully set AWS envs",
			expectedError: false,
			k8sObjects: []client.Object{
				newAWSCredentialSecret("accessTest", "secretTest"),
			},
			expectedAwsCredential: &aws.Credentials{AccessKeyID: "accessTest", SecretAccessKey: "secretTest"},
		},
		{
			description:   "Should fail if can't get secret",
			expectedError: true,
			k8sObjects:    []client.Object{},
		},
	}

	RegisterFailHandler(Fail)
	g := NewWithT(t)
	ctx := context.TODO()

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(tc.k8sObjects...).Build()
			credential, err := GetAWSCredentialsFromKopsControlPlaneSecret(ctx, fakeClient, "11111111-credential", "kubernetes-kops-operator-system")
			if !tc.expectedError {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(credential).To(Equal(tc.expectedAwsCredential))
			} else {
				g.Expect(err).To(HaveOccurred())
			}

		})
	}
}

func newAWSCredentialSecret(accessKey, secret string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "11111111-credential",
			Namespace: "kubernetes-kops-operator-system",
		},
		Data: map[string][]byte{
			"AccessKeyID":     []byte(accessKey),
			"SecretAccessKey": []byte(secret),
		},
	}
}

func newCluster(name, controlplane, namespace string) *clusterv1betav1.Cluster {
	return &clusterv1betav1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      fmt.Sprintf("%s.k8s.cluster", name),
		},
		Spec: clusterv1betav1.ClusterSpec{
			ControlPlaneRef: &corev1.ObjectReference{
				Name:      controlplane,
				Namespace: namespace,
				Kind:      "KopsControlPlane",
			},
		},
	}
}
