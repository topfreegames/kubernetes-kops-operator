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
	"crypto/sha256"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
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
	"k8s.io/klog/v2"
	kopsapi "k8s.io/kops/pkg/apis/kops"
	"k8s.io/kops/pkg/assets"
	"k8s.io/kops/pkg/client/simple"
	"k8s.io/kops/pkg/featureflag"
	"k8s.io/kops/pkg/validation"
	"k8s.io/kops/upup/pkg/fi"
	"k8s.io/kops/upup/pkg/fi/cloudup"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/controllers/external"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/kubeconfig"
	"sigs.k8s.io/cluster-api/util/predicates"
	"sigs.k8s.io/cluster-api/util/secret"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"sigs.k8s.io/yaml"
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
	Mux                          *sync.Mutex
	Recorder                     record.EventRecorder
	TfExecPath                   string
	GetKopsClientSetFactory      func(configBase string) (simple.Clientset, error)
	BuildCloudFactory            func(*kopsapi.Cluster) (fi.Cloud, error)
	PopulateClusterSpecFactory   func(kopsCluster *kopsapi.Cluster, kopsClientset simple.Clientset, cloud fi.Cloud) (*kopsapi.Cluster, error)
	PrepareCloudResourcesFactory func(kopsClientset simple.Clientset, kubeClient client.Client, ctx context.Context, kopsCluster *kopsapi.Cluster, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, kmps []infrastructurev1alpha1.KopsMachinePool, shouldEnableKarpenter bool, configBase, terraformOutputDir string, cloud fi.Cloud, shouldIgnoreSG bool, credentials *aws.Credentials) error
	ApplyTerraformFactory        func(ctx context.Context, terraformDir, tfExecPath string, credentials aws.Credentials) error
	ValidateKopsClusterFactory   func(kubeConfig *rest.Config, kopsCluster *kopsapi.Cluster, cloud fi.Cloud, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error)
	GetClusterStatusFactory      func(kopsCluster *kopsapi.Cluster, cloud fi.Cloud) (*kopsapi.ClusterStatus, error)
	GetASGByNameFactory          func(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, credentials *aws.Credentials) (*asgTypes.AutoScalingGroup, error)
}

