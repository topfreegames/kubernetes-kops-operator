package utils

import (
	"context"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"text/template"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/go-logr/logr"
	"github.com/hashicorp/terraform-exec/tfexec"
	"go.mercari.io/hcledit"
)

type Template struct {
	TemplateFilename string
	OutputFilename   string
	EmbeddedFiles    embed.FS
	Data             any
}

//go:embed templates/*.tpl
var templates embed.FS

func lockPluginCache(pluginCacheDir string) (*os.File, error) {
	if err := os.MkdirAll(pluginCacheDir, 0755); err != nil {
		return nil, err
	}

	lockPath := filepath.Join(pluginCacheDir, ".terraform-plugin.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open lock file: %w", err)
	}

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		_ = lockFile.Close()
		return nil, fmt.Errorf("failed to acquire exclusive lock: %w", err)
	}

	return lockFile, nil
}

func unlockPluginCache(lockFile *os.File) error {
	if lockFile == nil {
		return nil
	}

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN); err != nil {
		_ = lockFile.Close()
		return fmt.Errorf("failed to unlock plugin cache: %w", err)
	}

	return lockFile.Close()
}

func cleanCorruptedProvider(pluginCacheDir, providerName string) error {
	if pluginCacheDir == "" || providerName == "" {
		return nil
	}

	providerPaths := []string{
		filepath.Join(pluginCacheDir, "registry.terraform.io", "hashicorp", providerName),
		filepath.Join(pluginCacheDir, "registry.terraform.io/hashicorp", providerName),
	}

	for _, path := range providerPaths {
		if _, err := os.Stat(path); err == nil {
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("failed to remove corrupted provider at %s: %w", path, err)
			}
		}
	}

	return nil
}

func validatePluginCache(pluginCacheDir string) error {
	if pluginCacheDir == "" {
		return nil
	}

	registryPath := filepath.Join(pluginCacheDir, "registry.terraform.io")
	if _, err := os.Stat(registryPath); os.IsNotExist(err) {
		return nil // No cache yet, which is fine
	}

	return filepath.Walk(registryPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files we can't access
		}

		if !info.IsDir() && info.Mode()&0111 != 0 {
			if info.Size() == 0 {
				providerDir := filepath.Dir(path)
				return os.RemoveAll(providerDir)
			}
		}

		return nil
	})
}

func isRetriableError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	retriablePatterns := []string{
		"text file busy",
		"checksums",
		"does not match",
		"Required plugins are not installed",
		"cached package for",
	}

	for _, pattern := range retriablePatterns {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}

	return false
}

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
		defer func() {
			_ = file.Close()
		}()

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

// ModifyTerraformProviderVersion modifies the existing Terraform files to add AWS provider version constraint
func ModifyTerraformProviderVersion(terraformOutputDir, awsProviderVersion string) error {
	kubernetesFile := terraformOutputDir + "/kubernetes.tf"

	cleanVersion := strings.Trim(awsProviderVersion, `"'`)

	content, err := os.ReadFile(kubernetesFile)
	if err != nil {
		return fmt.Errorf("failed to read kubernetes.tf: %w", err)
	}

	awsVersionRegex := regexp.MustCompile(`(?s)(aws\s*=\s*\{[^}]*"?version"?\s*=\s*)"[^"]*"`)

	if !awsVersionRegex.MatchString(string(content)) {
		return fmt.Errorf("failed to find AWS provider version pattern in kubernetes.tf")
	}

	newVersionString := fmt.Sprintf(`${1}"%s"`, cleanVersion)
	newContent := awsVersionRegex.ReplaceAllString(string(content), newVersionString)

	err = os.WriteFile(kubernetesFile, []byte(newContent), 0644)
	if err != nil {
		return fmt.Errorf("failed to write modified kubernetes.tf: %w", err)
	}

	return nil
}

