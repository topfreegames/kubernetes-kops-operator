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

package controlplane

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	controlplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	"github.com/topfreegames/kubernetes-kops-operator/utils"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/tools/record"
	kopsapi "k8s.io/kops/pkg/apis/kops"
	"k8s.io/kops/pkg/assets"
	"k8s.io/kops/pkg/validation"
	"sigs.k8s.io/cluster-api/controllers/external"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/kops/pkg/client/simple"
	"k8s.io/kops/pkg/commands"
	"k8s.io/kops/upup/pkg/fi"
	"k8s.io/kops/upup/pkg/fi/cloudup"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/kubeconfig"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/predicates"
	"sigs.k8s.io/cluster-api/util/secret"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// KopsControlPlaneReconciler reconciles a KopsControlPlane object
type KopsControlPlaneReconciler struct {
	client.Client
	Scheme                       *runtime.Scheme
	kopsClientset                simple.Clientset
	log                          logr.Logger
	Recorder                     record.EventRecorder
	BuildCloudFactory            func(*kopsapi.Cluster) (fi.Cloud, error)
	PopulateClusterSpecFactory   func(kopsCluster *kopsapi.Cluster, kopsClientset simple.Clientset, cloud fi.Cloud) (*kopsapi.Cluster, error)
	PrepareCloudResourcesFactory func(kopsClientset simple.Clientset, ctx context.Context, kopsCluster *kopsapi.Cluster, configBase string, cloud fi.Cloud) (string, error)
	ApplyTerraformFactory        func(ctx context.Context, terraformDir string) error
	ValidateKopsClusterFactory   func(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error)
	GetClusterStatusFactory      func(kopsCluster *kopsapi.Cluster) (*kopsapi.ClusterStatus, error)
}

func ApplyTerraform(ctx context.Context, terraformDir string) error {
	err := utils.ApplyTerraform(ctx, terraformDir)
	if err != nil {
		return err
	}
	return nil
}

func GetClusterStatus(kopsCluster *kopsapi.Cluster) (*kopsapi.ClusterStatus, error) {
	statusDiscovery := &commands.CloudDiscoveryStatusStore{}
	status, err := statusDiscovery.FindClusterStatus(kopsCluster)
	if err != nil {
		return nil, err
	}
	return status, nil
}

// prepareCloudResources renders the terraform files and effectively apply them in the cloud provider
func PrepareCloudResources(kopsClientset simple.Clientset, ctx context.Context, kopsCluster *kopsapi.Cluster, configBase string, cloud fi.Cloud) (string, error) {
	s3Bucket, err := utils.GetBucketName(configBase)
	if err != nil {
		return "", err
	}

	terraformOutputDir := fmt.Sprintf("/tmp/%s", kopsCluster.Name)

	applyCmd := &cloudup.ApplyClusterCmd{
		Cloud:              cloud,
		Clientset:          kopsClientset,
		Cluster:            kopsCluster,
		DryRun:             true,
		AllowKopsDowngrade: false,
		OutDir:             terraformOutputDir,
		TargetName:         "terraform",
	}

	err = utils.CreateTerraformBackendFile(s3Bucket, kopsCluster.Name, terraformOutputDir)
	if err != nil {
		return "", err
	}

	if err := applyCmd.Run(ctx); err != nil {
		return "", err
	}

	return terraformOutputDir, nil

}

// updateKopsState creates or updates the kops state in the remote storage
func (r *KopsControlPlaneReconciler) updateKopsState(ctx context.Context, kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, SSHPublicKey string) error {
	log := ctrl.LoggerFrom(ctx)
	oldCluster, _ := kopsClientset.GetCluster(ctx, kopsCluster.Name)
	if oldCluster != nil {
		status, err := r.GetClusterStatusFactory(oldCluster)
		if err != nil {
			return err
		}
		_, err = kopsClientset.UpdateCluster(ctx, kopsCluster, status)
		if err != nil {
			return err
		}
		log.Info(fmt.Sprintf("updated kops state for cluster %s", kopsCluster.ObjectMeta.Name))
		return nil
	}

	_, err := kopsClientset.CreateCluster(ctx, kopsCluster)
	if err != nil {
		return err
	}

	err = addSSHCredential(kopsCluster, kopsClientset, SSHPublicKey)
	if err != nil {
		return err
	}

	log.Info(fmt.Sprintf("created kops state for cluster %s", kopsCluster.ObjectMeta.Name))

	return nil
}

