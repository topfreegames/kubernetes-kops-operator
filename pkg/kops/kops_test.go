package kops

import (
	"context"
	"net"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	crossec2v1beta1 "github.com/crossplane-contrib/provider-aws/apis/ec2/v1beta1"
	"k8s.io/client-go/kubernetes/scheme"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	kcontrolplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	kinfrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kopsapi "k8s.io/kops/pkg/apis/kops"
)

func TestGetSubnetFromKopsControlPlane(t *testing.T) {
	testCases := []map[string]interface{}{
		{
			"description": "should succeed getting subnet from KCP",
			"input": &kcontrolplanev1alpha1.KopsControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: metav1.NamespaceDefault,
					Name:      "test-kops-control-plane",
				},
				Spec: kcontrolplanev1alpha1.KopsControlPlaneSpec{
					KopsClusterSpec: kopsapi.ClusterSpec{
						Networking: kopsapi.NetworkingSpec{
							Subnets: []kopsapi.ClusterSubnetSpec{
								{
									Name: "test-subnet",
									CIDR: "0.0.0.0/26",
									Zone: "us-east-1d",
								},
							},
						},
					},
				},
			},
			"isErrorExpected": false,
		},
		{
			"description": "should fail when missing subnets",
			"input": &kcontrolplanev1alpha1.KopsControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: metav1.NamespaceDefault,
					Name:      "test-kops-control-plane",
				},
				Spec: kcontrolplanev1alpha1.KopsControlPlaneSpec{
					KopsClusterSpec: kopsapi.ClusterSpec{},
				},
			},
			"isErrorExpected": true,
		},
	}
	RegisterFailHandler(Fail)
	g := NewWithT(t)
	for _, tc := range testCases {
		t.Run(tc["description"].(string), func(t *testing.T) {
			subnet, err := GetSubnetFromKopsControlPlane(tc["input"].(*kcontrolplanev1alpha1.KopsControlPlane))
			if !tc["isErrorExpected"].(bool) {
				g.Expect(err).To(BeNil())
				g.Expect(subnet).ToNot(BeNil())
			} else {
				g.Expect(err).ToNot(BeNil())
			}
		})
	}
}

func TestGetRegionFromKopsSubnet(t *testing.T) {
	testCases := []map[string]interface{}{
		{
			"description": "should succeed using subnet with zone",
			"input": kopsapi.ClusterSubnetSpec{
				Zone: "us-east-1d",
			},
			"expected":        "us-east-1",
			"isErrorExpected": false,
		},
		{
			"description": "should succeed using subnet with region",
			"input": kopsapi.ClusterSubnetSpec{
				Region: "us-east-1",
			},
			"expected":        "us-east-1",
			"isErrorExpected": false,
		},
		{
			"description":     "should fail using subnet empty",
			"input":           kopsapi.ClusterSubnetSpec{},
			"isErrorExpected": true,
		},
	}
	RegisterFailHandler(Fail)
	g := NewWithT(t)
	for _, tc := range testCases {
		t.Run(tc["description"].(string), func(t *testing.T) {
			region, err := GetRegionFromKopsSubnet(tc["input"].(kopsapi.ClusterSubnetSpec))
			if !tc["isErrorExpected"].(bool) {
				g.Expect(region).ToNot(BeNil())
				g.Expect(err).To(BeNil())
				g.Expect(*region).To(Equal(tc["expected"].(string)))
			} else {
				g.Expect(err).ToNot(BeNil())
			}
		})
	}
}

