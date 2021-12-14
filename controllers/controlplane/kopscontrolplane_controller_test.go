package controlplane

import (
	"context"
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	controlplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	kopsapi "k8s.io/kops/pkg/apis/kops"
	"k8s.io/kops/pkg/client/simple"
	"k8s.io/kops/pkg/client/simple/vfsclientset"
	"k8s.io/kops/upup/pkg/fi"
	"k8s.io/kops/util/pkg/vfs"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestAddSSHCredential(t *testing.T) {

	testCases := []map[string]interface{}{
		{
			"description":         "Should successfully create SSH credential",
			"kopsClusterFunction": nil,
			"expectedError":       false,
		},
		{
			"description": "Should fail without proper configBase in cluster",
			"kopsClusterFunction": func(kopsCluster *kopsapi.Cluster) *kopsapi.Cluster {
				kopsCluster.Spec.ConfigBase = ""
				return kopsCluster
			},
			"expectedError": true,
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

			reconciler := &KopsControlPlaneReconciler{
				kopsClientset: fakeKopsClientset,
				log:           ctrl.LoggerFrom(ctx),
			}

			err = reconciler.addSSHCredential(kopsCluster)
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
				log:           ctrl.LoggerFrom(ctx),
				kopsClientset: fakeKopsClientset,
				GetClusterStatus: func(kopsCluster *kopsapi.Cluster) (*kopsapi.ClusterStatus, error) {
					return &kopsapi.ClusterStatus{}, nil
				},
			}

			err := reconciler.updateKopsState(ctx, bareKopsCluster)
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
			kopsControlPlane := newKopsControlPlane("test-kops-control-plane", "default")
			if tc["kopsControlPlaneFunction"] != nil {
				kopsControlPlaneFunction := tc["kopsControlPlaneFunction"].(func(kopsControlPlane *controlplanev1alpha1.KopsControlPlane) *controlplanev1alpha1.KopsControlPlane)
				kopsControlPlane = kopsControlPlaneFunction(kopsControlPlane)
			}

			fakeClient := fake.NewClientBuilder().WithObjects(kopsControlPlane).WithScheme(scheme.Scheme).Build()

			reconciler := &KopsControlPlaneReconciler{
				log:    ctrl.LoggerFrom(ctx),
				Client: fakeClient,
				PopulateClusterSpec: func(cluster *kopsapi.Cluster, kopsClientset simple.Clientset) (*kopsapi.Cluster, error) {
					return cluster, nil
				},
				CreateCloudResources: func(kopsClientset simple.Clientset, ctx context.Context, kopsCluster *kopsapi.Cluster, configBase string) error {
					return nil
				},
			}
			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: client.ObjectKey{
					Namespace: "default",
					Name:      "test-kops-control-plane",
				},
			})
			if !tc["expectedError"].(bool) {
				g.Expect(result).ToNot(BeNil())
				g.Expect(err).NotTo(HaveOccurred())
			} else {
				g.Expect(err).To(HaveOccurred())
			}
		})
	}
}

func newFakeKopsClientset() simple.Clientset {
	memFSContext := vfs.NewMemFSContext()
	memfspath := vfs.NewMemFSPath(memFSContext, "memfs://tests")

	return vfsclientset.NewVFSClientset(memfspath)
}

func newKopsCluster(name string) *kopsapi.Cluster {
	return &kopsapi.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s.test.k8s.cluster", name),
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
			Name:      name,
			Labels: map[string]string{
				"kops.k8s.io/cluster": fmt.Sprintf("%s.test.k8s.cluster", name),
			},
		},
		Spec: controlplanev1alpha1.KopsControlPlaneSpec{
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
