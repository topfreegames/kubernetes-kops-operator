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
	"crypto/x509/pkix"
	"fmt"

	"github.com/go-logr/logr"
	controlplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	"github.com/topfreegames/kubernetes-kops-operator/utils"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	kopsapi "k8s.io/kops/pkg/apis/kops"
	"k8s.io/kops/pkg/assets"
	"k8s.io/kops/pkg/pki"
	"k8s.io/kops/pkg/rbac"
	"sigs.k8s.io/cluster-api/controllers/external"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/kops/pkg/client/simple"
	"k8s.io/kops/pkg/commands"
	"k8s.io/kops/pkg/kubeconfig"
	"k8s.io/kops/pkg/validation"
	"k8s.io/kops/upup/pkg/fi"
	"k8s.io/kops/upup/pkg/fi/cloudup"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/predicates"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// KopsControlPlaneReconciler reconciles a KopsControlPlane object
type KopsControlPlaneReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	kopsClientset simple.Clientset
	log           logr.Logger
	ManagedScope  IKopsControlPlaneManagedScope
}

type IKopsControlPlaneManagedScope interface {
	buildCloud(*kopsapi.Cluster) (fi.Cloud, error)
	populateClusterSpec(kopsCluster *kopsapi.Cluster, kopsClientset simple.Clientset) (*kopsapi.Cluster, error)
	createCloudResources(kopsClientset simple.Clientset, ctx context.Context, kopsCluster *kopsapi.Cluster, configBase string) error
	getClusterStatus(kopsCluster *kopsapi.Cluster) (*kopsapi.ClusterStatus, error)
}
type KopsControlPlaneManagedScope struct{}

func (kcpms *KopsControlPlaneManagedScope) getClusterStatus(kopsCluster *kopsapi.Cluster) (*kopsapi.ClusterStatus, error) {
	statusDiscovery := &commands.CloudDiscoveryStatusStore{}
	status, err := statusDiscovery.FindClusterStatus(kopsCluster)
	if err != nil {
		return nil, err
	}
	return status, nil
}

func (kcpms *KopsControlPlaneManagedScope) buildCloud(kopscluster *kopsapi.Cluster) (fi.Cloud, error) {
	cloud, err := cloudup.BuildCloud(kopscluster)
	if err != nil {
		return nil, err
	}

	return cloud, nil
}

// CreateCloudResources renders the terraform files and effectively apply them in the cloud provider
func (kcpms *KopsControlPlaneManagedScope) createCloudResources(kopsClientset simple.Clientset, ctx context.Context, kopsCluster *kopsapi.Cluster, configBase string) error {
	s3Bucket, err := utils.GetBucketName(configBase)
	if err != nil {
		return err
	}

	terraformOutputDir := fmt.Sprintf("/tmp/%s", kopsCluster.Name)

	cloud, err := kcpms.buildCloud(kopsCluster)
	if err != nil {
		return err
	}

	applyCmd := &cloudup.ApplyClusterCmd{
		Cloud:              cloud,
		Clientset:          kopsClientset,
		Cluster:            kopsCluster,
		DryRun:             true,
		AllowKopsDowngrade: false,
		OutDir:             terraformOutputDir,
		TargetName:         "terraform",
	}

	if err := applyCmd.Run(ctx); err != nil {
		return err
	}

	err = utils.CreateTerraformBackendFile(s3Bucket, kopsCluster.Name, terraformOutputDir)
	if err != nil {
		return err
	}

	err = utils.ApplyTerraform(ctx, terraformOutputDir)
	if err != nil {
		return err
	}
	return nil

}