type KopsControlPlaneReconciliation struct {
	KopsControlPlaneReconciler
	log            logr.Logger
	start          time.Time
	awsCredentials aws.Credentials
	kcp            *controlplanev1alpha1.KopsControlPlane
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
func PrepareCloudResources(kopsClientset simple.Clientset, kubeClient client.Client, ctx context.Context, kopsCluster *kopsapi.Cluster, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, kmps []infrastructurev1alpha1.KopsMachinePool, shouldEnableKarpenter bool, configBase, terraformOutputDir string, cloud fi.Cloud, shouldIgnoreSG bool, credentials *aws.Credentials) error {

	s3Bucket, err := utils.GetBucketName(configBase)
	if err != nil {
		return err
	}

	err = os.MkdirAll(terraformOutputDir+"/data", 0755)
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

	if shouldEnableKarpenter {
		karpenterProvisionersContent, err := os.Create(terraformOutputDir + "/data/aws_s3_object_karpenter_provisioners_content")
		if err != nil {
			return err
		}
		defer karpenterProvisionersContent.Close()

		for _, kmp := range kmps {
			for _, provisioner := range kmp.Spec.KarpenterProvisioners {
				if _, err := karpenterProvisionersContent.Write([]byte("---\n")); err != nil {
					return err
				}
				output, err := yaml.Marshal(provisioner)
				if err != nil {
					return err
				}
				if _, err := karpenterProvisionersContent.Write(output); err != nil {
					return err
				}
			}
		}
		fileData, err := os.ReadFile(karpenterProvisionersContent.Name())
		if err != nil {
			return err
		}
		contentHash := fmt.Sprintf("%x", sha256.Sum256(fileData))

		karpenterTemplate := struct {
			Bucket       string
			ClusterName  string
			ManifestHash string
		}{
			s3Bucket,
			kopsCluster.Name,
			contentHash,
		}

		err = utils.CreateTerraformFilesFromTemplate("templates/karpenter_custom_addon_boostrap.tf.tpl", "karpenter_custom_addon_boostrap.tf", terraformOutputDir, karpenterTemplate)
		if err != nil {
			return err
		}

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

func (r *KopsControlPlaneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var lockInitTime time.Time
	var shouldIUnlock bool

	log := ctrl.LoggerFrom(ctx)

	initTime := time.Now()
	kopsControlPlane := &controlplanev1alpha1.KopsControlPlane{}
	if err := r.Get(ctx, req.NamespacedName, kopsControlPlane); err != nil {
		return resultError, client.IgnoreNotFound(err)
	}

	awsCredentials, err := util.GetAWSCredentialsFromKopsControlPlaneSecret(ctx, r.Client, kopsControlPlane.Spec.IdentityRef.Name, kopsControlPlane.Spec.IdentityRef.Namespace)
	if err != nil {
		log.Error(err, "failed to get AWS credentials")
		return resultError, err
	}

	reconciler := &KopsControlPlaneReconciliation{
		KopsControlPlaneReconciler: *r,
		log:                        log,
		start:                      time.Now(),
		awsCredentials:             *awsCredentials,
		kcp:                        kopsControlPlane,
	}

	owner, err := reconciler.getClusterOwnerRef(ctx, kopsControlPlane)
	if err != nil || owner == nil {
		if apierrors.IsNotFound(err) {
			reconciler.Recorder.Event(kopsControlPlane, corev1.EventTypeNormal, "ClusterDoesNotExistYet", "cluster does not exist yet, re-queueing until it is created")
			return requeue1min, nil
		}
		if err == nil && owner == nil {
			reconciler.Recorder.Eventf(kopsControlPlane, corev1.EventTypeNormal, "NoClusterYet", "kopscontrolplane/%s does not belong to a cluster yet, waiting until it's part of a cluster", kopsControlPlane.ObjectMeta.Name)
			return requeue1min, nil
		}
		reconciler.Recorder.Eventf(kopsControlPlane, corev1.EventTypeWarning, "FailedToGetClusterMetadata", "could not get cluster with metadata: %s", err)
		return resultError, err
	}

	kmps, err := kopsutils.GetKopsMachinePoolsWithLabel(ctx, reconciler.Client, "cluster.x-k8s.io/cluster-name", kopsControlPlane.Name)
	if err != nil {
		return resultError, err
	}

	// Attempt to Update the KopsControlPlane and KopsMachinePool object and status after each reconciliation if no error occurs.
	defer func() {
		if shouldIUnlock {
			reconciler.Mux.Unlock()
			log.Info(fmt.Sprintf("unexpected Unlock step for %s, took %s", kopsControlPlane.Name, time.Since(lockInitTime)))
		}
		failedToUpdateKCPReason := "FailedToUpdate"

		kopsControlPlaneHelper := kopsControlPlane.DeepCopy()
		if err := reconciler.Update(ctx, kopsControlPlane); err != nil {
			reconciler.Recorder.Eventf(kopsControlPlane, corev1.EventTypeWarning, failedToUpdateKCPReason, "failed to update kopsControlPlane: %s", err)
		}

		kopsControlPlane.Status = kopsControlPlaneHelper.Status
		if err := reconciler.Status().Update(ctx, kopsControlPlane); err != nil {
			r.Recorder.Eventf(kopsControlPlane, corev1.EventTypeWarning, failedToUpdateKCPReason, "failed to update kopsControlPlane: %s", err)
		}

		for _, kopsMachinePool := range kmps {
			kopsMachinePoolHelper := kopsMachinePool.DeepCopy()
			if err := reconciler.Update(ctx, &kopsMachinePool); err != nil {
				r.Recorder.Eventf(kopsControlPlane, corev1.EventTypeWarning, failedToUpdateKCPReason, "failed to update kopsControlPlane: %s", err)
			}

			kopsMachinePool.Status = kopsMachinePoolHelper.Status
			if err := reconciler.Status().Update(ctx, &kopsMachinePool); err != nil {
				r.Recorder.Eventf(kopsControlPlane, corev1.EventTypeWarning, failedToUpdateKCPReason, "failed to update kopsControlPlane: %s", err)
			}
		}

		log.Info(fmt.Sprintf("finished reconcile loop for %s, took %s", kopsControlPlane.ObjectMeta.GetName(), time.Since(initTime)))
	}()

	if annotations.HasPaused(owner) {
		reconciler.Recorder.Eventf(kopsControlPlane, corev1.EventTypeNormal, "ClusterPaused", "reconciliation is paused since cluster %s is paused", owner.GetName())
		kopsControlPlane.Status.Paused = true
		return resultDefault, nil
	}
	kopsControlPlane.Status.Paused = false

	reconciler.Mux.Lock()
	shouldIUnlock = true

	log.Info(fmt.Sprintf("starting reconcile loop for %s", kopsControlPlane.ObjectMeta.GetName()))
	reconciler.Recorder.Event(kopsControlPlane, corev1.EventTypeNormal, "ReconciliationStarted", "reconciliation started")

	err = util.SetEnvVarsFromAWSCredentials(reconciler.awsCredentials)
	if err != nil {
		reconciler.Recorder.Eventf(kopsControlPlane, corev1.EventTypeWarning, "FailedToSetAWSEnvVars", "failed to set AWS environment variables: %s", err)
		return resultError, err
	}

	kopsClientset, err := reconciler.GetKopsClientSetFactory(kopsControlPlane.Spec.KopsClusterSpec.ConfigBase)
	if err != nil {
		reconciler.Recorder.Eventf(kopsControlPlane, corev1.EventTypeWarning, "FailedInstantiateKopsClient", "failed to instantiate Kops client: %s", err)
		return resultError, err
	}

	shouldEnableKarpenter := false
	for i, kopsMachinePool := range kmps {
		err = reconciler.reconcileKopsMachinePool(ctx, log, kopsClientset, kopsControlPlane, &kmps[i])
		if err != nil {
			reconciler.Recorder.Eventf(&kopsMachinePool, corev1.EventTypeWarning, "KopsMachinePoolReconcileFailed", err.Error())
		} else {
			reconciler.Recorder.Eventf(&kopsMachinePool, corev1.EventTypeNormal, "KopsMachinePoolReconcileSuccess", kopsMachinePool.Name)
		}
		if len(kopsMachinePool.Spec.KarpenterProvisioners) > 0 {
			shouldEnableKarpenter = true
		}
	}

	if shouldEnableKarpenter {
		kopsControlPlane.Spec.KopsClusterSpec.Addons = []kopsapi.AddonSpec{
			{
				Manifest: kopsControlPlane.Spec.KopsClusterSpec.ConfigBase + "/custom-addons/addon.yaml",
			},
		}
	}

	kopsCluster := &kopsapi.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: owner.GetName(),
		},
		Spec: kopsControlPlane.Spec.KopsClusterSpec,
	}

	featureflag.ParseFlags("-Karpenter")
	if reconciler.kcp.Spec.KopsClusterSpec.Karpenter != nil {
		if reconciler.kcp.Spec.KopsClusterSpec.Karpenter.Enabled {
			featureflag.ParseFlags("Karpenter")
		}
	}

	err = utils.ParseSpotinstFeatureflags(kopsControlPlane)
	if err != nil {
		reconciler.Recorder.Eventf(kopsControlPlane, corev1.EventTypeWarning, "FailedToParseKopsFeatureFlags", "failed to parse Kops feature flags: %s", err)
		return resultError, err
	}

	cloud, err := reconciler.BuildCloudFactory(kopsCluster)
	if err != nil {
		reconciler.Recorder.Eventf(kopsControlPlane, corev1.EventTypeWarning, "FailedToBuildCloudConfig", "failed to build Cloud Config: %s", err)
		return resultError, err
	}

	fullCluster, err := reconciler.PopulateClusterSpecFactory(kopsCluster, kopsClientset, cloud)
	if err != nil {
		reconciler.Recorder.Eventf(kopsControlPlane, corev1.EventTypeWarning, "FailedToPopulateClusterSpec", "failed to populate Cluster spec: %s", err)
		return resultError, err
	}

	err = reconciler.createOrUpdateKopsCluster(ctx, kopsClientset, fullCluster, kopsControlPlane.Spec.SSHPublicKey, cloud)
	if err != nil {
		reconciler.Recorder.Eventf(kopsControlPlane, corev1.EventTypeWarning, "FailedToManageKopsState", "failed to manage Kops state: %s", err)
		conditions.MarkFalse(kopsControlPlane, controlplanev1alpha1.KopsControlPlaneStateReadyCondition, controlplanev1alpha1.KopsControlPlaneStateReconciliationFailedReason, clusterv1.ConditionSeverityError, err.Error())
		return resultError, err
	}
	conditions.MarkTrue(kopsControlPlane, controlplanev1alpha1.KopsControlPlaneStateReadyCondition)

	err = reconciler.ReconcileKopsSecrets(ctx, kopsClientset, kopsCluster)
	if err != nil {
		conditions.MarkFalse(reconciler.kcp, controlplanev1alpha1.KopsControlPlaneSecretsReadyCondition, controlplanev1alpha1.KopsControlPlaneSecretsReconciliationFailedReason, clusterv1.ConditionSeverityWarning, err.Error())
	}
	conditions.MarkTrue(reconciler.kcp, controlplanev1alpha1.KopsControlPlaneSecretsReadyCondition)

	terraformOutputDir := fmt.Sprintf("/tmp/%s", kopsCluster.Name)

	var shouldIgnoreSG bool
	if _, ok := owner.GetAnnotations()["kopscontrolplane.controlplane.wildlife.io/external-security-groups"]; ok {
		shouldIgnoreSG = true
	}

	err = reconciler.PrepareCloudResourcesFactory(kopsClientset, r.Client, ctx, kopsCluster, kopsControlPlane, kmps, shouldEnableKarpenter, fullCluster.Spec.ConfigBase, terraformOutputDir, cloud, shouldIgnoreSG, &reconciler.awsCredentials)
	if err != nil {
		conditions.MarkFalse(kopsControlPlane, controlplanev1alpha1.KopsTerraformGenerationReadyCondition, controlplanev1alpha1.KopsTerraformGenerationReconciliationFailedReason, clusterv1.ConditionSeverityError, err.Error())
		reconciler.Recorder.Eventf(kopsControlPlane, corev1.EventTypeWarning, "FailedToPrepareCloudResources", "failed to prepare cloud resources: %s", err)
		return resultError, err
	}
	conditions.MarkTrue(kopsControlPlane, controlplanev1alpha1.KopsTerraformGenerationReadyCondition)

	// TODO: This is needed because we are using a method from kops lib, we should be
	// we should check alternatives
	kubeConfig, err := utils.GetKubeconfigFromKopsState(kopsCluster, kopsClientset)
	if err != nil {
		return resultError, err
	}

	err = reconciler.reconcileKubeconfig(ctx, kubeConfig, log, kopsClientset, kopsCluster, owner)
	if err != nil {
		reconciler.Recorder.Eventf(kopsControlPlane, corev1.EventTypeWarning, "FailedToReconcileKubeconfig", "failed to reconcile kubeconfig: %s", err)
		return resultError, err
	}

	reconciler.Mux.Unlock()
	shouldIUnlock = false

	err = reconciler.ApplyTerraformFactory(ctx, terraformOutputDir, r.TfExecPath, reconciler.awsCredentials)
	if err != nil {
		conditions.MarkFalse(kopsControlPlane, controlplanev1alpha1.TerraformApplyReadyCondition, controlplanev1alpha1.TerraformApplyReconciliationFailedReason, clusterv1.ConditionSeverityError, err.Error())
		reconciler.Recorder.Eventf(kopsControlPlane, corev1.EventTypeWarning, "FailedToApplyTerraform", "failed to apply terraform: %s", err)
		return resultError, err
	}
	conditions.MarkTrue(kopsControlPlane, controlplanev1alpha1.TerraformApplyReadyCondition)

	err = reconciler.updateKopsMachinePoolWithProviderIDList(ctx, log, kopsControlPlane, kmps, &reconciler.awsCredentials)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return requeue1min, nil
		}
		return resultError, err
	}

	igList, err := kopsClientset.InstanceGroupsFor(kopsCluster).List(ctx, metav1.ListOptions{})
	if err != nil || len(igList.Items) == 0 {
		reconciler.Recorder.Eventf(kopsControlPlane, corev1.EventTypeWarning, "FailedToGetIGs", "cannot get InstanceGroups: %s", err)
		return resultError, fmt.Errorf("cannot get InstanceGroups for %q: %w", kopsCluster.ObjectMeta.Name, err)
	}

	val, err := reconciler.ValidateKopsClusterFactory(kubeConfig, kopsCluster, cloud, igList)

	if err != nil {
		reconciler.Recorder.Eventf(kopsControlPlane, corev1.EventTypeWarning, "FailedToValidateKubernetesCluster", "failed trying to validate Kubernetes cluster: %v", err)
		return resultError, err
	}

	statusReady, err := utils.KopsClusterValidation(kopsControlPlane, r.Recorder, log, val)
	if err != nil {
		reconciler.Recorder.Eventf(kopsControlPlane, corev1.EventTypeWarning, "FailedToValidateKubernetesCluster", "failed trying to validate Kubernetes cluster: %v", err)
		return resultError, err
	}
	kopsControlPlane.Status.Ready = statusReady
	reconciler.Recorder.Event(kopsControlPlane, corev1.EventTypeNormal, "ClusterReconciledSuccessfully", "cluster reconcile finished sucessfully")
	return resultDefault, nil
}

