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
	"encoding/json"
	"fmt"
	"os"

	"github.com/go-logr/logr"
	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hc-install/product"
	"github.com/hashicorp/hc-install/releases"
	"github.com/hashicorp/terraform-exec/tfexec"
	controlplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	"github.com/topfreegames/kubernetes-kops-operator/utils"
	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kopsapi "k8s.io/kops/pkg/apis/kops"
	"k8s.io/kops/pkg/assets"
	"k8s.io/kops/pkg/client/simple"
	"k8s.io/kops/pkg/commands"
	"k8s.io/kops/upup/pkg/fi/cloudup"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KopsControlPlaneReconciler reconciles a KopsControlPlane object
type KopsControlPlaneReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	kopsClientset simple.Clientset
	log           logr.Logger
}

// populateClusterSpec populates the full cluster spec
func (r *KopsControlPlaneReconciler) populateClusterSpec(cluster *kopsapi.Cluster) (*kopsapi.Cluster, error) {
	cloud, err := cloudup.BuildCloud(cluster)
	if err != nil {
		return nil, err
	}

	err = cloudup.PerformAssignments(cluster, cloud)
	if err != nil {
		return nil, err
	}

	assetBuilder := assets.NewAssetBuilder(cluster, "")
	fullCluster, err := cloudup.PopulateClusterSpec(r.kopsClientset, cluster, cloud, assetBuilder)
	if err != nil {
		return nil, err
	}

	return fullCluster, nil
}

// addSSHCredential adds a predefined public ssh key that is added in the nodes
// TODO: Add the public and private key in the vault, all newly created
// clusters will use the same for now
func (r *KopsControlPlaneReconciler) addSSHCredential(cluster *kopsapi.Cluster) error {
	sshCredential := kopsapi.SSHCredential{
		Spec: kopsapi.SSHCredentialSpec{
			PublicKey: "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCu7OR4k/qpc6VFqQsMGk7wQcnGzDA/hKABnj3qN85tgIDVsbnOIVgXl4FV1gO+gBjblCLkAmbZYlwhhkosL4xpEc8uk8QWJIzRqalvnLEofdIjClngGqzC40Yu6oVPiqImDazlVNvJ7UdzX02mmYJMe4eRzS1w1dA2hm9uTsaq6CNZuJF2/joV+SKLW88IEXWnb7PdOPZWFy0iN/9JcQKqON7zmR0j1zb4Ydj6Pt9MMIOTRiJpyeTqw0Gy4RWgkKJpwuRhOTnhZ1I8zigXgu4+keMYBgtLLP90Wx6/SI6vt+sG/Zrx5+s0av6vHFH/fDzqX4BSsxY83cOMH6ILLQ1C0hE9ykXx/EAKoou+DT8Doe0wabVxZNMRDOAb0ZnLF1HwUItW+MvgIjtCVpap/jBGmSSqZ5B9cvib7UV+JfLHty7n3AP2SKf52+E3Fp1fP4UiXQ/YUXZksopHLXLtwMdam/qijq5tjk0lVh7j8GGNuejt17+tSOCaP2kNKFyc1u8=",
		},
	}

	sshCredentialStore, err := r.kopsClientset.SSHCredentialStore(cluster)
	if err != nil {
		return err
	}

	sshKeyArr := []byte(sshCredential.Spec.PublicKey)
	err = sshCredentialStore.AddSSHPublicKey("ubuntu", sshKeyArr)
	if err != nil {
		return err
	}

	r.log.Info("Added ssh credential")

	return nil
}

// createTerraformBackendFile creates the backend file for the remote state
func (r *KopsControlPlaneReconciler) createTerraformBackendFile(bucket, clusterName, backendPath string) error {
	backendContent := fmt.Sprintf(`
	terraform {
		backend "s3" {
			bucket = "%s"
			key = "%s/terraform/%s.tfstate"
			region = "us-east-1"
		}
	}`, bucket, clusterName, clusterName)

	file, err := os.Create(fmt.Sprintf("%s/backend.tf", backendPath))
	if err != nil {
		r.log.Error(err, fmt.Sprintf("failed to create backend file: %v", err))
		return err
	}
	defer file.Close()

	_, err = file.WriteString(backendContent)
	if err != nil {
		r.log.Error(err, fmt.Sprintf("failed to write backend to file: %v", err))
		return err
	}

	return nil
}

