package controlplane

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	asgTypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	infrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	controlplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	"github.com/topfreegames/kubernetes-kops-operator/utils"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	kopsapi "k8s.io/kops/pkg/apis/kops"
	"k8s.io/kops/pkg/client/simple"
	"k8s.io/kops/pkg/client/simple/vfsclientset"
	"k8s.io/kops/pkg/pki"
	"k8s.io/kops/pkg/validation"
	"k8s.io/kops/upup/pkg/fi"
	"k8s.io/kops/util/pkg/vfs"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/secret"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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
			fakeClient := newMockedK8sClient(cluster)
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
			fakeClient := newMockedK8sClient(cluster)
			reconciler := &KopsControlPlaneReconciler{
				log:    ctrl.LoggerFrom(ctx),
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
			fakeKopsClientset := newFakeKopsClientset()
			vfs.Context.ResetMemfsContext(true)
			bareKopsCluster := newKopsCluster("test-cluster")
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
			bareKopsCluster := newKopsCluster("test-cluster")
			if tc.kopsClusterFunction != nil {
				kopsClusterFunction := tc.kopsClusterFunction
				bareKopsCluster = kopsClusterFunction(bareKopsCluster)
			}
			fakeKopsClientset := newFakeKopsClientset()

			if tc.updateCluster == true {
				bareKopsCluster, err := fakeKopsClientset.CreateCluster(ctx, bareKopsCluster)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(bareKopsCluster).NotTo(BeNil())
				bareKopsCluster.Spec.KubernetesVersion = "1.21.0"
			}

			reconciler := &KopsControlPlaneReconciler{
				log: ctrl.LoggerFrom(ctx),
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
		expectedError            bool
		expectedRequeue          bool
		kopsControlPlaneFunction func(kopsControlPlane *controlplanev1alpha1.KopsControlPlane) *controlplanev1alpha1.KopsControlPlane
		clusterFunction          func(cluster *clusterv1.Cluster) *clusterv1.Cluster
		getASGByNameFactory      func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, credentials *aws.CredentialsCache) (*asgTypes.AutoScalingGroup, error)
		createKubeconfigSecret   bool
		updateKubeconfigSecret   bool
	}{
		{
			description: "should successfully create Kops Cluster",
		},
		{
			description: "should successfully create a Kops Cluster that belongs a mesh",
			clusterFunction: func(cluster *clusterv1.Cluster) *clusterv1.Cluster {
				cluster.Annotations = map[string]string{
					"clustermesh.infrastructure.wildlife.io": "true",
				}
				return cluster
			},
		},
		{
			description: "should successfully create Kops Cluster with a custom Kops Secret",
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
			description:     "should not fail to if ASG not ready",
			expectedRequeue: true,
			getASGByNameFactory: func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, credentials *aws.CredentialsCache) (*asgTypes.AutoScalingGroup, error) {
				return nil, apierrors.NewNotFound(schema.GroupResource{}, "ASG not ready")
			},
		},
		{
			description:   "should fail to if can't retrieve ASG",
			expectedError: true,
			getASGByNameFactory: func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, credentials *aws.CredentialsCache) (*asgTypes.AutoScalingGroup, error) {
				return nil, errors.New("error")
			},
		},
		{
			description:   "should fail to create Kops Cluster",
			expectedError: true,
			kopsControlPlaneFunction: func(kopsControlPlane *controlplanev1alpha1.KopsControlPlane) *controlplanev1alpha1.KopsControlPlane {
				kopsControlPlane.Spec.KopsClusterSpec.KubernetesVersion = ""
				return kopsControlPlane
			},
		},
		{
			description:            "should successfully create secret with Kubeconfig",
			createKubeconfigSecret: true,
		},
		{
			description:            "should successfully update Kubeconfig secret",
			updateKubeconfigSecret: true,
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
			kopsControlPlane := newKopsControlPlane("testCluster", metav1.NamespaceDefault)
			kopsControlPlaneSecret := newAWSCredentialSecret()

			cluster := newCluster("testCluster", getFQDN(kopsControlPlane.Name), metav1.NamespaceDefault)
			if tc.clusterFunction != nil {
				clusterFunction := tc.clusterFunction
				cluster = clusterFunction(cluster)
			}
			kopsMachinePool := newKopsMachinePool("testIG", kopsControlPlane.Namespace, cluster.Name)

			fakeKopsClientset := newFakeKopsClientset()

			kopsCluster := &kopsapi.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: getFQDN("testCluster"),
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
			var getASGByName func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, credentials *aws.CredentialsCache) (*asgTypes.AutoScalingGroup, error)
			if tc.getASGByNameFactory != nil {
				getASGByName = tc.getASGByNameFactory
			} else {
				getASGByName = func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, credentials *aws.CredentialsCache) (*asgTypes.AutoScalingGroup, error) {
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

			err = createFakeKopsKeyPair(keyStore)
			g.Expect(err).NotTo(HaveOccurred())

			reconciler := &KopsControlPlaneReconciler{
				log:    ctrl.LoggerFrom(ctx),
				Client: fakeClient,
				GetKopsClientSetFactory: func(configBase string) (simple.Clientset, error) {
					return fakeKopsClientset, nil
				},
				Recorder: record.NewFakeRecorder(5),
				BuildCloudFactory: func(*kopsapi.Cluster) (fi.Cloud, error) {
					return nil, nil
				},
				PopulateClusterSpecFactory: func(kopsCluster *kopsapi.Cluster, kopsClientset simple.Clientset, cloud fi.Cloud) (*kopsapi.Cluster, error) {
					return kopsCluster, nil
				},
				PrepareCloudResourcesFactory: func(kopsClientset simple.Clientset, kubeClient client.Client, ctx context.Context, kopsCluster *kopsapi.Cluster, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, configBase, terraformOutputDir string, cloud fi.Cloud, shouldIgnoreSG bool) error {
					return nil
				},
				ApplyTerraformFactory: func(ctx context.Context, terraformDir, tfExecPath string) error {
					return nil
				},
				GetClusterStatusFactory: func(kopsCluster *kopsapi.Cluster, cloud fi.Cloud) (*kopsapi.ClusterStatus, error) {
					return nil, nil
				},
				ValidateKopsClusterFactory: func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, cloud fi.Cloud, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error) {
					return &validation.ValidationCluster{}, nil
				},
				GetASGByNameFactory: getASGByName,
			}
			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: client.ObjectKey{
					Namespace: metav1.NamespaceDefault,
					Name:      getFQDN("testCluster"),
				},
			})
			if !tc.expectedError {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result.Requeue).To(BeFalse())
				if tc.expectedRequeue {
					g.Expect(result.RequeueAfter).To(Equal(time.Duration(1 * time.Minute)))
				} else {
					g.Expect(result.RequeueAfter).To(Equal(time.Duration(20 * time.Minute)))
				}

				kopsCluster, err := fakeKopsClientset.GetCluster(ctx, cluster.GetObjectMeta().GetName())
				g.Expect(kopsCluster).ToNot(BeNil())
				g.Expect(err).ToNot(HaveOccurred())
			} else {
				g.Expect(err).To(HaveOccurred())
				g.Expect(result.RequeueAfter).To(Equal(time.Duration(5 * time.Minute)))
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
		description                          string
		expectedReconcilerError              bool
		clusterFunction                      func(cluster *clusterv1.Cluster) *clusterv1.Cluster
		expectedStatus                       *controlplanev1alpha1.KopsControlPlaneStatus
		conditionsToAssert                   []*clusterv1.Condition
		eventsToAssert                       []string
		expectedErrorGetClusterStatusFactory func(kopsCluster *kopsapi.Cluster, cloud fi.Cloud) (*kopsapi.ClusterStatus, error)
		expectedErrorPrepareCloudResources   func(kopsClientset simple.Clientset, kubeClient client.Client, ctx context.Context, kopsCluster *kopsapi.Cluster, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, configBase, terraformOutputDir string, cloud fi.Cloud, shouldIgnoreSG bool) error
		expectedErrorApplyTerraform          func(ctx context.Context, terraformDir, tfExecPath string) error
		expectedValidateKopsCluster          func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, cloud fi.Cloud, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error)
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
			expectedErrorPrepareCloudResources: func(kopsClientset simple.Clientset, kubeClient client.Client, ctx context.Context, kopsCluster *kopsapi.Cluster, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, configBase, terraformOutputDir string, cloud fi.Cloud, shouldIgnoreSG bool) error {
				return errors.New("")
			},
			conditionsToAssert: []*clusterv1.Condition{
				conditions.FalseCondition(controlplanev1alpha1.KopsTerraformGenerationReadyCondition, controlplanev1alpha1.KopsTerraformGenerationReconciliationFailedReason, clusterv1.ConditionSeverityError, ""),
			},
		},
		{
			description:             "should mark false for condition TerraformApplyReadyCondition",
			expectedReconcilerError: true,
			expectedErrorApplyTerraform: func(ctx context.Context, terraformDir, tfExecPath string) error {
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
				"Normal KopsMachinePoolReconcileSuccess testIG",
				"dummy error message",
			},
			expectedValidateKopsCluster: func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, cloud fi.Cloud, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error) {
				return nil, errors.New("dummy error message")
			},
		},
		{
			description: "should have an event when the validation succeeds",
			eventsToAssert: []string{
				"Normal KopsMachinePoolReconcileSuccess testIG",
				"Kops validation succeed",
			},
		},
		{
			description: "should have an event with the failed validation",
			eventsToAssert: []string{
				"Normal KopsMachinePoolReconcileSuccess testIG",
				"failed to validate this test case",
			},
			expectedValidateKopsCluster: func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, cloud fi.Cloud, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error) {
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
				"Normal KopsMachinePoolReconcileSuccess testIG",
				"test case A",
				"test case B",
				"node hostA condition is False",
			},
			expectedValidateKopsCluster: func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, cloud fi.Cloud, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error) {
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
			kopsControlPlane := newKopsControlPlane("testCluster", metav1.NamespaceDefault)
			kopsControlPlaneSecret := newAWSCredentialSecret()
			cluster := newCluster("testCluster", getFQDN(kopsControlPlane.Name), metav1.NamespaceDefault)
			if tc.clusterFunction != nil {
				clusterFunction := tc.clusterFunction
				cluster = clusterFunction(cluster)
			}
			kopsMachinePool := newKopsMachinePool("testIG", kopsControlPlane.Namespace, cluster.Name)

			fakeClient := fake.NewClientBuilder().WithObjects(kopsControlPlane, kopsControlPlaneSecret, cluster, kopsMachinePool).WithScheme(scheme.Scheme).Build()

			fakeKopsClientset := newFakeKopsClientset()

			kopsCluster := &kopsapi.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: getFQDN("testCluster"),
				},
				Spec: kopsControlPlane.Spec.KopsClusterSpec,
			}

			kopsCluster, err := fakeKopsClientset.CreateCluster(ctx, kopsCluster)
			g.Expect(cluster).NotTo(BeNil())
			g.Expect(err).NotTo(HaveOccurred())

			keyStore, err := fakeKopsClientset.KeyStore(kopsCluster)
			g.Expect(err).NotTo(HaveOccurred())

			err = createFakeKopsKeyPair(keyStore)
			g.Expect(err).NotTo(HaveOccurred())

			recorder := record.NewFakeRecorder(5)

			var getClusterStatus func(kopsCluster *kopsapi.Cluster, cloud fi.Cloud) (*kopsapi.ClusterStatus, error)
			if tc.expectedErrorGetClusterStatusFactory != nil {
				getClusterStatus = tc.expectedErrorGetClusterStatusFactory
			} else {
				getClusterStatus = func(kopsCluster *kopsapi.Cluster, cloud fi.Cloud) (*kopsapi.ClusterStatus, error) {
					return nil, nil
				}
			}

			var prepareCloudResources func(kopsClientset simple.Clientset, kubeClient client.Client, ctx context.Context, kopsCluster *kopsapi.Cluster, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, configBase, terraformOutputDir string, cloud fi.Cloud, shouldIgnoreSG bool) error
			if tc.expectedErrorPrepareCloudResources != nil {
				prepareCloudResources = tc.expectedErrorPrepareCloudResources
			} else {
				prepareCloudResources = func(kopsClientset simple.Clientset, kubeClient client.Client, ctx context.Context, kopsCluster *kopsapi.Cluster, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, configBase, terraformOutputDir string, cloud fi.Cloud, shouldIgnoreSG bool) error {
					return nil
				}
			}

			var applyTerraform func(ctx context.Context, terraformDir, tfExecPath string) error
			if tc.expectedErrorApplyTerraform != nil {
				applyTerraform = tc.expectedErrorApplyTerraform
			} else {
				applyTerraform = func(ctx context.Context, terraformDir, tfExecPath string) error {
					return nil
				}
			}

			var validateKopsCluster func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, cloud fi.Cloud, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error)
			if tc.expectedValidateKopsCluster != nil {
				validateKopsCluster = tc.expectedValidateKopsCluster
			} else {
				validateKopsCluster = func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, cloud fi.Cloud, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error) {
					return &validation.ValidationCluster{}, nil
				}
			}

			reconciler := &KopsControlPlaneReconciler{
				log:      ctrl.LoggerFrom(ctx),
				Client:   fakeClient,
				Recorder: recorder,
				GetKopsClientSetFactory: func(configBase string) (simple.Clientset, error) {
					return fakeKopsClientset, nil
				},
				BuildCloudFactory: func(*kopsapi.Cluster) (fi.Cloud, error) {
					return nil, nil
				},
				PopulateClusterSpecFactory: func(kopsCluster *kopsapi.Cluster, kopsClientset simple.Clientset, cloud fi.Cloud) (*kopsapi.Cluster, error) {
					return kopsCluster, nil
				},
				PrepareCloudResourcesFactory: prepareCloudResources,
				ApplyTerraformFactory:        applyTerraform,
				GetClusterStatusFactory:      getClusterStatus,
				ValidateKopsClusterFactory:   validateKopsCluster,
				GetASGByNameFactory: func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, credentials *aws.CredentialsCache) (*asgTypes.AutoScalingGroup, error) {
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
					Name:      getFQDN("testCluster"),
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
				for _, eventMessage := range tc.eventsToAssert {
					g.Expect(recorder.Events).Should(Receive(ContainSubstring(eventMessage)))
				}
			}
		})
	}
}

