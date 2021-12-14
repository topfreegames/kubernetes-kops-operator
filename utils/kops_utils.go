package utils

import (
	"errors"
	"strings"

	"k8s.io/kops/cmd/kops/util"
	"k8s.io/kops/pkg/client/simple"
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
