package controlplane

import (
	infrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"
	kopsapi "k8s.io/kops/pkg/apis/kops"
)

const (
	KarpenterProvisionersLabel = "provisioners"
	KarpenterNodePoolsLabel    = "nodepools"
)

type EnableKarpenterParams struct {
	S3BucketName       string
	KindName           string
	TerraformOutputDir string
	KopsCluster        *kopsapi.Cluster
	KMPS               []infrastructurev1alpha1.KopsMachinePool
}