func TestClusterToInfrastructureMapFunc(t *testing.T) {
	testCases := []struct {
		description    string
		input          client.Object
		expectedOutput []ctrl.Request
		expectedPanic  bool
	}{
		{
			description: "should return objectKey for KopsControlPlane",
			input: &clusterv1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testCluster",
					Namespace: metav1.NamespaceDefault,
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
			description: "should panic with an object different from kopsMachinePool",
			input: &clusterv1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testMachine",
					Namespace: metav1.NamespaceDefault,
				},
			},
			expectedPanic: true,
		},
		{
			description: "should return a empty list of requests when input don't have InfrastructureRef",
			input: &clusterv1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testCluster",
					Namespace: metav1.NamespaceDefault,
				},
				Spec: clusterv1.ClusterSpec{},
			},
		},
		{
			description: "should return a empty list of requests when Cluster's InfrastructureRef isn't a KopsControlPlane",
			input: &clusterv1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testCluster",
					Namespace: metav1.NamespaceDefault,
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
	}

	RegisterFailHandler(Fail)
	g := NewWithT(t)

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {

			if tc.expectedPanic {
				g.Expect(func() { clusterToInfrastructureMapFunc(tc.input) }).To(Panic())
			} else {
				req := clusterToInfrastructureMapFunc(tc.input)
				g.Expect(req).To(Equal(tc.expectedOutput))
			}
		})
	}
}

