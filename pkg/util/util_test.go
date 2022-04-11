package util

import (
	"context"
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubectl/pkg/scheme"
	clusterv1betav1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

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
