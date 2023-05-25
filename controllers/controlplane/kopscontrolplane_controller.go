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
	"os"
	"sync"
	"time"

	"k8s.io/klog/v2"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/predicates"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	asgTypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/pkg/errors"
	controlplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	infrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"
	kopsutils "github.com/topfreegames/kubernetes-kops-operator/pkg/kops"
	"github.com/topfreegames/kubernetes-kops-operator/pkg/util"
	"github.com/topfreegames/kubernetes-kops-operator/utils"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/tools/record"
	kopsapi "k8s.io/kops/pkg/apis/kops"
	"k8s.io/kops/pkg/assets"
	"k8s.io/kops/pkg/client/simple"
	"k8s.io/kops/pkg/featureflag"
	"k8s.io/kops/pkg/validation"
	"k8s.io/kops/upup/pkg/fi"
	"k8s.io/kops/upup/pkg/fi/cloudup"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/controllers/external"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/kubeconfig"
	"sigs.k8s.io/cluster-api/util/secret"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var (
	requeue1min   = ctrl.Result{RequeueAfter: 1 * time.Minute}
	resultDefault = ctrl.Result{RequeueAfter: 20 * time.Minute}
	resultError   = ctrl.Result{RequeueAfter: 5 * time.Minute}
)

// KopsControlPlaneReconciler reconciles a KopsControlPlane object
type KopsControlPlaneReconciler struct {
	client.Client
	Scheme                       *runtime.Scheme
	mux                          sync.Mutex
	Recorder                     record.EventRecorder
	TfExecPath                   string
	GetKopsClientSetFactory      func(configBase string) (simple.Clientset, error)
	BuildCloudFactory            func(*kopsapi.Cluster) (fi.Cloud, error)
	PopulateClusterSpecFactory   func(kopsCluster *kopsapi.Cluster, kopsClientset simple.Clientset, cloud fi.Cloud) (*kopsapi.Cluster, error)
	PrepareCloudResourcesFactory func(kopsClientset simple.Clientset, kubeClient client.Client, ctx context.Context, kopsCluster *kopsapi.Cluster, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, configBase, terraformOutputDir string, cloud fi.Cloud, shouldIgnoreSG bool, credentials *aws.Credentials) error
	ApplyTerraformFactory        func(ctx context.Context, terraformDir, tfExecPath string, credentials aws.Credentials) error
	ValidateKopsClusterFactory   func(kubeConfig *rest.Config, kopsCluster *kopsapi.Cluster, cloud fi.Cloud, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error)
	GetClusterStatusFactory      func(kopsCluster *kopsapi.Cluster, cloud fi.Cloud) (*kopsapi.ClusterStatus, error)
	GetASGByNameFactory          func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, credentials *aws.CredentialsCache) (*asgTypes.AutoScalingGroup, error)
}

func init() {
	// Set kops lib verbosity to ERROR
	var log logr.Logger
	zc := zap.NewProductionConfig()
	zc.Level = zap.NewAtomicLevelAt(zapcore.Level(2))
	z, err := zc.Build()
	if err != nil {
		panic(fmt.Sprintf("failed to initialize logging (%v)?", err))
	}
	log = zapr.NewLogger(z)
	klog.SetLogger(log)
}

func ApplyTerraform(ctx context.Context, terraformDir, tfExecPath string, credentials aws.Credentials) error {
	err := utils.ApplyTerraform(ctx, terraformDir, tfExecPath, credentials)
	if err != nil {
		return err
	}
	return nil
}

func GetClusterStatus(kopsCluster *kopsapi.Cluster, cloud fi.Cloud) (*kopsapi.ClusterStatus, error) {
	status, err := cloud.FindClusterStatus(kopsCluster)
	if err != nil {
		return nil, err
	}
	return status, nil
}

