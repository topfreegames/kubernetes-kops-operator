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

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	controlplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	infrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"
	"github.com/topfreegames/kubernetes-kops-operator/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kopsapi "k8s.io/kops/pkg/apis/kops"
	"k8s.io/kops/pkg/client/simple"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	expclusterv1 "sigs.k8s.io/cluster-api/exp/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KopsMachinePoolReconciler reconciles a KopsMachinePool object
type KopsMachinePoolReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	log              logr.Logger
	WatchFilterValue string
	kopsClientset    simple.Clientset
}

// getClusterByName returns cluster from Kubernetes by its name
func (r *KopsMachinePoolReconciler) getClusterByName(ctx context.Context, namespace, name string) (*clusterv1.Cluster, error) {
	cluster := &clusterv1.Cluster{}
	key := client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}

	if err := r.Client.Get(ctx, key, cluster); err != nil {
		return nil, errors.Wrapf(err, "failed to get Cluster/%s", name)
	}

	return cluster, nil
}

// getKopsControlPlaneByName returns kopsControlPLane by its name
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

//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io.cluster.x-k8s.io,resources=kopsmachinepools,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io.cluster.x-k8s.io,resources=kopsmachinepools/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io.cluster.x-k8s.io,resources=kopsmachinepools/finalizers,verbs=update
func (r *KopsMachinePoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.log = ctrl.LoggerFrom(ctx)

	kopsMachinePool := &infrastructurev1alpha1.KopsMachinePool{}
	err := r.Get(ctx, req.NamespacedName, kopsMachinePool)
	if err != nil {
		return ctrl.Result{}, err
	}

	kopsInstanceGroup := &kopsapi.InstanceGroup{
		ObjectMeta: kopsMachinePool.ObjectMeta,
		Spec:       kopsMachinePool.Spec.KopsInstanceGroupSpec,
	}

	cluster, err := r.getClusterByName(ctx, kopsInstanceGroup.ObjectMeta.Namespace, kopsMachinePool.Spec.ClusterName)
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

	kopsClientset, err := utils.GetKopsClientset(kopsControlPlane.Spec.KopsClusterSpec.ConfigBase)
	if err != nil {
		return ctrl.Result{}, err
	}

	r.kopsClientset = kopsClientset

	kopsCluster := &kopsapi.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: kopsMachinePool.Spec.ClusterName,
		},
		Spec: kopsControlPlane.Spec.KopsClusterSpec,
	}
	err = r.updateInstanceGroup(ctx, kopsCluster, kopsInstanceGroup)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
// func (r *KopsMachinePoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
// 	return ctrl.NewControllerManagedBy(mgr).
// 		For(&infrastructurev1alpha1.KopsMachinePool{}).
// 		Watches(
// 			&source.Kind{Type: &expclusterv1.MachinePool{}},
// 			handler.EnqueueRequestsFromMapFunc(r.MachinePoolToInfrastructureMapFunc),
// 		).
// 		Complete(r)
// }

func (r *KopsMachinePoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1alpha1.KopsMachinePool{}).
		Complete(r)
}

func (r *KopsMachinePoolReconciler) MachinePoolToInfrastructureMapFunc(o client.Object) []ctrl.Request {
	result := []ctrl.Request{}
	mp, ok := o.(*expclusterv1.MachinePool)
	if !ok {
		panic(fmt.Sprintf("Expected a MachinePool but got a %T", o))
	}

	if mp.Spec.Template.Spec.InfrastructureRef.GroupVersionKind().GroupKind() == infrastructurev1alpha1.GroupVersion.WithKind("KopsMachinePool").GroupKind() {
		name := client.ObjectKey{Namespace: mp.Namespace, Name: mp.Spec.Template.Spec.Bootstrap.ConfigRef.Name}
		result = append(result, ctrl.Request{NamespacedName: name})

	}

	return result
}
