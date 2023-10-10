package utils

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"syscall"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	//go:embed fixtures/*.tpl
	templates embed.FS
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
					EmbeddedFiles:    templates,
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
					EmbeddedFiles:    templates,
					Data:             "test.test.us-east-1.k8s.tfgco.com",
				},
				{
					TemplateFilename: "fixtures/test_multiple_template_b.tpl",
					OutputFilename:   fmt.Sprintf("%s/test_output_B", tmpDir),
					EmbeddedFiles:    templates,
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
					EmbeddedFiles:    templates,
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
					EmbeddedFiles:    templates,
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
					EmbeddedFiles:    templates,
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

func TestCleanupTerraformDirectory(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test")

	testCases := []struct {
		description   string
		input         string
		files         []string
		validateFunc  func([]string) bool
		expectedError error
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
			description: "should return error when trying to remove files in an invalid directory",
			input:       "/invalid-directory",
			expectedError: &fs.PathError{
				Op:   "open",
				Path: "/invalid-directory",
				Err:  syscall.ENOENT,
			},
		},
		{
			description: "should return error when the file isn't a directory",
			input:       fmt.Sprintf(tmpDir + "/a"),
			files: []string{
				"a",
			},
			expectedError: &fs.PathError{
				Op:   "fdopendir",
				Path: fmt.Sprintf(tmpDir + "/a"),
				Err:  syscall.ENOTDIR,
			},
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
			if tc.expectedError != nil {
				g.Expect(err).To(BeEquivalentTo(tc.expectedError))
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
