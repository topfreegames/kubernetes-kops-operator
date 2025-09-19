package utils

import (
	"embed"
	"fmt"
	"os"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	//go:embed fixtures/*.tpl
	testTemplates embed.FS
)

func TestCreateAdditionalTerraformFiles(t *testing.T) {
	RegisterFailHandler(Fail)
	g := NewWithT(t)

	tmpDir, err := os.MkdirTemp("", "test_terraform")
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
					TemplateFilename: "fixtures/test_template.tpl",
					OutputFilename:   fmt.Sprintf("%s/test_output", tmpDir),
					EmbeddedFiles:    testTemplates,
					Data:             "test.test.us-east-1.k8s.tfgco.com",
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
					TemplateFilename: "fixtures/test_multiple_template_a.tpl",
					OutputFilename:   fmt.Sprintf("%s/test_output_A", tmpDir),
					EmbeddedFiles:    testTemplates,
					Data:             "test.test.us-east-1.k8s.tfgco.com",
				},
				{
					TemplateFilename: "fixtures/test_multiple_template_b.tpl",
					OutputFilename:   fmt.Sprintf("%s/test_output_B", tmpDir),
					EmbeddedFiles:    testTemplates,
					Data:             "test.test.us-east-1.k8s.tfgco.com",
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
					TemplateFilename: "fixtures/test_template.tpl",
					OutputFilename:   fmt.Sprintf("%s/invalid-directory/test_output", tmpDir),
					EmbeddedFiles:    testTemplates,
					Data:             "test.test.us-east-1.k8s.tfgco.com",
				},
			},
			expectedError: true,
		},
		{
			description: "should fail when trying to load a inexisting template",
			input: []Template{
				{
					TemplateFilename: "fixtures/inexisting_template.tpl",
					OutputFilename:   fmt.Sprintf("%s/test_output", tmpDir),
					EmbeddedFiles:    testTemplates,
					Data:             "test.test.us-east-1.k8s.tfgco.com",
				},
			},
			expectedError: true,
		},
		{
			description: "should fail when trying to execute an invalid template",
			input: []Template{
				{
					TemplateFilename: "fixtures/test_invalid_template.tpl",
					OutputFilename:   fmt.Sprintf("%s/test_output", tmpDir),
					EmbeddedFiles:    testTemplates,
					Data:             "test.test.us-east-1.k8s.tfgco.com",
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

func readFixtureFile(filename string) (string, error) {
	content, err := os.ReadFile(fmt.Sprintf("fixtures/terraform/%s", filename))
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func TestModifyTerraformProviderVersion(t *testing.T) {
	RegisterFailHandler(Fail)
	g := NewWithT(t)

	testCases := []struct {
		description          string
		fixtureFile          string
		expectedFixtureFile  string
		awsProviderVersion   string
		expectedError        bool
		expectedErrorMessage string
		skipFileCreation     bool
	}{
		{
			description:         "should modify AWS provider version with double quotes in input",
			fixtureFile:         "kubernetes_basic.tf",
			expectedFixtureFile: "kubernetes_basic_expected.tf",
			awsProviderVersion:  `">= 5.0.0"`,
			expectedError:       false,
		},
		{
			description:         "should modify AWS provider version with single quotes in input",
			fixtureFile:         "kubernetes_basic.tf",
			expectedFixtureFile: "kubernetes_basic_expected.tf",
			awsProviderVersion:  `'>= 5.0.0'`,
			expectedError:       false,
		},
		{
			description:         "should modify AWS provider version with mixed quotes in input",
			fixtureFile:         "kubernetes_basic.tf",
			expectedFixtureFile: "kubernetes_basic_expected.tf",
			awsProviderVersion:  `'">= 5.0.0"'`,
			expectedError:       false,
		},
		{
			description:         "should modify AWS provider version without quotes in input",
			fixtureFile:         "kubernetes_basic.tf",
			expectedFixtureFile: "kubernetes_basic_expected.tf",
			awsProviderVersion:  `>= 5.0.0`,
			expectedError:       false,
		},
		{
			description:         "should handle quoted version key AWS provider block",
			fixtureFile:         "kubernetes_quoted_version.tf",
			expectedFixtureFile: "kubernetes_quoted_version_expected.tf",
			awsProviderVersion:  `">= 5.50.0"`,
			expectedError:       false,
		},
		{
			description:          "should return error when AWS provider version pattern not found",
			fixtureFile:          "kubernetes_no_aws_provider.tf",
			awsProviderVersion:   `">= 5.0.0"`,
			expectedError:        true,
			expectedErrorMessage: "failed to find AWS provider version pattern",
		},
		{
			description:         "should handle exact version format",
			fixtureFile:         "kubernetes_exact_version.tf",
			expectedFixtureFile: "kubernetes_exact_version_expected.tf",
			awsProviderVersion:  `'5.0.0'`,
			expectedError:       false,
		},
		{
			description:          "should return error when kubernetes.tf file doesn't exist",
			skipFileCreation:     true,
			awsProviderVersion:   `"~> 5.0"`,
			expectedError:        true,
			expectedErrorMessage: "failed to read kubernetes.tf",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			tmpDir, err := os.MkdirTemp("", "test_terraform_provider")
			g.Expect(err).NotTo(HaveOccurred())
			defer os.RemoveAll(tmpDir)

			kubernetesFile := fmt.Sprintf("%s/kubernetes.tf", tmpDir)

			if !tc.skipFileCreation {
				fixtureContent, err := readFixtureFile(tc.fixtureFile)
				g.Expect(err).NotTo(HaveOccurred())

				err = os.WriteFile(kubernetesFile, []byte(fixtureContent), 0644)
				g.Expect(err).NotTo(HaveOccurred())
			}

			err = ModifyTerraformProviderVersion(tmpDir, tc.awsProviderVersion)

			if tc.expectedError {
				g.Expect(err).To(HaveOccurred())
				if tc.expectedErrorMessage != "" {
					g.Expect(err.Error()).To(ContainSubstring(tc.expectedErrorMessage))
				}
			} else {
				g.Expect(err).NotTo(HaveOccurred())

				expectedContent, err := readFixtureFile(tc.expectedFixtureFile)
				g.Expect(err).NotTo(HaveOccurred())

				actualContent, err := os.ReadFile(kubernetesFile)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(actualContent)).To(Equal(expectedContent))
			}
		})
	}
}

func TestCleanupTerraformDirectory(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test")

	testCases := []struct {
		description          string
		input                string
		files                []string
		validateFunc         func([]string) bool
		expectedErrorMessage string
	}{
		{
			description: "should remove all files in the directory",
			input:       tmpDir,
			files: []string{
				"a",
				"b",
				"c",
			},
			validateFunc: func(files []string) bool {
				return len(files) == 0
			},
		},
		{
			description: "should remove all files, except .terraform",
			input:       tmpDir,
			files: []string{
				".terraform",
				"a",
				"b",
				"c",
			},
			validateFunc: func(files []string) bool {
				return len(files) == 1 && files[0] == ".terraform"
			},
		},
		{
			description:          "should return error when trying to remove files in an invalid directory",
			input:                "/invalid-directory",
			expectedErrorMessage: "no such file or directory",
		},
		{
			description: "should return error when the file isn't a directory",
			input:       tmpDir + "/a",
			files: []string{
				"a",
			},
			expectedErrorMessage: "not a directory",
		},
	}

	RegisterFailHandler(Fail)
	g := NewWithT(t)

	g.Expect(err).To(BeNil())

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			for _, file := range tc.files {
				_, err := os.Create(fmt.Sprintf("%s/%s", tmpDir, file))
				g.Expect(err).To(BeNil())
			}
			err := CleanupTerraformDirectory(tc.input)
			if len(tc.expectedErrorMessage) > 0 {
				g.Expect(err).ToNot(BeNil())
				g.Expect(strings.Contains(err.Error(), tc.expectedErrorMessage)).To(BeTrue())
				return
			} else {
				g.Expect(err).To(BeNil())
			}
			d, err := os.Open(tmpDir)
			g.Expect(err).To(BeNil())
			files, err := d.Readdirnames(-1)
			g.Expect(err).To(BeNil())
			g.Expect(tc.validateFunc(files)).To(BeTrue())
		})
	}
}