// PopulateClusterSpec populates the full cluster spec with some values it fetchs from provider
func PopulateClusterSpec(kopsCluster *kopsapi.Cluster, kopsClientset simple.Clientset, cloud fi.Cloud) (*kopsapi.Cluster, error) {

	err := cloudup.PerformAssignments(kopsCluster, cloud)
	if err != nil {
		return nil, err
	}

	assetBuilder := assets.NewAssetBuilder(kopsCluster, "")
	fullCluster, err := cloudup.PopulateClusterSpec(kopsClientset, kopsCluster, cloud, assetBuilder)
	if err != nil {
		return nil, err
	}

	return fullCluster, nil
}

// addSSHCredential creates a SSHCredential using the PublicKey retrieved from the KopsControlPlane
func addSSHCredential(kopsCluster *kopsapi.Cluster, kopsClientset simple.Clientset, SSHPublicKey string) error {
	sshCredential := kopsapi.SSHCredential{
		Spec: kopsapi.SSHCredentialSpec{
			PublicKey: SSHPublicKey,
		},
	}

	sshCredentialStore, err := kopsClientset.SSHCredentialStore(kopsCluster)
	if err != nil {
		return err
	}
	sshKeyArr := []byte(sshCredential.Spec.PublicKey)
	err = sshCredentialStore.AddSSHPublicKey("admin", sshKeyArr)
	if err != nil {
		return err
	}

	return nil
}

// GetOwnerByRef finds and returns the owner by looking at the object reference.
func getOwnerByRef(ctx context.Context, c client.Client, ref *corev1.ObjectReference) (*unstructured.Unstructured, error) {
	obj, err := external.Get(ctx, c, ref, ref.Namespace)
	if err != nil {
		return nil, err
	}
	return obj, nil
}

// getOwner returns the Cluster owning the KopsControlPlane object.
func (r *KopsControlPlaneReconciler) getClusterOwnerRef(ctx context.Context, obj metav1.Object) (*unstructured.Unstructured, error) {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.Kind != "Cluster" {
			continue
		}
		owner, err := getOwnerByRef(ctx, r.Client, &corev1.ObjectReference{
			APIVersion: ref.APIVersion,
			Kind:       ref.Kind,
			Name:       ref.Name,
			Namespace:  obj.GetNamespace(),
		})
		if err != nil {
			return nil, err
		}

		return owner, nil
	}
	return nil, nil
}

func (r *KopsControlPlaneReconciler) reconcileKubeconfig(ctx context.Context, kopsCluster *kopsapi.Cluster, cluster *unstructured.Unstructured) error {
	config, err := utils.GetKubeconfigFromKopsState(kopsCluster, r.kopsClientset)
	if err != nil {
		return err
	}

	clusterName := cluster.GetName()

	cfg := &api.Config{
		Clusters: map[string]*api.Cluster{
			clusterName: {
				Server:                   config.Host,
				CertificateAuthorityData: config.CAData,
			},
		},
		Contexts: map[string]*api.Context{
			clusterName: {
				Cluster:  clusterName,
				AuthInfo: clusterName,
			},
		},
		CurrentContext: kopsCluster.ObjectMeta.Name,
		AuthInfos: map[string]*api.AuthInfo{
			clusterName: {
				ClientCertificateData: config.CertData,
				ClientKeyData:         config.KeyData,
			},
		},
	}

	out, err := clientcmd.Write(*cfg)
	if err != nil {
		return errors.Wrap(err, "failed to serialize config to yaml")
	}

	clusterRef := types.NamespacedName{
		Namespace: cluster.GetNamespace(),
		Name:      clusterName,
	}

	ref := metav1.OwnerReference{
		APIVersion: clusterv1.GroupVersion.String(),
		Kind:       "Cluster",
		UID:        cluster.GetUID(),
		Name:       clusterName,
	}

	kubeconfigSecret := kubeconfig.GenerateSecretWithOwner(clusterRef, out, ref)

	_, err = secret.GetFromNamespacedName(ctx, r.Client, clusterRef, secret.Kubeconfig)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return errors.Wrap(err, "failed to get kubeconfig secret")
		}
		err = r.Client.Create(ctx, kubeconfigSecret)
		if err != nil {
			return errors.Wrap(err, "failed creating kubeconfig secret")
		}
	} else {
		updateErr := r.Client.Update(ctx, kubeconfigSecret)
		if updateErr != nil {
			return errors.Wrap(err, "failed updating kubeconfig secret")
		}
	}

	return nil
}

