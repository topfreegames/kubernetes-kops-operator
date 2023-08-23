package controlplane

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/topfreegames/kubernetes-kops-operator/pkg/helpers"

	asgTypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	infrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	controlplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	"github.com/topfreegames/kubernetes-kops-operator/utils"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	kopsapi "k8s.io/kops/pkg/apis/kops"
	"k8s.io/kops/pkg/client/simple"
	"k8s.io/kops/pkg/validation"
	"k8s.io/kops/upup/pkg/fi"
	"k8s.io/kops/util/pkg/vfs"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/secret"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestEvaluateKopsValidationResult(t *testing.T) {
	testCases := []map[string]interface{}{
		{
			"description":    "should succeeded without failures and nodes",
			"input":          &validation.ValidationCluster{},
			"expectedResult": true,
		},
		{
			"description": "should fail with failures not empty",
			"input": &validation.ValidationCluster{
				Failures: []*validation.ValidationError{
					{
						Name: "TestError",
					},
				},
			},
			"expectedResult": false,
		},
		{
			"description": "should succeed with nodes with condition true",
			"input": &validation.ValidationCluster{
				Failures: []*validation.ValidationError{},
				Nodes: []*validation.ValidationNode{
					{
						Name:   "Test1",
						Status: corev1.ConditionTrue,
					},
					{
						Name:   "Test2",
						Status: corev1.ConditionTrue,
					},
				},
			},
			"expectedResult": true,
		},
		{
			"description": "should fail if any node with condition false",
			"input": &validation.ValidationCluster{
				Failures: []*validation.ValidationError{},
				Nodes: []*validation.ValidationNode{
					{
						Name:   "Test1",
						Status: corev1.ConditionTrue,
					},
					{
						Name:   "Test2",
						Status: corev1.ConditionFalse,
					},
				},
			},
			"expectedResult": false,
		},
	}

	RegisterFailHandler(Fail)
	g := NewWithT(t)

	for _, tc := range testCases {
		result, _ := utils.EvaluateKopsValidationResult(tc["input"].(*validation.ValidationCluster))
		if tc["expectedResult"].(bool) {
			g.Expect(result).To(BeTrue())
		} else {
			g.Expect(result).To(BeFalse())
		}
	}
}

func TestGetOwnerByRef(t *testing.T) {
	testCases := []map[string]interface{}{
		{
			"description": "Should succeed in retrieving owner",
			"input": &corev1.ObjectReference{
				APIVersion: "cluster.x-k8s.io/v1beta1",
				Kind:       "Cluster",
				Name:       "testCluster",
			},
			"expectedError": false,
		},
		{
			"description": "Should fail to retrieve the owner referenced",
			"input": &corev1.ObjectReference{
				APIVersion: "v1",
				Kind:       "Pod",
				Name:       "testPod",
			},
			"expectedError": true,
		},
	}
	RegisterFailHandler(Fail)
	g := NewWithT(t)

	for _, tc := range testCases {
		t.Run(tc["description"].(string), func(t *testing.T) {
			ctx := context.TODO()
			cluster := &clusterv1.Cluster{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Cluster",
					APIVersion: "cluster.x-k8s.io/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "testCluster",
				},
			}
			fakeClient := helpers.NewMockedK8sClient(cluster)
			owner, err := getOwnerByRef(ctx, fakeClient, tc["input"].(*corev1.ObjectReference))

			if !tc["expectedError"].(bool) {
				g.Expect(err).NotTo(HaveOccurred())
				if owner != nil {
					g.Expect(owner.GetName()).To(Equal(cluster.ObjectMeta.GetName()))
				}
			} else {
				g.Expect(err).To(HaveOccurred())
			}
		})
	}
}

func TestGetClusterOwnerRef(t *testing.T) {
	testCases := []map[string]interface{}{
		{
			"description": "Should succeed in retrieving owner",
			"input": &controlplanev1alpha1.KopsControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "cluster.x-k8s.io/v1beta1",
							Kind:       "Cluster",
							Name:       "testCluster",
						},
					},
				},
			},
			"expectedError": false,
		},
		{
			"description": "Should succeed with many ownerReferences",
			"input": &controlplanev1alpha1.KopsControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "apps/v1",
							Kind:       "Pod",
							Name:       "testPod",
						},
						{
							APIVersion: "apps/v1",
							Kind:       "Deploy",
							Name:       "testDeploy",
						},
						{
							APIVersion: "cluster.x-k8s.io/v1beta1",
							Kind:       "Cluster",
							Name:       "testCluster",
						},
					},
				},
			},
			"expectedError": false,
		},
		{
			"description": "Should return nil when don't belong to a cluster yet",
			"input": &controlplanev1alpha1.KopsControlPlane{
				ObjectMeta: metav1.ObjectMeta{},
			},
			"expectedError": false,
		},
		{
			"description": "Should return NoCluster error when cluster not yet created",
			"input": &controlplanev1alpha1.KopsControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "cluster.x-k8s.io/v1beta1",
							Kind:       "Cluster",
							Name:       "nonExistingCluster",
						},
					},
				},
			},
			"expectedError":     true,
			"expectedErrorType": metav1.StatusReasonNotFound,
		},
	}
	RegisterFailHandler(Fail)
	g := NewWithT(t)

	for _, tc := range testCases {
		t.Run(tc["description"].(string), func(t *testing.T) {
			ctx := context.TODO()
			cluster := &clusterv1.Cluster{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Cluster",
					APIVersion: "cluster.x-k8s.io/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "testCluster",
				},
			}
			fakeClient := helpers.NewMockedK8sClient(cluster)
			reconciler := &KopsControlPlaneReconciler{
				Client: fakeClient,
			}

			owner, err := reconciler.getClusterOwnerRef(ctx, tc["input"].(*controlplanev1alpha1.KopsControlPlane))
			if !tc["expectedError"].(bool) {
				g.Expect(err).NotTo(HaveOccurred())
				if owner != nil {
					g.Expect(owner.GetName()).To(Equal(cluster.ObjectMeta.GetName()))
				}
			} else {
				g.Expect(err).To(HaveOccurred())
				if tc["expectedErrorType"] != nil {
					g.Expect(apierrors.ReasonForError(err)).To(Equal(tc["expectedErrorType"].(metav1.StatusReason)))

				}
			}
		})
	}
}

