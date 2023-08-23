package utils

import (
	"bytes"
	"context"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/go-logr/logr"
	controlplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"k8s.io/kops/cmd/kops/util"
	kopsapi "k8s.io/kops/pkg/apis/kops"
	"k8s.io/kops/pkg/client/simple"
	"k8s.io/kops/pkg/featureflag"
	"k8s.io/kops/pkg/kubeconfig"
	"k8s.io/kops/pkg/pki"
	"k8s.io/kops/pkg/rbac"
	"k8s.io/kops/pkg/validation"
	"k8s.io/kops/upup/pkg/fi"
	"k8s.io/kops/upup/pkg/fi/cloudup"
	"k8s.io/kops/upup/pkg/fi/cloudup/awsup"
	"k8s.io/kops/util/pkg/vfs"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	vfs.Context = vfs.NewVFSContext()
	kopsClientset, err := factory.KopsClient()
	if err != nil {
		return nil, err
	}
	return kopsClientset, nil
}

func ValidateKopsCluster(kubeConfig *rest.Config, kopsCluster *kopsapi.Cluster, cloud fi.Cloud, igs *kopsapi.InstanceGroupList) (*validation.ValidationCluster, error) {
	k8sClient, err := kubernetes.NewForConfig(kubeConfig)
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

func ParseSpotinstFeatureflags(kopsControlPlane *controlplanev1alpha1.KopsControlPlane) error {
	featureflag.ParseFlags("-Spotinst,-SpotinstOcean,-SpotinstHybrid,-SpotinstController")
	if kopsControlPlane.Spec.SpotInst.Enabled {
		if _, ok := os.LookupEnv("SPOTINST_TOKEN"); !ok {
			return fmt.Errorf("missing SPOINST_TOKEN environment variable")
		}
		if _, ok := os.LookupEnv("SPOTINST_ACCOUNT"); !ok {
			return fmt.Errorf("missing SPOTINST_ACCOUNT environment variable")
		}
		featureflag.ParseFlags("Spotinst")
		if len(kopsControlPlane.Spec.SpotInst.FeatureFlags) > 0 {
			for _, featureFlag := range strings.Split(kopsControlPlane.Spec.SpotInst.FeatureFlags, ",") {
				if !strings.Contains(featureFlag, "Spotinst") {
					continue
				}
				featureflag.ParseFlags(featureFlag)

			}
		}
	}
	return nil
}

func BuildCloud(kopscluster *kopsapi.Cluster) (_ fi.Cloud, rerr error) {
	defer func() {
		if r := recover(); r != nil {
			rerr = fmt.Errorf(fmt.Sprintf("failed to instantiate cloud for %s", kopscluster.ObjectMeta.GetName()))
		}
	}()
	awsup.ResetAWSCloudInstances()
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

func GetKubeconfigFromKopsState(ctx context.Context, kopsCluster *kopsapi.Cluster, kopsClientset simple.Clientset) (*rest.Config, error) {
	builder := kubeconfig.NewKubeconfigBuilder()

	keyStore, err := kopsClientset.KeyStore(kopsCluster)
	if err != nil {
		return nil, err
	}

	builder.Context = kopsCluster.ObjectMeta.Name
	builder.Server = fmt.Sprintf("https://api.%s", kopsCluster.ObjectMeta.Name)
	keySet, err := keyStore.FindKeyset(ctx, fi.CertificateIDCA)
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
	cert, privateKey, _, err := pki.IssueCert(ctx, &req, fi.NewPKIKeystoreAdapter(keyStore))
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

	restConfig := &rest.Config{
		Host: builder.Server,
	}
	restConfig.CAData = builder.CACerts
	restConfig.CertData = builder.ClientCert
	restConfig.KeyData = builder.ClientKey
	restConfig.Username = builder.KubeUser
	restConfig.Password = builder.KubePassword

	return restConfig, nil
}

func reconcileKopsSecretsDelete(secretStore fi.SecretStore, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, k8sSecretData map[string][]byte) error {

	if len(kopsControlPlane.Status.Secrets) == 0 {
		return nil
	}

	actualKopsSecretNames := make([]string, len(kopsControlPlane.Status.Secrets))

	copy(actualKopsSecretNames, kopsControlPlane.Status.Secrets)

	actualKopsSecretNamesMap := make(map[string]struct{}, len(k8sSecretData))
	for kopsSecretKey := range k8sSecretData {
		actualKopsSecretNamesMap[kopsSecretKey] = struct{}{}
	}
	for _, kopsSecretKey := range actualKopsSecretNames {
		if _, found := actualKopsSecretNamesMap[kopsSecretKey]; !found {
			err := secretStore.DeleteSecret(kopsSecretKey)
			if err != nil {
				return err
			}
			for index, kopsSecretNameStatus := range kopsControlPlane.Status.Secrets {
				if kopsSecretNameStatus == kopsSecretKey {
					kopsControlPlane.Status.Secrets = append(kopsControlPlane.Status.Secrets[:index], kopsControlPlane.Status.Secrets[index+1:]...)
					break
				}
			}
		}
	}
	return nil
}

func reconcileKopsSecretsNormal(ctx context.Context, secretStore fi.SecretStore, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, k8sSecretData map[string][]byte) error {
	for kopsSecretName, data := range k8sSecretData {

		kopsSecret := &fi.Secret{
			Data: data,
		}

		stateKopsSecret, created, err := secretStore.GetOrCreateSecret(ctx, kopsSecretName, kopsSecret)
		if err != nil {
			return err
		}
		if !created && !bytes.Equal(stateKopsSecret.Data, kopsSecret.Data) {
			_, err := secretStore.ReplaceSecret(kopsSecretName, kopsSecret)
			if err != nil {
				return err
			}
		}

		if !isKopsSecretInStatus(kopsSecretName, kopsControlPlane.Status.Secrets) {
			kopsControlPlane.Status.Secrets = append(kopsControlPlane.Status.Secrets, kopsSecretName)
		}

	}
	return nil
}

func ReconcileKopsSecrets(ctx context.Context, k8sClient client.Client, secretStore fi.SecretStore, kopsControlPlane *controlplanev1alpha1.KopsControlPlane, k8sSecretKey client.ObjectKey) error {

	k8sSecret := &corev1.Secret{}
	if err := k8sClient.Get(ctx, k8sSecretKey, k8sSecret); err != nil {
		return err
	}

	err := reconcileKopsSecretsDelete(secretStore, kopsControlPlane, k8sSecret.Data)
	if err != nil {
		return nil
	}

	err = reconcileKopsSecretsNormal(ctx, secretStore, kopsControlPlane, k8sSecret.Data)
	if err != nil {
		return nil
	}

	return nil
}

func isKopsSecretInStatus(kopsSecretName string, kopsSecretStatus []string) bool {
	for _, statusKopsSecretName := range kopsSecretStatus {
		if kopsSecretName == statusKopsSecretName {
			return true
		}
	}
	return false
}
