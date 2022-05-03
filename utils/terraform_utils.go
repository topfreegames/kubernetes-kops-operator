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
)

// ApplyTerraform just applies the already created terraform files
func ApplyTerraform(ctx context.Context, workingDir string) error {

	installer := &releases.ExactVersion{
		Product: product.Terraform,
		Version: version.Must(version.NewVersion("1.1.7")),
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

// CreateTerraformBackendFile creates the backend file for the remote state
func CreateTerraformBackendFile(bucket, clusterName, backendPath string) error {
	backendContent := fmt.Sprintf(`
	terraform {
		backend "s3" {
			bucket = "%s"
			key = "_terraform/%s.tfstate"
			region = "us-east-1"
		}
	}`, bucket, clusterName)

	err := os.MkdirAll(backendPath, 0755)
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
