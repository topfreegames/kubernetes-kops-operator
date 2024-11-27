package utils

import (
	"context"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/hashicorp/terraform-exec/tfexec"
)

type Template struct {
	TemplateFilename string
	OutputFilename   string
	EmbeddedFiles    embed.FS
	Data             any
}

//go:embed templates/*.tpl
var templates embed.FS

// CreateTerraformFileFromTemplate populates a Terraform template and create files in the state
func CreateTerraformFilesFromTemplate(terraformTemplateFilePath string, TerraformOutputFileName string, terraformOutputDir string, templateData any) error {
	template := Template{
		TemplateFilename: terraformTemplateFilePath,
		EmbeddedFiles:    templates,
		OutputFilename:   fmt.Sprintf("%s/%s", terraformOutputDir, TerraformOutputFileName),
		Data:             templateData,
	}
	return CreateAdditionalTerraformFiles(template)
}

// CreateAdditionalTerraformFiles create files in the terraform state directory from a template
func CreateAdditionalTerraformFiles(tfFiles ...Template) error {
	for _, tfFile := range tfFiles {
		file, err := os.Create(tfFile.OutputFilename)
		if err != nil {
			return err
		}
		defer file.Close()

		t := template.New(filepath.Base(tfFile.TemplateFilename)).Funcs(template.FuncMap{
			"stringReplace": strings.Replace,
		})

		t, err = t.ParseFS(tfFile.EmbeddedFiles, tfFile.TemplateFilename)
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

func initTerraform(ctx context.Context, workingDir, terraformExecPath string, credentials aws.Credentials) (*tfexec.Terraform, error) {
	tf, err := tfexec.NewTerraform(workingDir, terraformExecPath)
	if err != nil {
		return nil, err
	}

	env := map[string]string{
		"AWS_ACCESS_KEY_ID":     credentials.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY": credentials.SecretAccessKey,
		"SPOTINST_TOKEN":        os.Getenv("SPOTINST_TOKEN"),
		"SPOTINST_ACCOUNT":      os.Getenv("SPOTINST_ACCOUNT"),
		"TF_PLUGIN_CACHE_DIR":   fmt.Sprintf("%s/plugin-cache", filepath.Dir(terraformExecPath)),
	}

	// this overrides all ENVVARs that are passed to Terraform
	err = tf.SetEnv(env)
	if err != nil {
		return nil, err
	}

	err = tf.Init(ctx, tfexec.Upgrade(true))
	if err != nil {
		return nil, err
	}

	return tf, nil

}

// ApplyTerraform just applies the already created terraform files
func ApplyTerraform(ctx context.Context, workingDir, terraformExecPath string, credentials aws.Credentials) error {

	tf, err := initTerraform(ctx, workingDir, terraformExecPath, credentials)
	if err != nil {
		return err
	}

	err = tf.Apply(ctx)
	if err != nil {
		return err
	}

	return nil
}

func DestroyTerraform(ctx context.Context, workingDir, terraformExecPath string, credentials aws.Credentials) error {
	tf, err := initTerraform(ctx, workingDir, terraformExecPath, credentials)
	if err != nil {
		return err
	}

	err = tf.Destroy(ctx)
	if err != nil {
		return err
	}

	return nil
}

func CleanupTerraformDirectory(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		if strings.HasPrefix(name, ".terraform") {
			continue
		}
		err = os.RemoveAll(filepath.Join(dir, name))
		if err != nil {
			return err
		}
	}
	return nil
}
