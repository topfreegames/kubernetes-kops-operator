package infrastructureclusterxk8sio

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/pkg/errors"
	controlplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	infrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
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

func TestGetInstanceGroupName(t *testing.T) {
	testCases := []map[string]interface{}{
		{
			"description": "Should successfully return IG name",
			"input": &infrastructurev1alpha1.KopsMachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name: "<cluster-name>-<ig-name>",
				},
				Spec: infrastructurev1alpha1.KopsMachinePoolSpec{
					ClusterName: "<cluster-name>",
				},
			},
			"expectedError": false,
			"expected":      "<ig-name>",
		},
		{
			"description": "Should fail with an unexpected input",
			"input": &infrastructurev1alpha1.KopsMachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name: "<ig-name>",
				},
				Spec: infrastructurev1alpha1.KopsMachinePoolSpec{
					ClusterName: "<cluster-name>",
				},
			},
			"expectedError": true,
		},
		{
			"description": "Should fail if the kopsMachinePool name has the same length as the clusterName",
			"input": &infrastructurev1alpha1.KopsMachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name: "<cluster-name>",
				},
				Spec: infrastructurev1alpha1.KopsMachinePoolSpec{
					ClusterName: "<cluster-name>",
				},
			},
			"expectedError": true,
		},
	}

	RegisterFailHandler(Fail)
	g := NewWithT(t)

	for _, tc := range testCases {
		t.Run(tc["description"].(string), func(t *testing.T) {
			igName, err := getInstanceGroupNameFromKopsMachinePool(tc["input"].(*infrastructurev1alpha1.KopsMachinePool))
			if !tc["expectedError"].(bool) {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(igName).To(Equal(tc["expected"].(string)))
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

func TestKopsMachinePoolReconciler(t *testing.T) {
	testCases := []map[string]interface{}{
		{
			"description":             "Should trigger a reconcile with KopsMachinePool object",
			"kopsMachinePoolFunction": nil,
			"expectedError":           false,
			"asgNotFound":             false,
		},
		{
			"description": "Should fail if kopsMachinePool don't have clusterName defined",
			"kopsMachinePoolFunction": func(kmp *infrastructurev1alpha1.KopsMachinePool, _ *session.Session) *infrastructurev1alpha1.KopsMachinePool {
				kmp.Spec.ClusterName = ""
				return kmp
			},
			"expectedError": true,
			"asgNotFound":   false,
		},
		{
			"description":             "Should requeue if couldn't retrieve ASG",
			"kopsMachinePoolFunction": nil,
			"getASGByTagFunction": func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, _ *session.Session) (*autoscaling.Group, error) {
				return nil, apierrors.NewNotFound(schema.GroupResource{}, "ASG not ready")
			},
			"expectedError": false,
			"asgNotFound":   true,
		},
		{
			"description":             "Should failt if couldn't retrieve ASG and it's not a NotFound error",
			"kopsMachinePoolFunction": nil,
			"getASGByTagFunction": func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, _ *session.Session) (*autoscaling.Group, error) {
				return nil, errors.New("error")
			},
			"expectedError": true,
			"asgNotFound":   false,
		},
		{
			"description":             "Should have sucessfull populate the providerID",
			"kopsMachinePoolFunction": nil,
			"expectedError":           false,
			"asgNotFound":             false,
			"providerIDList": []string{
				"aws:///us-east-1/<teste>",
			},
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
				kopsMachinePoolFunction := tc["kopsMachinePoolFunction"].(func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, _ *session.Session) *infrastructurev1alpha1.KopsMachinePool)
				kopsMachinePool = kopsMachinePoolFunction(kopsMachinePool, nil)
			}
			kopsControlPlane := newKopsControlPlane("test-kops-control-plane", "default")
			fakeClient := fake.NewClientBuilder().WithObjects(kopsMachinePool, kopsControlPlane, cluster).WithScheme(scheme.Scheme).Build()

			var getASGByTag func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, _ *session.Session) (*autoscaling.Group, error)
			if _, ok := tc["getASGByTagFunction"]; ok {
				getASGByTag = tc["getASGByTagFunction"].(func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, _ *session.Session) (*autoscaling.Group, error))
			} else {
				getASGByTag = func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, _ *session.Session) (*autoscaling.Group, error) {
					return &autoscaling.Group{
						Instances: []*autoscaling.Instance{
							{
								AvailabilityZone: aws.String("us-east-1"),
								InstanceId:       aws.String("<teste>"),
							},
						},
					}, nil
				}
			}

			reconciler := KopsMachinePoolReconciler{
				Client:   fakeClient,
				Recorder: record.NewFakeRecorder(5),
				ValidateKopsClusterFactory: func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error) {
					return &validation.ValidationCluster{}, nil
				},
				GetASGByTagFactory: getASGByTag,
			}

			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: client.ObjectKey{
					Namespace: "default",
					Name:      "test-cluster.k8s.cluster-test-kops-machine-pool",
				},
			})
			if !tc["expectedError"].(bool) {
				g.Expect(result).ToNot(BeNil())
				g.Expect(err).To(BeNil())
			} else if tc["asgNotFound"].(bool) {
				g.Expect(err).To(BeNil())
				g.Expect(result.RequeueAfter).To(Equal(1 * time.Minute))
			} else {
				g.Expect(err).NotTo(BeNil())
			}
			if _, ok := tc["providerIDList"]; ok {
				kmp := &infrastructurev1alpha1.KopsMachinePool{}
				err = fakeClient.Get(ctx, client.ObjectKeyFromObject(kopsMachinePool), kmp)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(kmp.Spec.ProviderIDList).To(Equal(tc["providerIDList"].([]string)))
			}
		})
	}
}

