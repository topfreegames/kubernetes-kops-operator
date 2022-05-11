package utils

import (
	"bytes"
	"context"
	"github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kops/pkg/client/simple"
	"k8s.io/kops/pkg/client/simple/vfsclientset"
	"k8s.io/kops/upup/pkg/fi"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	kopsapi "k8s.io/kops/pkg/apis/kops"

	"k8s.io/kops/util/pkg/vfs"
)

func Test_GetBucketName(t *testing.T) {

	testCases := []map[string]interface{}{
		{
			"description":    "Should return bucket name",
			"expectedResult": "bucket-test",
			"expectedError":  false,
			"input":          "s3://bucket-test/cluster.general.test.wildlife.io",
		},
		{
			"description":    "Should return err",
			"expectedResult": "bucket-test",
			"expectedError":  true,
			"input":          "",
		},
	}
	for _, tc := range testCases {

		t.Run(tc["description"].(string), func(t *testing.T) {
			bucketName, err := GetBucketName(tc["input"].(string))
			if err == nil && tc["expectedError"] == true {
				assert.Fail(t, "expected error")
			}
			if tc["expectedError"] == false {
				assert.Equal(t, bucketName, tc["expectedResult"])
			}
		})
	}

}

func Test_GetKopsClientset(t *testing.T) {

	testCases := []map[string]interface{}{
		{
			"description":        "Should return clientset for s3",
			"expectedPathPrefix": "s3://",
			"expectedError":      false,
			"input":              "s3://bucket-test/cluster.general.test.wildlife.io",
		},
		{
			"description":        "Should return clientset for memfs",
			"expectedPathPrefix": "memfs://",
			"expectedError":      false,
			"input":              "memfs://bucket-test/cluster.general.test.wildlife.io",
		},
	}
	vfs.Context.ResetMemfsContext(true)
	for _, tc := range testCases {
		t.Run(tc["description"].(string), func(t *testing.T) {
			configBase := tc["input"].(string)
			clientset, err := GetKopsClientset(configBase)
			if tc["expectedError"].(bool) == false && err != nil {
				assert.Fail(t, "expected error")
			}
			kopsCluster := &kopsapi.Cluster{
				Spec: kopsapi.ClusterSpec{
					ConfigBase: configBase,
				},
			}
			path, _ := clientset.ConfigBaseFor(kopsCluster)
			assert.True(t, strings.HasPrefix(path.Path(), tc["expectedPathPrefix"].(string)))
		})
	}

}

func TestReconcileKopsSecretsDelete(t *testing.T) {
	testCases := []struct {
		description               string
		statusKopsSecret          []string
		actualKopsSecrets         []string
		desiredKopsSecrets        map[string][]byte
		expectedKopsSecrets       []string
		expectedStatusKopsSecrets []string
	}{
		{
			description: "shouldn't remove anything",
			statusKopsSecret: []string{
				"dockerconfig",
				"customSecret",
			},
			actualKopsSecrets: []string{
				"dockerconfig",
				"customSecret",
				"admin",
				"kube-proxy",
			},
			desiredKopsSecrets: map[string][]byte{
				"dockerconfig": []byte(""),
				"customSecret": []byte(""),
			},
			expectedKopsSecrets: []string{
				"dockerconfig",
				"customSecret",
				"admin",
				"kube-proxy",
			},
			expectedStatusKopsSecrets: []string{
				"dockerconfig",
				"customSecret",
			},
		},
		{
			description: "should remove customSecret",
			statusKopsSecret: []string{
				"dockerconfig",
				"customSecret",
			},
			actualKopsSecrets: []string{
				"dockerconfig",
				"customSecret",
				"admin",
				"kube-proxy",
			},
			desiredKopsSecrets: map[string][]byte{
				"dockerconfig": []byte(""),
			},
			expectedKopsSecrets: []string{
				"dockerconfig",
				"admin",
				"kube-proxy",
			},
			expectedStatusKopsSecrets: []string{
				"dockerconfig",
			},
		},
		{
			description: "should only remove secret from status when it was already deleted",
			statusKopsSecret: []string{
				"dockerconfig",
				"customSecret",
			},
			actualKopsSecrets: []string{
				"dockerconfig",
				"admin",
				"kube-proxy",
			},
			desiredKopsSecrets: map[string][]byte{
				"dockerconfig": []byte(""),
			},
			expectedKopsSecrets: []string{
				"dockerconfig",
				"admin",
				"kube-proxy",
			},
			expectedStatusKopsSecrets: []string{
				"dockerconfig",
			},
		},
	}

	ctx := context.TODO()
	vfs.Context.ResetMemfsContext(true)
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {

			fakeKopsClientset := newFakeKopsClientset()
			kopsCluster := &kopsapi.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testCluster",
				},
				Spec: kopsapi.ClusterSpec{
					ConfigBase: "memfs://bucket-test/testCluster",
				},
			}
			_, _ = fakeKopsClientset.CreateCluster(ctx, kopsCluster)
			fakeSecretStore, _ := fakeKopsClientset.SecretStore(kopsCluster)
			kopsSecret := &fi.Secret{}
			for _, kopsSecretName := range tc.actualKopsSecrets {
				_, _, _ = fakeSecretStore.GetOrCreateSecret(kopsSecretName, kopsSecret)
			}

			kopsControlPlane := &v1alpha1.KopsControlPlane{
				Status: v1alpha1.KopsControlPlaneStatus{
					Secrets: tc.statusKopsSecret,
				},
			}

			err := reconcileKopsSecretsDelete(fakeSecretStore, kopsControlPlane, tc.desiredKopsSecrets)
			assert.Nil(t, err)
			for _, secretName := range tc.expectedKopsSecrets {
				secret, _ := fakeSecretStore.FindSecret(secretName)
				assert.NotNil(t, secret)
			}
			assert.Equal(t, tc.expectedStatusKopsSecrets, kopsControlPlane.Status.Secrets)

		})
	}
}