func TestAddSSHCredential(t *testing.T) {

	testCases := []map[string]interface{}{
		{
			"description":         "Should successfully create SSH credential",
			"kopsClusterFunction": nil,
			"SSHPublicKey":        "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCu7OR4k/qpc6VFqQsMGk7wQcnGzDA/hKABnj3qN85tgIDVsbnOIVgXl4FV1gO+gBjblCLkAmbZYlwhhkosL4xpEc8uk8QWJIzRqalvnLEofdIjClngGqzC40Yu6oVPiqImDazlVNvJ7UdzX02mmYJMe4eRzS1w1dA2hm9uTsaq6CNZuJF2/joV+SKLW88IEXWnb7PdOPZWFy0iN/9JcQKqON7zmR0j1zb4Ydj6Pt9MMIOTRiJpyeTqw0Gy4RWgkKJpwuRhOTnhZ1I8zigXgu4+keMYBgtLLP90Wx6/SI6vt+sG/Zrx5+s0av6vHFH/fDzqX4BSsxY83cOMH6ILLQ1C0hE9ykXx/EAKoou+DT8Doe0wabVxZNMRDOAb0ZnLF1HwUItW+MvgIjtCVpap/jBGmSSqZ5B9cvib7UV+JfLHty7n3AP2SKf52+E3Fp1fP4UiXQ/YUXZksopHLXLtwMdam/qijq5tjk0lVh7j8GGNuejt17+tSOCaP2kNKFyc1u8=",
			"expectedError":       false,
		},
		{
			"description": "Should fail without proper configBase in cluster",
			"kopsClusterFunction": func(kopsCluster *kopsapi.Cluster) *kopsapi.Cluster {
				kopsCluster.Spec.ConfigBase = ""
				return kopsCluster
			},
			"SSHPublicKey":  "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCu7OR4k/qpc6VFqQsMGk7wQcnGzDA/hKABnj3qN85tgIDVsbnOIVgXl4FV1gO+gBjblCLkAmbZYlwhhkosL4xpEc8uk8QWJIzRqalvnLEofdIjClngGqzC40Yu6oVPiqImDazlVNvJ7UdzX02mmYJMe4eRzS1w1dA2hm9uTsaq6CNZuJF2/joV+SKLW88IEXWnb7PdOPZWFy0iN/9JcQKqON7zmR0j1zb4Ydj6Pt9MMIOTRiJpyeTqw0Gy4RWgkKJpwuRhOTnhZ1I8zigXgu4+keMYBgtLLP90Wx6/SI6vt+sG/Zrx5+s0av6vHFH/fDzqX4BSsxY83cOMH6ILLQ1C0hE9ykXx/EAKoou+DT8Doe0wabVxZNMRDOAb0ZnLF1HwUItW+MvgIjtCVpap/jBGmSSqZ5B9cvib7UV+JfLHty7n3AP2SKf52+E3Fp1fP4UiXQ/YUXZksopHLXLtwMdam/qijq5tjk0lVh7j8GGNuejt17+tSOCaP2kNKFyc1u8=",
			"expectedError": true,
		},
		{
			"description":         "Should fail without defining a Public Key",
			"kopsClusterFunction": nil,
			"SSHPublicKey":        "",
			"expectedError":       true,
		},
		{
			"description":         "Should fail with a invalid Public Key",
			"kopsClusterFunction": nil,
			"SSHPublicKey":        "ssh-rsa AAAA/BBBB/CCCC/DDDD=",
			"expectedError":       true,
		},
	}
	RegisterFailHandler(Fail)
	g := NewWithT(t)
	ctx := context.TODO()
	for _, tc := range testCases {
		t.Run(tc["description"].(string), func(t *testing.T) {
			fakeKopsClientset := helpers.NewFakeKopsClientset()
			vfs.Context.ResetMemfsContext(true)
			bareKopsCluster := helpers.NewKopsCluster("test-cluster")
			if tc["kopsClusterFunction"] != nil {
				kopsClusterFunction := tc["kopsClusterFunction"].(func(kopsCluster *kopsapi.Cluster) *kopsapi.Cluster)
				bareKopsCluster = kopsClusterFunction(bareKopsCluster)
			}
			kopsCluster, err := fakeKopsClientset.CreateCluster(ctx, bareKopsCluster)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(kopsCluster).NotTo(BeNil())

			err = addSSHCredential(kopsCluster, fakeKopsClientset, tc["SSHPublicKey"].(string))
			if !tc["expectedError"].(bool) {
				g.Expect(err).NotTo(HaveOccurred())

				sshCredentialStore, err := fakeKopsClientset.SSHCredentialStore(kopsCluster)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(sshCredentialStore).NotTo(BeNil())

				var sshCredentials []*kopsapi.SSHCredential
				sshCredentials, err = sshCredentialStore.FindSSHPublicKeys()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(sshCredentials).NotTo(BeNil())
			} else {
				g.Expect(err).To(HaveOccurred())
			}

		})
	}

}

func TestCreateOrUpdateKopsCluster(t *testing.T) {
	testCases := []struct {
		description         string
		expectedError       bool
		kopsClusterFunction func(kopsCluster *kopsapi.Cluster) *kopsapi.Cluster
		updateCluster       bool
	}{
		{
			description: "Should successfully create Kops Cluster",
		},
		{
			description:   "Should successfully update Kops Cluster",
			updateCluster: true,
		},
		{
			description:   "Should fail validation without required Kops Cluster fields",
			expectedError: true,
			kopsClusterFunction: func(kopsCluster *kopsapi.Cluster) *kopsapi.Cluster {
				return &kopsapi.Cluster{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster",
					},
					Spec: kopsapi.ClusterSpec{},
				}
			},
		},
	}
	RegisterFailHandler(Fail)
	vfs.Context.ResetMemfsContext(true)
	g := NewWithT(t)
	ctx := context.TODO()
	dummySSHPublicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCu7OR4k/qpc6VFqQsMGk7wQcnGzDA/hKABnj3qN85tgIDVsbnOIVgXl4FV1gO+gBjblCLkAmbZYlwhhkosL4xpEc8uk8QWJIzRqalvnLEofdIjClngGqzC40Yu6oVPiqImDazlVNvJ7UdzX02mmYJMe4eRzS1w1dA2hm9uTsaq6CNZuJF2/joV+SKLW88IEXWnb7PdOPZWFy0iN/9JcQKqON7zmR0j1zb4Ydj6Pt9MMIOTRiJpyeTqw0Gy4RWgkKJpwuRhOTnhZ1I8zigXgu4+keMYBgtLLP90Wx6/SI6vt+sG/Zrx5+s0av6vHFH/fDzqX4BSsxY83cOMH6ILLQ1C0hE9ykXx/EAKoou+DT8Doe0wabVxZNMRDOAb0ZnLF1HwUItW+MvgIjtCVpap/jBGmSSqZ5B9cvib7UV+JfLHty7n3AP2SKf52+E3Fp1fP4UiXQ/YUXZksopHLXLtwMdam/qijq5tjk0lVh7j8GGNuejt17+tSOCaP2kNKFyc1u8="
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			bareKopsCluster := helpers.NewKopsCluster("test-cluster")
			if tc.kopsClusterFunction != nil {
				kopsClusterFunction := tc.kopsClusterFunction
				bareKopsCluster = kopsClusterFunction(bareKopsCluster)
			}
			fakeKopsClientset := helpers.NewFakeKopsClientset()

			if tc.updateCluster == true {
				bareKopsCluster, err := fakeKopsClientset.CreateCluster(ctx, bareKopsCluster)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(bareKopsCluster).NotTo(BeNil())
				bareKopsCluster.Spec.KubernetesVersion = "1.21.0"
			}

			reconciler := &KopsControlPlaneReconciler{
				GetClusterStatusFactory: func(kopsCluster *kopsapi.Cluster, cloud fi.Cloud) (*kopsapi.ClusterStatus, error) {
					return nil, nil
				},
			}

			err := reconciler.createOrUpdateKopsCluster(ctx, fakeKopsClientset, bareKopsCluster, dummySSHPublicKey, nil)
			if !tc.expectedError {
				g.Expect(err).NotTo(HaveOccurred())
				cluster, err := fakeKopsClientset.GetCluster(ctx, bareKopsCluster.Name)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(cluster).NotTo(BeNil())
				if tc.updateCluster == true {
					g.Expect(cluster.Spec.KubernetesVersion).To(Equal("1.21.0"))
				}
			} else {
				g.Expect(err).To(HaveOccurred())
			}
		})
	}
}

