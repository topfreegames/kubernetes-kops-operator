/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package infrastructureclusterxk8sio

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	controlplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	infrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"
	"github.com/topfreegames/kubernetes-kops-operator/pkg/util"
	"github.com/topfreegames/kubernetes-kops-operator/utils"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	kopsapi "k8s.io/kops/pkg/apis/kops"
	"k8s.io/kops/pkg/client/simple"
	"k8s.io/kops/pkg/validation"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/predicates"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KopsMachinePoolReconciler reconciles a KopsMachinePool object
type KopsMachinePoolReconciler struct {
	client.Client
	Scheme                     *runtime.Scheme
	log                        logr.Logger
	WatchFilterValue           string
	kopsClientset              simple.Clientset
	Recorder                   record.EventRecorder
	ValidateKopsClusterFactory func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error)
	GetASGByTagFactory         func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, awsClient *session.Session) (*autoscaling.Group, error)
}

// getKopsControlPlaneByName returns kopsControlPlane by its name
func (r *KopsMachinePoolReconciler) getKopsControlPlaneByName(ctx context.Context, namespace, name string) (*controlplanev1alpha1.KopsControlPlane, error) {
	kopsControlPlane := &controlplanev1alpha1.KopsControlPlane{}
	key := client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}

	if err := r.Client.Get(ctx, key, kopsControlPlane); err != nil {
		return nil, errors.Wrapf(err, "failed to get KopsControlPlane/%s", name)
	}

	return kopsControlPlane, nil
}

// isInstanceGroupCreated check if IG is created in kops state
func (r *KopsMachinePoolReconciler) isInstanceGroupCreated(ctx context.Context, cluster *kopsapi.Cluster, instanceGroupName string) bool {
	ig, _ := r.kopsClientset.InstanceGroupsFor(cluster).Get(ctx, instanceGroupName, metav1.GetOptions{})
	return ig != nil
}

// updateInstanceGroup create or update the instance group in kops state
func (r *KopsMachinePoolReconciler) updateInstanceGroup(ctx context.Context, kopsCluster *kopsapi.Cluster, kopsInstanceGroup *kopsapi.InstanceGroup) error {

	instanceGroupName := kopsInstanceGroup.ObjectMeta.Name
	if r.isInstanceGroupCreated(ctx, kopsCluster, kopsInstanceGroup.Name) {
		_, err := r.kopsClientset.InstanceGroupsFor(kopsCluster).Update(ctx, kopsInstanceGroup, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("error updating instanceGroup: %v", err)
		}
		r.log.Info(fmt.Sprintf("updated instancegroup/%s", instanceGroupName))
		return nil
	}

	_, err := r.kopsClientset.InstanceGroupsFor(kopsCluster).Create(ctx, kopsInstanceGroup, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("error creating instanceGroup: %v", err)
	}
	r.log.Info(fmt.Sprintf("created instancegroup/%s", instanceGroupName))

	return nil
}

func getInstanceGroupNameFromKopsMachinePool(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool) (string, error) {
	clusterName := kopsMachinePool.Spec.ClusterName
	kopsMachinePoolName := kopsMachinePool.ObjectMeta.Name

	if len(kopsMachinePoolName) <= len(clusterName) {
		return "", errors.New("kopsMachinePool name unexpected format")
	}

	igName := kopsMachinePool.ObjectMeta.Name[len(kopsMachinePool.Spec.ClusterName)+1:]

	return igName, nil
}

//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kopsmachinepools,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kopsmachinepools/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kopsmachinepools/finalizers,verbs=update