//+kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=kopscontrolplanes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=kopscontrolplanes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=kopscontrolplanes/finalizers,verbs=update
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters/finalizers,verbs=update
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters/status,verbs=get;update;patch
//+kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;patch

func (r *KopsControlPlaneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, rerr error) {
	r.log = ctrl.LoggerFrom(ctx)

	kopsControlPlane := &controlplanev1alpha1.KopsControlPlane{}
	if err := r.Get(ctx, req.NamespacedName, kopsControlPlane); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	r.log.Info(fmt.Sprintf("starting reconcile loop for %s", kopsControlPlane.ObjectMeta.GetName()))

	// Look up the owner of this kopscontrolplane config if there is one
	owner, err := r.getClusterOwnerRef(ctx, kopsControlPlane)
	if err != nil || owner == nil {
		if apierrors.IsNotFound(err) {
			r.log.Info("Cluster does not exist yet, re-queueing until it is created")
			return ctrl.Result{}, nil
		}
		if err == nil && owner == nil {
			r.log.Info(fmt.Sprintf("kopscontrolplane/%s does not belong to a cluster yet, waiting until it's part of a cluster", kopsControlPlane.ObjectMeta.Name))

			return ctrl.Result{}, nil
		}
		r.log.Error(err, "could not get cluster with metadata")
		return ctrl.Result{}, err
	}

	// Initialize the patch helper.
	patchHelper, err := patch.NewHelper(kopsControlPlane, r.Client)
	if err != nil {
		r.log.Error(err, "failed to initialize patch helper")
		return ctrl.Result{}, err
	}

	// Attempt to Patch the KopsControlPlane object and status after each reconciliation if no error occurs.
	defer func() {
		err = patchHelper.Patch(ctx, kopsControlPlane)

		if err != nil {
			r.log.Error(rerr, "Failed to patch kopsControlPlane")
			if rerr == nil {
				rerr = err
			}
		}
		r.log.Info(fmt.Sprintf("finished reconcile loop for %s", kopsControlPlane.ObjectMeta.GetName()))
	}()

	if r.kopsClientset == nil {
		kopsClientset, err := utils.GetKopsClientset(kopsControlPlane.Spec.KopsClusterSpec.ConfigBase)
		if err != nil {
			r.log.Error(rerr, "failed to get kops clientset")
			return ctrl.Result{}, err
		}
		r.kopsClientset = kopsClientset
	}

	kopsCluster := &kopsapi.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: owner.GetName(),
		},
		Spec: kopsControlPlane.Spec.KopsClusterSpec,
	}

	err = r.reconcileKubeconfig(ctx, kopsCluster, owner)
	if err != nil {
		r.log.Error(rerr, "failed to reconcile kubeconfig")
		return ctrl.Result{}, err
	}

	cloud, err := r.BuildCloudFactory(kopsCluster)
	if err != nil {
		r.log.Error(rerr, "failed to build cloud")
		return ctrl.Result{}, err
	}

	fullCluster, err := r.PopulateClusterSpecFactory(kopsCluster, r.kopsClientset, cloud)
	if err != nil {
		r.log.Error(rerr, "failed to populated cluster Spec")
		return ctrl.Result{}, err
	}

	err = r.updateKopsState(ctx, r.kopsClientset, fullCluster, kopsControlPlane.Spec.SSHPublicKey)
	if err != nil {
		r.log.Error(err, fmt.Sprintf("failed to manage kops state: %v", err))
		conditions.MarkFalse(kopsControlPlane, controlplanev1alpha1.KopsControlPlaneStateReadyCondition, controlplanev1alpha1.KopsControlPlaneStateReconciliationFailedReason, clusterv1.ConditionSeverityError, err.Error())
		return ctrl.Result{}, err
	}
	conditions.MarkTrue(kopsControlPlane, controlplanev1alpha1.KopsControlPlaneStateReadyCondition)

	terraformOutputDir, err := r.PrepareCloudResourcesFactory(r.kopsClientset, ctx, kopsCluster, fullCluster.Spec.ConfigBase, cloud)
	if err != nil {
		conditions.MarkFalse(kopsControlPlane, controlplanev1alpha1.KopsTerraformGenerationReadyCondition, controlplanev1alpha1.KopsTerraformGenerationReconciliationFailedReason, clusterv1.ConditionSeverityError, err.Error())
		r.log.Error(err, fmt.Sprintf("failed to prepare cloud resources: %v", err))
		return ctrl.Result{}, err
	}
	conditions.MarkTrue(kopsControlPlane, controlplanev1alpha1.KopsTerraformGenerationReadyCondition)

	err = r.ApplyTerraformFactory(ctx, terraformOutputDir)
	if err != nil {
		conditions.MarkFalse(kopsControlPlane, controlplanev1alpha1.TerraformApplyReadyCondition, controlplanev1alpha1.TerraformApplyReconciliationFailedReason, clusterv1.ConditionSeverityError, err.Error())
		r.log.Error(err, fmt.Sprintf("failed to apply terraform: %v", err))
		return ctrl.Result{}, err
	}
	conditions.MarkTrue(kopsControlPlane, controlplanev1alpha1.TerraformApplyReadyCondition)

	list, err := r.kopsClientset.InstanceGroupsFor(kopsCluster).List(ctx, metav1.ListOptions{})
	if err != nil || len(list.Items) == 0 {
		return ctrl.Result{}, fmt.Errorf("cannot get InstanceGroups for %q: %v", kopsCluster.ObjectMeta.Name, err)
	}

	masterIGs := &kopsapi.InstanceGroupList{}
	for _, ig := range list.Items {
		if ig.Spec.Role == "Master" {
			masterIGs.Items = append(masterIGs.Items, ig)
		}
	}

	validation, err := r.ValidateKopsClusterFactory(r.kopsClientset, kopsCluster, masterIGs)

	if err != nil {
		r.log.Error(err, fmt.Sprintf("failed trying to validate Kubernetes cluster: %v", err))
		r.Recorder.Eventf(kopsControlPlane, corev1.EventTypeWarning, "KubernetesClusterValidationFailed", err.Error())
		return ctrl.Result{}, err
	}

	statusReady, err := utils.KopsClusterValidation(kopsControlPlane, r.Recorder, r.log, validation)
	if err != nil {
		return ctrl.Result{}, err
	}
	kopsControlPlane.Status.Ready = statusReady

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *KopsControlPlaneReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&controlplanev1alpha1.KopsControlPlane{}).
		WithEventFilter(predicates.ResourceNotPaused(ctrl.LoggerFrom(ctx))).
		Watches(
			&source.Kind{Type: &clusterv1.Cluster{}},
			handler.EnqueueRequestsFromMapFunc(clusterToInfrastructureMapFunc),
		).
		Complete(r)
}

func clusterToInfrastructureMapFunc(o client.Object) []ctrl.Request {
	c, ok := o.(*clusterv1.Cluster)
	if !ok {
		panic(fmt.Sprintf("Expected a Cluster but got a %T", o))
	}

	result := []ctrl.Request{}
	if c.Spec.InfrastructureRef != nil && c.Spec.InfrastructureRef.GroupVersionKind() == controlplanev1alpha1.GroupVersion.WithKind("KopsControlPlane") {
		name := client.ObjectKey{Namespace: c.Spec.InfrastructureRef.Namespace, Name: c.Spec.InfrastructureRef.Name}
		result = append(result, ctrl.Request{NamespacedName: name})
	}

	return result
}
