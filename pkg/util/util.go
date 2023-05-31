package util

import (
	"context"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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

func GetAWSCredentialsFromKopsControlPlaneSecret(ctx context.Context, c client.Client, secretName, namespace string) (*aws.Credentials, error) {
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