func TestKopsMachinePoolReconcilerSpotinst(t *testing.T) {
	testCases := []struct {
		description string
		spotOptions map[string]string
		kopsIG      *kopsapi.InstanceGroup
	}{
		{
			description: "should add spotinst metadata labels to Kops Instance Group",
			spotOptions: map[string]string{
				"spotinst.io/hybrid":                               "true",
				"spotinst.io/autoscaler-headroom-cpu-per-unit":     "560",
				"spotinst.io/autoscaler-headroom-mem-per-unit":     "601",
				"spotinst.io/autoscaler-headroom-num-of-units":     "20",
				"spotinst.io/autoscaler-scale-down-max-percentage": "100",
				"spotinst.io/utilize-reserved-instances":           "false",
				"spotinst.io/ocean-instance-types":                 "c3.4xlarge",
			},
		},
		{
			description: "should remove some spotinst metadata labels to Kops Instance Group",
			spotOptions: map[string]string{
				"spotinst.io/hybrid":                           "true",
				"spotinst.io/autoscaler-headroom-cpu-per-unit": "560",
				"spotinst.io/autoscaler-headroom-mem-per-unit": "601",
				"spotinst.io/autoscaler-headroom-num-of-units": "20",
			},
			kopsIG: &kopsapi.InstanceGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-kops-machine-pool",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						"spotinst.io/hybrid":                               "true",
						"spotinst.io/autoscaler-headroom-cpu-per-unit":     "560",
						"spotinst.io/autoscaler-headroom-mem-per-unit":     "601",
						"spotinst.io/autoscaler-headroom-num-of-units":     "20",
						"spotinst.io/autoscaler-scale-down-max-percentage": "100",
						"spotinst.io/utilize-reserved-instances":           "false",
						"spotinst.io/ocean-instance-types":                 "c3.4xlarge",
						"kops.k8s.io/cluster":                              "test-cluster.k8s.cluster",
					},
				},
				Spec: kopsapi.InstanceGroupSpec{
					Role: "Node",
				},
			},
		},
		{
			description: "should add some spotinst metadata labels to Kops Instance Group",
			spotOptions: map[string]string{
				"spotinst.io/hybrid":                               "true",
				"spotinst.io/autoscaler-headroom-cpu-per-unit":     "560",
				"spotinst.io/autoscaler-headroom-mem-per-unit":     "601",
				"spotinst.io/autoscaler-headroom-num-of-units":     "20",
				"spotinst.io/autoscaler-scale-down-max-percentage": "100",
				"spotinst.io/utilize-reserved-instances":           "false",
				"spotinst.io/ocean-instance-types":                 "c3.4xlarge",
				"kops.k8s.io/cluster":                              "test-cluster.k8s.cluster",
			},
			kopsIG: &kopsapi.InstanceGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-kops-machine-pool",
					Namespace: metav1.NamespaceDefault,
					Labels: map[string]string{
						"spotinst.io/hybrid":                           "true",
						"spotinst.io/autoscaler-headroom-cpu-per-unit": "560",
						"spotinst.io/autoscaler-headroom-mem-per-unit": "601",
						"spotinst.io/autoscaler-headroom-num-of-units": "20",
						"kops.k8s.io/cluster":                          "test-cluster.k8s.cluster",
					},
				},
				Spec: kopsapi.InstanceGroupSpec{
					Role: "Node",
				},
			},
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
		t.Run(tc.description, func(t *testing.T) {
			vfs.Context.ResetMemfsContext(true)
			fakeKopsClientset := newFakeKopsClientset()
			kopsCluster := newKopsCluster("test-cluster")
			_, err = fakeKopsClientset.CreateCluster(ctx, kopsCluster)
			if tc.kopsIG != nil {
				ig, err := fakeKopsClientset.InstanceGroupsFor(kopsCluster).Create(ctx, tc.kopsIG, metav1.CreateOptions{})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(ig).NotTo(BeNil())
			}
			cluster := newCluster("test-cluster", "test-kops-control-plane", "default")
			kopsMachinePool := newKopsMachinePool("test-kops-machine-pool", "test-cluster", "default")
			kopsMachinePool.Spec.SpotInstOptions = tc.spotOptions
			kopsControlPlane := newKopsControlPlane("test-kops-control-plane", "default")

			fakeClient := fake.NewClientBuilder().WithObjects(kopsMachinePool, kopsControlPlane, cluster).WithScheme(scheme.Scheme).Build()

			reconciler := KopsMachinePoolReconciler{
				Client:        fakeClient,
				kopsClientset: fakeKopsClientset,
				Recorder:      record.NewFakeRecorder(5),
				ValidateKopsClusterFactory: func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error) {
					return &validation.ValidationCluster{}, nil
				},
				GetASGByTagFactory: func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, _ *session.Session) (*autoscaling.Group, error) {
					return &autoscaling.Group{
						Instances: []*autoscaling.Instance{
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
					Namespace: "default",
					Name:      fmt.Sprintf("%s.k8s.cluster-%s", "test-cluster", "test-kops-machine-pool"),
				},
			})
			g.Expect(result).ToNot(BeNil())
			g.Expect(err).To(BeNil())

			tc.spotOptions["kops.k8s.io/cluster"] = "test-cluster.k8s.cluster"
			ig, err := fakeKopsClientset.InstanceGroupsFor(kopsCluster).Get(ctx, "test-kops-machine-pool", metav1.GetOptions{})
			g.Expect(ig).ToNot(BeNil())
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(ig.Labels).To(BeEquivalentTo(tc.spotOptions))
		})
	}
}