func TestKopsControlPlaneReconciler(t *testing.T) {
	testCases := []struct {
		description              string
		expectedResult           ctrl.Result
		expectedError            bool
		kopsControlPlaneFunction func(kopsControlPlane *controlplanev1alpha1.KopsControlPlane) *controlplanev1alpha1.KopsControlPlane
		clusterFunction          func(cluster *clusterv1.Cluster) *clusterv1.Cluster
		getASGByNameFactory      func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, credentials *aws.Credentials) (*asgTypes.AutoScalingGroup, error)
		createKubeconfigSecret   bool
		updateKubeconfigSecret   bool
	}{
		{
			description:    "should successfully create Kops Cluster",
			expectedResult: resultDefault,
		},
		{
			description: "should successfully create a Kops Cluster that belongs a mesh",
			clusterFunction: func(cluster *clusterv1.Cluster) *clusterv1.Cluster {
				cluster.Annotations = map[string]string{
					"clustermesh.infrastructure.wildlife.io": "true",
				}
				return cluster
			},
			expectedResult: resultDefault,
		},
		{
			description:    "should not requeue a KopsControlPlane that don't match the controllerClass",
			expectedResult: resultNotRequeue,
			kopsControlPlaneFunction: func(kopsControlPlane *controlplanev1alpha1.KopsControlPlane) *controlplanev1alpha1.KopsControlPlane {
				kopsControlPlane.Spec.ControllerClass = "foo"
				return kopsControlPlane
			},
		},
		{
			description:    "should successfully create Kops Cluster with a custom Kops Secret",
			expectedResult: resultDefault,
			kopsControlPlaneFunction: func(kopsControlPlane *controlplanev1alpha1.KopsControlPlane) *controlplanev1alpha1.KopsControlPlane {
				kopsControlPlane.Spec.KopsSecret = &corev1.ObjectReference{
					Namespace:  corev1.NamespaceDefault,
					Kind:       "Secret",
					APIVersion: "v1",
					Name:       "customSecret",
				}
				return kopsControlPlane
			},
		},
		{
			description:    "should not fail to if ASG not ready",
			expectedResult: requeue1min,
			getASGByNameFactory: func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, credentials *aws.Credentials) (*asgTypes.AutoScalingGroup, error) {
				return nil, apierrors.NewNotFound(schema.GroupResource{}, "ASG not ready")
			},
		},
		{
			description:    "should fail to if can't retrieve ASG",
			expectedError:  true,
			expectedResult: resultError,
			getASGByNameFactory: func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, credentials *aws.Credentials) (*asgTypes.AutoScalingGroup, error) {
				return nil, errors.New("error")
			},
		},
		{
			description:    "should fail to create Kops Cluster",
			expectedError:  true,
			expectedResult: resultError,
			kopsControlPlaneFunction: func(kopsControlPlane *controlplanev1alpha1.KopsControlPlane) *controlplanev1alpha1.KopsControlPlane {
				kopsControlPlane.Spec.KopsClusterSpec.KubernetesVersion = ""
				return kopsControlPlane
			},
		},
		{
			description:            "should successfully create secret with Kubeconfig",
			createKubeconfigSecret: true,
			expectedResult:         resultDefault,
		},
		{
			description:            "should successfully update Kubeconfig secret",
			updateKubeconfigSecret: true,
			expectedResult:         resultDefault,
		},
	}
	RegisterFailHandler(Fail)
	vfs.Context.ResetMemfsContext(true)
	g := NewWithT(t)
	ctx := context.TODO()
	err := controlplanev1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = clusterv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = infrastructurev1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			kopsControlPlane := helpers.NewKopsControlPlane("testCluster", metav1.NamespaceDefault)
			kopsControlPlaneSecret := helpers.NewAWSCredentialSecret()

			cluster := helpers.NewCluster("testCluster", helpers.GetFQDN(kopsControlPlane.Name), metav1.NamespaceDefault)
			if tc.clusterFunction != nil {
				clusterFunction := tc.clusterFunction
				cluster = clusterFunction(cluster)
			}
			kopsMachinePool := helpers.NewKopsMachinePool("testIG", kopsControlPlane.Namespace, cluster.Name)

			fakeKopsClientset := helpers.NewFakeKopsClientset()

			kopsCluster := &kopsapi.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: helpers.GetFQDN("testCluster"),
				},
				Spec: kopsControlPlane.Spec.KopsClusterSpec,
			}

			kopsCluster, err := fakeKopsClientset.CreateCluster(ctx, kopsCluster)
			g.Expect(cluster).NotTo(BeNil())
			g.Expect(err).NotTo(HaveOccurred())

			if tc.kopsControlPlaneFunction != nil {
				kopsControlPlaneFunction := tc.kopsControlPlaneFunction
				kopsControlPlane = kopsControlPlaneFunction(kopsControlPlane)
			}
			var getASGByName func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, credentials *aws.Credentials) (*asgTypes.AutoScalingGroup, error)
			if tc.getASGByNameFactory != nil {
				getASGByName = tc.getASGByNameFactory
			} else {
				getASGByName = func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, credentials *aws.Credentials) (*asgTypes.AutoScalingGroup, error) {
					return &asgTypes.AutoScalingGroup{
						Instances: []asgTypes.Instance{
							{
								AvailabilityZone: aws.String("us-east-1"),
								InstanceId:       aws.String("<teste>"),
							},
						},
					}, nil
				}
			}

			fakeClient := fake.NewClientBuilder().WithObjects(kopsControlPlane, cluster, kopsControlPlaneSecret, kopsMachinePool).WithScheme(scheme.Scheme).Build()

			if tc.updateKubeconfigSecret {
				secretKubeconfig := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("%s-%s", cluster.GetName(), "kubeconfig"),
						Namespace: cluster.GetNamespace(),
					},
					Type: clusterv1.ClusterSecretType,
				}

				err = fakeClient.Create(ctx, secretKubeconfig)
				g.Expect(err).NotTo(HaveOccurred())
			}

			keyStore, err := fakeKopsClientset.KeyStore(kopsCluster)
			g.Expect(err).NotTo(HaveOccurred())

			err = helpers.CreateFakeKopsKeyPair(keyStore)
			g.Expect(err).NotTo(HaveOccurred())

			reconciler := &KopsControlPlaneReconciler{
				Client: fakeClient,
				GetKopsClientSetFactory: func(configBase string) (simple.Clientset, error) {
					return fakeKopsClientset, nil
				},
				Mux:      new(sync.Mutex),
				Recorder: record.NewFakeRecorder(10),
				BuildCloudFactory: func(*kopsapi.Cluster) (fi.Cloud, error) {
					return nil, nil
				},
				PopulateClusterSpecFactory: func(kopsCluster *kopsapi.Cluster, kopsClientset simple.Clientset, cloud fi.Cloud) (*kopsapi.Cluster, error) {
					return kopsCluster, nil
				},
				PrepareKopsCloudResourcesFactory: func(ctx context.Context, kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, terraformOutputDir string, cloud fi.Cloud) error {
					return nil
				},
				ApplyTerraformFactory: func(ctx context.Context, terraformDir, tfExecPath string, credentials aws.Credentials) error {
					return nil
				},
				GetClusterStatusFactory: func(kopsCluster *kopsapi.Cluster, cloud fi.Cloud) (*kopsapi.ClusterStatus, error) {
					return nil, nil
				},
				ValidateKopsClusterFactory: func(kubeConfig *rest.Config, kopsCluster *kopsapi.Cluster, cloud fi.Cloud, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error) {
					return &validation.ValidationCluster{}, nil
				},
				GetASGByNameFactory: getASGByName,
			}
			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: client.ObjectKey{
					Namespace: metav1.NamespaceDefault,
					Name:      helpers.GetFQDN("testCluster"),
				},
			})

			g.Expect(result).To(Equal(tc.expectedResult))
			if tc.expectedError {
				g.Expect(err).To(HaveOccurred())
			} else if result != resultNotRequeue {
				g.Expect(err).ToNot(HaveOccurred())
				kopsCluster, err := fakeKopsClientset.GetCluster(ctx, cluster.GetObjectMeta().GetName())
				g.Expect(kopsCluster).ToNot(BeNil())
				g.Expect(err).ToNot(HaveOccurred())
			}

			if tc.createKubeconfigSecret {
				secretKubeconfig := corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("%s-%s", cluster.GetName(), "kubeconfig"),
						Namespace: cluster.GetNamespace(),
					},
				}
				clusterRef := types.NamespacedName{
					Namespace: cluster.GetNamespace(),
					Name:      cluster.GetName(),
				}
				fakeSecret, err := secret.GetFromNamespacedName(ctx, fakeClient, clusterRef, secret.Kubeconfig)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(fakeSecret.Name).To(Equal(secretKubeconfig.Name))
				g.Expect(fakeSecret.Data).NotTo(BeNil())
			}
			if tc.updateKubeconfigSecret {
				clusterRef := types.NamespacedName{
					Namespace: cluster.GetNamespace(),
					Name:      cluster.GetName(),
				}
				fakeSecret, err := secret.GetFromNamespacedName(ctx, fakeClient, clusterRef, secret.Kubeconfig)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(fakeSecret.Data).NotTo(BeNil())
			}
		})
	}
}