func initTerraform(ctx context.Context, workingDir, terraformExecPath string, credentials aws.Credentials) (*tfexec.Terraform, error) {
	log := logr.FromContextOrDiscard(ctx)

	tf, err := tfexec.NewTerraform(workingDir, terraformExecPath)
	if err != nil {
		return nil, err
	}

	pluginCacheDir := fmt.Sprintf("%s/plugin-cache", filepath.Dir(terraformExecPath))

	env := map[string]string{
		"AWS_ACCESS_KEY_ID":     credentials.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY": credentials.SecretAccessKey,
		"SPOTINST_TOKEN":        os.Getenv("SPOTINST_TOKEN"),
		"SPOTINST_ACCOUNT":      os.Getenv("SPOTINST_ACCOUNT"),
		"TF_PLUGIN_CACHE_DIR":   pluginCacheDir,
	}

	err = tf.SetEnv(env)
	if err != nil {
		return nil, err
	}

	lockFile, err := lockPluginCache(pluginCacheDir)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire plugin cache lock: %w", err)
	}
	defer func() {
		time.Sleep(500 * time.Millisecond)
		if err := unlockPluginCache(lockFile); err != nil {
			log.Error(err, "failed to unlock plugin cache")
		}
	}()

	if err := validatePluginCache(pluginCacheDir); err != nil {
		log.Info("plugin cache validation found issues, will attempt cleanup", "severity", "warning", "error", err.Error())
	}

	var initErr error
	maxRetries := 5
	cleanedCache := false

	for i := 0; i < maxRetries; i++ {
		initErr = tf.Init(ctx, tfexec.Upgrade(true))
		if initErr == nil {
			break
		}

		if !isRetriableError(initErr) {
			break
		}

		if i >= maxRetries-1 {
			break
		}

		errStr := initErr.Error()

		if (strings.Contains(errStr, "checksums") ||
			strings.Contains(errStr, "does not match") ||
			strings.Contains(errStr, "Required plugins are not installed") ||
			strings.Contains(errStr, "cached package for")) && !cleanedCache {

			log.Info("terraform init detected plugin cache corruption, cleaning provider", "severity", "warning", "attempt", i+1, "maxRetries", maxRetries)
			if err := cleanCorruptedProvider(pluginCacheDir, "aws"); err != nil {
				log.Info("failed to clean corrupted provider, will retry anyway", "severity", "warning", "error", err.Error())
			}
			cleanedCache = true

			time.Sleep(1 * time.Second)
			continue
		}

		waitTime := time.Duration(1<<uint(i)) * time.Second
		log.Info("terraform init failed, retrying", "severity", "warning", "attempt", i+1, "maxRetries", maxRetries, "retryIn", waitTime.String())
		time.Sleep(waitTime)
	}

	if initErr != nil {
		return nil, fmt.Errorf("terraform init failed after %d retries: %w", maxRetries, initErr)
	}

	return tf, nil

}

// ApplyTerraform just applies the already created terraform files
func ApplyTerraform(ctx context.Context, workingDir, terraformExecPath string, credentials aws.Credentials) error {
	log := logr.FromContextOrDiscard(ctx)

	tf, err := initTerraform(ctx, workingDir, terraformExecPath, credentials)
	if err != nil {
		return err
	}

	var applyErr error
	maxRetries := 5
	for i := 0; i < maxRetries; i++ {
		applyErr = tf.Apply(ctx)
		if applyErr == nil {
			return nil
		}

		if !isRetriableError(applyErr) {
			break
		}

		if i >= maxRetries-1 {
			break
		}

		waitTime := time.Duration(1<<uint(i)) * time.Second
		log.Info("terraform apply failed, retrying", "severity", "warning", "attempt", i+1, "maxRetries", maxRetries, "retryIn", waitTime.String())
		time.Sleep(waitTime)
	}

	if applyErr != nil {
		return fmt.Errorf("terraform apply failed after %d retries: %w", maxRetries, applyErr)
	}

	return nil
}

// PlanTerraform just applies the already created terraform files
func PlanTerraform(ctx context.Context, workingDir, terraformExecPath string, credentials aws.Credentials) error {
	log := logr.FromContextOrDiscard(ctx)

	// For some unknown reason (as of this writing) the generated terraform managed files have empty strings for
	// server_side_encryption and acl properties, which causes an error. These aren't really needed for this hacks cleans them out
	editor, _ := hcledit.ReadFile(workingDir + "/kubernetes.tf")
	_ = editor.Delete("resource.aws_s3_object.*.acl")
	_ = editor.Delete("resource.aws_s3_object.*.server_side_encryption")
	_ = editor.OverWriteFile()

	tf, err := initTerraform(ctx, workingDir, terraformExecPath, credentials)
	if err != nil {
		return err
	}

	var planErr error
	maxRetries := 5
	for i := 0; i < maxRetries; i++ {
		_, planErr = tf.Plan(ctx, tfexec.Out(workingDir+"/plan.out"))
		if planErr == nil {
			return nil
		}

		if !isRetriableError(planErr) {
			break
		}

		if i >= maxRetries-1 {
			break
		}

		waitTime := time.Duration(1<<uint(i)) * time.Second
		log.Info("terraform plan failed, retrying", "severity", "warning", "attempt", i+1, "maxRetries", maxRetries, "retryIn", waitTime.String())
		time.Sleep(waitTime)
	}

	if planErr != nil {
		return fmt.Errorf("terraform plan failed after %d retries: %w", maxRetries, planErr)
	}

	return nil
}

func DestroyTerraform(ctx context.Context, workingDir, terraformExecPath string, credentials aws.Credentials) error {
	log := logr.FromContextOrDiscard(ctx)

	tf, err := initTerraform(ctx, workingDir, terraformExecPath, credentials)
	if err != nil {
		return err
	}

	var destroyErr error
	maxRetries := 5
	for i := 0; i < maxRetries; i++ {
		destroyErr = tf.Destroy(ctx)
		if destroyErr == nil {
			return nil
		}

		if !isRetriableError(destroyErr) {
			break
		}

		if i >= maxRetries-1 {
			break
		}

		waitTime := time.Duration(1<<uint(i)) * time.Second
		log.Info("terraform destroy failed, retrying", "severity", "warning", "attempt", i+1, "maxRetries", maxRetries, "retryIn", waitTime.String())
		time.Sleep(waitTime)
	}

	if destroyErr != nil {
		return fmt.Errorf("terraform destroy failed after %d retries: %w", maxRetries, destroyErr)
	}

	return nil
}

func CleanupTerraformDirectory(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() {
		_ = d.Close()
	}()
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
