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
	"encoding/json"
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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KopsMachinePoolReconciler reconciles a KopsMachinePool object
type KopsMachinePoolReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	log              logr.Logger
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
	clusterName := kopsInstanceGroup.ObjectMeta.Labels[kopsapi.LabelClusterName]
	if clusterName == "" {
		return fmt.Errorf("must specify %q label with cluster name to create instanceGroup", kopsapi.LabelClusterName)
	}

	if r.isInstanceGroupCreated(ctx, kopsCluster, kopsInstanceGroup.Name) {
		_, err := r.kopsClientset.InstanceGroupsFor(kopsCluster).Update(ctx, kopsInstanceGroup, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("error updating instanceGroup: %v", err)
		}
		r.log.Info(fmt.Sprintf("updated instancegroup/%s", kopsInstanceGroup.ObjectMeta.Name))
		return nil
	}

	_, err := r.kopsClientset.InstanceGroupsFor(kopsCluster).Create(ctx, kopsInstanceGroup, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("error creating instanceGroup: %v", err)
	}
	r.log.Info(fmt.Sprintf("created instancegroup/%s", kopsInstanceGroup.ObjectMeta.Name))

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

	kopsInstaGroupSpecBytes, err := json.Marshal(kopsMachinePool.Spec.KopsInstanceGroupSpec)
	if err != nil {
		return ctrl.Result{}, err
	}

	var kopsInstanceGroupSpec kopsapi.InstanceGroupSpec

	err = json.Unmarshal(kopsInstaGroupSpecBytes, &kopsInstanceGroupSpec)
	if err != nil {
		return ctrl.Result{}, err
	}

	kopsInstanceGroup := &kopsapi.InstanceGroup{
		ObjectMeta: kopsMachinePool.ObjectMeta,
		Spec:       kopsInstanceGroupSpec,
	}

	clusterName := kopsInstanceGroup.Spec.NodeLabels[kopsapi.LabelClusterName]

	cluster, err := r.getClusterByName(ctx, kopsInstanceGroup.ObjectMeta.Namespace, clusterName)
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

	kopsClusterSpecBytes, err := json.Marshal(kopsControlPlane.Spec.KopsClusterSpec)
	if err != nil {
		return ctrl.Result{}, err
	}

	var kopsClusterSpec kopsapi.ClusterSpec

	err = json.Unmarshal(kopsClusterSpecBytes, &kopsClusterSpec)
	if err != nil {
		return ctrl.Result{}, err
	}

	s3Bucket := utils.GetBucketName(kopsClusterSpec.ConfigBase)

	kopsClientset, err := utils.GetKopsClientset(s3Bucket)
	if err != nil {
		return ctrl.Result{}, err
	}

	r.kopsClientset = kopsClientset

	kopsCluster := &kopsapi.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterName,
		},
		Spec: kopsClusterSpec,
	}
	err = r.updateInstanceGroup(ctx, kopsCluster, kopsInstanceGroup)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}
func (r *KopsMachinePoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1alpha1.KopsMachinePool{}).
		Complete(r)
}

