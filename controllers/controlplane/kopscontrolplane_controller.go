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

	"github.com/go-logr/logr"
	controlplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	"github.com/topfreegames/kubernetes-kops-operator/utils"
	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kopsapi "k8s.io/kops/pkg/apis/kops"
	"k8s.io/kops/pkg/client/simple"
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

// isKopsStateCreated checks if the kops state for the cluster is already created by its name
func (r *KopsControlPlaneReconciler) isKopsStateCreated(ctx context.Context, clusterName string) bool {
	cluster, _ := r.kopsClientset.GetCluster(ctx, clusterName)
	return cluster != nil
}


// createTerraformBackendFile creates the backend file for the remote state
func (r *KopsControlPlaneReconciler) createTerraformBackendFile(bucket, clusterName, path string) error {
	backendContent := fmt.Sprintf(`
	terraform {
		backend "s3" {
			bucket = "%s"
			key = "%s/terraform/%s.tfstate"
			region = "us-east-1"
		}
	}`, bucket, clusterName, clusterName)

	file, err := os.Create(path)
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

// createKopsState creates the kops state in the remote storage
// It's equivalent with the kops create command
func (r *KopsControlPlaneReconciler) createKopsState(ctx context.Context, cluster *kopsapi.Cluster) error {

	if r.isKopsStateCreated(ctx, cluster.Name) {
		r.log.Info(fmt.Sprintf("cluster %q already exists, skipping creation", cluster.Name))
		return nil
	}

	cloud, err := cloudup.BuildCloud(cluster)
	if err != nil {
		return err
	}

	err = cloudup.PerformAssignments(cluster, cloud)
	if err != nil {
		return err
	}

	_, err = r.kopsClientset.CreateCluster(ctx, cluster)
	if err != nil {
		return err
	}
	r.log.Info(fmt.Sprintf("Created kops state for cluster %s", cluster.ObjectMeta.Name))

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

	err = r.createKopsState(ctx, kopsCluster)
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


	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *KopsControlPlaneReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&controlplanev1alpha1.KopsControlPlane{}).
		Complete(r)
}