// PrepareCloudResources renders the terraform files and effectively apply them in the cloud provider
func PrepareCloudResources(kopsClientset simple.Clientset, kubeClient client.Client, ctx context.Context, kopsCluster *kopsapi.Cluster, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, configBase, terraformOutputDir string, cloud fi.Cloud, shouldIgnoreSG bool, credentials *aws.Credentials) error {

	s3Bucket, err := utils.GetBucketName(configBase)
	if err != nil {
		return err
	}

	err = os.MkdirAll(terraformOutputDir, 0755)
	if err != nil {
		return err
	}

	backendTemplate := struct {
		Bucket      string
		ClusterName string
	}{
		s3Bucket,
		kopsCluster.Name,
	}

	err = utils.CreateTerraformFilesFromTemplate("templates/backend.tf.tpl", "backend.tf", terraformOutputDir, backendTemplate)
	if err != nil {
		return err
	}

	kmps, err := kopsutils.GetKopsMachinePoolsWithLabel(ctx, kubeClient, "cluster.x-k8s.io/cluster-name", kopsControlPlane.Name)
	if err != nil {
		return err
	}

	// TODO: Refactor to assert if spot is enabled in a better way
	if kopsControlPlane.Spec.SpotInst.Enabled {
		for _, kmp := range kmps {
			if _, ok := kmp.Spec.SpotInstOptions["spotinst.io/hybrid"]; ok {
				err = utils.CreateTerraformFilesFromTemplate("templates/spotinst_ocean_aws_override.tf.tpl", "spotinst_ocean_aws_override.tf", terraformOutputDir, kopsCluster.Name)
				if err != nil {
					return err
				}
				break
			}
		}
	}

	if shouldIgnoreSG {
		asgNames := []string{}
		vngNames := []string{}
		for _, kmp := range kmps {
			if _, ok := kmp.Spec.SpotInstOptions["spotinst.io/hybrid"]; ok {
				if kmp.Spec.SpotInstOptions["spotinst.io/hybrid"] == "true" {
					vngName, err := kopsutils.GetCloudResourceNameFromKopsMachinePool(kmp)
					if err != nil {
						return err
					}
					vngNames = append(vngNames, vngName)
				}
			} else {
				asgName, err := kopsutils.GetCloudResourceNameFromKopsMachinePool(kmp)
				if err != nil {
					return err
				}
				asgNames = append(asgNames, asgName)
			}
		}

		if len(asgNames) > 0 {
			err = utils.CreateTerraformFilesFromTemplate("templates/launch_template_override.tf.tpl", "launch_template_override.tf", terraformOutputDir, asgNames)
			if err != nil {
				return err
			}
		}

		if len(vngNames) > 0 {
			err = utils.CreateTerraformFilesFromTemplate("templates/spotinst_launch_spec_override.tf.tpl", "spotinst_launch_spec_override.tf", terraformOutputDir, vngNames)
			if err != nil {
				return err
			}
		}
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

	stdout := os.Stdout
	defer func() {
		os.Stdout = stdout
	}()
	os.Stdout = nil
	if err := applyCmd.Run(ctx); err != nil {
		return err
	}

	return nil

}

// createOrUpdateKopsCluster creates or updates the kops state in the remote storage
func (r *KopsControlPlaneReconciler) createOrUpdateKopsCluster(ctx context.Context, kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, SSHPublicKey string, cloud fi.Cloud) error {
	log := ctrl.LoggerFrom(ctx)
	oldCluster, err := kopsClientset.GetCluster(ctx, kopsCluster.Name)
	if apierrors.IsNotFound(err) {
		_, err = kopsClientset.CreateCluster(ctx, kopsCluster)
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
	if err != nil {
		return err
	}

	status, err := r.GetClusterStatusFactory(oldCluster, cloud)
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

// PopulateClusterSpec populates the full cluster spec with some values it fetchs from provider
func PopulateClusterSpec(kopsCluster *kopsapi.Cluster, kopsClientset simple.Clientset, cloud fi.Cloud) (*kopsapi.Cluster, error) {

	err := cloudup.PerformAssignments(kopsCluster, cloud)
	if err != nil {
		return nil, err
	}

	assetBuilder := assets.NewAssetBuilder(kopsCluster, true)
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
	err = sshCredentialStore.AddSSHPublicKey(sshKeyArr)
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

func (r *KopsControlPlaneReconciler) reconcileKubeconfig(ctx context.Context, kubeConfig *rest.Config, log logr.Logger, kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, cluster *unstructured.Unstructured) error {

	clusterName := cluster.GetName()

	cfg := &api.Config{
		Clusters: map[string]*api.Cluster{
			clusterName: {
				Server:                   kubeConfig.Host,
				CertificateAuthorityData: kubeConfig.CAData,
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
				ClientCertificateData: kubeConfig.CertData,
				ClientKeyData:         kubeConfig.KeyData,
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
		err := r.Client.Update(ctx, kubeconfigSecret)
		if err != nil {
			return errors.Wrap(err, "failed updating kubeconfig secret")
		}
	}

	return nil
}

//+kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=kopscontrolplanes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=kopscontrolplanes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=kopscontrolplanes/finalizers,verbs=update
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kopsmachinepools,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kopsmachinepools/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kopsmachinepools/finalizers,verbs=update
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters/finalizers,verbs=update
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters/status,verbs=get;update;patch
//+kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;patch
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update

func (r *KopsControlPlaneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, rerr error) {
	log := ctrl.LoggerFrom(ctx)

	initTime := time.Now()
	log.Info("BATATA - Reconcile start")
	kopsControlPlane := &controlplanev1alpha1.KopsControlPlane{}
	if err := r.Get(ctx, req.NamespacedName, kopsControlPlane); err != nil {
		return resultError, client.IgnoreNotFound(err)
	}

	owner, err := r.getClusterOwnerRef(ctx, kopsControlPlane)
	if err != nil || owner == nil {
		if apierrors.IsNotFound(err) {
			log.Info("cluster does not exist yet, re-queueing until it is created")
			return requeue1min, nil
		}
		if err == nil && owner == nil {
			log.Info(fmt.Sprintf("kopscontrolplane/%s does not belong to a cluster yet, waiting until it's part of a cluster", kopsControlPlane.ObjectMeta.Name))

			return requeue1min, nil
		}
		log.Error(err, "could not get cluster with metadata")
		return resultError, err
	}

	kmps, err := kopsutils.GetKopsMachinePoolsWithLabel(ctx, r.Client, "cluster.x-k8s.io/cluster-name", kopsControlPlane.Name)
	if err != nil {
		return resultError, err
	}

	// Attempt to Update the KopsControlPlane and KopsMachinePool object and status after each reconciliation if no error occurs.
	defer func() {
		kopsControlPlaneHelper := kopsControlPlane.DeepCopy()
		if err := r.Update(ctx, kopsControlPlane); err != nil {
			log.Error(rerr, fmt.Sprintf("failed to update kopsControlPlane %s", kopsControlPlane.Name))
		}

		kopsControlPlane.Status = kopsControlPlaneHelper.Status
		if err := r.Status().Update(ctx, kopsControlPlane); err != nil {
			log.Error(rerr, fmt.Sprintf("failed to update kopsControlPlane %s status", kopsControlPlane.Name))
		}

		for _, kopsMachinePool := range kmps {
			kopsMachinePoolHelper := kopsMachinePool.DeepCopy()
			if err := r.Update(ctx, &kopsMachinePool); err != nil {
				log.Error(rerr, fmt.Sprintf("failed to update kopsMachinePool %s", kopsMachinePool.Name))
			}

			kopsMachinePool.Status = kopsMachinePoolHelper.Status
			if err := r.Status().Update(ctx, &kopsMachinePool); err != nil {
				log.Error(rerr, fmt.Sprintf("failed to update kopsMachinePool %s status", kopsMachinePool.Name))
			}
		}
		log.Info(fmt.Sprintf("finished reconcile loop for %s", kopsControlPlane.ObjectMeta.GetName()))
		log.Info("BATATA - Reconcile finished, took %s", time.Since(initTime))
	}()

	if annotations.HasPaused(owner) {
		log.Info(fmt.Sprintf("reconciliation is paused for kcp %s since cluster %s is paused", kopsControlPlane.ObjectMeta.Name, owner.GetName()))
		kopsControlPlane.Status.Paused = true
		return resultDefault, nil
	}
	kopsControlPlane.Status.Paused = false

	lockInitTime := time.Now()
	r.mux.Lock()
	log.Info(fmt.Sprintf("LOCK %s %s", kopsControlPlane.Name, lockInitTime))

	log.Info(fmt.Sprintf("starting reconcile loop for %s", kopsControlPlane.ObjectMeta.GetName()))

	err = util.SetAWSEnvFromKopsControlPlaneSecret(ctx, r.Client, kopsControlPlane.Spec.IdentityRef.Name, kopsControlPlane.Spec.IdentityRef.Namespace)
	if err != nil {
		log.Error(rerr, "failed to set AWS envs")
		return resultError, err
	}

	kopsClientset, err := r.GetKopsClientSetFactory(kopsControlPlane.Spec.KopsClusterSpec.ConfigBase)
	if err != nil {
		return resultError, err
	}

	for i, kopsMachinePool := range kmps {
		err = r.reconcileKopsMachinePool(ctx, log, kopsClientset, kopsControlPlane, &kmps[i])
		if err != nil {
			r.Recorder.Eventf(&kopsMachinePool, corev1.EventTypeWarning, "KopsMachinePoolReconcileFailed", err.Error())
		} else {
			r.Recorder.Eventf(&kopsMachinePool, corev1.EventTypeNormal, "KopsMachinePoolReconcileSuccess", kopsMachinePool.Name)
		}
	}

	kopsCluster := &kopsapi.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: owner.GetName(),
		},
		Spec: kopsControlPlane.Spec.KopsClusterSpec,
	}

	featureflag.ParseFlags("-Karpenter")
	if kopsControlPlane.Spec.KopsClusterSpec.Karpenter != nil {
		if kopsControlPlane.Spec.KopsClusterSpec.Karpenter.Enabled {
			featureflag.ParseFlags("Karpenter")
		}
	}

	err = utils.ParseSpotinstFeatureflags(kopsControlPlane)
	if err != nil {
		return resultError, err
	}

	// TODO: create a lock for multiple workers
	cloud, err := r.BuildCloudFactory(kopsCluster)
	if err != nil {
		log.Error(rerr, "failed to build cloud")
		return resultError, err
	}

	fullCluster, err := r.PopulateClusterSpecFactory(kopsCluster, kopsClientset, cloud)
	if err != nil {
		log.Error(rerr, "failed to populated cluster Spec")
		return resultError, err
	}

	err = r.createOrUpdateKopsCluster(ctx, kopsClientset, fullCluster, kopsControlPlane.Spec.SSHPublicKey, cloud)
	if err != nil {
		log.Error(err, fmt.Sprintf("failed to manage kops state: %v", err))
		conditions.MarkFalse(kopsControlPlane, controlplanev1alpha1.KopsControlPlaneStateReadyCondition, controlplanev1alpha1.KopsControlPlaneStateReconciliationFailedReason, clusterv1.ConditionSeverityError, err.Error())
		return resultError, err
	}
	conditions.MarkTrue(kopsControlPlane, controlplanev1alpha1.KopsControlPlaneStateReadyCondition)

	if kopsControlPlane.Spec.KopsSecret != nil {
		secretStore, err := kopsClientset.SecretStore(kopsCluster)
		if err != nil {
			return resultError, err
		}

		err = utils.ReconcileKopsSecrets(ctx, r.Client, secretStore, kopsControlPlane, client.ObjectKey{
			Name:      kopsControlPlane.Spec.KopsSecret.Name,
			Namespace: kopsControlPlane.Spec.KopsSecret.Namespace,
		})
		if err != nil {
			conditions.MarkFalse(kopsControlPlane, controlplanev1alpha1.KopsControlPlaneSecretsReadyCondition, controlplanev1alpha1.KopsControlPlaneSecretsReconciliationFailedReason, clusterv1.ConditionSeverityWarning, err.Error())
		}
		conditions.MarkTrue(kopsControlPlane, controlplanev1alpha1.KopsControlPlaneSecretsReadyCondition)
	}

	terraformOutputDir := fmt.Sprintf("/tmp/%s", kopsCluster.Name)

	credentialsCache, err := util.GetAwsCredentialsFromKopsControlPlaneSecret(ctx, r.Client, kopsControlPlane.Spec.IdentityRef.Name, kopsControlPlane.Spec.IdentityRef.Namespace)
	if err != nil {
		return resultError, err
	}

	credentials, err := credentialsCache.Retrieve(ctx)
	if err != nil {
		return resultError, err
	}

	var shouldIgnoreSG bool
	if _, ok := owner.GetAnnotations()["kopscontrolplane.controlplane.wildlife.io/external-security-groups"]; ok {
		shouldIgnoreSG = true
	}

	err = r.PrepareCloudResourcesFactory(kopsClientset, r.Client, ctx, kopsCluster, kopsControlPlane, fullCluster.Spec.ConfigBase, terraformOutputDir, cloud, shouldIgnoreSG, &credentials)
	if err != nil {
		conditions.MarkFalse(kopsControlPlane, controlplanev1alpha1.KopsTerraformGenerationReadyCondition, controlplanev1alpha1.KopsTerraformGenerationReconciliationFailedReason, clusterv1.ConditionSeverityError, err.Error())
		log.Error(err, fmt.Sprintf("failed to prepare cloud resources: %v", err))
		return resultError, err
	}
	conditions.MarkTrue(kopsControlPlane, controlplanev1alpha1.KopsTerraformGenerationReadyCondition)

	// TODO: This is needed because we are using a method from kops lib, we should be
	// we should check alternatives
	kubeConfig, err := utils.GetKubeconfigFromKopsState(kopsCluster, kopsClientset)
	if err != nil {
		return resultError, err
	}

	err = r.reconcileKubeconfig(ctx, kubeConfig, log, kopsClientset, kopsCluster, owner)
	if err != nil {
		log.Error(rerr, "failed to reconcile kubeconfig")
		return resultError, err
	}

	lockFinishTime := time.Now()
	r.mux.Unlock()
	log.Info(fmt.Sprintf("UNLOCK for %s %s", kopsControlPlane.Name, lockFinishTime))
	log.Info("BATATA - Lock step took %s", lockFinishTime.Sub(lockInitTime))

	err = r.ApplyTerraformFactory(ctx, terraformOutputDir, r.TfExecPath, credentials)
	if err != nil {
		conditions.MarkFalse(kopsControlPlane, controlplanev1alpha1.TerraformApplyReadyCondition, controlplanev1alpha1.TerraformApplyReconciliationFailedReason, clusterv1.ConditionSeverityError, err.Error())
		log.Error(err, fmt.Sprintf("failed to apply terraform: %v", err))
		return resultError, err
	}
	conditions.MarkTrue(kopsControlPlane, controlplanev1alpha1.TerraformApplyReadyCondition)

	err = r.updateKopsMachinePoolWithProviderIDList(ctx, log, kopsControlPlane, kmps, credentialsCache)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return requeue1min, nil
		} else {
			return resultError, err
		}
	}

	igList, err := kopsClientset.InstanceGroupsFor(kopsCluster).List(ctx, metav1.ListOptions{})
	if err != nil || len(igList.Items) == 0 {
		return resultError, fmt.Errorf("cannot get InstanceGroups for %q: %v", kopsCluster.ObjectMeta.Name, err)
	}

	val, err := r.ValidateKopsClusterFactory(kubeConfig, kopsCluster, cloud, igList)

	if err != nil {
		log.Error(err, fmt.Sprintf("failed trying to validate Kubernetes cluster: %v", err))
		r.Recorder.Eventf(kopsControlPlane, corev1.EventTypeWarning, "KubernetesClusterValidationFailed", err.Error())
		return resultError, err
	}

	statusReady, err := utils.KopsClusterValidation(kopsControlPlane, r.Recorder, log, val)
	if err != nil {
		return resultError, err
	}
	kopsControlPlane.Status.Ready = statusReady
	return resultDefault, nil
}

func (r *KopsControlPlaneReconciler) updateKopsMachinePoolWithProviderIDList(ctx context.Context, log logr.Logger, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, kmps []infrastructurev1alpha1.KopsMachinePool, credentials *aws.CredentialsCache) error {
	for i, kopsMachinePool := range kmps {
		// TODO: retrieve karpenter providerIDList
		if len(kopsMachinePool.Spec.SpotInstOptions) == 0 && kopsMachinePool.Spec.KopsInstanceGroupSpec.Manager != "Karpenter" {
			asg, err := r.GetASGByNameFactory(&kopsMachinePool, kopsControlPlane, credentials)
			if err != nil {
				if apierrors.IsNotFound(err) {
					log.Info("ASG not created yet, requeue after 1 minute")
					kmps[i].Status.Ready = false
					return err
				}
				log.Error(err, fmt.Sprintf("failed retriving ASG: %v", err))
				kmps[i].Status.Ready = false
				return err
			}

			providerIDList := make([]string, len(asg.Instances))
			for i, instance := range asg.Instances {
				providerIDList[i] = fmt.Sprintf("aws:///%s/%s", *instance.AvailabilityZone, *instance.InstanceId)
			}
			kmps[i].Spec.ProviderIDList = providerIDList
			kmps[i].Status.Replicas = int32(len(providerIDList))
			kmps[i].Status.Ready = true
		}
	}
	return nil
}

func (r *KopsControlPlaneReconciler) reconcileKopsMachinePool(ctx context.Context, log logr.Logger, kopsClientset simple.Clientset, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, kopsMachinePool *infrastructurev1alpha1.KopsMachinePool) error {
	// Ensure correct NodeLabel for the IG
	if kopsMachinePool.Spec.KopsInstanceGroupSpec.NodeLabels != nil {
		kopsMachinePool.Spec.KopsInstanceGroupSpec.NodeLabels["kops.k8s.io/instance-group-name"] = kopsMachinePool.Name
	} else {
		kopsMachinePool.Spec.KopsInstanceGroupSpec.NodeLabels = map[string]string{
			"kops.k8s.io/instance-group-name": kopsMachinePool.Name,
		}
	}
	kopsInstanceGroup := &kopsapi.InstanceGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kopsMachinePool.Name,
			Namespace: kopsMachinePool.ObjectMeta.Namespace,
			Labels:    kopsMachinePool.Spec.SpotInstOptions,
		},
		Spec: kopsMachinePool.Spec.KopsInstanceGroupSpec,
	}
	kopsCluster := &kopsapi.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: kopsMachinePool.Spec.ClusterName,
		},
		Spec: kopsControlPlane.Spec.KopsClusterSpec,
	}
	err := r.createOrUpdateInstanceGroup(ctx, log, kopsClientset, kopsCluster, kopsInstanceGroup)
	if err != nil {
		conditions.MarkFalse(kopsMachinePool, infrastructurev1alpha1.KopsMachinePoolStateReadyCondition, infrastructurev1alpha1.KopsMachinePoolStateReconciliationFailedReason, clusterv1.ConditionSeverityError, err.Error())
		return err
	}
	conditions.MarkTrue(kopsMachinePool, infrastructurev1alpha1.KopsMachinePoolStateReadyCondition)

	return nil
}