func TestKopsMachinePoolToInfrastructureMapFunc(t *testing.T) {
	testCases := []struct {
		description    string
		input          client.Object
		objects        []client.Object
		expectedOutput []ctrl.Request
		expectedPanic  bool
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
			description: "should panic with an object different from kopsMachinePool",
			input: &clusterv1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testMachine",
					Namespace: metav1.NamespaceDefault,
				},
			},
			expectedPanic: true,
		},
		{
			description: "should panic when Cluster isn't found",
			input: &infrastructurev1alpha1.KopsMachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testKopsMachinePool",
					Namespace: metav1.NamespaceDefault,
				},
				Spec: infrastructurev1alpha1.KopsMachinePoolSpec{
					ClusterName: "testCluster",
				},
			},
			expectedPanic: true,
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
		},
	}

	RegisterFailHandler(Fail)
	g := NewWithT(t)
	ctx := context.TODO()

	err := clusterv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {

			fakeClient := fake.NewClientBuilder().WithObjects(tc.objects...).WithScheme(scheme.Scheme).Build()

			reconciler := &KopsControlPlaneReconciler{
				log:    ctrl.LoggerFrom(ctx),
				Client: fakeClient,
			}
			if tc.expectedPanic {
				g.Expect(func() { reconciler.kopsMachinePoolToInfrastructureMapFunc(tc.input) }).To(Panic())
			} else {
				req := reconciler.kopsMachinePoolToInfrastructureMapFunc(tc.input)
				g.Expect(req).To(Equal(tc.expectedOutput))
			}
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
			fakeKopsClientset := newFakeKopsClientset()
			kopsCluster := newKopsCluster("test-kopscluster")
			_, err := fakeKopsClientset.CreateCluster(ctx, kopsCluster)
			g.Expect(err).NotTo(HaveOccurred())
			ig := newKopsIG("test-kopsig", kopsCluster.GetObjectMeta().GetName())
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

			reconciler := &KopsControlPlaneReconciler{
				kopsClientset: fakeKopsClientset,
				log:           ctrl.LoggerFrom(ctx),
			}
			err = reconciler.createOrUpdateInstanceGroup(ctx, kopsCluster, ig)
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
		region, err := regionBySubnet(kcp)

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

func newCluster(name, controlplane, namespace string) *clusterv1.Cluster {
	return &clusterv1.Cluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Cluster",
			APIVersion: "cluster.x-k8s.io/v1beta1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      getFQDN(name),
			UID:       "12793d7abd2813dnba87e6",
		},
		Spec: clusterv1.ClusterSpec{
			ControlPlaneRef: &corev1.ObjectReference{
				Name:      controlplane,
				Namespace: namespace,
				Kind:      "KopsControlPlane",
			},
		},
	}
}

func newAWSCredentialSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "11111111-credential",
			Namespace: "kubernetes-kops-operator-system",
		},
		Data: map[string][]byte{
			"AccessKeyID":     []byte("test"),
			"SecretAccessKey": []byte("test"),
		},
	}
}

func newKopsCluster(name string) *kopsapi.Cluster {
	return &kopsapi.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: getFQDN(name),
		},
		Spec: kopsapi.ClusterSpec{
			KubernetesVersion: "1.23.12",
			CloudProvider: kopsapi.CloudProviderSpec{
				AWS: &kopsapi.AWSSpec{},
			},
			ConfigBase:        fmt.Sprintf("memfs://tests/%s.test.k8s.cluster", name),
			NonMasqueradeCIDR: "10.0.1.0/21",
			NetworkCIDR:       "10.0.1.0/21",
			Subnets: []kopsapi.ClusterSubnetSpec{
				{
					Name: "test-subnet",
					CIDR: "10.0.1.0/24",
					Type: kopsapi.SubnetTypePrivate,
					Zone: "us-east-1",
				},
			},
			EtcdClusters: []kopsapi.EtcdClusterSpec{
				{
					Name:     "main",
					Provider: kopsapi.EtcdProviderTypeManager,
					Members: []kopsapi.EtcdMemberSpec{
						{
							Name:          "a",
							InstanceGroup: fi.String("eu-central-1a"),
						},
						{
							Name:          "b",
							InstanceGroup: fi.String("eu-central-1b"),
						},
						{
							Name:          "c",
							InstanceGroup: fi.String("eu-central-1c"),
						},
					},
				},
			},
			IAM: &kopsapi.IAMSpec{
				Legacy: false,
			},
		},
	}
}

