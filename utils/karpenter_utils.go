package utils

import (
	"bytes"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	infrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"
	kopsapi "k8s.io/kops/pkg/apis/kops"
)

func CreateEC2NodeClassFromKops(kopsCluster *kopsapi.Cluster, kmp *infrastructurev1alpha1.KopsMachinePool, terraformOutputDir string) (string, error) {
	amiName, err := GetAmiNameFromImageSource(kmp.Spec.KopsInstanceGroupSpec.Image)
	if err != nil {
		return "", err
	}

	userData, err := GetUserDataFromTerraformFile(kopsCluster.Name, kmp.Name, terraformOutputDir)
	if err != nil {
		return "", err
	}

	data := struct {
		Name        string
		AmiName     string
		ClusterName string
		Tags        map[string]string
		UserData    string
	}{
		Name:        kmp.Name,
		AmiName:     amiName,
		ClusterName: kopsCluster.Name,
		Tags:        kopsCluster.Spec.CloudLabels,
		UserData:    userData,
	}

	content, err := templates.ReadFile("templates/ec2nodeclass.yaml.tpl")
	if err != nil {
		return "", err
	}

	t, err := template.New("template").Funcs(sprig.TxtFuncMap()).Parse(string(content))
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	err = t.Execute(&buf, data)
	if err != nil {
		return "", err
	}

	return buf.String(), nil
}