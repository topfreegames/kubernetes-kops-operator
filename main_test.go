package main

import (
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	controlplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
)

func TestDryRunControlPlanes(t *testing.T) {
	kcp := func(name, controllerClass string) controlplanev1alpha1.KopsControlPlane {
		return controlplanev1alpha1.KopsControlPlane{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       controlplanev1alpha1.KopsControlPlaneSpec{ControllerClass: controllerClass},
		}
	}
	kcps := []controlplanev1alpha1.KopsControlPlane{
		kcp("a", "stable"),
		kcp("b", "canary"),
		kcp("c", "stable"),
	}

	testCases := []struct {
		description     string
		controllerClass string
		cluster         string
		expected        []string
	}{
		{
			description:     "should select every Kops Control Plane of the controller class",
			controllerClass: "stable",
			expected:        []string{"a", "c"},
		},
		{
			description:     "should select only the given cluster",
			controllerClass: "stable",
			cluster:         "c",
			expected:        []string{"c"},
		},
		{
			description:     "should select nothing when the cluster has another controller class",
			controllerClass: "stable",
			cluster:         "b",
		},
		{
			description:     "should select nothing when the cluster does not exist",
			controllerClass: "stable",
			cluster:         "d",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			g := NewWithT(t)
			var names []string
			for _, kcp := range dryRunControlPlanes(kcps, tc.controllerClass, tc.cluster) {
				names = append(names, kcp.Name)
			}
			g.Expect(names).To(Equal(tc.expected))
		})
	}
}
