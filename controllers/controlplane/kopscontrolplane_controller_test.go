package controlplane

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
	controlplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	kopsapi "k8s.io/kops/pkg/apis/kops"
	"k8s.io/kops/pkg/client/simple"
	"k8s.io/kops/pkg/client/simple/vfsclientset"
	"k8s.io/kops/pkg/validation"
	"k8s.io/kops/upup/pkg/fi"
	"k8s.io/kops/util/pkg/vfs"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type MockKopsControlPlaneManagedScope struct {
	mock.Mock
}

func (mkcpms *MockKopsControlPlaneManagedScope) applyTerraform(ctx context.Context, terraformDir string) error {
	args := mkcpms.Called()
	return args.Error(0)
}

func (mkcpms *MockKopsControlPlaneManagedScope) buildCloud(kopscluster *kopsapi.Cluster) (fi.Cloud, error) {
	return nil, nil
}

func (mkcpms *MockKopsControlPlaneManagedScope) populateClusterSpec(kopsCluster *kopsapi.Cluster, kopsClientset simple.Clientset) (*kopsapi.Cluster, error) {
	return kopsCluster, nil
}

func (mkcpms *MockKopsControlPlaneManagedScope) prepareCloudResources(kopsClientset simple.Clientset, ctx context.Context, kopsCluster *kopsapi.Cluster, configBase string) (string, error) {
	args := mkcpms.Called()
	result := args.Get(0)
	return result.(string), args.Error(1)
}

func (mkcpms *MockKopsControlPlaneManagedScope) getClusterStatus(kopsCluster *kopsapi.Cluster) (*kopsapi.ClusterStatus, error) {
	clusterStatus := &kopsapi.ClusterStatus{}
	return clusterStatus, nil
}

func (mkcpms *MockKopsControlPlaneManagedScope) updateKopsState(ctx context.Context, kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, SSHPublicKey string) error {
	args := mkcpms.Called()
	return args.Error(0)
}

func (mkcpms *MockKopsControlPlaneManagedScope) validateKopsCluster(k8sClient *kubernetes.Clientset, kopsCluster *kopsapi.Cluster, cloud fi.Cloud, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error) {
	result := &validation.ValidationCluster{}
	return result, nil
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
		result := evaluateKopsValidationResult(tc["input"].(*validation.ValidationCluster))
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
				log:          ctrl.LoggerFrom(ctx),
				Client:       fakeClient,
				ManagedScope: &MockKopsControlPlaneManagedScope{},
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

				sshCredential, err := sshCredentialStore.FindSSHPublicKeys("admin")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(sshCredential).NotTo(BeNil())
			} else {
				g.Expect(err).To(HaveOccurred())
			}

		})
	}

}

func TestUpdateKopsState(t *testing.T) {
	testCases := []map[string]interface{}{
		{
			"description":         "Should successfully create Kops Cluster",
			"expectedError":       false,
			"kopsClusterFunction": nil,
			"updateCluster":       false,
		},
		{
			"description":   "Should fail validation without required Kops Cluster fields",
			"expectedError": true,
			"kopsClusterFunction": func(kopsCluster *kopsapi.Cluster) *kopsapi.Cluster {
				return &kopsapi.Cluster{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster",
					},
					Spec: kopsapi.ClusterSpec{},
				}
			},
			"updateCluster": false,
		},
	}
	RegisterFailHandler(Fail)
	vfs.Context.ResetMemfsContext(true)
	g := NewWithT(t)
	ctx := context.TODO()
	dummySSHPublicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCu7OR4k/qpc6VFqQsMGk7wQcnGzDA/hKABnj3qN85tgIDVsbnOIVgXl4FV1gO+gBjblCLkAmbZYlwhhkosL4xpEc8uk8QWJIzRqalvnLEofdIjClngGqzC40Yu6oVPiqImDazlVNvJ7UdzX02mmYJMe4eRzS1w1dA2hm9uTsaq6CNZuJF2/joV+SKLW88IEXWnb7PdOPZWFy0iN/9JcQKqON7zmR0j1zb4Ydj6Pt9MMIOTRiJpyeTqw0Gy4RWgkKJpwuRhOTnhZ1I8zigXgu4+keMYBgtLLP90Wx6/SI6vt+sG/Zrx5+s0av6vHFH/fDzqX4BSsxY83cOMH6ILLQ1C0hE9ykXx/EAKoou+DT8Doe0wabVxZNMRDOAb0ZnLF1HwUItW+MvgIjtCVpap/jBGmSSqZ5B9cvib7UV+JfLHty7n3AP2SKf52+E3Fp1fP4UiXQ/YUXZksopHLXLtwMdam/qijq5tjk0lVh7j8GGNuejt17+tSOCaP2kNKFyc1u8="
	for _, tc := range testCases {
		t.Run(tc["description"].(string), func(t *testing.T) {
			bareKopsCluster := newKopsCluster("test-cluster")
			if tc["kopsClusterFunction"] != nil {
				kopsClusterFunction := tc["kopsClusterFunction"].(func(kopsCluster *kopsapi.Cluster) *kopsapi.Cluster)
				bareKopsCluster = kopsClusterFunction(bareKopsCluster)
			}
			fakeKopsClientset := newFakeKopsClientset()

			if tc["updateCluster"].(bool) == true {
				bareKopsCluster, err := fakeKopsClientset.CreateCluster(ctx, bareKopsCluster)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(bareKopsCluster).NotTo(BeNil())
				bareKopsCluster.Spec.KubernetesVersion = "1.21.0"
			}

			reconciler := &KopsControlPlaneReconciler{
				log:          ctrl.LoggerFrom(ctx),
				ManagedScope: &KopsControlPlaneManagedScope{},
			}

			err := reconciler.ManagedScope.updateKopsState(ctx, fakeKopsClientset, bareKopsCluster, dummySSHPublicKey)
			if !tc["expectedError"].(bool) {
				g.Expect(err).NotTo(HaveOccurred())
				cluster, err := fakeKopsClientset.GetCluster(ctx, bareKopsCluster.Name)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(cluster).NotTo(BeNil())
				if tc["updateCluster"].(bool) == true {
					g.Expect(cluster.Spec.KubernetesVersion).To(Equal("1.21.0"))
				}
			} else {
				g.Expect(err).To(HaveOccurred())
			}
		})
	}
}