// updateKopsState creates or updates the kops state in the remote storage
func (r *KopsControlPlaneReconciler) updateKopsState(ctx context.Context, cluster *kopsapi.Cluster) error {

	fullCluster, err := r.populateClusterSpec(cluster)
	if err != nil {
		return err
	}

	oldCluster, _ := r.kopsClientset.GetCluster(ctx, fullCluster.Name)
	if oldCluster != nil {
		statusDiscovery := &commands.CloudDiscoveryStatusStore{}
		status, err := statusDiscovery.FindClusterStatus(oldCluster)
		if err != nil {
			return err
		}
		r.kopsClientset.UpdateCluster(ctx, fullCluster, status)
		r.log.Info(fmt.Sprintf("updated kops state for cluster %s", cluster.ObjectMeta.Name))
		return nil
	}

	_, err = r.kopsClientset.CreateCluster(ctx, fullCluster)
	if err != nil {
		return err
	}

	err = r.addSSHCredential(fullCluster)
	if err != nil {
		return err
	}

	r.log.Info(fmt.Sprintf("created kops state for cluster %s", cluster.ObjectMeta.Name))

	return nil
}

// generateTerraformFiles generates the terraform files for the cloud resources
func (r *KopsControlPlaneReconciler) generateTerraformFiles(ctx context.Context, cluster *kopsapi.Cluster, s3Bucket, outputDir string) error {
	cloud, err := cloudup.BuildCloud(cluster)
	if err != nil {
		return err
	}

	applyCmd := &cloudup.ApplyClusterCmd{
		Cloud:              cloud,
		Clientset:          r.kopsClientset,
		Cluster:            cluster,
		DryRun:             true,
		AllowKopsDowngrade: false,
		OutDir:             outputDir,
		TargetName:         "terraform",
	}

	if err := applyCmd.Run(ctx); err != nil {
		return err
	}

	if err = r.createTerraformBackendFile(s3Bucket, cluster.Name, outputDir); err != nil {
		return err
	}

	return nil
}

// applyTerraform just applies the already created terraform files
func (r *KopsControlPlaneReconciler) applyTerraform(ctx context.Context, workingDir string) error {

	installer := &releases.ExactVersion{
		Product: product.Terraform,
		Version: version.Must(version.NewVersion("0.15.0")),
	}

	execPath, err := installer.Install(ctx)
	if err != nil {
		r.log.Error(err, fmt.Sprintf("error installing Terraform: %v", err))
		return err
	}

	tf, err := tfexec.NewTerraform(workingDir, execPath)
	if err != nil {
		r.log.Error(err, fmt.Sprintf("error running NewTerraform: %v", err))
		return err
	}

	err = tf.Init(ctx, tfexec.Upgrade(true))
	if err != nil {
		r.log.Error(err, fmt.Sprintf("error running Init: %v", err))
		return err
	}

	err = tf.Apply(ctx)
	if err != nil {
		r.log.Error(err, fmt.Sprintf("error running Apply: %v", err))
		return err
	}

	return nil
}

//+kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=kopscontrolplanes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=kopscontrolplanes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=kopscontrolplanes/finalizers,verbs=update
func (r *KopsControlPlaneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.log = ctrl.LoggerFrom(ctx)

	var kopsControlPlane controlplanev1alpha1.KopsControlPlane
	if err := r.Get(ctx, req.NamespacedName, &kopsControlPlane); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
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
		ObjectMeta: metaV1.ObjectMeta{
			Name: kopsControlPlane.ObjectMeta.Labels[kopsapi.LabelClusterName],
		},
		Spec: kopsClusterSpec,
	}

	err = r.updateKopsState(ctx, kopsCluster)
	if err != nil {
		r.log.Error(err, fmt.Sprintf("failed to create cluster: %v", err))
		return ctrl.Result{}, err
	}

	terraformOutputDir := fmt.Sprintf("/tmp/%s", kopsCluster.Name)
	err = r.generateTerraformFiles(ctx, kopsCluster, s3Bucket, terraformOutputDir)
	if err != nil {
		r.log.Error(err, fmt.Sprintf("failed to update cluster: %v", err))
		return ctrl.Result{}, err
	}

	err = r.applyTerraform(ctx, terraformOutputDir)
	if err != nil {
		r.log.Error(err, fmt.Sprintf("failed to apply terraform: %v", err))
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *KopsControlPlaneReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&controlplanev1alpha1.KopsControlPlane{}).
		Complete(r)
}