func TestMachinePoolStatus(t *testing.T) {
	testCases := []struct {
		description                 string
		expectedReconcilerError     bool
		conditionsToAssert          []*clusterv1.Condition
		eventsToAssert              []string
		expectedStatus              *infrastructurev1alpha1.KopsMachinePoolStatus
		expectedValidateKopsCluster func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error)
		kopsMachinePoolFunction     func(kmp *infrastructurev1alpha1.KopsMachinePool) *infrastructurev1alpha1.KopsMachinePool
		kopsControlPlaneFunction    func(kcp *controlplanev1alpha1.KopsControlPlane) *controlplanev1alpha1.KopsControlPlane
		clusterFunction             func(cluster *clusterv1.Cluster) *clusterv1.Cluster
		expectedReplicas            int32
	}{
		{
			description:             "should successfully create ready condition and patch KopsMachinePool",
			expectedReconcilerError: false,
			conditionsToAssert: []*clusterv1.Condition{
				conditions.TrueCondition(infrastructurev1alpha1.KopsMachinePoolStateReadyCondition),
			},
		},
		{
			description: "should mark the kmp as paused when the cluster is paused",
			clusterFunction: func(cluster *clusterv1.Cluster) *clusterv1.Cluster {
				cluster.Annotations = map[string]string{
					"cluster.x-k8s.io/paused": "true",
				}
				return cluster
			},
			expectedStatus: &infrastructurev1alpha1.KopsMachinePoolStatus{
				Paused: true,
			},
		},
		{
			description: "should mark the kmp as paused when the kcp is paused",
			kopsControlPlaneFunction: func(kcp *controlplanev1alpha1.KopsControlPlane) *controlplanev1alpha1.KopsControlPlane {
				kcp.Annotations = map[string]string{
					"cluster.x-k8s.io/paused": "true",
				}
				return kcp
			},
			expectedStatus: &infrastructurev1alpha1.KopsMachinePoolStatus{
				Paused: true,
			},
		},
		{
			description:             "should have a failed event when there is a validation error",
			expectedReconcilerError: false,
			eventsToAssert: []string{
				"failed to validate this test case",
			},
			expectedValidateKopsCluster: func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error) {
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
			description:             "should have a failed event when there is a error with the validation function",
			expectedReconcilerError: true,
			eventsToAssert: []string{
				"failed to validate this test case",
			},
			expectedValidateKopsCluster: func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error) {
				return &validation.ValidationCluster{
					Failures: []*validation.ValidationError{
						{
							Message: "failed to validate this test case",
						},
					},
				}, errors.New("failed to validate this test case")
			},
		},
		{
			description:             "should have an event when the validation succeeds",
			expectedReconcilerError: false,
			eventsToAssert: []string{
				"Kops validation succeed",
			},
		},
		{
			description: "should have a failed condition when InstanceGroupSpec is not set",
			kopsMachinePoolFunction: func(kmp *infrastructurev1alpha1.KopsMachinePool) *infrastructurev1alpha1.KopsMachinePool {
				kmp.Spec.KopsInstanceGroupSpec = kopsapi.InstanceGroupSpec{}
				return kmp
			},
			expectedReconcilerError: true,
			conditionsToAssert: []*clusterv1.Condition{
				conditions.FalseCondition(infrastructurev1alpha1.KopsMachinePoolStateReadyCondition, infrastructurev1alpha1.KopsMachinePoolStateReconciliationFailedReason, clusterv1.ConditionSeverityError, ""),
			},
		},
		{
			description:             "should set the status replicas",
			expectedReconcilerError: false,
			expectedReplicas:        int32(1),
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
		t.Run(tc.description, func(t *testing.T) {
			vfs.Context.ResetMemfsContext(true)
			cluster := newCluster("test-cluster", "test-kops-control-plane", "default")
			if tc.clusterFunction != nil {
				clusterFunction := tc.clusterFunction
				cluster = clusterFunction(cluster)
			}

			kopsMachinePool := newKopsMachinePool("test-kops-machine-pool", "test-cluster", "default")
			if tc.kopsMachinePoolFunction != nil {
				kopsMachinePoolFunction := tc.kopsMachinePoolFunction
				kopsMachinePool = kopsMachinePoolFunction(kopsMachinePool)
			}

			kopsControlPlane := newKopsControlPlane("test-kops-control-plane", "default")
			if tc.kopsControlPlaneFunction != nil {
				kopsControlPlaneFunction := tc.kopsControlPlaneFunction
				kopsControlPlane = kopsControlPlaneFunction(kopsControlPlane)
			}
			fakeClient := fake.NewClientBuilder().WithObjects(kopsMachinePool, kopsControlPlane, cluster).WithScheme(scheme.Scheme).Build()
			recorder := record.NewFakeRecorder(5)
			var validateKopsCluster func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error)
			if tc.expectedValidateKopsCluster != nil {
				validateKopsCluster = tc.expectedValidateKopsCluster
			} else {
				validateKopsCluster = func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error) {
					return &validation.ValidationCluster{}, nil
				}
			}

			reconciler := KopsMachinePoolReconciler{
				Client:                     fakeClient,
				Recorder:                   recorder,
				ValidateKopsClusterFactory: validateKopsCluster,
				GetASGByTagFactory: func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, _ *session.Session) (*autoscaling.Group, error) {
					return &autoscaling.Group{
						Instances: []*autoscaling.Instance{
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
					Namespace: "default",
					Name:      "test-cluster.k8s.cluster-test-kops-machine-pool",
				},
			})
			if !tc.expectedReconcilerError {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result.Requeue).To(BeFalse())
				g.Expect(result.RequeueAfter).To(Equal(1 * time.Hour))
			} else {
				g.Expect(err).To(HaveOccurred())
				g.Expect(result.RequeueAfter).To(Equal(30 * time.Minute))
			}

			if tc.expectedStatus != nil {
				kmp := &infrastructurev1alpha1.KopsMachinePool{}
				err = fakeClient.Get(ctx, client.ObjectKeyFromObject(kopsMachinePool), kmp)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(*tc.expectedStatus).To(BeEquivalentTo(kmp.Status))
			}

			if tc.conditionsToAssert != nil {
				kmp := &infrastructurev1alpha1.KopsMachinePool{}
				err = fakeClient.Get(ctx, client.ObjectKeyFromObject(kopsMachinePool), kmp)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(kmp.Status.Conditions).ToNot(BeNil())
				assertConditions(g, kmp, tc.conditionsToAssert...)
			}

			if tc.eventsToAssert != nil {
				for _, eventMessage := range tc.eventsToAssert {
					g.Expect(recorder.Events).Should(Receive(ContainSubstring(eventMessage)))
				}
			}

			if tc.expectedReplicas != 0 {
				kmp := &infrastructurev1alpha1.KopsMachinePool{}
				err = fakeClient.Get(ctx, client.ObjectKeyFromObject(kopsMachinePool), kmp)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(kmp.Status.Replicas).To(Equal(tc.expectedReplicas))
			}
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
				Subnets: []kopsapi.ClusterSubnetSpec{
					{
						Name: "test-subnet-a",
						Zone: "us-east-1a",
					},
				},
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
			Name:      fmt.Sprintf("%s.k8s.cluster-%s", clusterName, name),
		},
		Spec: infrastructurev1alpha1.KopsMachinePoolSpec{
			ClusterName: fmt.Sprintf("%s.k8s.cluster", clusterName),
			KopsInstanceGroupSpec: kopsapi.InstanceGroupSpec{
				Role: "Node",
			},
		},
	}
}