// GetASGByName returns the existing ASG or nothing if it doesn't exist.
func GetASGByName(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, credentials *aws.CredentialsCache) (*asgTypes.AutoScalingGroup, error) {
	ctx := context.TODO()
	region, err := regionBySubnet(kopsControlPlane)
	if err != nil {
		return nil, err
	}
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials),
	)
	if err != nil {
		return nil, err
	}

	svc := autoscaling.NewFromConfig(cfg)

	asgName, err := kopsutils.GetCloudResourceNameFromKopsMachinePool(*kopsMachinePool)
	if err != nil {
		return nil, err
	}

	input := &autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []string{
			asgName,
		},
	}

	out, err := svc.DescribeAutoScalingGroups(ctx, input)
	if err != nil {
		var invalidNextToken *asgTypes.InvalidNextToken
		if errors.As(err, &invalidNextToken) {
			return nil, fmt.Errorf(err.Error(), invalidNextToken)
		}
		var resourceContentionFault *asgTypes.ResourceContentionFault
		if errors.As(err, &resourceContentionFault) {
			return nil, fmt.Errorf(err.Error(), resourceContentionFault)
		}
		return nil, fmt.Errorf(err.Error(), err.Error())
	}
	if len(out.AutoScalingGroups) > 0 {
		return &out.AutoScalingGroups[0], nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{}, "ASG not ready")
}