func (r *KopsMachinePoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, rerr error) {
	r.log = ctrl.LoggerFrom(ctx)

	kopsMachinePool := &infrastructurev1alpha1.KopsMachinePool{}
	err := r.Get(ctx, req.NamespacedName, kopsMachinePool)
	if err != nil {
		return ctrl.Result{}, err
	}
	igName, err := getInstanceGroupNameFromKopsMachinePool(kopsMachinePool)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Initialize the patch helper.
	patchHelper, err := patch.NewHelper(kopsMachinePool, r.Client)
	if err != nil {
		r.log.Error(err, "failed to initialize patch helper")
		return ctrl.Result{}, err
	}
	// Attempt to Patch the KopsMachinePool object and status after each reconciliation if no error occurs.
	defer func() {
		err = patchHelper.Patch(ctx, kopsMachinePool)

		if err != nil {
			r.log.Error(rerr, "Failed to patch kopsMachinePool")
			if rerr == nil {
				rerr = err
			}
		}
		r.log.Info(fmt.Sprintf("finished reconcile loop for %s", kopsMachinePool.ObjectMeta.GetName()))
	}()

	kopsInstanceGroup := &kopsapi.InstanceGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      igName,
			Namespace: kopsMachinePool.ObjectMeta.Namespace,
			Labels:    kopsMachinePool.Spec.SpotInstOptions,
		},
		Spec: kopsMachinePool.Spec.KopsInstanceGroupSpec,
	}

	cluster, err := util.GetClusterByName(ctx, r.Client, kopsInstanceGroup.ObjectMeta.Namespace, kopsMachinePool.Spec.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	kopsControlPlane := &controlplanev1alpha1.KopsControlPlane{}
	if cluster.Spec.ControlPlaneRef != nil && cluster.Spec.ControlPlaneRef.Kind == "KopsControlPlane" {
		kopsControlPlane, err = r.getKopsControlPlaneByName(ctx, kopsInstanceGroup.ObjectMeta.Namespace, cluster.Spec.ControlPlaneRef.Name)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	if r.kopsClientset == nil {
		kopsClientset, err := utils.GetKopsClientset(kopsControlPlane.Spec.KopsClusterSpec.ConfigBase)
		if err != nil {
			return ctrl.Result{}, err
		}

		r.kopsClientset = kopsClientset
	}

	kopsCluster := &kopsapi.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: kopsMachinePool.Spec.ClusterName,
		},
		Spec: kopsControlPlane.Spec.KopsClusterSpec,
	}

	err = r.updateInstanceGroup(ctx, kopsCluster, kopsInstanceGroup)
	if err != nil {
		conditions.MarkFalse(kopsMachinePool, infrastructurev1alpha1.KopsMachinePoolStateReadyCondition, infrastructurev1alpha1.KopsMachinePoolStateReconciliationFailedReason, clusterv1.ConditionSeverityError, err.Error())
		return ctrl.Result{}, err
	}
	conditions.MarkTrue(kopsMachinePool, infrastructurev1alpha1.KopsMachinePoolStateReadyCondition)

	if len(kopsMachinePool.Spec.SpotInstOptions) == 0 {

		region, err := regionBySubnet(kopsControlPlane)
		if err != nil {
			return ctrl.Result{}, err
		}

		awsClient, err := session.NewSession(&aws.Config{Region: &region})
		if err != nil {
			return ctrl.Result{}, err
		}

		asg, err := r.GetASGByTagFactory(kopsMachinePool, awsClient)
		if err != nil {
			r.log.Error(err, fmt.Sprintf("failed retriving ASG: %v", err))
			return ctrl.Result{}, err
		}

		providerIDList := make([]string, len(asg.Instances))
		for i, instance := range asg.Instances {
			providerIDList[i] = fmt.Sprintf("aws:///%s/%s", *instance.AvailabilityZone, *instance.InstanceId)
		}

		kopsMachinePool.Spec.ProviderIDList = providerIDList
		kopsMachinePool.Status.Replicas = int32(len(providerIDList))

	}

	igList := &kopsapi.InstanceGroupList{
		Items: []kopsapi.InstanceGroup{
			*kopsInstanceGroup,
		},
	}

	val, err := r.ValidateKopsClusterFactory(r.kopsClientset, kopsCluster, igList)

	if err != nil {
		r.log.Error(err, fmt.Sprintf("failed trying to validate Kubernetes cluster: %v", err))
		r.Recorder.Eventf(kopsMachinePool, corev1.EventTypeWarning, "KubernetesClusterValidationFailed", err.Error())
		return ctrl.Result{}, err
	}

	statusReady, err := utils.KopsClusterValidation(kopsMachinePool, r.Recorder, r.log, val)
	if err != nil {
		return ctrl.Result{}, err
	}
	kopsMachinePool.Status.Ready = statusReady

	return ctrl.Result{}, nil
}

func regionBySubnet(kopsControlPlane *controlplanev1alpha1.KopsControlPlane) (string, error) {
	subnets := kopsControlPlane.Spec.KopsClusterSpec.Subnets
	if len(subnets) == 0 {
		return "", errors.New("kopsControlPlane with no subnets")
	}

	zone := subnets[0].Zone

	return zone[:len(zone)-1], nil
}

// GetASGByTag returns the existing ASG or nothing if it doesn't exist.
func GetASGByTag(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, awsClient *session.Session) (*autoscaling.Group, error) {
	svc := autoscaling.New(awsClient)

	clusterFilterKey := "tag:KubernetesCluster"
	instanceGroupKey := "tag:kops.k8s.io/instancegroup"

	igName, err := getInstanceGroupNameFromKopsMachinePool(kopsMachinePool)
	if err != nil {
		return nil, err
	}

	input := &autoscaling.DescribeAutoScalingGroupsInput{
		Filters: []*autoscaling.Filter{
			{
				Name: &clusterFilterKey,
				Values: []*string{
					&kopsMachinePool.Spec.ClusterName,
				},
			},
			{
				Name: &instanceGroupKey,
				Values: []*string{
					&igName,
				},
			},
		},
	}

	out, err := svc.DescribeAutoScalingGroups(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case autoscaling.ErrCodeInvalidNextToken:
				return nil, fmt.Errorf(aerr.Error(), autoscaling.ErrCodeInvalidNextToken)
			case autoscaling.ErrCodeResourceContentionFault:
				return nil, fmt.Errorf(aerr.Error(), autoscaling.ErrCodeResourceContentionFault)
			default:
				return nil, fmt.Errorf(aerr.Error(), aerr.Error())
			}
		} else {
			return nil, fmt.Errorf(aerr.Error(), aerr.Error())
		}
	}
	if len(out.AutoScalingGroups) > 0 {
		return out.AutoScalingGroups[0], nil
	}
	return nil, errors.New("fail to retrieve ASG")
}

func (r *KopsMachinePoolReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1alpha1.KopsMachinePool{}).
		WithEventFilter(predicates.ResourceNotPaused(ctrl.LoggerFrom(ctx))).
		Complete(r)
}
