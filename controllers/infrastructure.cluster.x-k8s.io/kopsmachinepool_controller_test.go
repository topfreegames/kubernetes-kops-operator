package infrastructureclusterxk8sio

import (
	"context"
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	controlplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	infrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	kopsapi "k8s.io/kops/pkg/apis/kops"
	"k8s.io/kops/pkg/client/simple"
	"k8s.io/kops/pkg/client/simple/vfsclientset"
	"k8s.io/kops/upup/pkg/fi"
	"k8s.io/kops/util/pkg/vfs"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetClusterByName(t *testing.T) {
	testCases := []map[string]interface{}{
		{
			"description":     "Should successfully return cluster",
			"expectedError":   false,
			"clusterFunction": nil,
		},
		{
			"description":   "Cluster don't exist, should return error",
			"expectedError": true,
			"clusterFunction": func(kopsCluster *clusterv1.Cluster) *clusterv1.Cluster {
				return &clusterv1.Cluster{
					ObjectMeta: metav1.ObjectMeta{
						Name: "another-test-cluster",
					},
				}
			},
		},
	}

	RegisterFailHandler(Fail)
	g := NewWithT(t)
	ctx := context.TODO()

	err := clusterv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	for _, tc := range testCases {
		t.Run(tc["description"].(string), func(t *testing.T) {
			cluster := newCluster("test-cluster", "", metav1.NamespaceDefault)
			if tc["clusterFunction"] != nil {
				clusterFunction := tc["clusterFunction"].(func(cluster *clusterv1.Cluster) *clusterv1.Cluster)
				cluster = clusterFunction(cluster)
			}
			fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(cluster).Build()
			reconciler := &KopsMachinePoolReconciler{
				Client: fakeClient,
			}
			cluster, err := reconciler.getClusterByName(ctx, metav1.NamespaceDefault, "test-cluster.k8s.cluster")
			if !tc["expectedError"].(bool) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(cluster).NotTo(BeNil())
			} else {
				g.Expect(err).To(HaveOccurred())
			}

		})
	}
}

func TestIsInstanceGroupCreated(t *testing.T) {

	testCases := []map[string]interface{}{
		{
			"description":    "Should return true for IG created",
			"expectedError":  false,
			"kopsIGFunction": nil,
		},
		{
			"description":   "Should return false for IG not created",
			"expectedError": true,
			"kopsIGFunction": func(kopsIG *kopsapi.InstanceGroup) *kopsapi.InstanceGroup {
				return &kopsapi.InstanceGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name: "another-test-kopsig",
					},
					Spec: kopsapi.InstanceGroupSpec{
						Role: "Node",
					},
				}
			},
		},
	}
	RegisterFailHandler(Fail)
	g := NewWithT(t)
	ctx := context.TODO()
	for _, tc := range testCases {
		t.Run(tc["description"].(string), func(t *testing.T) {
			fakeKopsClientset := newFakeKopsClientset()

			kopsCluster := newKopsCluster("test-kopscluster")

			ig := newKopsIG("test-kopsig", kopsCluster.GetObjectMeta().GetName())
			if tc["kopsIGFunction"] != nil {
				kopsIGFunction := tc["kopsIGFunction"].(func(kopsIG *kopsapi.InstanceGroup) *kopsapi.InstanceGroup)
				ig = kopsIGFunction(ig)
			}
			cluster, err := fakeKopsClientset.CreateCluster(ctx, kopsCluster)
			g.Expect(cluster).NotTo(BeNil())
			g.Expect(err).NotTo(HaveOccurred())

			ig, err = fakeKopsClientset.InstanceGroupsFor(kopsCluster).Create(ctx, ig, metav1.CreateOptions{})
			g.Expect(ig).NotTo(BeNil())
			g.Expect(err).NotTo(HaveOccurred())

			reconciler := &KopsMachinePoolReconciler{
				kopsClientset: fakeKopsClientset,
			}
			isIGCreated := reconciler.isInstanceGroupCreated(ctx, kopsCluster, "test-kopsig")
			if !tc["expectedError"].(bool) {
				g.Expect(isIGCreated).To(BeTrue())
			} else {
				g.Expect(isIGCreated).To(BeFalse())
			}

		})
	}
}

