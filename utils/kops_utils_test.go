package utils

import (
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
