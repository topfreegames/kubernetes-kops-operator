package utils

import (
	"bytes"
	"context"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/go-logr/logr"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
	capiutil "sigs.k8s.io/cluster-api/util"

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
	"k8s.io/kops/pkg/resources"
	resourceops "k8s.io/kops/pkg/resources/ops"
	"k8s.io/kops/pkg/validation"
	"k8s.io/kops/upup/pkg/fi"
	"k8s.io/kops/upup/pkg/fi/cloudup"
	"k8s.io/kops/upup/pkg/fi/cloudup/awsup"
	"k8s.io/kops/util/pkg/vfs"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Subnet struct {
	Name        string
	ClusterName string
}

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
	// Context holds the global VFS state.
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

	validator, err := validation.NewClusterValidator(kopsCluster, cloud, igs, nil, nil, kubeConfig, k8sClient)
	if err != nil {
		return nil, fmt.Errorf("unexpected error creating validator: %v", err)
	}

	result, err := validator.Validate(context.Background())
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
			rerr = fmt.Errorf("failed to instantiate cloud for %s", kopscluster.ObjectMeta.GetName())
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
		for _, failure := range failures {
			// Ignore scheduling pod validations
			if failure.Kind == "Pod" {
				continue
			}
			errorMessages = append(errorMessages, failure.Message)
		}
	}

	nodes := validation.Nodes
	for _, node := range nodes {
		if node.Status == corev1.ConditionFalse {
			errorMessages = append(errorMessages, fmt.Sprintf("node %s condition is %s", node.Hostname, node.Status))
		}
	}

	if len(errorMessages) > 0 {
		result = false
	}
	return result, errorMessages
}

func KopsClusterValidation(object runtime.Object, recorder record.EventRecorder, log logr.Logger, validation *validation.ValidationCluster) bool {
	result, errorMessages := EvaluateKopsValidationResult(validation)
	if result {
		recorder.Eventf(object, corev1.EventTypeNormal, "KubernetesClusterValidationSucceeded", "kops validation succeeded")
		return true
	} else {
		for _, errorMessage := range errorMessages {
			recorder.Eventf(object, corev1.EventTypeWarning, "KubernetesClusterValidationFailed", errorMessage)
		}
		return false
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

func KopsDeleteResources(ctx context.Context, log logr.Logger, cloud fi.Cloud, kopsClientset simple.Clientset, kopsCluster *kopsapi.Cluster) error {
	if len(kopsCluster.Name) == 0 {
		return errors.New("cluster name is required")
	}

	// Disable stdout to avoid printing the kops output
	stdout := os.Stdout
	defer func() {
		os.Stdout = stdout
	}()
	os.Stdout = nil

	log.Info("listing leftover cloud resources for deletion")
	allResources, err := resourceops.ListResources(cloud, kopsCluster)
	if err != nil {
		return err
	}

	clusterResources := make(map[string]*resources.Resource)
	for k, resource := range allResources {
		if resource.Shared {
			continue
		}
		clusterResources[k] = resource
	}

	log.Info(fmt.Sprintf("deleting %d leftover cloud resources", len(clusterResources)))
	err = resourceops.DeleteResources(cloud, clusterResources, 50, 10*time.Second, 60*time.Second)
	if err != nil {
		return err
	}
	log.Info(fmt.Sprintf("deleted %d leftover cloud resources", len(clusterResources)))

	log.Info("deleting cluster from kops state")
	err = kopsClientset.DeleteCluster(ctx, kopsCluster)
	if err != nil {
		return fmt.Errorf("error removing cluster from state store: %v", err)
	}
	log.Info("deleted cluster from kops state")

	return nil

}

func GetAmiNameFromImageSource(image string) (string, string, error) {
	parts := strings.SplitN(image, "/", 2)
	if len(parts) > 1 {
		return parts[1], parts[0], nil
	} else {
		return "", "", errors.New("invalid image format, should receive image source")
	}
}

func GetUserDataFromTerraformFile(clusterName, igName, terraformOutputDir string) (string, error) {
	userDataFile, err := os.Open(fmt.Sprintf(terraformOutputDir+"/data/aws_launch_template_%s.%s_user_data", igName, clusterName))
	if err != nil {
		return "", err
	}
	defer func() {
		_ = userDataFile.Close()
	}()
	userData, err := io.ReadAll(userDataFile)
	if err != nil {
		return "", err
	}
	if len(userData) == 0 {
		return "", errors.New("user data file is empty")
	}
	return string(userData), nil
}

func DeleteOwnerResources(ctx context.Context, kubeClient client.Client, resource client.Object) error {
	for _, ownerReference := range resource.GetOwnerReferences() {
		err := kubeClient.Delete(ctx, capiutil.ObjectReferenceToUnstructured(
			corev1.ObjectReference{
				Kind:       ownerReference.Kind,
				Namespace:  resource.GetNamespace(),
				Name:       ownerReference.Name,
				UID:        ownerReference.UID,
				APIVersion: ownerReference.APIVersion,
			},
		))
		if client.IgnoreNotFound(err) != nil {
			return err
		}
	}

	// Wait for child resources to be marked for deletion
	// Either this or a complex retry until
	time.Sleep(3 * time.Second)
	return nil
}

// GetClusterByName finds and return a Cluster object using the specified params.
func GetClusterByName(ctx context.Context, c client.Client, namespace, name string) (*clusterv1beta1.Cluster, error) {
	cluster := &clusterv1beta1.Cluster{}
	key := client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}

	if err := c.Get(ctx, key, cluster); err != nil {
		return nil, errors.Wrapf(err, "failed to get Cluster/%s", name)
	}

	return cluster, nil
}

func SetEnvVarsFromAWSCredentials(awsConfig aws.Credentials) error {
	err := os.Unsetenv("AWS_ACCESS_KEY_ID")
	if err != nil {
		return err
	}
	err = os.Unsetenv("AWS_SECRET_ACCESS_KEY")
	if err != nil {
		return err
	}

	err = os.Setenv("AWS_ACCESS_KEY_ID", awsConfig.AccessKeyID)
	if err != nil {
		return err
	}
	err = os.Setenv("AWS_SECRET_ACCESS_KEY", awsConfig.SecretAccessKey)
	if err != nil {
		return err
	}

	return nil
}

func GetAWSCredentialsFromKopsControlPlaneSecret(ctx context.Context, c client.Reader, secretName, namespace string) (*aws.Credentials, error) {
	secret := &corev1.Secret{}
	key := client.ObjectKey{
		Namespace: namespace,
		Name:      secretName,
	}
	if err := c.Get(ctx, key, secret); err != nil {
		return nil, errors.Wrapf(err, "failed to get Secret/%s", secretName)
	}
	accessKeyID := string(secret.Data["AccessKeyID"])
	secretAccessKey := string(secret.Data["SecretAccessKey"])

	creds := &aws.Credentials{
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
	}

	return creds, nil
}