func TestKopsControlPlaneStatus(t *testing.T) {

	testCases := []struct {
		description                            string
		expectedReconcilerError                bool
		clusterFunction                        func(cluster *clusterv1.Cluster) *clusterv1.Cluster
		expectedStatus                         *controlplanev1alpha1.KopsControlPlaneStatus
		conditionsToAssert                     []*clusterv1.Condition
		eventsToAssert                         []string
		expectedErrorGetClusterStatusFactory   func(kopsCluster *kopsapi.Cluster, cloud fi.Cloud) (*kopsapi.ClusterStatus, error)
		expectedErrorPrepareKopsCloudResources func(ctx context.Context, kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, terraformOutputDir string, cloud fi.Cloud) error
		expectedErrorApplyTerraform            func(ctx context.Context, terraformDir, tfExecPath string, credentials aws.Credentials) error
		expectedValidateKopsCluster            func(kubeConfig *rest.Config, kopsCluster *kopsapi.Cluster, cloud fi.Cloud, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error)
	}{
		{
			description: "should successfully patch KopsControlPlane",
		},
		{
			description: "should mark the cluster as paused",
			clusterFunction: func(cluster *clusterv1.Cluster) *clusterv1.Cluster {
				cluster.Annotations = map[string]string{
					"cluster.x-k8s.io/paused": "true",
				}
				return cluster
			},
			expectedStatus: &controlplanev1alpha1.KopsControlPlaneStatus{
				Paused: true,
			},
		},
		{
			description:             "should mark false for condition KopsControlPlaneStateReadyCondition",
			expectedReconcilerError: true,
			expectedErrorGetClusterStatusFactory: func(kopsCluster *kopsapi.Cluster, cloud fi.Cloud) (*kopsapi.ClusterStatus, error) {
				return nil, errors.New("")
			},
			conditionsToAssert: []*clusterv1.Condition{
				conditions.FalseCondition(controlplanev1alpha1.KopsControlPlaneStateReadyCondition, controlplanev1alpha1.KopsControlPlaneStateReconciliationFailedReason, clusterv1.ConditionSeverityError, ""),
			},
		},
		{
			description:             "should mark false for condition KopsTerraformGenerationReadyCondition",
			expectedReconcilerError: true,
			expectedErrorPrepareKopsCloudResources: func(ctx context.Context, kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, terraformOutputDir string, cloud fi.Cloud) error {
				return errors.New("")
			},
			conditionsToAssert: []*clusterv1.Condition{
				conditions.FalseCondition(controlplanev1alpha1.KopsTerraformGenerationReadyCondition, controlplanev1alpha1.KopsTerraformGenerationReconciliationFailedReason, clusterv1.ConditionSeverityError, ""),
			},
		},
		{
			description:             "should mark false for condition TerraformApplyReadyCondition",
			expectedReconcilerError: true,
			expectedErrorApplyTerraform: func(ctx context.Context, terraformDir, tfExecPath string, credentials aws.Credentials) error {
				return errors.New("")
			},
			conditionsToAssert: []*clusterv1.Condition{
				conditions.FalseCondition(controlplanev1alpha1.TerraformApplyReadyCondition, controlplanev1alpha1.TerraformApplyReconciliationFailedReason, clusterv1.ConditionSeverityError, ""),
			},
		},
		{
			description:             "should have an event with the error from ValidateKopsCluster",
			expectedReconcilerError: true,
			eventsToAssert: []string{
				"ReconciliationStarted",
				"KopsMachinePoolReconcileSuccess",
				"dummy error message",
			},
			expectedValidateKopsCluster: func(kubeConfig *rest.Config, kopsCluster *kopsapi.Cluster, cloud fi.Cloud, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error) {
				return nil, errors.New("dummy error message")
			},
		},
		{
			description: "should have an event when the validation succeeds",
			eventsToAssert: []string{
				"ReconciliationStarted",
				"KopsMachinePoolReconcileSuccess",
				"KubernetesClusterValidationSucceed",
				"ClusterReconciledSuccessfully",
			},
		},
		{
			description: "should have an event with the failed validation",
			eventsToAssert: []string{
				"ReconciliationStarted",
				"KopsMachinePoolReconcileSuccess",
				"failed to validate this test case",
			},
			expectedValidateKopsCluster: func(kubeConfig *rest.Config, kopsCluster *kopsapi.Cluster, cloud fi.Cloud, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error) {
				return &validation.ValidationCluster{
					Failures: []*validation.ValidationError{
						{
							Message: "failed to validate this test case",
						},
					},
				}, nil
			},
		},
		{
			description:             "should have an event with the failed validations",
			expectedReconcilerError: false,
			eventsToAssert: []string{
				"ReconciliationStarted",
				"Normal KopsMachinePoolReconcileSuccess testIG",
				"test case A",
				"test case B",
				"node hostA condition is False",
			},
			expectedValidateKopsCluster: func(kubeConfig *rest.Config, kopsCluster *kopsapi.Cluster, cloud fi.Cloud, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error) {
				return &validation.ValidationCluster{
					Failures: []*validation.ValidationError{
						{
							Message: "failed to validate this test case A",
						},
						{
							Message: "failed to validate this test case B",
						},
					},
					Nodes: []*validation.ValidationNode{
						{
							Hostname: "hostA",
							Status:   corev1.ConditionFalse,
						},
					},
				}, nil
			},
		},
	}
	RegisterFailHandler(Fail)
	vfs.Context.ResetMemfsContext(true)
	g := NewWithT(t)
	ctx := context.TODO()
	err := controlplanev1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = clusterv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = infrastructurev1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			kopsControlPlane := helpers.NewKopsControlPlane("testCluster", metav1.NamespaceDefault)
			kopsControlPlaneSecret := helpers.NewAWSCredentialSecret()
			cluster := helpers.NewCluster("testCluster", helpers.GetFQDN(kopsControlPlane.Name), metav1.NamespaceDefault)
			if tc.clusterFunction != nil {
				clusterFunction := tc.clusterFunction
				cluster = clusterFunction(cluster)
			}
			kopsMachinePool := helpers.NewKopsMachinePool("testIG", kopsControlPlane.Namespace, cluster.Name)

			fakeClient := fake.NewClientBuilder().WithStatusSubresource(kopsControlPlane).WithObjects(kopsControlPlane, kopsControlPlaneSecret, cluster, kopsMachinePool).WithScheme(scheme.Scheme).Build()

			fakeKopsClientset := helpers.NewFakeKopsClientset()

			kopsCluster := &kopsapi.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: helpers.GetFQDN("testCluster"),
				},
				Spec: kopsControlPlane.Spec.KopsClusterSpec,
			}

			kopsCluster, err := fakeKopsClientset.CreateCluster(ctx, kopsCluster)
			g.Expect(cluster).NotTo(BeNil())
			g.Expect(err).NotTo(HaveOccurred())

			keyStore, err := fakeKopsClientset.KeyStore(kopsCluster)
			g.Expect(err).NotTo(HaveOccurred())

			err = helpers.CreateFakeKopsKeyPair(keyStore)
			g.Expect(err).NotTo(HaveOccurred())

			recorderSize := 10
			recorder := record.NewFakeRecorder(recorderSize)

			var getClusterStatus func(kopsCluster *kopsapi.Cluster, cloud fi.Cloud) (*kopsapi.ClusterStatus, error)
			if tc.expectedErrorGetClusterStatusFactory != nil {
				getClusterStatus = tc.expectedErrorGetClusterStatusFactory
			} else {
				getClusterStatus = func(kopsCluster *kopsapi.Cluster, cloud fi.Cloud) (*kopsapi.ClusterStatus, error) {
					return nil, nil
				}
			}

			var prepareKopsCloudResources func(ctx context.Context, kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, terraformOutputDir string, cloud fi.Cloud) error
			if tc.expectedErrorPrepareKopsCloudResources != nil {
				prepareKopsCloudResources = tc.expectedErrorPrepareKopsCloudResources
			} else {
				prepareKopsCloudResources = func(ctx context.Context, kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, terraformOutputDir string, cloud fi.Cloud) error {
					return nil
				}
			}

			var applyTerraform func(ctx context.Context, terraformDir, tfExecPath string, credentials aws.Credentials) error
			if tc.expectedErrorApplyTerraform != nil {
				applyTerraform = tc.expectedErrorApplyTerraform
			} else {
				applyTerraform = func(ctx context.Context, terraformDir, tfExecPath string, credentials aws.Credentials) error {
					return nil
				}
			}

			var validateKopsCluster func(kubeConfig *rest.Config, kopsCluster *kopsapi.Cluster, cloud fi.Cloud, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error)
			if tc.expectedValidateKopsCluster != nil {
				validateKopsCluster = tc.expectedValidateKopsCluster
			} else {
				validateKopsCluster = func(kubeConfig *rest.Config, kopsCluster *kopsapi.Cluster, cloud fi.Cloud, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error) {
					return &validation.ValidationCluster{}, nil
				}
			}

			reconciler := &KopsControlPlaneReconciler{
				Client:   fakeClient,
				Recorder: recorder,
				Mux:      new(sync.Mutex),
				GetKopsClientSetFactory: func(configBase string) (simple.Clientset, error) {
					return fakeKopsClientset, nil
				},
				BuildCloudFactory: func(*kopsapi.Cluster) (fi.Cloud, error) {
					return nil, nil
				},
				PopulateClusterSpecFactory: func(kopsCluster *kopsapi.Cluster, kopsClientset simple.Clientset, cloud fi.Cloud) (*kopsapi.Cluster, error) {
					return kopsCluster, nil
				},
				PrepareKopsCloudResourcesFactory: prepareKopsCloudResources,
				ApplyTerraformFactory:            applyTerraform,
				GetClusterStatusFactory:          getClusterStatus,
				ValidateKopsClusterFactory:       validateKopsCluster,
				GetASGByNameFactory: func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, credentials *aws.Credentials) (*asgTypes.AutoScalingGroup, error) {
					return &asgTypes.AutoScalingGroup{
						Instances: []asgTypes.Instance{
							{
								AvailabilityZone: aws.String("us-east-1"),
								InstanceId:       aws.String("<teste>"),
							},
						},
					}, nil
				},
			}

			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: client.ObjectKey{
					Namespace: metav1.NamespaceDefault,
					Name:      helpers.GetFQDN("testCluster"),
				},
			})
			if !tc.expectedReconcilerError {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result.Requeue).To(BeFalse())
				g.Expect(result.RequeueAfter).To(Equal(time.Duration(20 * time.Minute)))
			} else {
				g.Expect(err).To(HaveOccurred())
				g.Expect(result.RequeueAfter).To(Equal(time.Duration(5 * time.Minute)))
			}

			if tc.expectedStatus != nil {
				kcp := &controlplanev1alpha1.KopsControlPlane{}
				err = fakeClient.Get(ctx, client.ObjectKeyFromObject(kopsControlPlane), kcp)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(*tc.expectedStatus).To(BeEquivalentTo(kcp.Status))

			}

			if tc.conditionsToAssert != nil {
				kcp := &controlplanev1alpha1.KopsControlPlane{}
				err = fakeClient.Get(ctx, client.ObjectKeyFromObject(kopsControlPlane), kcp)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(kcp.Status.Conditions).ToNot(BeNil())
				conditionsToAssert := tc.conditionsToAssert
				assertConditions(g, kcp, conditionsToAssert...)
			}

			if tc.eventsToAssert != nil {
				events := []string{}
				loopEnded := false
				for {
					select {
					case event := <-recorder.Events:
						events = append(events, event)
					default:
						loopEnded = true
					}
					if loopEnded {
						break
					}
				}
				for _, eventMessage := range tc.eventsToAssert {
					foundEvent := false
					for _, event := range events {
						if strings.Contains(event, eventMessage) {
							foundEvent = true
							break
						}
					}
					g.Expect(foundEvent).To(BeTrue())
				}
			}
		})
	}
}