func TestReconcileKopsSecrets(t *testing.T) {
	testCases := []struct {
		description               string
		statusKopsSecret          []string
		actualKopsSecrets         map[string][]byte
		desiredKopsSecrets        map[string][]byte
		expectedKopsSecrets       map[string][]byte
		expectedStatusKopsSecrets []string
	}{
		{
			description: "should create desired secrets",
			actualKopsSecrets: map[string][]byte{
				"admin":      []byte("admin"),
				"kube-proxy": []byte("kube-proxy"),
			},
			desiredKopsSecrets: map[string][]byte{
				"dockerconfig": []byte("{}"),
				"customSecret": []byte("testSecretContent"),
			},
			expectedKopsSecrets: map[string][]byte{
				"admin":        []byte("admin"),
				"kube-proxy":   []byte("kube-proxy"),
				"dockerconfig": []byte("{}"),
				"customSecret": []byte("testSecretContent"),
			},
			expectedStatusKopsSecrets: []string{
				"dockerconfig",
				"customSecret",
			},
		},
		{
			description: "should update dockerconfig secret",
			statusKopsSecret: []string{
				"dockerconfig",
				"customSecret",
			},
			actualKopsSecrets: map[string][]byte{
				"admin":        []byte("admin"),
				"kube-proxy":   []byte("kube-proxy"),
				"dockerconfig": []byte("{}"),
				"customSecret": []byte("testSecretContent"),
			},
			desiredKopsSecrets: map[string][]byte{
				"dockerconfig": []byte("dockerconfig updated"),
				"customSecret": []byte("testSecretContent"),
			},
			expectedKopsSecrets: map[string][]byte{
				"admin":        []byte("admin"),
				"kube-proxy":   []byte("kube-proxy"),
				"dockerconfig": []byte("dockerconfig updated"),
				"customSecret": []byte("testSecretContent"),
			},
			expectedStatusKopsSecrets: []string{
				"dockerconfig",
				"customSecret",
			},
		},
		{
			description: "should just update status",
			statusKopsSecret: []string{
				"dockerconfig",
			},
			actualKopsSecrets: map[string][]byte{
				"admin":        []byte("admin"),
				"kube-proxy":   []byte("kube-proxy"),
				"dockerconfig": []byte("{}"),
				"customSecret": []byte("testSecretContent"),
			},
			desiredKopsSecrets: map[string][]byte{
				"dockerconfig": []byte("{}"),
				"customSecret": []byte("testSecretContent"),
			},
			expectedKopsSecrets: map[string][]byte{
				"admin":        []byte("admin"),
				"kube-proxy":   []byte("kube-proxy"),
				"dockerconfig": []byte("{}"),
				"customSecret": []byte("testSecretContent"),
			},
			expectedStatusKopsSecrets: []string{
				"dockerconfig",
				"customSecret",
			},
		},
	}

	ctx := context.TODO()
	vfs.Context.ResetMemfsContext(true)
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {

			fakeKopsClientset := newFakeKopsClientset()
			kopsCluster := &kopsapi.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testCluster",
				},
				Spec: kopsapi.ClusterSpec{
					ConfigBase: "memfs://bucket-test/testCluster",
				},
			}
			_, _ = fakeKopsClientset.CreateCluster(ctx, kopsCluster)
			fakeSecretStore, _ := fakeKopsClientset.SecretStore(kopsCluster)
			for kopsSecretName, data := range tc.actualKopsSecrets {
				kopsSecret := &fi.Secret{
					Data: data,
				}
				_, _, _ = fakeSecretStore.GetOrCreateSecret(kopsSecretName, kopsSecret)
			}

			kopsControlPlane := &v1alpha1.KopsControlPlane{
				Status: v1alpha1.KopsControlPlaneStatus{
					Secrets: tc.statusKopsSecret,
				},
			}

			err := reconcileKopsSecretsNormal(fakeSecretStore, kopsControlPlane, tc.desiredKopsSecrets)
			assert.Nil(t, err)
			for secretName, data := range tc.expectedKopsSecrets {
				secret, _ := fakeSecretStore.FindSecret(secretName)
				assert.NotNil(t, secret)
				assert.True(t, bytes.Equal(data, secret.Data))
			}
			assert.ElementsMatch(t, tc.expectedStatusKopsSecrets, kopsControlPlane.Status.Secrets)
		})
	}
}

func newFakeKopsClientset() simple.Clientset {
	memFSContext := vfs.NewMemFSContext()
	memfspath := vfs.NewMemFSPath(memFSContext, "memfs://tests")

	return vfsclientset.NewVFSClientset(memfspath)
}
