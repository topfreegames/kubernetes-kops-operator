package controlplane

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/template"

	dto "github.com/prometheus/client_model/go"

	"sync"
	"testing"
	"time"

	expv1 "sigs.k8s.io/cluster-api/exp/api/v1beta1"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/topfreegames/kubernetes-kops-operator/pkg/helpers"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	karpenterv1beta1 "sigs.k8s.io/karpenter/pkg/apis/v1beta1"

	asgTypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	infrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	controlplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	custommetrics "github.com/topfreegames/kubernetes-kops-operator/metrics"

	"github.com/topfreegames/kubernetes-kops-operator/utils"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	kopsapi "k8s.io/kops/pkg/apis/kops"
	"k8s.io/kops/pkg/client/simple"
	"k8s.io/kops/pkg/kubemanifest"
	"k8s.io/kops/pkg/validation"
	"k8s.io/kops/upup/pkg/fi"
	"k8s.io/kops/util/pkg/vfs"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/secret"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestEvaluateKopsValidationResult(t *testing.T) {
	testCases := []struct {
		description    string
		input          *validation.ValidationCluster
		expectedResult bool
	}{
		{
			description:    "should succeed without failures and nodes",
			input:          &validation.ValidationCluster{},
			expectedResult: true,
		},
		{
			description: "should fail with failures not empty",
			input: &validation.ValidationCluster{
				Failures: []*validation.ValidationError{
					{
						Name: "TestError",
					},
				},
			},
			expectedResult: false,
		},
		{
			description: "should succeed with nodes with condition true",
			input: &validation.ValidationCluster{
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
			expectedResult: true,
		},
		{
			description: "should fail if any node with condition false",
			input: &validation.ValidationCluster{
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
			expectedResult: false,
		},
		{
			description: "should succeed with only pods failures",
			input: &validation.ValidationCluster{
				Failures: []*validation.ValidationError{
					{
						Name:    "pod-test",
						Kind:    "Pod",
						Message: "pod-test pod is not ready",
					},
				},
			},
			expectedResult: true,
		},
	}

	RegisterFailHandler(Fail)
	g := NewWithT(t)

	for _, tc := range testCases {
		result, _ := utils.EvaluateKopsValidationResult(tc.input)
		if tc.expectedResult {
			g.Expect(result).To(BeTrue())
		} else {
			g.Expect(result).To(BeFalse())
		}
	}
}

func TestReconcileClusterAddons(t *testing.T) {
	addon := `---
apiVersion: kubescheduler.config.k8s.io/v1beta3
kind: KubeSchedulerConfiguration
profiles:
- schedulerName: default-scheduler
  pluginConfig:
  - name: PodTopologySpread
    args:
      defaultConstraints:
      - maxSkew: 1
        topologyKey: "kubernetes.io/hostname"
        whenUnsatisfiable: ScheduleAnyway
      - maxSkew: 3
        topologyKey: "topology.kubernetes.io/zone"
        whenUnsatisfiable: ScheduleAnyway
      defaultingType: List`
	addons := `---
apiVersion: kubescheduler.config.k8s.io/v1beta3
kind: KubeSchedulerConfiguration
profiles:
- schedulerName: default-scheduler
  pluginConfig:
  - name: PodTopologySpread
    args:
      defaultConstraints:
      - maxSkew: 1
        topologyKey: "kubernetes.io/hostname"
        whenUnsatisfiable: ScheduleAnyway
      defaultingType: List
---
apiVersion: kubescheduler.config.k8s.io/v1beta3
kind: RandomObject`

	testCases := []struct {
		description    string
		currentAddons  string
		kcp            *controlplanev1alpha1.KopsControlPlane
		assertFunction func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, err error) bool
	}{
		{
			description: "should do nothing when the kcp don't define a cluster addon",
			kcp: &controlplanev1alpha1.KopsControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: metav1.NamespaceDefault,
				},
				Spec: controlplanev1alpha1.KopsControlPlaneSpec{},
			},
			assertFunction: func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, err error) bool {
				return err == nil
			},
		},
		{
			description: "should create the addon in the kops state",
			kcp: &controlplanev1alpha1.KopsControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: metav1.NamespaceDefault,
				},
				Spec: controlplanev1alpha1.KopsControlPlaneSpec{
					KopsClusterAddons: addon,
				},
			},
			assertFunction: func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, _ error) bool {
				obj, err := kopsClientset.AddonsFor(kopsCluster).List(context.TODO())
				if err != nil {
					return false
				}
				return len(obj) == 1

			},
		},
		{
			description: "should remove the addon from the kops state",
			kcp: &controlplanev1alpha1.KopsControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: metav1.NamespaceDefault,
				},
			},
			currentAddons: addon,
			assertFunction: func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, _ error) bool {
				obj, err := kopsClientset.AddonsFor(kopsCluster).List(context.TODO())
				if err != nil {
					return false
				}
				return len(obj) == 0

			},
		},
		{
			description: "should replace the addon for both addons in the kops state",
			kcp: &controlplanev1alpha1.KopsControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: metav1.NamespaceDefault,
				},
				Spec: controlplanev1alpha1.KopsControlPlaneSpec{
					KopsClusterAddons: addons,
				},
			},
			currentAddons: addon,
			assertFunction: func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, _ error) bool {
				obj, err := kopsClientset.AddonsFor(kopsCluster).List(context.TODO())
				if err != nil {
					return false
				}
				return len(obj) == 2

			},
		},
		{
			description: "should replace for only the default-scheduler in the kops state",
			kcp: &controlplanev1alpha1.KopsControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: metav1.NamespaceDefault,
				},
				Spec: controlplanev1alpha1.KopsControlPlaneSpec{
					KopsClusterAddons: addon,
				},
			},
			currentAddons: addons,
			assertFunction: func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, _ error) bool {
				obj, err := kopsClientset.AddonsFor(kopsCluster).List(context.TODO())
				if err != nil {
					return false
				}

				return obj[0].ToUnstructured().GetKind() == "KubeSchedulerConfiguration"
			},
		},
		{
			description: "should fail when the yaml is invalid",
			kcp: &controlplanev1alpha1.KopsControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: metav1.NamespaceDefault,
				},
				Spec: controlplanev1alpha1.KopsControlPlaneSpec{
					KopsClusterAddons: "invalid-yaml",
				},
			},
			assertFunction: func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, err error) bool {
				return strings.HasPrefix(err.Error(), "error parsing yaml")
			},
		},
	}

	RegisterFailHandler(Fail)
	vfs.Context.ResetMemfsContext(true)
	g := NewWithT(t)
	ctx := context.TODO()
	err := controlplanev1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			reconciler := &KopsControlPlaneReconciler{}

			fakeKopsClientset := helpers.NewFakeKopsClientset()
			kopsCluster := helpers.NewKopsCluster("test-cluster")
			kopsCluster, err := fakeKopsClientset.CreateCluster(ctx, kopsCluster)

			if tc.currentAddons != "" {
				addons, err := kubemanifest.LoadObjectsFrom([]byte(tc.currentAddons))
				g.Expect(err).NotTo(HaveOccurred())
				err = fakeKopsClientset.AddonsFor(kopsCluster).Replace(addons)
				g.Expect(err).NotTo(HaveOccurred())
			}
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(kopsCluster).NotTo(BeNil())

			reconciliation := &KopsControlPlaneReconciliation{
				KopsControlPlaneReconciler: *reconciler,
				kcp:                        tc.kcp,
			}

			err = reconciliation.reconcileClusterAddons(fakeKopsClientset, kopsCluster)
			g.Expect(tc.assertFunction(fakeKopsClientset, kopsCluster, err)).To(BeTrue())
		})
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
				kopsCluster.Spec.ConfigStore.Base = ""
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

			err = addSSHCredential(ctx, kopsCluster, fakeKopsClientset, tc["SSHPublicKey"].(string))
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

			reconciliation := &KopsControlPlaneReconciliation{
				KopsControlPlaneReconciler: *reconciler,
			}

			err := reconciliation.createOrUpdateKopsCluster(ctx, fakeKopsClientset, bareKopsCluster, dummySSHPublicKey, nil)
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

			err = helpers.CreateFakeKopsKeyPair(ctx, keyStore)
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
				PopulateClusterSpecFactory: func(ctx context.Context, kopsCluster *kopsapi.Cluster, kopsClientset simple.Clientset, cloud fi.Cloud) (*kopsapi.Cluster, error) {
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

func TestCustomMetrics(t *testing.T) {
	var reconciler *KopsControlPlaneReconciler

	testCases := []struct {
		description                string
		expectedResult             float64
		reconciliationStatusResult []string
	}{
		{
			description:                "should be zero on successful reconciliation",
			expectedResult:             0.0,
			reconciliationStatusResult: []string{"succeed"},
		},
		{
			description:                "should get incremented on reconcile errors",
			expectedResult:             1.0,
			reconciliationStatusResult: []string{"fail"},
		},
		{
			description:                "should get incremented on consecutive reconcile errors",
			expectedResult:             2.0,
			reconciliationStatusResult: []string{"fail", "fail"},
		},
		{
			description:                "should be zero after a successful reconciliation",
			expectedResult:             0.0,
			reconciliationStatusResult: []string{"fail", "succeed"},
		},
	}
	RegisterFailHandler(Fail)
	custommetrics.ReconciliationConsecutiveErrorsTotal.Reset()
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
			custommetrics.ReconciliationConsecutiveErrorsTotal.Reset()

			kopsControlPlane := helpers.NewKopsControlPlane("testCluster", metav1.NamespaceDefault)
			kopsControlPlaneSecret := helpers.NewAWSCredentialSecret()

			cluster := helpers.NewCluster("testCluster", helpers.GetFQDN(kopsControlPlane.Name), metav1.NamespaceDefault)
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

			fakeClient := fake.NewClientBuilder().WithObjects(kopsControlPlane, cluster, kopsControlPlaneSecret, kopsMachinePool).WithScheme(scheme.Scheme).Build()

			keyStore, err := fakeKopsClientset.KeyStore(kopsCluster)
			g.Expect(err).NotTo(HaveOccurred())

			err = helpers.CreateFakeKopsKeyPair(ctx, keyStore)
			g.Expect(err).NotTo(HaveOccurred())

			reconciler = &KopsControlPlaneReconciler{
				Client: fakeClient,
				GetKopsClientSetFactory: func(configBase string) (simple.Clientset, error) {
					return fakeKopsClientset, nil
				},
				Mux:      new(sync.Mutex),
				Recorder: record.NewFakeRecorder(10),
				PopulateClusterSpecFactory: func(ctx context.Context, kopsCluster *kopsapi.Cluster, kopsClientset simple.Clientset, cloud fi.Cloud) (*kopsapi.Cluster, error) {
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

			var buildCloudFactory func(*kopsapi.Cluster) (fi.Cloud, error)
			for _, reconciliationStatus := range tc.reconciliationStatusResult {
				reconciler.Recorder = record.NewFakeRecorder(10)
				// This is just to force a reconciliation error so we can increment the metric
				if reconciliationStatus == "fail" {
					buildCloudFactory = func(*kopsapi.Cluster) (fi.Cloud, error) {
						return nil, errors.New("error")
					}
				} else {
					buildCloudFactory = func(*kopsapi.Cluster) (fi.Cloud, error) {
						return nil, nil
					}
				}
				reconciler.BuildCloudFactory = buildCloudFactory

				_, _ = reconciler.Reconcile(context.TODO(), ctrl.Request{
					NamespacedName: client.ObjectKey{
						Namespace: metav1.NamespaceDefault,
						Name:      helpers.GetFQDN("testCluster"),
					},
				})

			}

			var reconciliationConsecutiveErrorsTotal dto.Metric
			Expect(func() error {
				Expect(custommetrics.ReconciliationConsecutiveErrorsTotal.WithLabelValues("kops-operator", "testcluster.test.k8s.cluster", "testing").Write(&reconciliationConsecutiveErrorsTotal)).To(Succeed())
				metricValue := reconciliationConsecutiveErrorsTotal.GetGauge().GetValue()
				if metricValue != tc.expectedResult {
					return fmt.Errorf("metric value differs from expected: %f != %f", metricValue, tc.expectedResult)
				}
				return nil
			}()).Should(Succeed())

		})
	}
}

