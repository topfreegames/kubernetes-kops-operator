package utils

import (
	"fmt"
	"os"
	"path"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

func TestCreateTerraformBackendFile(t *testing.T) {
	s3Bucket := "tests"
	clusterName := "test-cluster.test.k8s.cluster"
	RegisterFailHandler(Fail)
	g := NewWithT(t)

	testCases := []map[string]interface{}{
		{
			"description":   "Should successfully create the file",
			"outputDir":     fmt.Sprintf("%s/%s", os.TempDir(), clusterName),
			"expectedError": false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc["description"].(string), func(t *testing.T) {
			err := CreateTerraformBackendFile(s3Bucket, clusterName, tc["outputDir"].(string))
			if !tc["expectedError"].(bool) {
				g.Expect(err).NotTo(HaveOccurred())
				_, err = os.Stat(path.Join(tc["outputDir"].(string), "backend.tf"))
				g.Expect(err).NotTo(HaveOccurred())
			} else {
				g.Expect(err).To(HaveOccurred())
			}

		})
	}
}
