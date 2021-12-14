package utils

import (
	"context"
	"fmt"
	"os"
	"path"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hc-install/product"
	"github.com/hashicorp/hc-install/releases"
	"github.com/hashicorp/terraform-exec/tfexec"
	kopsapi "k8s.io/kops/pkg/apis/kops"
	"k8s.io/kops/pkg/client/simple"
)

// generateTerraformFiles generates the terraform files for the cloud resources
func GenerateTerraformFiles(kopsClientset simple.Clientset, ctx context.Context, cluster *kopsapi.Cluster, s3Bucket, outputDir string) error {

	// updateClusterOptions := &kopscmd.UpdateClusterOptions{
	// 	Target:             "terraform",
	// 	Yes:                false,
	// 	OutDir:             outputDir,
	// 	AllowKopsDowngrade: false,
	// }

	// kopscmd.RunUpdateCluster(ctx, nil, cluster.ClusterName, nil, updateClusterOptions)

	// cloud, err := cloudup.BuildCloud(cluster)
	// if err != nil {
	// 	return err
	// }

	// applyCmd := &cloudup.ApplyClusterCmd{
	// 	Cloud:              cloud,
	// 	Clientset:          kopsClientset,
	// 	Cluster:            cluster,
	// 	DryRun:             true,
	// 	AllowKopsDowngrade: false,
	// 	OutDir:             outputDir,
	// 	TargetName:         "terraform",
	// }

	// if err := applyCmd.Run(ctx); err != nil {
	// 	return err
	// }

	if err := CreateTerraformBackendFile(s3Bucket, cluster.Name, outputDir); err != nil {
		return err
	}

	return nil
}

// applyTerraform just applies the already created terraform files
func ApplyTerraform(ctx context.Context, workingDir string) error {

	installer := &releases.ExactVersion{
		Product: product.Terraform,
		Version: version.Must(version.NewVersion("0.15.0")),
	}

	execPath, err := installer.Install(ctx)
	if err != nil {
		return err
	}

	tf, err := tfexec.NewTerraform(workingDir, execPath)
	if err != nil {
		return err
	}

	err = tf.Init(ctx, tfexec.Upgrade(true))
	if err != nil {
		return err
	}

	err = tf.Apply(ctx)
	if err != nil {
		return err
	}

	return nil
}

// createTerraformBackendFile creates the backend file for the remote state
func CreateTerraformBackendFile(bucket, clusterName, backendPath string) error {
	backendContent := fmt.Sprintf(`
	terraform {
		backend "s3" {
			bucket = "%s"
			key = "%s/terraform/%s.tfstate"
			region = "us-east-1"
		}
	}`, bucket, clusterName, clusterName)

	err := os.MkdirAll(backendPath, os.ModeDir)
	if err != nil {
		return err
	}

	file, err := os.Create(path.Join(backendPath, "backend.tf"))
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(backendContent)
	if err != nil {
		return err
	}

	return nil
}