func TestKopsControlPlaneReconcilerDelete(t *testing.T) {
	testCases := []struct {
		description              string
		kopsControlPlaneFunction func(kopsControlPlane *controlplanev1alpha1.KopsControlPlane) *controlplanev1alpha1.KopsControlPlane
		kopsMachinePoolFunction  func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool) *infrastructurev1alpha1.KopsMachinePool
		assertFunction           func(client.Client) bool
	}{
		{
			description: "should delete ig",
			kopsMachinePoolFunction: func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool) *infrastructurev1alpha1.KopsMachinePool {
				kopsMachinePool.SetOwnerReferences(
					[]metav1.OwnerReference{
						{
							Kind:       "MachinePool",
							APIVersion: "cluster.x-k8s.io/v1beta1",
							Name:       "test-machine-pool",
							UID:        "1",
						},
					},
				)
				kopsMachinePool.DeletionTimestamp = &metav1.Time{Time: time.Now()}
				controllerutil.AddFinalizer(kopsMachinePool, infrastructurev1alpha1.KopsMachinePoolFinalizer)
				return kopsMachinePool
			},
			assertFunction: func(kubeClient client.Client) bool {
				kmp := &infrastructurev1alpha1.KopsMachinePool{}
				errKMP := kubeClient.Get(context.TODO(), client.ObjectKey{
					Namespace: metav1.NamespaceDefault,
					Name:      "test-kops-machine-pool",
				}, kmp)

				mp := &expv1.MachinePool{}
				errMP := kubeClient.Get(context.TODO(), client.ObjectKey{
					Namespace: metav1.NamespaceDefault,
					Name:      "test-machine-pool",
				}, mp)

				return apierrors.IsNotFound(errKMP) && apierrors.IsNotFound(errMP)
			},
		},
		{
			description: "should delete cluster",
			kopsControlPlaneFunction: func(kopsControlPlane *controlplanev1alpha1.KopsControlPlane) *controlplanev1alpha1.KopsControlPlane {
				kopsControlPlane.Finalizers = []string{
					controlplanev1alpha1.KopsControlPlaneFinalizer,
				}
				kopsControlPlane.ObjectMeta.DeletionTimestamp = &metav1.Time{Time: time.Now()}
				return kopsControlPlane
			},
			kopsMachinePoolFunction: func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool) *infrastructurev1alpha1.KopsMachinePool {
				kopsMachinePool.SetOwnerReferences(
					[]metav1.OwnerReference{
						{
							Kind:       "MachinePool",
							APIVersion: "cluster.x-k8s.io/v1beta1",
							Name:       "test-machine-pool",
							UID:        "1",
						},
					},
				)
				controllerutil.AddFinalizer(kopsMachinePool, infrastructurev1alpha1.KopsMachinePoolFinalizer)
				return kopsMachinePool
			},
			assertFunction: func(kubeClient client.Client) bool {
				cluster := &clusterv1.Cluster{}
				errCL := kubeClient.Get(context.TODO(), client.ObjectKey{
					Namespace: metav1.NamespaceDefault,
					Name:      "test-cluster",
				}, cluster)
				if !apierrors.IsNotFound(errCL) {
					return false
				}

				kcp := &controlplanev1alpha1.KopsControlPlane{}
				errKCP := kubeClient.Get(context.TODO(), client.ObjectKey{
					Namespace: metav1.NamespaceDefault,
					Name:      "test-cluster",
				}, kcp)
				if !apierrors.IsNotFound(errKCP) {
					return false
				}

				kmp := &infrastructurev1alpha1.KopsMachinePool{}
				errKMP := kubeClient.Get(context.TODO(), client.ObjectKey{
					Namespace: metav1.NamespaceDefault,
					Name:      "test-kops-machine-pool",
				}, kmp)
				if errKMP != nil || len(kmp.ObjectMeta.Finalizers) > 0 {
					return false
				}

				mp := &expv1.MachinePool{}
				errMP := kubeClient.Get(context.TODO(), client.ObjectKey{
					Namespace: metav1.NamespaceDefault,
					Name:      "test-machine-pool",
				}, mp)
				return apierrors.IsNotFound(errMP)
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
	err = expv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = infrastructurev1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			kopsControlPlane := helpers.NewKopsControlPlane("test-cluster", metav1.NamespaceDefault)
			if tc.kopsControlPlaneFunction != nil {
				kopsControlPlane = tc.kopsControlPlaneFunction(kopsControlPlane)
			}
			cluster := helpers.NewCluster("test-cluster", helpers.GetFQDN(kopsControlPlane.Name), metav1.NamespaceDefault)
			machinePool := &expv1.MachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-machine-pool",
					Namespace: metav1.NamespaceDefault,
					UID:       "1",
				},
			}
			kopsMachinePool := helpers.NewKopsMachinePool("test-kops-machine-pool", kopsControlPlane.Namespace, cluster.Name)
			if tc.kopsMachinePoolFunction != nil {
				kopsMachinePool = tc.kopsMachinePoolFunction(kopsMachinePool)
			}
			kopsControlPlaneSecret := helpers.NewAWSCredentialSecret()

			fakeClient := fake.NewClientBuilder().WithObjects(kopsControlPlane, cluster, kopsControlPlaneSecret, kopsMachinePool, machinePool).WithStatusSubresource(kopsControlPlane, kopsMachinePool).WithScheme(scheme.Scheme).Build()
			fakeKopsClientset := helpers.NewFakeKopsClientset()

			kopsCluster := &kopsapi.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: helpers.GetFQDN("test-cluster"),
				},
				Spec: kopsControlPlane.Spec.KopsClusterSpec,
			}

			keyStore, err := fakeKopsClientset.KeyStore(kopsCluster)
			g.Expect(err).NotTo(HaveOccurred())

			err = helpers.CreateFakeKopsKeyPair(ctx, keyStore)
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
				PopulateClusterSpecFactory: func(ctx context.Context, kopsCluster *kopsapi.Cluster, kopsClientset simple.Clientset, cloud fi.Cloud) (*kopsapi.Cluster, error) {
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
				DestroyTerraformFactory: func(ctx context.Context, terraformDir, tfExecPath string, credentials aws.Credentials) error {
					return nil
				},
				KopsDeleteResourcesFactory: func(ctx context.Context, cloud fi.Cloud, kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster) error {
					return nil
				},
			}
			_, _ = reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: client.ObjectKey{
					Namespace: metav1.NamespaceDefault,
					Name:      helpers.GetFQDN("test-cluster"),
				},
			})
			g.Expect(tc.assertFunction(fakeClient)).To(BeTrue())

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
				"ClusterReconciliationFinished",
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

			err = helpers.CreateFakeKopsKeyPair(ctx, keyStore)
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
				PopulateClusterSpecFactory: func(ctx context.Context, kopsCluster *kopsapi.Cluster, kopsClientset simple.Clientset, cloud fi.Cloud) (*kopsapi.Cluster, error) {
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

			if result != resultNotRequeue {
				err = fakeClient.Get(ctx, types.NamespacedName{
					Namespace: kopsControlPlane.GetNamespace(),
					Name:      kopsControlPlane.GetName(),
				}, kopsControlPlane)
				Expect(err).NotTo(HaveOccurred())

				g.Expect(kopsControlPlane.Status.LastReconciled.Time).To(BeTemporally("~", time.Now(), 5*time.Second))
			}

			if tc.expectedStatus != nil {
				kcp := &controlplanev1alpha1.KopsControlPlane{}
				err = fakeClient.Get(ctx, client.ObjectKeyFromObject(kopsControlPlane), kcp)
				g.Expect(err).NotTo(HaveOccurred())
				kcp.Status.LastReconciled = nil
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

			reconciler := &KopsControlPlaneReconciliation{}
			err = reconciler.createOrUpdateInstanceGroup(ctx, fakeKopsClientset, kopsCluster, ig)
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
		kcp.Spec.KopsClusterSpec.Networking.Subnets = tc.input
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
		description              string
		kopsMachinePoolFunction  func(*infrastructurev1alpha1.KopsMachinePool) *infrastructurev1alpha1.KopsMachinePool
		karpenterResourcesOutput string
		manifestHash             string
		spotInstEnabled          bool
	}{
		{
			description: "Should generate files based on template with one Provisioner",
			kopsMachinePoolFunction: func(kmp *infrastructurev1alpha1.KopsMachinePool) *infrastructurev1alpha1.KopsMachinePool {
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
								"kops.k8s.io/cluster":             helpers.GetFQDN("test-cluster"),
								"kops.k8s.io/cluster-name":        helpers.GetFQDN("test-cluster"),
								"kops.k8s.io/instance-group-name": kmp.Name,
								"kops.k8s.io/instance-group-role": "Node",
								"kops.k8s.io/instancegroup":       kmp.Name,
								"kops.k8s.io/managed-by":          "kops-controller",
							},
							Provider: &v1alpha5.Provider{
								Raw: []byte("{\"launchTemplate\":\"" + kmp.Name + "." + helpers.GetFQDN("test-cluster") + "\",\"subnetSelector\":{\"kops.k8s.io/instance-group/" + kmp.Name + "\":\"*\",\"kubernetes.io/cluster/" + helpers.GetFQDN("test-cluster") + "\":\"*\"}}"),
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
				return kmp
			},
			karpenterResourcesOutput: "karpenter_resource_output_provisioner.yaml",
			manifestHash:             "d67c9504589dd859e46f1913780fb69bafb8df5328d90e6675affc79d3573f78",
		},
		{
			description: "Should generate files based on template with one NodePool",
			kopsMachinePoolFunction: func(kmp *infrastructurev1alpha1.KopsMachinePool) *infrastructurev1alpha1.KopsMachinePool {
				kmp.Spec.KopsInstanceGroupSpec.NodeLabels = map[string]string{
					"kops.k8s.io/instance-group-role": "Node",
				}
				kmp.Spec.KarpenterNodePools = []karpenterv1beta1.NodePool{
					{
						TypeMeta: metav1.TypeMeta{
							Kind:       "NodePool",
							APIVersion: "karpenter.sh/v1beta1",
						},
						ObjectMeta: metav1.ObjectMeta{
							Name: "test-node-pool",
						},
						Spec: karpenterv1beta1.NodePoolSpec{
							Disruption: karpenterv1beta1.Disruption{
								ConsolidationPolicy: karpenterv1beta1.ConsolidationPolicyWhenUnderutilized,
							},
							Template: karpenterv1beta1.NodeClaimTemplate{
								ObjectMeta: karpenterv1beta1.ObjectMeta{
									Labels: map[string]string{
										"kops.k8s.io/cluster":             helpers.GetFQDN("test-cluster"),
										"kops.k8s.io/cluster-name":        helpers.GetFQDN("test-cluster"),
										"kops.k8s.io/instance-group-name": kmp.Name,
										"kops.k8s.io/instance-group-role": "Node",
										"kops.k8s.io/instancegroup":       kmp.Name,
										"kops.k8s.io/managed-by":          "kops-controller",
									},
								},
								Spec: karpenterv1beta1.NodeClaimSpec{
									Kubelet: &karpenterv1beta1.KubeletConfiguration{
										KubeReserved: map[string]string{
											"cpu":               "150m",
											"memory":            "150Mi",
											"ephemeral-storage": "1Gi",
										},
										SystemReserved: map[string]string{
											"cpu":               "150m",
											"memory":            "200Mi",
											"ephemeral-storage": "1Gi",
										},
									},
									NodeClassRef: &karpenterv1beta1.NodeClassReference{
										Name: "test-ig",
									},
									Requirements: []karpenterv1beta1.NodeSelectorRequirementWithMinValues{
										{
											NodeSelectorRequirement: corev1.NodeSelectorRequirement{
												Key:      "kubernetes.io/arch",
												Operator: corev1.NodeSelectorOperator(corev1.NodeSelectorOpIn),
												Values:   []string{"amd64"},
											},
										},
										{

											NodeSelectorRequirement: corev1.NodeSelectorRequirement{
												Key:      "kubernetes.io/os",
												Operator: corev1.NodeSelectorOperator(corev1.NodeSelectorOpIn),
												Values:   []string{"linux"},
											},
										},
										{
											NodeSelectorRequirement: corev1.NodeSelectorRequirement{
												Key:      "node.kubernetes.io/instance-type",
												Operator: corev1.NodeSelectorOperator(corev1.NodeSelectorOpIn),
												Values:   []string{"m5.large"},
											},
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
						},
					},
				}
				return kmp
			},
			karpenterResourcesOutput: "karpenter_resource_output_node_pool.yaml",
			manifestHash:             "21e937a12016f39a20f07651d3ebe5e70b312a627379d2d05c154260dd0ab87c",
		},
		{
			description: "Should generate files based on template with one NodePool and one Provisioner",
			kopsMachinePoolFunction: func(kmp *infrastructurev1alpha1.KopsMachinePool) *infrastructurev1alpha1.KopsMachinePool {
				kmp.Spec.KopsInstanceGroupSpec.NodeLabels = map[string]string{
					"kops.k8s.io/instance-group-role": "Node",
				}
				kmp.Spec.KarpenterNodePools = []karpenterv1beta1.NodePool{
					{
						TypeMeta: metav1.TypeMeta{
							Kind:       "NodePool",
							APIVersion: "karpenter.sh/v1beta1",
						},
						ObjectMeta: metav1.ObjectMeta{
							Name: "test-node-pool",
						},
						Spec: karpenterv1beta1.NodePoolSpec{
							Disruption: karpenterv1beta1.Disruption{
								ConsolidationPolicy: karpenterv1beta1.ConsolidationPolicyWhenUnderutilized,
							},
							Template: karpenterv1beta1.NodeClaimTemplate{
								ObjectMeta: karpenterv1beta1.ObjectMeta{
									Labels: map[string]string{
										"kops.k8s.io/cluster":             helpers.GetFQDN("test-cluster"),
										"kops.k8s.io/cluster-name":        helpers.GetFQDN("test-cluster"),
										"kops.k8s.io/instance-group-name": kmp.Name,
										"kops.k8s.io/instance-group-role": "Node",
										"kops.k8s.io/instancegroup":       kmp.Name,
										"kops.k8s.io/managed-by":          "kops-controller",
									},
								},
								Spec: karpenterv1beta1.NodeClaimSpec{
									Kubelet: &karpenterv1beta1.KubeletConfiguration{
										KubeReserved: map[string]string{
											"cpu":               "150m",
											"memory":            "150Mi",
											"ephemeral-storage": "1Gi",
										},
										SystemReserved: map[string]string{
											"cpu":               "150m",
											"memory":            "200Mi",
											"ephemeral-storage": "1Gi",
										},
									},
									NodeClassRef: &karpenterv1beta1.NodeClassReference{
										Name: "test-ig",
									},
									Requirements: []karpenterv1beta1.NodeSelectorRequirementWithMinValues{
										{
											NodeSelectorRequirement: corev1.NodeSelectorRequirement{
												Key:      "kubernetes.io/arch",
												Operator: corev1.NodeSelectorOperator(corev1.NodeSelectorOpIn),
												Values:   []string{"amd64"},
											},
										},
										{

											NodeSelectorRequirement: corev1.NodeSelectorRequirement{
												Key:      "kubernetes.io/os",
												Operator: corev1.NodeSelectorOperator(corev1.NodeSelectorOpIn),
												Values:   []string{"linux"},
											},
										},
										{
											NodeSelectorRequirement: corev1.NodeSelectorRequirement{
												Key:      "node.kubernetes.io/instance-type",
												Operator: corev1.NodeSelectorOperator(corev1.NodeSelectorOpIn),
												Values:   []string{"m5.large"},
											},
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
						},
					},
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
								"kops.k8s.io/cluster":             helpers.GetFQDN("test-cluster"),
								"kops.k8s.io/cluster-name":        helpers.GetFQDN("test-cluster"),
								"kops.k8s.io/instance-group-name": kmp.Name,
								"kops.k8s.io/instance-group-role": "Node",
								"kops.k8s.io/instancegroup":       kmp.Name,
								"kops.k8s.io/managed-by":          "kops-controller",
							},
							Provider: &v1alpha5.Provider{
								Raw: []byte("{\"launchTemplate\":\"" + kmp.Name + "." + helpers.GetFQDN("test-cluster") + "\",\"subnetSelector\":{\"kops.k8s.io/instance-group/" + kmp.Name + "\":\"*\",\"kubernetes.io/cluster/" + helpers.GetFQDN("test-cluster") + "\":\"*\"}}"),
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
				return kmp
			},
			karpenterResourcesOutput: "karpenter_resource_output_node_pool_and_provisioner.yaml",
			manifestHash:             "38f8e21d8e1bedd7867e6300bfe2afada9c8908c06d05eecdc482d960dd4f338",
		},
		{
			description: "Should generate files based on with spotinst enabled",
			kopsMachinePoolFunction: func(kmp *infrastructurev1alpha1.KopsMachinePool) *infrastructurev1alpha1.KopsMachinePool {
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
								"kops.k8s.io/cluster":             helpers.GetFQDN("test-cluster"),
								"kops.k8s.io/cluster-name":        helpers.GetFQDN("test-cluster"),
								"kops.k8s.io/instance-group-name": kmp.Name,
								"kops.k8s.io/instance-group-role": "Node",
								"kops.k8s.io/instancegroup":       kmp.Name,
								"kops.k8s.io/managed-by":          "kops-controller",
							},
							Provider: &v1alpha5.Provider{
								Raw: []byte("{\"launchTemplate\":\"" + kmp.Name + "." + helpers.GetFQDN("test-cluster") + "\",\"subnetSelector\":{\"kops.k8s.io/instance-group/" + kmp.Name + "\":\"*\",\"kubernetes.io/cluster/" + helpers.GetFQDN("test-cluster") + "\":\"*\"}}"),
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
				return kmp
			},
			karpenterResourcesOutput: "karpenter_resource_output_provisioner.yaml",
			manifestHash:             "d67c9504589dd859e46f1913780fb69bafb8df5328d90e6675affc79d3573f78",
			spotInstEnabled:          true,
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
			kmp := helpers.NewKopsMachinePool("test-ig", metav1.NamespaceDefault, "test-cluster")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(kopsCluster).NotTo(BeNil())
			kcp := &controlplanev1alpha1.KopsControlPlane{}
			kcp.Spec.SpotInst.Enabled = tc.spotInstEnabled

			if tc.kopsMachinePoolFunction != nil {
				kmp = tc.kopsMachinePoolFunction(kmp)
			}

			if tc.spotInstEnabled {
				kcp.Spec.SpotInst.Enabled = true
				kmp.Spec.SpotInstOptions = map[string]string{
					"spotinst.io/hybrid": "true",
				}
			}

			terraformOutputDir := fmt.Sprintf("/tmp/%s", kopsCluster.Name)
			templateTestDir := "../../utils/templates/tests"

			err = os.WriteFile(terraformOutputDir+"/data/aws_launch_template_"+kmp.Name+"."+kopsCluster.Name+"_user_data", []byte("dummy content"), 0644)
			g.Expect(err).NotTo(HaveOccurred())

			reconciler := &KopsControlPlaneReconciler{}
			err = reconciler.PrepareCustomCloudResources(ctx, kopsCluster, kcp, []infrastructurev1alpha1.KopsMachinePool{*kmp}, true, kopsCluster.Spec.ConfigStore.Base, terraformOutputDir, true)
			g.Expect(err).NotTo(HaveOccurred())

			generatedBackendTF, err := os.ReadFile(terraformOutputDir + "/backend.tf")
			g.Expect(err).NotTo(HaveOccurred())
			templatedBackendTF, err := os.ReadFile(templateTestDir + "/backend.tf")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(string(generatedBackendTF)).To(BeEquivalentTo(string(templatedBackendTF)))

			generatedKarpenterBoostrapTF, err := os.ReadFile(terraformOutputDir + "/karpenter_custom_addon_boostrap.tf")
			g.Expect(err).NotTo(HaveOccurred())

			content, err := os.ReadFile(templateTestDir + "/karpenter_custom_addon_boostrap.tf")
			g.Expect(err).NotTo(HaveOccurred())

			templ, err := template.New(templateTestDir + "/karpenter_custom_addon_boostrap.tf").Parse(string(content))
			g.Expect(err).NotTo(HaveOccurred())

			var templatedKarpenterBoostrapTF bytes.Buffer
			data := struct {
				ManifestHash string
			}{
				ManifestHash: tc.manifestHash,
			}

			err = templ.Execute(&templatedKarpenterBoostrapTF, data)
			g.Expect(err).NotTo(HaveOccurred())

			g.Expect(string(generatedKarpenterBoostrapTF)).To(BeEquivalentTo(templatedKarpenterBoostrapTF.String()))

			generatedLaunchTemplateTF, err := os.ReadFile(terraformOutputDir + "/launch_template_override.tf")
			g.Expect(err).NotTo(HaveOccurred())
			templatedLaunchTemplateTF, err := os.ReadFile(templateTestDir + "/launch_template_override.tf")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(string(generatedLaunchTemplateTF)).To(BeEquivalentTo(string(templatedLaunchTemplateTF)))

			generatedKarpenterResources, err := os.ReadFile(terraformOutputDir + "/data/aws_s3_object_karpenter_resources_content")
			g.Expect(err).NotTo(HaveOccurred())
			templatedKarpenterResources, err := os.ReadFile(templateTestDir + "/data/" + tc.karpenterResourcesOutput)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(string(generatedKarpenterResources)).To(BeEquivalentTo(string(templatedKarpenterResources)))

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

func TestReconcileKopsMachinePool(t *testing.T) {
	g := NewWithT(t)
	var testCases = []struct {
		description    string
		instanceGroups []*kopsapi.InstanceGroup
		input          *infrastructurev1alpha1.KopsMachinePool
		assertFunction func(simple.Clientset, *kopsapi.Cluster) bool
	}{
		{
			description: "should create a new IG",
			input: &infrastructurev1alpha1.KopsMachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-kops-machine-pool-a",
					Namespace: metav1.NamespaceDefault,
				},
				Spec: infrastructurev1alpha1.KopsMachinePoolSpec{
					ClusterName: helpers.GetFQDN("test-cluster"),
					KopsInstanceGroupSpec: kopsapi.InstanceGroupSpec{
						Role: "Node",
					},
				},
			},
			assertFunction: func(fakeKopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster) bool {
				ig, err := fakeKopsClientset.InstanceGroupsFor(kopsCluster).Get(context.TODO(), "test-kops-machine-pool-a", metav1.GetOptions{})
				return err == nil && ig.Name == "test-kops-machine-pool-a"
			},
		},
		{
			description: "should update an already created IG",
			instanceGroups: []*kopsapi.InstanceGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-kops-machine-pool-a",
						Namespace: metav1.NamespaceDefault,
					},
					Spec: kopsapi.InstanceGroupSpec{
						Role:  "Node",
						Image: "image-a",
					},
				},
			},
			input: &infrastructurev1alpha1.KopsMachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-kops-machine-pool-a",
					Namespace: metav1.NamespaceDefault,
				},
				Spec: infrastructurev1alpha1.KopsMachinePoolSpec{
					ClusterName: helpers.GetFQDN("test-cluster"),
					KopsInstanceGroupSpec: kopsapi.InstanceGroupSpec{
						Role:  "Node",
						Image: "image-b",
					},
				},
			},
			assertFunction: func(fakeKopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster) bool {
				ig, err := fakeKopsClientset.InstanceGroupsFor(kopsCluster).Get(context.TODO(), "test-kops-machine-pool-a", metav1.GetOptions{})
				return err == nil && ig.Spec.Image == "image-b"
			},
		},
		{
			description: "should delete kmp marked for deletion",
			instanceGroups: []*kopsapi.InstanceGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-kops-machine-pool-a",
						Namespace: metav1.NamespaceDefault,
					},
					Spec: kopsapi.InstanceGroupSpec{
						Role:  "Node",
						Image: "image-a",
					},
				},
			},
			input: &infrastructurev1alpha1.KopsMachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-kops-machine-pool-a",
					Namespace: metav1.NamespaceDefault,
					DeletionTimestamp: &metav1.Time{
						Time: time.Now(),
					},
				},
				Spec: infrastructurev1alpha1.KopsMachinePoolSpec{
					ClusterName: helpers.GetFQDN("test-cluster"),
					KopsInstanceGroupSpec: kopsapi.InstanceGroupSpec{
						Role: "Node",
					},
				},
			},
			assertFunction: func(fakeKopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster) bool {
				_, err := fakeKopsClientset.InstanceGroupsFor(kopsCluster).Get(context.TODO(), "test-kops-machine-pool-a", metav1.GetOptions{})
				return err != nil && apierrors.IsNotFound(err)
			},
		},
		{
			description: "should do nothing for a ig already deleted",
			input: &infrastructurev1alpha1.KopsMachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-kops-machine-pool-a",
					Namespace: metav1.NamespaceDefault,
					DeletionTimestamp: &metav1.Time{
						Time: time.Now(),
					},
				},
				Spec: infrastructurev1alpha1.KopsMachinePoolSpec{
					ClusterName: helpers.GetFQDN("test-cluster"),
					KopsInstanceGroupSpec: kopsapi.InstanceGroupSpec{
						Role: "Node",
					},
				},
			},
			assertFunction: func(fakeKopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster) bool {
				_, err := fakeKopsClientset.InstanceGroupsFor(kopsCluster).Get(context.TODO(), "test-kops-machine-pool-a", metav1.GetOptions{})
				return err != nil && apierrors.IsNotFound(err)
			},
		},
	}

	RegisterFailHandler(Fail)

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			kopsControlPlane := &controlplanev1alpha1.KopsControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-kops-control-plane",
					Namespace: metav1.NamespaceDefault,
				},
			}
			kopsCluster := helpers.NewKopsCluster("test-cluster")
			fakeKopsClientset := helpers.NewFakeKopsClientset()
			_, err := fakeKopsClientset.CreateCluster(context.TODO(), kopsCluster)
			g.Expect(err).NotTo(HaveOccurred())
			for _, ig := range tc.instanceGroups {
				_, err := fakeKopsClientset.InstanceGroupsFor(kopsCluster).Create(context.TODO(), ig, metav1.CreateOptions{})
				g.Expect(err).NotTo(HaveOccurred())
			}

			reconciler := &KopsControlPlaneReconciler{
				Recorder: record.NewFakeRecorder(10),
			}
			reconciliation := &KopsControlPlaneReconciliation{
				KopsControlPlaneReconciler: *reconciler,
			}
			_ = reconciliation.reconcileKopsMachinePool(context.TODO(), fakeKopsClientset, kopsControlPlane, tc.input)
			g.Expect(tc.assertFunction(fakeKopsClientset, kopsCluster)).To(BeTrue())
		})
	}
}

