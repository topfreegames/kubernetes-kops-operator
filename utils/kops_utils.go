package utils

import (
	"fmt"

	"k8s.io/kops/cmd/kops/util"
	"k8s.io/kops/pkg/client/simple"
)
func GetKopsClientset(s3Bucket string) (simple.Clientset, error) {
	factoryOptions := &util.FactoryOptions{
		RegistryPath: fmt.Sprintf("s3://%s", s3Bucket),
	}

	factory := util.NewFactory(factoryOptions)

	kopsClientset, err := factory.Clientset()
	if err != nil {
		return nil, err
	}
	return kopsClientset, nil
}