func TestKopsMachinePoolToInfrastructureMapFunc(t *testing.T) {
	testCases := []struct {
		description     string
		input           client.Object
		objects         []client.Object
		controllerClass string
		expectedOutput  []ctrl.Request
	}{
		{
			description: "should return objectKey for KopsControlPlane",
			input: &infrastructurev1alpha1.KopsMachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testKopsMachinePool",
					Namespace: metav1.NamespaceDefault,
				},
				Spec: infrastructurev1alpha1.KopsMachinePoolSpec{
					ClusterName: "testCluster",
				},
			},
			objects: []client.Object{
				&clusterv1.Cluster{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: metav1.NamespaceDefault,
						Name:      "testCluster",
					},
					Spec: clusterv1.ClusterSpec{
						InfrastructureRef: &corev1.ObjectReference{
							Name:       "testKopsControlPlane",
							Namespace:  metav1.NamespaceDefault,
							Kind:       "KopsControlPlane",
							APIVersion: "controlplane.cluster.x-k8s.io/v1alpha1",
						},
					},
				},
				&controlplanev1alpha1.KopsControlPlane{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "testKopsControlPlane",
						Namespace: metav1.NamespaceDefault,
					},
				},
			},
			expectedOutput: []ctrl.Request{
				{
					NamespacedName: client.ObjectKey{
						Namespace: metav1.NamespaceDefault,
						Name:      "testKopsControlPlane",
					},
				},
			},
		},
		{
			description: "should return objectKey for KopsControlPlane configured with the same controlleClass as the controller",
			input: &infrastructurev1alpha1.KopsMachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testKopsMachinePool",
					Namespace: metav1.NamespaceDefault,
				},
				Spec: infrastructurev1alpha1.KopsMachinePoolSpec{
					ClusterName: "testCluster",
				},
			},
			controllerClass: "bar",
			objects: []client.Object{
				&clusterv1.Cluster{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: metav1.NamespaceDefault,
						Name:      "testCluster",
					},
					Spec: clusterv1.ClusterSpec{
						InfrastructureRef: &corev1.ObjectReference{
							Name:       "testKopsControlPlane",
							Namespace:  metav1.NamespaceDefault,
							Kind:       "KopsControlPlane",
							APIVersion: "controlplane.cluster.x-k8s.io/v1alpha1",
						},
					},
				},
				&controlplanev1alpha1.KopsControlPlane{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "testKopsControlPlane",
						Namespace: metav1.NamespaceDefault,
					},
					Spec: controlplanev1alpha1.KopsControlPlaneSpec{
						ControllerClass: "bar",
					},
				},
			},
			expectedOutput: []ctrl.Request{
				{
					NamespacedName: client.ObjectKey{
						Namespace: metav1.NamespaceDefault,
						Name:      "testKopsControlPlane",
					},
				},
			},
		},
		{
			description: "should return empty list with an object different from kopsMachinePool",
			input: &clusterv1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testMachine",
					Namespace: metav1.NamespaceDefault,
				},
			},
			expectedOutput: []ctrl.Request{},
		},
		{
			description: "should return empty list when Cluster isn't found",
			input: &infrastructurev1alpha1.KopsMachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testKopsMachinePool",
					Namespace: metav1.NamespaceDefault,
				},
				Spec: infrastructurev1alpha1.KopsMachinePoolSpec{
					ClusterName: "testCluster",
				},
			},
			expectedOutput: []ctrl.Request{},
		},
		{
			description: "should return a empty list of requests when Cluster don't have InfrastructureRef",
			input: &infrastructurev1alpha1.KopsMachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testKopsMachinePool",
					Namespace: metav1.NamespaceDefault,
				},
				Spec: infrastructurev1alpha1.KopsMachinePoolSpec{
					ClusterName: "testCluster",
				},
			},
			objects: []client.Object{
				&clusterv1.Cluster{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: metav1.NamespaceDefault,
						Name:      "testCluster",
					},
					Spec: clusterv1.ClusterSpec{},
				},
			},
			expectedOutput: []ctrl.Request{},
		},
		{
			description: "should return a empty list of requests when Cluster's InfrastructureRef isn't a KopsControlPlane",
			input: &infrastructurev1alpha1.KopsMachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testKopsMachinePool",
					Namespace: metav1.NamespaceDefault,
				},
				Spec: infrastructurev1alpha1.KopsMachinePoolSpec{
					ClusterName: "testCluster",
				},
			},
			objects: []client.Object{
				&clusterv1.Cluster{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: metav1.NamespaceDefault,
						Name:      "testCluster",
					},
					Spec: clusterv1.ClusterSpec{
						InfrastructureRef: &corev1.ObjectReference{
							Name:       "testKubeAdmControlPlane",
							Namespace:  metav1.NamespaceDefault,
							Kind:       "KubeAdmControlPlane",
							APIVersion: "controlplane.cluster.x-k8s.io/v1alpha1",
						},
					},
				},
			},
			expectedOutput: []ctrl.Request{},
		},
		{
			description: "should return empty list when KopsControlPlane isn't found",
			input: &infrastructurev1alpha1.KopsMachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testKopsMachinePool",
					Namespace: metav1.NamespaceDefault,
				},
				Spec: infrastructurev1alpha1.KopsMachinePoolSpec{
					ClusterName: "testCluster",
				},
			},
			objects: []client.Object{
				&clusterv1.Cluster{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: metav1.NamespaceDefault,
						Name:      "testCluster",
					},
					Spec: clusterv1.ClusterSpec{
						InfrastructureRef: &corev1.ObjectReference{
							Name:       "testKopsControlPlane",
							Namespace:  metav1.NamespaceDefault,
							Kind:       "KopsControlPlane",
							APIVersion: "controlplane.cluster.x-k8s.io/v1alpha1",
						},
					},
				},
			},
			expectedOutput: []ctrl.Request{},
		},
		{
			description: "should return empty list when KopsControlPlane is configured with a different controllerClass",
			input: &infrastructurev1alpha1.KopsMachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testKopsMachinePool",
					Namespace: metav1.NamespaceDefault,
				},
				Spec: infrastructurev1alpha1.KopsMachinePoolSpec{
					ClusterName: "testCluster",
				},
			},
			objects: []client.Object{
				&clusterv1.Cluster{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: metav1.NamespaceDefault,
						Name:      "testCluster",
					},
					Spec: clusterv1.ClusterSpec{
						InfrastructureRef: &corev1.ObjectReference{
							Name:       "testKopsControlPlane",
							Namespace:  metav1.NamespaceDefault,
							Kind:       "KopsControlPlane",
							APIVersion: "controlplane.cluster.x-k8s.io/v1alpha1",
						},
					},
				},
				&controlplanev1alpha1.KopsControlPlane{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "testKopsControlPlane",
						Namespace: metav1.NamespaceDefault,
					},
					Spec: controlplanev1alpha1.KopsControlPlaneSpec{
						ControllerClass: "differentClass",
					},
				},
			},
			expectedOutput: []ctrl.Request{},
		},
	}

	RegisterFailHandler(Fail)
	g := NewWithT(t)

	err := clusterv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = controlplanev1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {

			fakeClient := fake.NewClientBuilder().WithObjects(tc.objects...).WithScheme(scheme.Scheme).Build()

			reconciler := &KopsControlPlaneReconciler{
				Client: fakeClient,
			}
			kopsMachinePoolToKopsControlPlaneMapper := reconciler.kopsMachinePoolToInfrastructureMapFunc(tc.controllerClass)
			req := kopsMachinePoolToKopsControlPlaneMapper(context.TODO(), tc.input)
			g.Expect(tc.expectedOutput).To(Equal(req))
		})
	}
}

