package utils

import (
	"fmt"
	"io/ioutil"
	"os"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

func TestCreateAdditionalTerraformFiles(t *testing.T) {
	RegisterFailHandler(Fail)
	g := NewWithT(t)

	tmpDir, err := ioutil.TempDir("", "test_terraform")
	g.Expect(err).NotTo(HaveOccurred())

	testCases := []struct {
		description    string
		input          []Template
		expectedOutput []struct {
			filename string
			content  string
		}
		expectedError bool
	}{
		{
			description: "should create the file in the destination",
			input: []Template{
				{
					Filename:     fmt.Sprintf("%s/test_output", tmpDir),
					TemplatePath: "fixtures/test_template.tpl",
					Data:         "test.test.us-east-1.k8s.tfgco.com",
				},
			},
			expectedOutput: []struct {
				filename string
				content  string
			}{
				{
					filename: fmt.Sprintf("%s/test_output", tmpDir),
					content:  "test.test.us-east-1.k8s.tfgco.com",
				},
			},
			expectedError: false,
		},
		{
			description: "should create multiple files in the destination",
			input: []Template{
				{
					Filename:     fmt.Sprintf("%s/test_output_A", tmpDir),
					TemplatePath: "fixtures/test_multiple_template_a.tpl",
					Data:         "test.test.us-east-1.k8s.tfgco.com",
				},
				{
					Filename:     fmt.Sprintf("%s/test_output_B", tmpDir),
					TemplatePath: "fixtures/test_multiple_template_b.tpl",
					Data:         "test.test.us-east-1.k8s.tfgco.com",
				},
			},
			expectedOutput: []struct {
				filename string
				content  string
			}{
				{
					filename: fmt.Sprintf("%s/test_output_A", tmpDir),
					content:  "test.test.us-east-1.k8s.tfgco.com-a",
				},
				{
					filename: fmt.Sprintf("%s/test_output_B", tmpDir),
					content:  "test.test.us-east-1.k8s.tfgco.com-b",
				},
			},
			expectedError: false,
		},
		{
			description: "should fail trying to create the file in an invalid directory",
			input: []Template{
				{
					Filename:     fmt.Sprintf("%s/invalid-directory/test_output", tmpDir),
					TemplatePath: "fixtures/test_template.tpl",
					Data:         "test.test.us-east-1.k8s.tfgco.com",
				},
			},
			expectedError: true,
		},
		{
			description: "should fail when trying to load a inexisting template",
			input: []Template{
				{
					Filename:     fmt.Sprintf("%s/test_output", tmpDir),
					TemplatePath: "fixtures/inexisting_template.tpl",
					Data:         "test.test.us-east-1.k8s.tfgco.com",
				},
			},
			expectedError: true,
		},
		{
			description: "should fail when trying to execute an invalid template",
			input: []Template{
				{
					Filename:     fmt.Sprintf("%s/test_output", tmpDir),
					TemplatePath: "fixtures/test_invalid_template.tpl",
					Data:         "test.test.us-east-1.k8s.tfgco.com",
				},
			},
			expectedError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			err := CreateAdditionalTerraformFiles(tc.input...)
			if !tc.expectedError {
				g.Expect(err).NotTo(HaveOccurred())
				for _, output := range tc.expectedOutput {
					content, err := os.ReadFile(output.filename)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(string(content)).To(Equal(output.content))
				}
			} else {
				g.Expect(err).To(HaveOccurred())
			}
		})
	}
}
