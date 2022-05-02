package utils

import (
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"k8s.io/kops/cmd/kops/util"
	kopsapi "k8s.io/kops/pkg/apis/kops"
	"k8s.io/kops/pkg/client/simple"
	"k8s.io/kops/pkg/kubeconfig"
	"k8s.io/kops/pkg/pki"
	"k8s.io/kops/pkg/rbac"
	"k8s.io/kops/pkg/validation"
	"k8s.io/kops/upup/pkg/fi"
	"k8s.io/kops/upup/pkg/fi/cloudup"
)

func GetBucketName(configBase string) (string, error) {
	configBaseList := strings.Split(configBase, "/")
	if len(configBaseList) < 3 || len(configBaseList[2]) == 0 {
		return "", errors.New("invalid kops configBase")
	}
	return configBaseList[2], nil
}

func GetKopsClientset(configBase string) (simple.Clientset, error) {
	lastIndex := strings.LastIndex(configBase, "/")
	factoryOptions := &util.FactoryOptions{
		RegistryPath: configBase[:lastIndex],
	}

	factory := util.NewFactory(factoryOptions)

	kopsClientset, err := factory.Clientset()
	if err != nil {
		return nil, err
	}
	return kopsClientset, nil
}

func ValidateKopsCluster(kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error) {
	config, err := GetKubeconfigFromKopsState(kopsCluster, kopsClientset)
	if err != nil {
		return nil, err
	}

	k8sClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	cloud, err := BuildCloud(kopsCluster)
	if err != nil {
		return nil, err
	}

	validator, err := validation.NewClusterValidator(kopsCluster, cloud, igs, fmt.Sprintf("https://api.%s:443", kopsCluster.ObjectMeta.Name), k8sClient)
	if err != nil {
		return nil, fmt.Errorf("unexpected error creating validator: %v", err)
	}

	result, err := validator.Validate()
	if err != nil {
		return nil, fmt.Errorf("%v", err)
	}
	return result, nil
}

func BuildCloud(kopscluster *kopsapi.Cluster) (fi.Cloud, error) {
	cloud, err := cloudup.BuildCloud(kopscluster)
	if err != nil {
		return nil, err
	}

	return cloud, nil
}

func EvaluateKopsValidationResult(validation *validation.ValidationCluster) (bool, []string) {
	result := true
	var errorMessages []string

	failures := validation.Failures
	if len(failures) > 0 {
		result = false
		for _, failure := range failures {
			errorMessages = append(errorMessages, failure.Message)
		}
	}

	nodes := validation.Nodes
	for _, node := range nodes {
		if node.Status == corev1.ConditionFalse {
			result = false
			errorMessages = append(errorMessages, fmt.Sprintf("node %s condition is %s", node.Hostname, node.Status))
		}
	}

	return result, errorMessages
}

func KopsClusterValidation(object runtime.Object, recorder record.EventRecorder, log logr.Logger, validation *validation.ValidationCluster) (bool, error) {
	result, errorMessages := EvaluateKopsValidationResult(validation)
	if result {
		recorder.Eventf(object, corev1.EventTypeNormal, "KubernetesClusterValidationSucceed", "Kops validation succeed")
		return true, nil
	} else {
		for _, errorMessage := range errorMessages {
			recorder.Eventf(object, corev1.EventTypeWarning, "KubernetesClusterValidationFailed", errorMessage)
		}
		return false, nil
	}
}

func GetKubeconfigFromKopsState(kopsCluster *kopsapi.Cluster, kopsClientset simple.Clientset) (*rest.Config, error) {
	builder := kubeconfig.NewKubeconfigBuilder()

	keyStore, err := kopsClientset.KeyStore(kopsCluster)
	if err != nil {
		return nil, err
	}

	builder.Context = kopsCluster.ObjectMeta.Name
	builder.Server = fmt.Sprintf("https://api.%s", kopsCluster.ObjectMeta.Name)
	keySet, err := keyStore.FindKeyset(fi.CertificateIDCA)
	if err != nil {
		return nil, err
	}
	if keySet != nil {
		builder.CACerts, err = keySet.ToCertificateBytes()
		if err != nil {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("cannot find CA certificate")
	}

	req := pki.IssueCertRequest{
		Signer: fi.CertificateIDCA,
		Type:   "client",
		Subject: pkix.Name{
			CommonName:   "kops-operator",
			Organization: []string{rbac.SystemPrivilegedGroup},
		},
		Validity: 64800000000000,
	}
	cert, privateKey, _, err := pki.IssueCert(&req, keyStore)
	if err != nil {
		return nil, err
	}
	builder.ClientCert, err = cert.AsBytes()
	if err != nil {
		return nil, err
	}
	builder.ClientKey, err = privateKey.AsBytes()
	if err != nil {
		return nil, err
	}

	config, err := builder.BuildRestConfig()
	if err != nil {
		return nil, err
	}

	return config, nil
}