func TestCreateOrUpdateInstanceGroup(t *testing.T) {

	testCases := []map[string]interface{}{
		{
			"description":    "Should successfully create a IG",
			"expectedError":  false,
			"kopsIGFunction": nil,
			"updateIG":       false,
		},
		{
			"description":    "Should successfully update a IG",
			"expectedError":  false,
			"kopsIGFunction": nil,
			"updateIG":       true,
		},
		{
			"description":   "Should fail without required fields",
			"expectedError": true,
			"kopsIGFunction": func(kopsIG *kopsapi.InstanceGroup) *kopsapi.InstanceGroup {
				return &kopsapi.InstanceGroup{}
			},
			"updateIG": false,
		},
	}
	RegisterFailHandler(Fail)
	g := NewWithT(t)
	ctx := context.TODO()
	for _, tc := range testCases {
		t.Run(tc["description"].(string), func(t *testing.T) {
			fakeKopsClientset := helpers.NewFakeKopsClientset()
			kopsCluster := helpers.NewKopsCluster("test-kopscluster")
			_, err := fakeKopsClientset.CreateCluster(ctx, kopsCluster)
			g.Expect(err).NotTo(HaveOccurred())
			ig := helpers.NewKopsIG("test-kopsig", kopsCluster.GetObjectMeta().GetName())
			if tc["kopsIGFunction"] != nil {
				kopsIGFunction := tc["kopsIGFunction"].(func(kopsIG *kopsapi.InstanceGroup) *kopsapi.InstanceGroup)
				ig = kopsIGFunction(ig)
			}
			if tc["updateIG"].(bool) == true {
				ig, err := fakeKopsClientset.InstanceGroupsFor(kopsCluster).Create(ctx, ig, metav1.CreateOptions{})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(ig).NotTo(BeNil())
				ig.Spec.MachineType = "m5.test"
			}

			reconciler := &KopsControlPlaneReconciler{}
			err = reconciler.createOrUpdateInstanceGroup(ctx, ctrl.LoggerFrom(ctx), fakeKopsClientset, kopsCluster, ig)
			if tc["expectedError"].(bool) {
				g.Expect(err).To(HaveOccurred())
				return
			}
			g.Expect(err).ToNot(HaveOccurred())

			ig, err = fakeKopsClientset.InstanceGroupsFor(kopsCluster).Get(ctx, "test-kopsig", metav1.GetOptions{})
			g.Expect(ig).NotTo(BeNil())
			g.Expect(err).ToNot(HaveOccurred())
			if tc["updateIG"].(bool) == true {
				g.Expect(ig.Spec.MachineType).To(Equal("m5.test"))
			}
		})
	}
}