func (r *KopsControlPlaneReconciler) updateKopsMachinePoolWithProviderIDList(ctx context.Context, log logr.Logger, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, kmps []infrastructurev1alpha1.KopsMachinePool, credentials *aws.Credentials) error {
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
func GetASGByName(kopsMachinePool *infrastructurev1alpha1.KopsMachinePool, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, creds *aws.Credentials) (*asgTypes.AutoScalingGroup, error) {
	ctx := context.TODO()
	region, err := regionBySubnet(kopsControlPlane)
	if err != nil {
		return nil, err
	}
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken)),
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
			return fmt.Errorf("error creating instanceGroup: %w", err)
		}
		log.Info(fmt.Sprintf("created instancegroup/%s", instanceGroupName))
		return nil
	}
	if err != nil {
		return err
	}

	_, err = kopsClientset.InstanceGroupsFor(kopsCluster).Update(ctx, kopsInstanceGroup, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("error updating instanceGroup: %w", err)
	}
	log.Info(fmt.Sprintf("updated instancegroup/%s", instanceGroupName))

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *KopsControlPlaneReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager, workerCount int) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&controlplanev1alpha1.KopsControlPlane{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: workerCount}).
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

// this needs to be better named
func (r *KopsControlPlaneReconciliation) ReconcileKopsSecrets(ctx context.Context, kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster) error {
	if r.kcp.Spec.KopsSecret != nil {
		secretStore, err := kopsClientset.SecretStore(kopsCluster)
		if err != nil {
			return err
		}

		err = utils.ReconcileKopsSecrets(ctx, r.Client, secretStore, r.kcp, client.ObjectKey{
			Name:      r.kcp.Spec.KopsSecret.Name,
			Namespace: r.kcp.Spec.KopsSecret.Namespace,
		})
		if err != nil {
			return err
		}
	}
	return nil
}
