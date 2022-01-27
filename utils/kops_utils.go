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

func GetKubeconfigFromKopsState(kopsCluster *kopsapi.Cluster, kopsClientset simple.Clientset) (*rest.Config, error) {
	builder := kubeconfig.NewKubeconfigBuilder()

	keyStore, err := kopsClientset.KeyStore(kopsCluster)
	if err != nil {
		return nil, err
	}

	builder.Context = kopsCluster.ObjectMeta.Name
	builder.Server = fmt.Sprintf("https://api.%s", kopsCluster.ObjectMeta.Name)
	caCert, _, _, err := keyStore.FindKeypair(fi.CertificateIDCA)
	if err != nil || caCert == nil {
		return nil, err
	}

	builder.CACert, err = caCert.AsBytes()
	if err != nil {
		return nil, err
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