func TestGetRegionBySubnet(t *testing.T) {
	var testCases = []struct {
		description   string
		expectedError bool
		expectedRes   string
		input         []kopsapi.ClusterSubnetSpec
	}{
		{"Should return error about no subnets found", true, "", []kopsapi.ClusterSubnetSpec{}},
		{"Should return the region", false, "ap-northeast-1",
			[]kopsapi.ClusterSubnetSpec{
				{
					Name: "ap-northeast-1d",
					Type: "Private",
					Zone: "ap-northeast-1d",
					CIDR: "172.27.48.0/24",
				},
				{
					Name: "ap-northeast-1b",
					Type: "Private",
					Zone: "ap-northeast-1b",
					CIDR: "172.27.49.0/24",
				},
			}},
		{"Should return the region", false, "us-west-1",
			[]kopsapi.ClusterSubnetSpec{
				{
					Name: "us-west-1a",
					Type: "Private",
					Zone: "us-west-1a",
					CIDR: "172.27.53.0/24",
				},
			},
		},
	}

	RegisterFailHandler(Fail)
	g := NewWithT(t)

	for _, tc := range testCases {
		kcp := &controlplanev1alpha1.KopsControlPlane{}
		kcp.Spec.KopsClusterSpec.Subnets = tc.input
		region, err := RegionBySubnet(kcp)

		t.Run(tc.description, func(t *testing.T) {
			if tc.expectedError {
				g.Expect(err).To(HaveOccurred())
				g.Expect(tc.expectedRes).To(Equal(""))
			} else {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(tc.expectedRes).To(Equal(region))
			}
		})
	}
}