func TestGetCloudResourceNameFromKopsMachinePool(t *testing.T) {
	testCases := []map[string]interface{}{
		{
			"description": "should return the correct node asgName",
			"input": kinfrastructurev1alpha1.KopsMachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name: "nodes-a",
				},
				Spec: kinfrastructurev1alpha1.KopsMachinePoolSpec{
					ClusterName: "test-cluster",
					KopsInstanceGroupSpec: kopsapi.InstanceGroupSpec{
						NodeLabels: map[string]string{
							"kops.k8s.io/instance-group-role": "Node",
						},
					},
				},
			},
			"expected":        "nodes-a.test-cluster",
			"isErrorExpected": false,
		},
		{
			"description": "should return the correct master asgName",
			"input": kinfrastructurev1alpha1.KopsMachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name: "master-1a",
				},
				Spec: kinfrastructurev1alpha1.KopsMachinePoolSpec{
					ClusterName: "test-cluster",
					KopsInstanceGroupSpec: kopsapi.InstanceGroupSpec{
						NodeLabels: map[string]string{
							"kops.k8s.io/instance-group-role": "Master",
						},
					},
				},
			},
			"expected":        "master-1a.masters.test-cluster",
			"isErrorExpected": false,
		},
		{
			"description": "should fail when missing clusterName",
			"input": kinfrastructurev1alpha1.KopsMachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name: "nodes-a",
				},
				Spec: kinfrastructurev1alpha1.KopsMachinePoolSpec{
					KopsInstanceGroupSpec: kopsapi.InstanceGroupSpec{
						NodeLabels: map[string]string{
							"kops.k8s.io/instance-group-role": "Node",
						},
					},
				},
			},
			"isErrorExpected":      true,
			"expectedErrorMessage": "failed to retrieve clusterName",
		},
		{
			"description": "should fail when missing role",
			"input": kinfrastructurev1alpha1.KopsMachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name: "nodes-a",
				},
				Spec: kinfrastructurev1alpha1.KopsMachinePoolSpec{
					ClusterName: "test-cluster",
					KopsInstanceGroupSpec: kopsapi.InstanceGroupSpec{
						NodeLabels: map[string]string{},
					},
				},
			},
			"isErrorExpected":      true,
			"expectedErrorMessage": "failed to retrieve role from",
		},
	}
	RegisterFailHandler(Fail)
	g := NewWithT(t)

	for _, tc := range testCases {
		t.Run(tc["description"].(string), func(t *testing.T) {
			asgName, err := GetCloudResourceNameFromKopsMachinePool(tc["input"].(kinfrastructurev1alpha1.KopsMachinePool))
			if !tc["isErrorExpected"].(bool) {
				g.Expect(asgName).ToNot(BeNil())
				g.Expect(err).To(BeNil())
				g.Expect(asgName).To(Equal(tc["expected"].(string)))
			} else {
				g.Expect(err).ToNot(BeNil())
				g.Expect(err.Error()).To(ContainSubstring(tc["expectedErrorMessage"].(string)))
			}
		})
	}
}