func regionBySubnet(kopsControlPlane *controlplanev1alpha1.KopsControlPlane) (string, error) {
	subnets := kopsControlPlane.Spec.KopsClusterSpec.Subnets
	if len(subnets) == 0 {
		return "", errors.New("kopsControlPlane with no subnets")
	}

	zone := subnets[0].Zone

	return zone[:len(zone)-1], nil
}

// createOrUpdateInstanceGroup create or update the instance group in kops state
func (r *KopsControlPlaneReconciler) createOrUpdateInstanceGroup(ctx context.Context, log logr.Logger, kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, kopsInstanceGroup *kopsapi.InstanceGroup) error {

	instanceGroupName := kopsInstanceGroup.ObjectMeta.Name
	_, err := kopsClientset.InstanceGroupsFor(kopsCluster).Get(ctx, instanceGroupName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = kopsClientset.InstanceGroupsFor(kopsCluster).Create(ctx, kopsInstanceGroup, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("error creating instanceGroup: %v", err)
		}
		log.Info(fmt.Sprintf("created instancegroup/%s", instanceGroupName))
		return nil
	}
	if err != nil {
		return err
	}

	_, err = kopsClientset.InstanceGroupsFor(kopsCluster).Update(ctx, kopsInstanceGroup, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("error updating instanceGroup: %v", err)
	}
	log.Info(fmt.Sprintf("updated instancegroup/%s", instanceGroupName))

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *KopsControlPlaneReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&controlplanev1alpha1.KopsControlPlane{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 5}).
		WithEventFilter(predicates.ResourceNotPaused(ctrl.LoggerFrom(ctx))).
		Watches(
			&source.Kind{Type: &clusterv1.Cluster{}},
			handler.EnqueueRequestsFromMapFunc(clusterToInfrastructureMapFunc),
		).
		Watches(
			&source.Kind{Type: &infrastructurev1alpha1.KopsMachinePool{}},
			handler.EnqueueRequestsFromMapFunc(r.kopsMachinePoolToInfrastructureMapFunc),
		).
		Complete(r)
}

