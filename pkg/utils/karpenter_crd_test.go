package utils

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	awsapis "github.com/aws/karpenter-provider-aws/pkg/apis"
	"github.com/awslabs/operatorpkg/object"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestKarpenterResourcesAreAcceptedByKarpenterCRDs(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		if _, err := exec.LookPath("kube-apiserver"); err != nil {
			t.Skip("KUBEBUILDER_ASSETS is not set")
		}
	}

	testEnv := &envtest.Environment{
		CRDs: []*apiextensionsv1.CustomResourceDefinition{
			object.Unmarshal[apiextensionsv1.CustomResourceDefinition](awsapis.EC2NodeClassCRD),
			object.Unmarshal[apiextensionsv1.CustomResourceDefinition](awsapis.NodePoolCRD),
		},
	}
	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = testEnv.Stop() }()

	c, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile("templates/tests/data/karpenter_resource_output_node_pool_v1.yaml")
	if err != nil {
		t.Fatal(err)
	}

	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(content), 4096)
	checked := 0
	for {
		obj := &unstructured.Unstructured{}
		if err := decoder.Decode(&obj.Object); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
		if !strings.HasPrefix(obj.GetAPIVersion(), "karpenter.") {
			continue
		}
		unstructured.RemoveNestedField(obj.Object, "status")
		if err := c.Apply(context.Background(), client.ApplyConfigurationFromUnstructured(obj), client.FieldOwner("kops"), client.ForceOwnership, client.DryRunAll); err != nil {
			t.Errorf("%s %s rejected: %v", obj.GetKind(), obj.GetName(), err)
		}
		checked++
	}
	if checked != 2 {
		t.Errorf("expected to check a NodePool and an EC2NodeClass, checked %d objects", checked)
	}
}
