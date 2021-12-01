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

	"github.com/pkg/errors"
	infrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kops/pkg/client/simple"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KopsMachinePoolReconciler reconciles a KopsMachinePool object
type KopsMachinePoolReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	clientset        simple.Clientset
}

//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io.cluster.x-k8s.io,resources=kopsmachinepools,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io.cluster.x-k8s.io,resources=kopsmachinepools/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io.cluster.x-k8s.io,resources=kopsmachinepools/finalizers,verbs=update
func (r *KopsMachinePoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = ctrl.LoggerFrom(ctx)

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
	return ctrl.Result{}, nil
}
func (r *KopsMachinePoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1alpha1.KopsMachinePool{}).
		Complete(r)
}