func clusterToInfrastructureMapFunc(o client.Object) []ctrl.Request {
	c, ok := o.(*clusterv1.Cluster)
	if !ok {
		panic(fmt.Sprintf("Expected a Cluster but got a %T", o))
	}

	var result []ctrl.Request
	if c.Spec.InfrastructureRef != nil && c.Spec.InfrastructureRef.GroupVersionKind() == controlplanev1alpha1.GroupVersion.WithKind("KopsControlPlane") {
		name := client.ObjectKey{Namespace: c.Spec.InfrastructureRef.Namespace, Name: c.Spec.InfrastructureRef.Name}
		result = append(result, ctrl.Request{NamespacedName: name})
	}

	return result
}

func (r *KopsControlPlaneReconciler) kopsMachinePoolToInfrastructureMapFunc(o client.Object) []ctrl.Request {
	kmp, ok := o.(*infrastructurev1alpha1.KopsMachinePool)
	if !ok {
		panic(fmt.Sprintf("expected a KopsMachinePool but got a %T", o))
	}

	var result []ctrl.Request
	cluster, err := util.GetClusterByName(context.TODO(), r.Client, kmp.GetNamespace(), kmp.Spec.ClusterName)
	if err != nil {
		panic(err)
	}
	if cluster.Spec.InfrastructureRef != nil && cluster.Spec.InfrastructureRef.GroupVersionKind() == controlplanev1alpha1.GroupVersion.WithKind("KopsControlPlane") {
		name := client.ObjectKey{Namespace: cluster.Spec.InfrastructureRef.Namespace, Name: cluster.Spec.InfrastructureRef.Name}
		result = append(result, ctrl.Request{NamespacedName: name})
	}

	return result
}
