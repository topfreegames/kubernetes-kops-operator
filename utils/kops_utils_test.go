package utils

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/topfreegames/kubernetes-kops-operator/pkg/helpers"

	controlplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kops/pkg/featureflag"
	"k8s.io/kops/upup/pkg/fi"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stretchr/testify/assert"
	kopsapi "k8s.io/kops/pkg/apis/kops"

	"k8s.io/kops/util/pkg/vfs"
)

func TestParseSpotinstFeatureflags(t *testing.T) {
	testCases := []struct {
		description          string
		input                *controlplanev1alpha1.KopsControlPlane
		environmentVariables map[string]string
		expectedError        bool
		expectedResult       map[string]bool
	}{
		{
			description: "should enable Spotinst, SpotinstOcean and SpotinstHybrid",
			input: &controlplanev1alpha1.KopsControlPlane{
				Spec: controlplanev1alpha1.KopsControlPlaneSpec{
					SpotInst: controlplanev1alpha1.SpotInstSpec{
						Enabled:      true,
						FeatureFlags: "+SpotinstOcean,SpotinstHybrid",
					},
				},
			},
			environmentVariables: map[string]string{
				"SPOTINST_TOKEN":   "token",
				"SPOTINST_ACCOUNT": "account",
			},
			expectedResult: map[string]bool{
				"Spotinst":       true,
				"SpotinstOcean":  true,
				"SpotinstHybrid": true,
			},
		},
		{
			description: "should enable Spotinst",
			input: &controlplanev1alpha1.KopsControlPlane{
				Spec: controlplanev1alpha1.KopsControlPlaneSpec{
					SpotInst: controlplanev1alpha1.SpotInstSpec{
						Enabled: true,
					},
				},
			},
			environmentVariables: map[string]string{
				"SPOTINST_TOKEN":   "token",
				"SPOTINST_ACCOUNT": "account",
			},
			expectedResult: map[string]bool{
				"Spotinst":       true,
				"SpotinstOcean":  false,
				"SpotinstHybrid": false,
			},
		},

		{
			description: "should not enable any feature flag",
			input:       helpers.NewKopsControlPlane("testKopsControlPlane", metav1.NamespaceDefault),
			environmentVariables: map[string]string{
				"SPOTINST_TOKEN":   "token",
				"SPOTINST_ACCOUNT": "account",
			},
			expectedResult: map[string]bool{
				"Spotinst":       false,
				"SpotinstOcean":  false,
				"SpotinstHybrid": false,
			},
		},
		{
			description: "should return error if credentials environment variables aren't defined",
			input: &controlplanev1alpha1.KopsControlPlane{
				Spec: controlplanev1alpha1.KopsControlPlaneSpec{
					SpotInst: controlplanev1alpha1.SpotInstSpec{
						Enabled:      true,
						FeatureFlags: "+SpotinstOcean,SpotinstHybrid",
					},
				},
			},
			expectedError: true,
			expectedResult: map[string]bool{
				"Spotinst":       false,
				"SpotinstOcean":  false,
				"SpotinstHybrid": false,
			},
		},
	}

	RegisterFailHandler(Fail)
	g := NewWithT(t)

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			os.Unsetenv("SPOTINST_TOKEN")
			os.Unsetenv("SPOTINST_ACCOUNT")
			for key, value := range tc.environmentVariables {
				os.Setenv(key, value)
			}

			err := ParseSpotinstFeatureflags(tc.input)
			if tc.expectedError {
				g.Expect(err).ToNot(BeNil())
			} else {
				g.Expect(err).To(BeNil())
			}
			g.Expect(featureflag.Spotinst.Enabled()).To(BeEquivalentTo(tc.expectedResult["Spotinst"]))
			g.Expect(featureflag.SpotinstOcean.Enabled()).To(BeEquivalentTo(tc.expectedResult["SpotinstOcean"]))
			g.Expect(featureflag.SpotinstHybrid.Enabled()).To(BeEquivalentTo(tc.expectedResult["SpotinstHybrid"]))
		})
	}
}

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
					ConfigStore: kopsapi.ConfigStoreSpec{
						Base: configBase,
					},
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

			fakeKopsClientset := helpers.NewFakeKopsClientset()
			kopsCluster := &kopsapi.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testCluster",
				},
				Spec: kopsapi.ClusterSpec{
					ConfigStore: kopsapi.ConfigStoreSpec{
						Base: "memfs://bucket-test/testCluster",
					},
				},
			}
			_, _ = fakeKopsClientset.CreateCluster(ctx, kopsCluster)
			fakeSecretStore, _ := fakeKopsClientset.SecretStore(kopsCluster)
			kopsSecret := &fi.Secret{}
			for _, kopsSecretName := range tc.actualKopsSecrets {
				_, _, _ = fakeSecretStore.GetOrCreateSecret(ctx, kopsSecretName, kopsSecret)
			}

			kopsControlPlane := &controlplanev1alpha1.KopsControlPlane{
				Status: controlplanev1alpha1.KopsControlPlaneStatus{
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

			fakeKopsClientset := helpers.NewFakeKopsClientset()
			kopsCluster := &kopsapi.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testCluster",
				},
				Spec: kopsapi.ClusterSpec{
					ConfigStore: kopsapi.ConfigStoreSpec{
						Base: "memfs://bucket-test/testCluster",
					},
				},
			}
			_, _ = fakeKopsClientset.CreateCluster(ctx, kopsCluster)
			fakeSecretStore, _ := fakeKopsClientset.SecretStore(kopsCluster)
			for kopsSecretName, data := range tc.actualKopsSecrets {
				kopsSecret := &fi.Secret{
					Data: data,
				}
				_, _, _ = fakeSecretStore.GetOrCreateSecret(ctx, kopsSecretName, kopsSecret)
			}

			kopsControlPlane := &controlplanev1alpha1.KopsControlPlane{
				Status: controlplanev1alpha1.KopsControlPlaneStatus{
					Secrets: tc.statusKopsSecret,
				},
			}

			err := reconcileKopsSecretsNormal(ctx, fakeSecretStore, kopsControlPlane, tc.desiredKopsSecrets)
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