func newKopsControlPlane(name, namespace string) *controlplanev1alpha1.KopsControlPlane {
	return &controlplanev1alpha1.KopsControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      getFQDN(name),
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "cluster.x-k8s.io/v1beta1",
					Kind:       "Cluster",
					Name:       getFQDN("testCluster"),
				},
			},
		},
		Spec: controlplanev1alpha1.KopsControlPlaneSpec{
			IdentityRef: &corev1.ObjectReference{
				Kind: "Secret",
				Name: "11111111-credential",
			},
			SSHPublicKey: "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCu7OR4k/qpc6VFqQsMGk7wQcnGzDA/hKABnj3qN85tgIDVsbnOIVgXl4FV1gO+gBjblCLkAmbZYlwhhkosL4xpEc8uk8QWJIzRqalvnLEofdIjClngGqzC40Yu6oVPiqImDazlVNvJ7UdzX02mmYJMe4eRzS1w1dA2hm9uTsaq6CNZuJF2/joV+SKLW88IEXWnb7PdOPZWFy0iN/9JcQKqON7zmR0j1zb4Ydj6Pt9MMIOTRiJpyeTqw0Gy4RWgkKJpwuRhOTnhZ1I8zigXgu4+keMYBgtLLP90Wx6/SI6vt+sG/Zrx5+s0av6vHFH/fDzqX4BSsxY83cOMH6ILLQ1C0hE9ykXx/EAKoou+DT8Doe0wabVxZNMRDOAb0ZnLF1HwUItW+MvgIjtCVpap/jBGmSSqZ5B9cvib7UV+JfLHty7n3AP2SKf52+E3Fp1fP4UiXQ/YUXZksopHLXLtwMdam/qijq5tjk0lVh7j8GGNuejt17+tSOCaP2kNKFyc1u8=",
			KopsClusterSpec: kopsapi.ClusterSpec{
				KubernetesVersion: "1.23.12",
				CloudProvider: kopsapi.CloudProviderSpec{
					AWS: &kopsapi.AWSSpec{},
				},
				Channel:           "none",
				ConfigBase:        fmt.Sprintf("memfs://tests/%s.test.k8s.cluster", name),
				NonMasqueradeCIDR: "10.0.1.0/21",
				NetworkCIDR:       "10.0.1.0/21",
				Subnets: []kopsapi.ClusterSubnetSpec{
					{
						Name: "test-subnet",
						CIDR: "10.0.1.0/24",
						Type: kopsapi.SubnetTypePrivate,
						Zone: "us-east-1",
					},
				},
				EtcdClusters: []kopsapi.EtcdClusterSpec{
					{
						Name:     "main",
						Provider: kopsapi.EtcdProviderTypeManager,
						Members: []kopsapi.EtcdMemberSpec{
							{
								Name:          "a",
								InstanceGroup: fi.String("eu-central-1a"),
							},
							{
								Name:          "b",
								InstanceGroup: fi.String("eu-central-1b"),
							},
							{
								Name:          "c",
								InstanceGroup: fi.String("eu-central-1c"),
							},
						},
					},
				},
				IAM: &kopsapi.IAMSpec{
					Legacy: false,
				},
			},
		},
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
func newMockedK8sClient(objects ...client.Object) client.Client {
	err := clusterv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	fakeClient := fake.NewClientBuilder().WithObjects(objects...).WithScheme(scheme.Scheme).Build()
	return fakeClient
}

