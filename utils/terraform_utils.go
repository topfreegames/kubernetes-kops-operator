package utils

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/hashicorp/terraform-exec/tfexec"
)

type Template struct {
	Filename     string
	TemplatePath string
	Data         any
}

// CreateAdditionalTerraformFiles create files in the terraform state directory
func CreateAdditionalTerraformFiles(tfFiles ...Template) error {
	for _, tfFile := range tfFiles {
		file, err := os.Create(tfFile.Filename)
		if err != nil {
			return err
		}
		defer file.Close()

		t := template.New(filepath.Base(tfFile.TemplatePath)).Funcs(template.FuncMap{
			"stringReplace": strings.Replace,
		})

		t, err = t.ParseFiles(tfFile.TemplatePath)
		if err != nil {
			return err
		}

		err = t.Execute(file, tfFile.Data)
		if err != nil {
			return err
		}
	}
	return nil
}

// ApplyTerraform just applies the already created terraform files
func ApplyTerraform(ctx context.Context, workingDir, terraformExecPath string) error {

	tf, err := tfexec.NewTerraform(workingDir, terraformExecPath)
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