func TestKopsControlPlaneReconciler(t *testing.T) {
	testCases := []map[string]interface{}{
		{
			"description":              "Should successfully create Kops Cluster",
			"expectedError":            false,
			"kopsControlPlaneFunction": nil,
		},
	}
	RegisterFailHandler(Fail)
	vfs.Context.ResetMemfsContext(true)
	g := NewWithT(t)
	ctx := context.TODO()
	err := controlplanev1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	for _, tc := range testCases {
		t.Run(tc["description"].(string), func(t *testing.T) {
			kopsControlPlane := newKopsControlPlane("testKopsControlPlane", metav1.NamespaceDefault)
			if tc["kopsControlPlaneFunction"] != nil {
				kopsControlPlaneFunction := tc["kopsControlPlaneFunction"].(func(kopsControlPlane *controlplanev1alpha1.KopsControlPlane) *controlplanev1alpha1.KopsControlPlane)
				kopsControlPlane = kopsControlPlaneFunction(kopsControlPlane)
			}

			fakeClient := fake.NewClientBuilder().WithObjects(kopsControlPlane).WithScheme(scheme.Scheme).Build()

			reconciler := &KopsControlPlaneReconciler{
				log:    ctrl.LoggerFrom(ctx),
				Client: fakeClient,
			}
			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: client.ObjectKey{
					Namespace: metav1.NamespaceDefault,
					Name:      getFQDN("testKopsControlPlane"),
				},
			})
			if !tc["expectedError"].(bool) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result.Requeue).To(BeFalse())
				g.Expect(result.RequeueAfter).To(Equal(time.Duration(0)))
			} else {
				g.Expect(err).To(HaveOccurred())
			}
		})
	}
}

