package utils

import (
	"context"
	"embed"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/hashicorp/terraform-exec/tfexec"
)

type Template struct {
	TemplateFilename string
	OutputFilename   string
	EmbeddedFiles    embed.FS
	Data             any
}

// CreateAdditionalTerraformFiles create files in the terraform state directory
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