func newFakeKopsClientset() simple.Clientset {
	memFSContext := vfs.NewMemFSContext()
	memfspath := vfs.NewMemFSPath(memFSContext, "memfs://tests")

	return vfsclientset.NewVFSClientset(memfspath)
}

func getFQDN(name string) string {
	return strings.ToLower(fmt.Sprintf("%s.test.k8s.cluster", name))
}

func newKopsIG(name, clusterName string) *kopsapi.InstanceGroup {
	return &kopsapi.InstanceGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: kopsapi.InstanceGroupSpec{
			Role: "Master",
			Subnets: []string{
				"dummy-subnet",
			},
		},
	}
}

func newKopsMachinePool(name, namespace, clusterName string) *infrastructurev1alpha1.KopsMachinePool {
	return &infrastructurev1alpha1.KopsMachinePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"cluster.x-k8s.io/cluster-name": clusterName,
			},
			ResourceVersion:   "1",
			CreationTimestamp: metav1.Time{},
		},
		Spec: infrastructurev1alpha1.KopsMachinePoolSpec{
			ClusterName: clusterName,
			KopsInstanceGroupSpec: kopsapi.InstanceGroupSpec{
				Role: "Master",
				Subnets: []string{
					"dummy-subnet",
				},
			},
		},
	}
}