func TestKopsControlPlaneReconciler_Patch(t *testing.T) {
	testCases := []map[string]interface{}{
		{
			"description":             "should successfully patch KopsControlPlane",
			"expectedReconcilerError": false,
			"conditionsToAssert":      []*clusterv1.Condition{},
		},
		{
			"description":                  "should patch condition failed for KopsControlPlaneStateReadyCondition",
			"expectedReconcilerError":      true,
			"expectedErrorUpdateKopsState": errors.New(""),
			"conditionsToAssert": []*clusterv1.Condition{
				conditions.FalseCondition(controlplanev1alpha1.KopsControlPlaneStateReadyCondition, controlplanev1alpha1.KopsControlPlaneStateReconciliationFailedReason, clusterv1.ConditionSeverityError, ""),
			},
		},
		{
			"description":                        "should patch condition failed for KopsTerraformGenerationReadyCondition",
			"expectedReconcilerError":            true,
			"expectedErrorPrepareCloudResources": errors.New(""),
			"conditionsToAssert": []*clusterv1.Condition{
				conditions.FalseCondition(controlplanev1alpha1.KopsTerraformGenerationReadyCondition, controlplanev1alpha1.KopsTerraformGenerationReconciliationFailedReason, clusterv1.ConditionSeverityError, ""),
			},
		},
		{
			"description":                 "should patch condition failed for TerraformApplyReadyCondition",
			"expectedReconcilerError":     true,
			"expectedErrorApplyTerraform": errors.New(""),
			"conditionsToAssert": []*clusterv1.Condition{
				conditions.FalseCondition(controlplanev1alpha1.TerraformApplyReadyCondition, controlplanev1alpha1.TerraformApplyReconciliationFailedReason, clusterv1.ConditionSeverityError, ""),
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

	for _, tc := range testCases {
		t.Run(tc["description"].(string), func(t *testing.T) {
			kopsControlPlane := newKopsControlPlane("testKopsControlPlane", metav1.NamespaceDefault)
			cluster := newCluster("testCluster", getFQDN(kopsControlPlane.Name), metav1.NamespaceDefault)

			fakeClient := fake.NewClientBuilder().WithObjects(kopsControlPlane, cluster).WithScheme(scheme.Scheme).Build()
			mockKopsControlPlaneManagedScope := &MockKopsControlPlaneManagedScope{}

			if _, ok := tc["expectedErrorUpdateKopsState"]; ok {
				mockKopsControlPlaneManagedScope.On("updateKopsState").Return(tc["expectedErrorUpdateKopsState"])
			} else {
				mockKopsControlPlaneManagedScope.On("updateKopsState").Return(nil)
			}

			if _, ok := tc["expectedErrorPrepareCloudResources"]; ok {
				mockKopsControlPlaneManagedScope.On("prepareCloudResources").Return("", tc["expectedErrorPrepareCloudResources"])
			} else {
				mockKopsControlPlaneManagedScope.On("prepareCloudResources").Return("", nil)
			}

			if _, ok := tc["expectedErrorApplyTerraform"]; ok {
				mockKopsControlPlaneManagedScope.On("applyTerraform").Return(tc["expectedErrorApplyTerraform"])
			} else {
				mockKopsControlPlaneManagedScope.On("applyTerraform").Return(nil)
			}

			reconciler := &KopsControlPlaneReconciler{
				log:          ctrl.LoggerFrom(ctx),
				Client:       fakeClient,
				ManagedScope: mockKopsControlPlaneManagedScope,
			}

			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: client.ObjectKey{
					Namespace: metav1.NamespaceDefault,
					Name:      getFQDN("testKopsControlPlane"),
				},
			})
			if !tc["expectedReconcilerError"].(bool) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result.Requeue).To(BeFalse())
				g.Expect(result.RequeueAfter).To(Equal(time.Duration(0)))
			} else {
				g.Expect(err).To(HaveOccurred())
			}

			kcp := &controlplanev1alpha1.KopsControlPlane{}
			err = fakeClient.Get(ctx, client.ObjectKeyFromObject(kopsControlPlane), kcp)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(kcp.Status.Conditions).ToNot(BeNil())
			conditionsToAssert := tc["conditionsToAssert"].([]*clusterv1.Condition)
			assertConditions(g, kcp, conditionsToAssert...)
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

func newKopsCluster(name string) *kopsapi.Cluster {
	return &kopsapi.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: getFQDN(name),
		},
		Spec: kopsapi.ClusterSpec{
			KubernetesVersion: "1.20.1",
			CloudProvider:     "aws",
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
			SSHPublicKey: "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCu7OR4k/qpc6VFqQsMGk7wQcnGzDA/hKABnj3qN85tgIDVsbnOIVgXl4FV1gO+gBjblCLkAmbZYlwhhkosL4xpEc8uk8QWJIzRqalvnLEofdIjClngGqzC40Yu6oVPiqImDazlVNvJ7UdzX02mmYJMe4eRzS1w1dA2hm9uTsaq6CNZuJF2/joV+SKLW88IEXWnb7PdOPZWFy0iN/9JcQKqON7zmR0j1zb4Ydj6Pt9MMIOTRiJpyeTqw0Gy4RWgkKJpwuRhOTnhZ1I8zigXgu4+keMYBgtLLP90Wx6/SI6vt+sG/Zrx5+s0av6vHFH/fDzqX4BSsxY83cOMH6ILLQ1C0hE9ykXx/EAKoou+DT8Doe0wabVxZNMRDOAb0ZnLF1HwUItW+MvgIjtCVpap/jBGmSSqZ5B9cvib7UV+JfLHty7n3AP2SKf52+E3Fp1fP4UiXQ/YUXZksopHLXLtwMdam/qijq5tjk0lVh7j8GGNuejt17+tSOCaP2kNKFyc1u8=",
			KopsClusterSpec: kopsapi.ClusterSpec{
				KubernetesVersion: "1.20.1",
				CloudProvider:     "aws",
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