func TestUpdateInstanceGroup(t *testing.T) {

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

			reconciler := &KopsMachinePoolReconciler{
				kopsClientset: fakeKopsClientset,
				log:           ctrl.LoggerFrom(ctx),
			}
			err = reconciler.updateInstanceGroup(ctx, kopsCluster, ig)
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

func TestKopsMachinePoolReconciler(t *testing.T) {
	testCases := []map[string]interface{}{
		{
			"description":             "Should trigger a reconcile with KopsMachinePool object",
			"kopsMachinePoolFunction": nil,
			"expectedError":           false,
		},
		{
			"description": "Should fail if kopsMachinePool don't have clusterName defined",
			"kopsMachinePoolFunction": func(kmp *infrastructurev1alpha1.KopsMachinePool) *infrastructurev1alpha1.KopsMachinePool {
				kmp.Spec.ClusterName = ""
				return kmp
			},
			"expectedError": true,
		},
	}
	RegisterFailHandler(Fail)
	g := NewWithT(t)
	ctx := context.TODO()
	err := clusterv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = infrastructurev1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = controlplanev1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	for _, tc := range testCases {
		t.Run(tc["description"].(string), func(t *testing.T) {
			vfs.Context.ResetMemfsContext(true)
			cluster := newCluster("test-cluster", "test-kops-control-plane", "default")
			kopsMachinePool := newKopsMachinePool("test-kops-machine-pool", "test-cluster", "default")
			if tc["kopsMachinePoolFunction"] != nil {
				kopsMachinePoolFunction := tc["kopsMachinePoolFunction"].(func(kops *infrastructurev1alpha1.KopsMachinePool) *infrastructurev1alpha1.KopsMachinePool)
				kopsMachinePool = kopsMachinePoolFunction(kopsMachinePool)
			}
			kopsControlPlane := newKopsControlPlane("test-kops-control-plane", "default")
			fakeClient := fake.NewClientBuilder().WithObjects(kopsMachinePool, kopsControlPlane, cluster).WithScheme(scheme.Scheme).Build()

			reconciler := KopsMachinePoolReconciler{
				Client: fakeClient,
			}

			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: client.ObjectKey{
					Namespace: "default",
					Name:      "test-kops-machine-pool",
				},
			})
			if !tc["expectedError"].(bool) {
				g.Expect(result).ToNot(BeNil())
				g.Expect(err).To(BeNil())
			} else {
				g.Expect(err).NotTo(BeNil())
			}

		})
	}

}

func newFakeKopsClientset() simple.Clientset {
	memFSContext := vfs.NewMemFSContext()
	memfspath := vfs.NewMemFSPath(memFSContext, "memfs://tests")

	return vfsclientset.NewVFSClientset(memfspath)
}

func newCluster(name, controlplane, namespace string) *clusterv1.Cluster {
	return &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      fmt.Sprintf("%s.k8s.cluster", name),
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

func newKopsControlPlane(name, namespace string) *controlplanev1alpha1.KopsControlPlane {
	return &controlplanev1alpha1.KopsControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Spec: controlplanev1alpha1.KopsControlPlaneSpec{
			KopsClusterSpec: kopsapi.ClusterSpec{
				ConfigBase: fmt.Sprintf("memfs://tests/%s.test.k8s.cluster", name),
			},
		},
	}
}

func newKopsCluster(name string) *kopsapi.Cluster {
	return &kopsapi.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s.k8s.cluster", name),
		},
		Spec: kopsapi.ClusterSpec{
			KubernetesVersion: "1.20.1",
			CloudProvider:     "aws",
			NonMasqueradeCIDR: "10.0.1.0/21",
			NetworkCIDR:       "10.0.1.0/21",
			Subnets: []kopsapi.ClusterSubnetSpec{
				{
					Name: "test-subnet",
					CIDR: "10.0.1.0/24",
					Type: kopsapi.SubnetTypePrivate,
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

func newKopsIG(name, clusterName string) *kopsapi.InstanceGroup {
	return &kopsapi.InstanceGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: kopsapi.InstanceGroupSpec{
			Role: "Node",
		},
	}
}

func newKopsMachinePool(name, clusterName, namespace string) *infrastructurev1alpha1.KopsMachinePool {
	return &infrastructurev1alpha1.KopsMachinePool{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Spec: infrastructurev1alpha1.KopsMachinePoolSpec{
			ClusterName: fmt.Sprintf("%s.k8s.cluster", clusterName),
			KopsInstanceGroupSpec: kopsapi.InstanceGroupSpec{
				Role: "Node",
			},
		},
	}
}