func TestPrepareCustomCloudResources(t *testing.T) {
	var testCases = []struct {
		description     string
		spotInstEnabled bool
		expectedError   bool
	}{
		{
			description:     "Should generate files based on template",
			spotInstEnabled: false,
			expectedError:   false,
		},
		{
			description:     "Should generate files based on with spotinst enabled",
			spotInstEnabled: true,
			expectedError:   false,
		},
	}

	RegisterFailHandler(Fail)
	g := NewWithT(t)

	ctx := context.TODO()
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			fakeKopsClientset := helpers.NewFakeKopsClientset()
			vfs.Context.ResetMemfsContext(true)
			bareKopsCluster := helpers.NewKopsCluster("test-cluster")
			kopsCluster, err := fakeKopsClientset.CreateCluster(ctx, bareKopsCluster)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(kopsCluster).NotTo(BeNil())
			kcp := &controlplanev1alpha1.KopsControlPlane{}
			kcp.Spec.SpotInst.Enabled = tc.spotInstEnabled

			kmp := helpers.NewKopsMachinePool("test-ig", metav1.NamespaceDefault, "test-cluster")
			kmp.Spec.KopsInstanceGroupSpec.NodeLabels = map[string]string{
				"kops.k8s.io/instance-group-role": "Node",
			}
			kmp.Spec.KarpenterProvisioners = []v1alpha5.Provisioner{
				{
					TypeMeta: metav1.TypeMeta{
						Kind:       "Provisioner",
						APIVersion: "karpenter.sh/v1alpha5",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-provisioner",
					},
					Spec: v1alpha5.ProvisionerSpec{
						Consolidation: &v1alpha5.Consolidation{
							Enabled: aws.Bool(true),
						},
						KubeletConfiguration: &v1alpha5.KubeletConfiguration{
							KubeReserved: corev1.ResourceList{
								corev1.ResourceCPU:              resource.MustParse("150m"),
								corev1.ResourceMemory:           resource.MustParse("150Mi"),
								corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
							},
							SystemReserved: corev1.ResourceList{
								corev1.ResourceCPU:              resource.MustParse("150m"),
								corev1.ResourceMemory:           resource.MustParse("200Mi"),
								corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
							},
						},
						Labels: map[string]string{
							"kops.k8s.io/cluster":             kopsCluster.Name,
							"kops.k8s.io/cluster-name":        kopsCluster.Name,
							"kops.k8s.io/instance-group-name": kmp.Name,
							"kops.k8s.io/instance-group-role": "Node",
							"kops.k8s.io/instancegroup":       kmp.Name,
							"kops.k8s.io/managed-by":          "kops-controller",
						},
						Provider: &v1alpha5.Provider{
							Raw: []byte("{\"launchTemplate\":\"" + kmp.Name + "." + kopsCluster.Name + "\",\"subnetSelector\":{\"kops.k8s.io/instance-group/" + kmp.Name + "\":\"*\",\"kubernetes.io/cluster/" + kopsCluster.Name + "\":\"*\"}}"),
						},
						Requirements: []corev1.NodeSelectorRequirement{
							{
								Key:      "kubernetes.io/arch",
								Operator: corev1.NodeSelectorOperator(corev1.NodeSelectorOpIn),
								Values:   []string{"amd64"},
							},
							{
								Key:      "kubernetes.io/os",
								Operator: corev1.NodeSelectorOperator(corev1.NodeSelectorOpIn),
								Values:   []string{"linux"},
							},
							{
								Key:      "node.kubernetes.io/instance-type",
								Operator: corev1.NodeSelectorOperator(corev1.NodeSelectorOpIn),
								Values:   []string{"m5.large"},
							},
						},
						StartupTaints: []corev1.Taint{
							{
								Key:    "node.cloudprovider.kubernetes.io/uninitialized",
								Effect: corev1.TaintEffect(corev1.TaintEffectNoSchedule),
							},
						},
					},
				},
			}

			if tc.spotInstEnabled {
				kcp.Spec.SpotInst.Enabled = true
				kmp.Spec.SpotInstOptions = map[string]string{
					"spotinst.io/hybrid": "true",
				}
			}

			reconciler := &KopsControlPlaneReconciler{}
			terraformOutputDir := fmt.Sprintf("/tmp/%s", kopsCluster.Name)
			err = reconciler.PrepareCustomCloudResources(ctx, kopsCluster, kcp, []infrastructurev1alpha1.KopsMachinePool{*kmp}, true, kopsCluster.Spec.ConfigBase, terraformOutputDir, true)
			g.Expect(err).NotTo(HaveOccurred())

			templateTestDir := "../../utils/templates/tests"
			generatedBackendTF, err := os.ReadFile(terraformOutputDir + "/backend.tf")
			g.Expect(err).NotTo(HaveOccurred())
			templatedBackendTF, err := os.ReadFile(templateTestDir + "/backend.tf")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(string(generatedBackendTF)).To(BeEquivalentTo(string(templatedBackendTF)))

			generatedKarpenterBoostrapTF, err := os.ReadFile(terraformOutputDir + "/karpenter_custom_addon_boostrap.tf")
			g.Expect(err).NotTo(HaveOccurred())
			templatedKarpenterBoostrapTF, err := os.ReadFile(templateTestDir + "/karpenter_custom_addon_boostrap.tf")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(string(generatedKarpenterBoostrapTF)).To(BeEquivalentTo(string(templatedKarpenterBoostrapTF)))

			generatedLaunchTemplateTF, err := os.ReadFile(terraformOutputDir + "/launch_template_override.tf")
			g.Expect(err).NotTo(HaveOccurred())
			templatedLaunchTemplateTF, err := os.ReadFile(templateTestDir + "/launch_template_override.tf")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(string(generatedLaunchTemplateTF)).To(BeEquivalentTo(string(templatedLaunchTemplateTF)))

			generatedProvisionerContentTF, err := os.ReadFile(terraformOutputDir + "/data/aws_s3_object_karpenter_provisioners_content")
			g.Expect(err).NotTo(HaveOccurred())
			templatedProvisionerContentTF, err := os.ReadFile(templateTestDir + "/data/aws_s3_object_karpenter_provisioners_content")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(string(generatedProvisionerContentTF)).To(BeEquivalentTo(string(templatedProvisionerContentTF)))

			if tc.spotInstEnabled {
				generatedSpotinstLaunchSpecTF, err := os.ReadFile(terraformOutputDir + "/spotinst_launch_spec_override.tf")
				g.Expect(err).NotTo(HaveOccurred())
				templatedSpotinstLaunchSpecTF, err := os.ReadFile(templateTestDir + "/spotinst_launch_spec_override.tf")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(generatedSpotinstLaunchSpecTF)).To(BeEquivalentTo(string(templatedSpotinstLaunchSpecTF)))

				generatedSpotinstOceanAWSTF, err := os.ReadFile(terraformOutputDir + "/spotinst_ocean_aws_override.tf")
				g.Expect(err).NotTo(HaveOccurred())
				templatedSpotinstOceanAWSTF, err := os.ReadFile(templateTestDir + "/spotinst_ocean_aws_override.tf")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(generatedSpotinstOceanAWSTF)).To(BeEquivalentTo(string(templatedSpotinstOceanAWSTF)))
			}
		})
	}
}

func TestControllerClassPredicate(t *testing.T) {
	var testCases = []struct {
		description     string
		input           client.Object
		controllerClass string
		expectedOutput  bool
	}{
		{
			description:    "should return true when controller and object don't have any class defined",
			input:          &controlplanev1alpha1.KopsControlPlane{},
			expectedOutput: true,
		},
		{
			description:     "should return true when controller and object have the same class defined",
			controllerClass: "foo",
			input: &controlplanev1alpha1.KopsControlPlane{
				Spec: controlplanev1alpha1.KopsControlPlaneSpec{
					ControllerClass: "foo",
				},
			},
			expectedOutput: true,
		},
		{
			description:     "should return false when controller and object have different classes defined",
			controllerClass: "foo",
			input: &controlplanev1alpha1.KopsControlPlane{
				Spec: controlplanev1alpha1.KopsControlPlaneSpec{
					ControllerClass: "bar",
				},
			},
		},
		{
			description:     "should return false when only controller has class defined",
			controllerClass: "foo",
			input:           &controlplanev1alpha1.KopsControlPlane{},
		},
		{
			description: "should return false when only object has class defined",
			input: &controlplanev1alpha1.KopsControlPlane{
				Spec: controlplanev1alpha1.KopsControlPlaneSpec{
					ControllerClass: "foo",
				},
			},
		},
		{
			description: "should return false when the object isn't a KopsControlPlane",
			input:       &infrastructurev1alpha1.KopsMachinePool{},
		},
	}

	RegisterFailHandler(Fail)
	g := NewWithT(t)

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			predicateFunc := controllerClassPredicate(tc.controllerClass)

			g.Expect(predicateFunc.Create(event.CreateEvent{
				Object: tc.input,
			})).To(BeEquivalentTo(tc.expectedOutput))
			g.Expect(predicateFunc.Update(event.UpdateEvent{
				ObjectNew: tc.input,
			})).To(BeEquivalentTo(tc.expectedOutput))
			g.Expect(predicateFunc.Delete(event.DeleteEvent{
				Object: tc.input,
			})).To(BeEquivalentTo(tc.expectedOutput))
			g.Expect(predicateFunc.Generic(event.GenericEvent{
				Object: tc.input,
			})).To(BeEquivalentTo(tc.expectedOutput))

		})
	}
}

func assertConditions(g *WithT, from conditions.Getter, conditions ...*clusterv1.Condition) {
	for _, condition := range conditions {
		assertCondition(g, from, condition)
	}
}

func assertCondition(g *WithT, from conditions.Getter, condition *clusterv1.Condition) {
	g.Expect(conditions.Has(from, condition.Type)).To(BeTrue())

	if condition.Status == corev1.ConditionTrue {
		conditions.IsTrue(from, condition.Type)
	} else {
		conditionToBeAsserted := conditions.Get(from, condition.Type)
		g.Expect(conditionToBeAsserted.Status).To(Equal(condition.Status))
		g.Expect(conditionToBeAsserted.Severity).To(Equal(condition.Severity))
		g.Expect(conditionToBeAsserted.Reason).To(Equal(condition.Reason))
		if condition.Message != "" {
			g.Expect(conditionToBeAsserted.Message).To(Equal(condition.Message))
		}
	}
}