func TestGetKopsMachinePoolsWithLabel(t *testing.T) {
	kmp := kinfrastructurev1alpha1.KopsMachinePool{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: metav1.NamespaceDefault,
			Name:      "test-kops-machine-pool",
			Labels: map[string]string{
				"cluster.x-k8s.io/cluster-name": "test-cluster",
			},
		},
		Spec: kinfrastructurev1alpha1.KopsMachinePoolSpec{
			ClusterName: "test-cluster",
		},
	}

	defaultRegisterFn := func(sc *runtime.Scheme, clientBuilder *fake.ClientBuilder) *fake.ClientBuilder {
		err := clusterv1beta1.AddToScheme(sc)
		Expect(err).NotTo(HaveOccurred())

		err = crossec2v1beta1.SchemeBuilder.AddToScheme(sc)
		Expect(err).NotTo(HaveOccurred())

		err = kinfrastructurev1alpha1.AddToScheme(sc)
		Expect(err).NotTo(HaveOccurred())

		err = kcontrolplanev1alpha1.AddToScheme(sc)
		Expect(err).NotTo(HaveOccurred())

		clientBuilder.WithScheme(sc)
		return clientBuilder
	}

	type testCase struct {
		description     string
		k8sObjects      []client.Object
		input           []string
		expected        []kinfrastructurev1alpha1.KopsMachinePool
		isErrorExpected bool
		expectedError   error
		registerFn      func(sc *runtime.Scheme, clientBuilder *fake.ClientBuilder) *fake.ClientBuilder
	}

	testCases := []testCase{
		{
			description: "should return the correct machinepool set",
			k8sObjects: []client.Object{
				&kmp,
				&kinfrastructurev1alpha1.KopsMachinePool{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: metav1.NamespaceDefault,
						Name:      "test-kops-machine-pool-b",
						Labels: map[string]string{
							"cluster.x-k8s.io/cluster-name": "test-cluster-b",
						},
					},
					Spec: kinfrastructurev1alpha1.KopsMachinePoolSpec{
						ClusterName: "test-cluster-b",
					},
				}},
			input:           []string{"cluster.x-k8s.io/cluster-name", "test-cluster"},
			expected:        []kinfrastructurev1alpha1.KopsMachinePool{kmp},
			isErrorExpected: false,
			registerFn:      defaultRegisterFn,
		},
		{
			description:     "should fail when missing label value",
			input:           []string{"cluster.x-k8s.io/cluster-name", ""},
			expected:        []kinfrastructurev1alpha1.KopsMachinePool{kmp},
			isErrorExpected: true,
			expectedError:   ErrLabelValueEmpty,
			registerFn:      defaultRegisterFn,
		},
		{
			description:     "should fail when missing label key",
			input:           []string{"", "cluster-test"},
			expected:        []kinfrastructurev1alpha1.KopsMachinePool{kmp},
			isErrorExpected: true,
			expectedError:   ErrLabelKeyEmpty,
			registerFn:      defaultRegisterFn,
		},
		{
			description:     "should fail when some error on retrieving machinepools",
			input:           []string{"cluster.x-k8s.io/cluster-name", "some-random-name"},
			expected:        []kinfrastructurev1alpha1.KopsMachinePool{},
			isErrorExpected: true,
			registerFn: func(sc *runtime.Scheme, clientBuilder *fake.ClientBuilder) *fake.ClientBuilder {
				clientBuilder.WithScheme(runtime.NewScheme()) // reset scheme
				return clientBuilder
			},
		},
	}
	RegisterFailHandler(Fail)
	g := NewWithT(t)

	for _, tc := range testCases {

		clientBuilder := fake.NewClientBuilder().WithObjects(tc.k8sObjects...)
		fakeClient := tc.registerFn(scheme.Scheme, clientBuilder).Build()
		ctx := context.TODO()
		input := tc.input

		t.Run(tc.description, func(t *testing.T) {
			kmps, err := GetKopsMachinePoolsWithLabel(ctx, fakeClient, input[0], input[1])
			if !tc.isErrorExpected { // no error expected
				g.Expect(err).To(BeNil())
				g.Expect(len(tc.expected)).To(Equal(len(kmps)))
			} else {
				g.Expect(err).ToNot(BeNil())
				g.Expect(len(kmps)).To(Equal(0))
				if tc.expectedError != nil {
					g.Expect(err).To(Equal(tc.expectedError))
				}
			}
		})
	}
}

func TestCalculateServiceClusterIPRange(t *testing.T) {
	type testCase struct {
		description    string
		input          string
		expectedOutput string
		expectedError  error
	}
	testCases := []testCase{
		{
			description:    "should return 1/8th of the input range",
			input:          "10.1.0.0/18",
			expectedOutput: "10.1.0.0/21",
		},
		{
			description:    "should return 1/8th of the input range",
			input:          "10.1.0.0/24",
			expectedOutput: "10.1.0.0/27",
		},
		{
			description:    "should return /20 as it's the maximum allowed range",
			input:          "10.1.0.0/16",
			expectedOutput: "10.1.0.0/20",
		},
		{
			description:    "should return /20 as it's the maximum allowed range",
			input:          "10.0.0.0/8",
			expectedOutput: "10.0.0.0/20",
		},
		{
			description:    "should return error with invalid network CIDR",
			input:          "10.x.0.0/16",
			expectedOutput: "10.1.0.0/20",
			expectedError:  &net.ParseError{Type: "CIDR address", Text: "10.x.0.0/16"},
		},
	}
	RegisterFailHandler(Fail)
	g := NewWithT(t)

	for _, tc := range testCases {

		t.Run(tc.description, func(t *testing.T) {
			serviceClusterIPRange, err := CalculateServiceClusterIPRange(tc.input)
			if tc.expectedError == nil {
				g.Expect(err).To(BeNil())
				g.Expect(serviceClusterIPRange).To(BeEquivalentTo(tc.expectedOutput))
			} else {
				g.Expect(err).To(BeEquivalentTo(tc.expectedError))
			}
		})
	}
}