func TestShouldDeleteCluster(t *testing.T) {
	testCases := []struct {
		description    string
		kcp            *controlplanev1alpha1.KopsControlPlane
		expectedOutput bool
	}{
		{
			description: "should return false when KopsControlPlane isn't marked for deletion",
			kcp: &controlplanev1alpha1.KopsControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-kops-control-plane",
					Namespace: metav1.NamespaceDefault,
				},
			},
		},
		{
			description: "should return false when KopsControlPlane is marked for deletion, but it still have the deletion protection enabled",
			kcp: &controlplanev1alpha1.KopsControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-kops-control-plane",
					Namespace: metav1.NamespaceDefault,
					DeletionTimestamp: &metav1.Time{
						Time: time.Now(),
					},
					Annotations: map[string]string{
						controlplanev1alpha1.ClusterDeleteProtectionAnnotation: "true",
					},
				},
			},
		},
		{
			description: "should return true when KopsControlPlane is marked for deletion and it doesn't have the deletion protection enabled",
			kcp: &controlplanev1alpha1.KopsControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-kops-control-plane",
					Namespace: metav1.NamespaceDefault,
					DeletionTimestamp: &metav1.Time{
						Time: time.Now(),
					},
				},
			},
			expectedOutput: true,
		},
	}
	g := NewWithT(t)
	RegisterFailHandler(Fail)

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {

			r := &KopsControlPlaneReconciler{
				Recorder: record.NewFakeRecorder(10),
			}
			reconciliation := &KopsControlPlaneReconciliation{
				KopsControlPlaneReconciler: *r,
			}
			result := reconciliation.shouldDeleteCluster(tc.kcp)
			g.Expect(result).To(BeEquivalentTo(tc.expectedOutput))
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
