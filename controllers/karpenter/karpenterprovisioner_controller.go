package karpenter

import (
	"context"
	"time"

	infrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/kops/pkg/client/simple"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	requeue1min   = ctrl.Result{RequeueAfter: 1 * time.Minute}
	resultDefault = ctrl.Result{RequeueAfter: 20 * time.Minute}
	resultError   = ctrl.Result{RequeueAfter: 5 * time.Minute}
)

type KarpenterProvisioner struct {
	client.Client
	Scheme                  *runtime.Scheme
	Recorder                record.EventRecorder
	GetKopsClientSetFactory func(configBase string) (simple.Clientset, error)
}

func (r *KarpenterProvisioner) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	var kopsMachinePool infrastructurev1alpha1.KopsMachinePool
	if err := r.Get(ctx, req.NamespacedName, &kopsMachinePool); err != nil {
		log.Error(err, "unable to fetch KopsMachinePool")
		return resultError, client.IgnoreNotFound(err)
	}

	

	return resultDefault, nil
}

func (r *KarpenterProvisioner) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1alpha1.KopsMachinePool{}).
		Complete(r)
}