func createFakeKopsKeyPair(keyStore fi.CAStore) error {
	certData := "-----BEGIN CERTIFICATE-----\nMIIC2DCCAcCgAwIBAgIRALJXAkVj964tq67wMSI8oJQwDQYJKoZIhvcNAQELBQAw\nFTETMBEGA1UEAxMKa3ViZXJuZXRlczAeFw0xNzEyMjcyMzUyNDBaFw0yNzEyMjcy\nMzUyNDBaMBUxEzARBgNVBAMTCmt1YmVybmV0ZXMwggEiMA0GCSqGSIb3DQEBAQUA\nA4IBDwAwggEKAoIBAQDgnCkSmtnmfxEgS3qNPaUCH5QOBGDH/inHbWCODLBCK9gd\nXEcBl7FVv8T2kFr1DYb0HVDtMI7tixRVFDLgkwNlW34xwWdZXB7GeoFgU1xWOQSY\nOACC8JgYTQ/139HBEvgq4sej67p+/s/SNcw34Kk7HIuFhlk1rRk5kMexKIlJBKP1\nYYUYetsJ/QpUOkqJ5HW4GoetE76YtHnORfYvnybviSMrh2wGGaN6r/s4ChOaIbZC\nAn8/YiPKGIDaZGpj6GXnmXARRX/TIdgSQkLwt0aTDBnPZ4XvtpI8aaL8DYJIqAzA\nNPH2b4/uNylat5jDo0b0G54agMi97+2AUrC9UUXpAgMBAAGjIzAhMA4GA1UdDwEB\n/wQEAwIBBjAPBgNVHRMBAf8EBTADAQH/MA0GCSqGSIb3DQEBCwUAA4IBAQBVGR2r\nhzXzRMU5wriPQAJScszNORvoBpXfZoZ09FIupudFxBVU3d4hV9StKnQgPSGA5XQO\nHE97+BxJDuA/rB5oBUsMBjc7y1cde/T6hmi3rLoEYBSnSudCOXJE4G9/0f8byAJe\nrN8+No1r2VgZvZh6p74TEkXv/l3HBPWM7IdUV0HO9JDhSgOVF1fyQKJxRuLJR8jt\nO6mPH2UX0vMwVa4jvwtkddqk2OAdYQvH9rbDjjbzaiW0KnmdueRo92KHAN7BsDZy\nVpXHpqo1Kzg7D3fpaXCf5si7lqqrdJVXH4JC72zxsPehqgi8eIuqOBkiDWmRxAxh\n8yGeRx9AbknHh4Ia\n-----END CERTIFICATE-----\n"
	privatekeyData := "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA4JwpEprZ5n8RIEt6jT2lAh+UDgRgx/4px21gjgywQivYHVxH\nAZexVb/E9pBa9Q2G9B1Q7TCO7YsUVRQy4JMDZVt+McFnWVwexnqBYFNcVjkEmDgA\ngvCYGE0P9d/RwRL4KuLHo+u6fv7P0jXMN+CpOxyLhYZZNa0ZOZDHsSiJSQSj9WGF\nGHrbCf0KVDpKieR1uBqHrRO+mLR5zkX2L58m74kjK4dsBhmjeq/7OAoTmiG2QgJ/\nP2IjyhiA2mRqY+hl55lwEUV/0yHYEkJC8LdGkwwZz2eF77aSPGmi/A2CSKgMwDTx\n9m+P7jcpWreYw6NG9BueGoDIve/tgFKwvVFF6QIDAQABAoIBAA0ktjaTfyrAxsTI\nBezb7Zr5NBW55dvuII299cd6MJo+rI/TRYhvUv48kY8IFXp/hyUjzgeDLunxmIf9\n/Zgsoic9Ol44/g45mMduhcGYPzAAeCdcJ5OB9rR9VfDCXyjYLlN8H8iU0734tTqM\n0V13tQ9zdSqkGPZOIcq/kR/pylbOZaQMe97BTlsAnOMSMKDgnftY4122Lq3GYy+t\nvpr+bKVaQZwvkLoSU3rECCaKaghgwCyX7jft9aEkhdJv+KlwbsGY6WErvxOaLWHd\ncuMQjGapY1Fa/4UD00mvrA260NyKfzrp6+P46RrVMwEYRJMIQ8YBAk6N6Hh7dc0G\n8Z6i1m0CgYEA9HeCJR0TSwbIQ1bDXUrzpftHuidG5BnSBtax/ND9qIPhR/FBW5nj\n22nwLc48KkyirlfIULd0ae4qVXJn7wfYcuX/cJMLDmSVtlM5Dzmi/91xRiFgIzx1\nAsbBzaFjISP2HpSgL+e9FtSXaaqeZVrflitVhYKUpI/AKV31qGHf04sCgYEA6zTV\n99Sb49Wdlns5IgsfnXl6ToRttB18lfEKcVfjAM4frnkk06JpFAZeR+9GGKUXZHqs\nz2qcplw4d/moCC6p3rYPBMLXsrGNEUFZqBlgz72QA6BBq3X0Cg1Bc2ZbK5VIzwkg\nST2SSux6ccROfgULmN5ZiLOtdUKNEZpFF3i3qtsCgYADT/s7dYFlatobz3kmMnXK\nsfTu2MllHdRys0YGHu7Q8biDuQkhrJwhxPW0KS83g4JQym+0aEfzh36bWcl+u6R7\nKhKj+9oSf9pndgk345gJz35RbPJYh+EuAHNvzdgCAvK6x1jETWeKf6btj5pF1U1i\nQ4QNIw/QiwIXjWZeubTGsQKBgQCbduLu2rLnlyyAaJZM8DlHZyH2gAXbBZpxqU8T\nt9mtkJDUS/KRiEoYGFV9CqS0aXrayVMsDfXY6B/S/UuZjO5u7LtklDzqOf1aKG3Q\ndGXPKibknqqJYH+bnUNjuYYNerETV57lijMGHuSYCf8vwLn3oxBfERRX61M/DU8Z\nworz/QKBgQDCTJI2+jdXg26XuYUmM4XXfnocfzAXhXBULt1nENcogNf1fcptAVtu\nBAiz4/HipQKqoWVUYmxfgbbLRKKLK0s0lOWKbYdVjhEm/m2ZU8wtXTagNwkIGoyq\nY/C1Lox4f1ROJnCjc/hfcOjcxX5M8A8peecHWlVtUPKTJgxQ7oMKcw==\n-----END RSA PRIVATE KEY-----\n"

	cert, _ := pki.ParsePEMCertificate([]byte(certData))
	key, _ := pki.ParsePEMPrivateKey([]byte(privatekeyData))
	keyset := &fi.Keyset{
		Items: map[string]*fi.KeysetItem{
			"1": {
				Id:          "1",
				Certificate: cert,
				PrivateKey:  key,
			},
		},
		Primary: &fi.KeysetItem{
			Id:          "1",
			Certificate: cert,
			PrivateKey:  key,
		},
	}
	err := keyStore.StoreKeyset(fi.CertificateIDCA, keyset)
	return err
}