// PopulateClusterSpec populates the full cluster spec with some values it fetchs from provider
func (kcpms *KopsControlPlaneManagedScope) populateClusterSpec(kopsCluster *kopsapi.Cluster, kopsClientset simple.Clientset) (*kopsapi.Cluster, error) {
	cloud, err := kcpms.buildCloud(kopsCluster)
	if err != nil {
		return nil, err
	}

	err = cloudup.PerformAssignments(kopsCluster, cloud)
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
func (r *KopsControlPlaneReconciler) addSSHCredential(kopsCluster *kopsapi.Cluster, SSHPublicKey string) error {
	sshCredential := kopsapi.SSHCredential{
		Spec: kopsapi.SSHCredentialSpec{
			PublicKey: SSHPublicKey,
		},
	}

	sshCredentialStore, err := r.kopsClientset.SSHCredentialStore(kopsCluster)
	if err != nil {
		return err
	}
	sshKeyArr := []byte(sshCredential.Spec.PublicKey)
	err = sshCredentialStore.AddSSHPublicKey("admin", sshKeyArr)
	if err != nil {
		return err
	}

	r.log.Info("added ssh credential")

	return nil
}

// updateKopsState creates or updates the kops state in the remote storage
func (r *KopsControlPlaneReconciler) updateKopsState(ctx context.Context, kopsCluster *kopsapi.Cluster, SSHPublicKey string) error {
	oldCluster, _ := r.kopsClientset.GetCluster(ctx, kopsCluster.Name)
	if oldCluster != nil {
		status, err := r.ManagedScope.getClusterStatus(oldCluster)
		if err != nil {
			return err
		}
		r.kopsClientset.UpdateCluster(ctx, kopsCluster, status)
		r.log.Info(fmt.Sprintf("updated kops state for cluster %s", kopsCluster.ObjectMeta.Name))
		return nil
	}

	_, err := r.kopsClientset.CreateCluster(ctx, kopsCluster)
	if err != nil {
		return err
	}

	err = r.addSSHCredential(kopsCluster, SSHPublicKey)
	if err != nil {
		return err
	}

	r.log.Info(fmt.Sprintf("created kops state for cluster %s", kopsCluster.ObjectMeta.Name))

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

func (r *KopsControlPlaneReconciler) getKubernetesClientFromKopsState(kopsCluster *kopsapi.Cluster) (*kubernetes.Clientset, error) {
	builder := kubeconfig.NewKubeconfigBuilder()

	keyStore, err := r.kopsClientset.KeyStore(kopsCluster)
	if err != nil {
		return nil, err
	}

	builder.Context = kopsCluster.ObjectMeta.Name
	builder.Server = fmt.Sprintf("https://api.%s", kopsCluster.ObjectMeta.Name)
	caCert, _, _, err := keyStore.FindKeypair(fi.CertificateIDCA)
	if err != nil || caCert == nil {
		return nil, err
	}

	builder.CACert, err = caCert.AsBytes()
	if err != nil {
		return nil, err
	}

	req := pki.IssueCertRequest{
		Signer: fi.CertificateIDCA,
		Type:   "client",
		Subject: pkix.Name{
			CommonName:   "kops-operator",
			Organization: []string{rbac.SystemPrivilegedGroup},
		},
		Validity: 64800000000000,
	}
	cert, privateKey, _, err := pki.IssueCert(&req, keyStore)
	if err != nil {
		return nil, err
	}
	builder.ClientCert, err = cert.AsBytes()
	if err != nil {
		return nil, err
	}
	builder.ClientKey, err = privateKey.AsBytes()
	if err != nil {
		return nil, err
	}

	config, err := builder.BuildRestConfig()
	if err != nil {
		return nil, err
	}

	k8sClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return k8sClient, nil
}

func (r *KopsControlPlaneReconciler) validateCluster(ctx context.Context, kopsCluster *kopsapi.Cluster) (*validation.ValidationCluster, error) {
	list, err := r.kopsClientset.InstanceGroupsFor(kopsCluster).List(ctx, metav1.ListOptions{})
	if err != nil || len(list.Items) == 0 {
		return nil, fmt.Errorf("cannot get InstanceGroups for %q: %v", kopsCluster.ObjectMeta.Name, err)
	}

	filteredIGs := &kopsapi.InstanceGroupList{}
	for _, ig := range list.Items {
		if ig.Spec.Role == "Master" {
			filteredIGs.Items = append(filteredIGs.Items, ig)
		}
	}

	k8sClient, err := r.getKubernetesClientFromKopsState(kopsCluster)
	if err != nil {
		return nil, err
	}

	cloud, err := r.ManagedScope.buildCloud(kopsCluster)
	if err != nil {
		return nil, err
	}

	validator, err := validation.NewClusterValidator(kopsCluster, cloud, filteredIGs, fmt.Sprintf("https://api.%s:443", kopsCluster.ObjectMeta.Name), k8sClient)
	if err != nil {
		return nil, fmt.Errorf("unexpected error creating validator: %v", err)
	}

	result, err := validator.Validate()
	if err != nil {
		return nil, fmt.Errorf("%v", err)
	}

	return result, nil
}

//+kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=kopscontrolplanes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=kopscontrolplanes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=kopscontrolplanes/finalizers,verbs=update
func (r *KopsControlPlaneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, rerr error) {
	r.log = ctrl.LoggerFrom(ctx)

	applicableConditions := []clusterv1.ConditionType{
		controlplanev1alpha1.KopsControlPlaneReadyCondition,
	}

	var kopsControlPlane controlplanev1alpha1.KopsControlPlane
	if err := r.Get(ctx, req.NamespacedName, &kopsControlPlane); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Look up the owner of this kopscontrolplane config if there is one
	owner, err := r.getClusterOwnerRef(ctx, &kopsControlPlane)
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
	patchHelper, err := patch.NewHelper(&kopsControlPlane, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Attempt to Patch the KopsControlPlane object and status after each reconciliation if no error occurs.
	defer func() {
		conditions.SetSummary(&kopsControlPlane,
			conditions.WithConditions(
				applicableConditions...,
			),
			conditions.WithStepCounter(),
		)

		err = patchHelper.Patch(ctx, &kopsControlPlane,
			patch.WithOwnedConditions{
				Conditions: applicableConditions,
			},
		)
		if err != nil {
			r.log.Error(rerr, "Failed to patch kopsControlPlane")
			if rerr == nil {
				rerr = err
			}
		}
	}()

	// controllerutil.AddFinalizer(&kopsControlPlane, controlplanev1alpha1.KopsControlPlaneFinalizer)
	err = patchHelper.Patch(ctx, &kopsControlPlane,
		patch.WithOwnedConditions{
			Conditions: applicableConditions,
		},
	)
	if err != nil {
		return ctrl.Result{}, err
	}

	kopsClientset, err := utils.GetKopsClientset(kopsControlPlane.Spec.KopsClusterSpec.ConfigBase)
	if err != nil {
		return ctrl.Result{}, err
	}

	r.kopsClientset = kopsClientset

	kopsCluster := &kopsapi.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: owner.GetName(),
		},
		Spec: kopsControlPlane.Spec.KopsClusterSpec,
	}

	fullCluster, err := r.ManagedScope.populateClusterSpec(kopsCluster, r.kopsClientset)
	if err != nil {
		return ctrl.Result{}, err
	}

	err = r.updateKopsState(ctx, fullCluster, kopsControlPlane.Spec.SSHPublicKey)
	if err != nil {
		r.log.Error(err, fmt.Sprintf("failed to create cluster: %v", err))
		return ctrl.Result{}, err
	}

	err = r.ManagedScope.createCloudResources(r.kopsClientset, ctx, kopsCluster, fullCluster.Spec.ConfigBase)
	if err != nil {
		return ctrl.Result{}, err
	}

	result, err := r.validateCluster(ctx, fullCluster)
	if err != nil {
		return ctrl.Result{}, nil
	}

	if len(result.Failures) == 0 {
		kopsControlPlane.Status.Ready = true
	} else {
		kopsControlPlane.Status.Ready = false
	}

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
