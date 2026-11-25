package controlplane

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kops/pkg/apis/kops"
	"k8s.io/kops/pkg/client/simple/vfsclientset"
	"k8s.io/kops/pkg/kopscodecs"
	"k8s.io/kops/pkg/testutils"
	"k8s.io/kops/upup/pkg/fi/cloudup"
	"k8s.io/kops/util/pkg/vfs"
)

// TestKarpenterInstanceGroupRender runs the real kops render, without mocks, for a cluster
// that runs Karpenter outside kops: the Karpenter addon is disabled and an instance group has
// manager Karpenter. The operator builds each EC2NodeClass from what kops renders for that
// instance group, so a kops upgrade that stops rendering it must fail here.
func TestKarpenterInstanceGroupRender(t *testing.T) {
	ctx := context.Background()
	t.Setenv("KOPS_RUN_TOO_NEW_VERSION", "1")

	h := testutils.NewIntegrationTestHarness(t)
	defer h.Close()
	h.SetupMockAWS()

	kopsModuleDir, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "k8s.io/kops").Output()
	if err != nil {
		t.Fatalf("finding the kops module directory: %v", err)
	}
	kops.DefaultChannelBase = "file://" + filepath.ToSlash(filepath.Join(strings.TrimSpace(string(kopsModuleDir)), "channels")) + "/"

	fixture, err := os.ReadFile("testdata/karpenter-render/in-v1alpha2.yaml")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	sshPublicKey, err := os.ReadFile("testdata/karpenter-render/id_rsa.pub")
	if err != nil {
		t.Fatalf("reading ssh public key: %v", err)
	}

	var cluster *kops.Cluster
	var instanceGroups []*kops.InstanceGroup
	for _, section := range bytes.Split(fixture, []byte("\n---\n")) {
		obj, _, err := kopscodecs.Decode(section, nil)
		if err != nil {
			t.Fatalf("parsing fixture: %v", err)
		}
		switch v := obj.(type) {
		case *kops.Cluster:
			cluster = v
		case *kops.InstanceGroup:
			instanceGroups = append(instanceGroups, v)
		default:
			t.Fatalf("unexpected object in fixture: %T", obj)
		}
	}

	basePath, err := vfs.Context.BuildVfsPath("memfs://clusters.example.com")
	if err != nil {
		t.Fatalf("building state store path: %v", err)
	}
	clientset := vfsclientset.NewVFSClientset(vfs.Context, basePath)
	if _, err := clientset.CreateCluster(ctx, cluster); err != nil {
		t.Fatalf("creating cluster: %v", err)
	}
	for _, ig := range instanceGroups {
		if _, err := clientset.InstanceGroupsFor(cluster).Create(ctx, ig, metav1.CreateOptions{}); err != nil {
			t.Fatalf("creating instance group %q: %v", ig.Name, err)
		}
	}
	if err := addSSHCredential(ctx, cluster, clientset, string(sshPublicKey)); err != nil {
		t.Fatalf("adding ssh credential: %v", err)
	}

	cloud, err := cloudup.BuildCloud(cluster)
	if err != nil {
		t.Fatalf("building cloud: %v", err)
	}
	if _, err := PopulateClusterSpec(ctx, cluster, clientset, cloud); err != nil {
		t.Fatalf("populating cluster spec: %v", err)
	}

	outDir := t.TempDir()
	if err := PrepareKopsCloudResources(ctx, clientset, cluster, outDir, cloud); err != nil {
		t.Fatalf("rendering cloud resources: %v", err)
	}

	userData, err := os.ReadFile(filepath.Join(outDir, "data", "aws_s3_object_nodeupscript-karpenter-nodes_content"))
	if err != nil {
		t.Fatalf("kops did not render the nodeup script that the EC2NodeClass uses as user data: %v", err)
	}
	if !strings.Contains(string(userData), "nodeup") {
		t.Errorf("nodeup script for the Karpenter instance group does not run nodeup:\n%s", userData)
	}

	nodeupConfig, err := os.ReadFile(filepath.Join(outDir, "data", "aws_s3_object_nodeupconfig-karpenter-nodes_content"))
	if err != nil {
		t.Fatalf("reading nodeup config of the Karpenter instance group: %v", err)
	}
	if !strings.Contains(string(nodeupConfig), "karpenter.sh/unregistered") {
		t.Errorf("nodeup config of the Karpenter instance group does not register nodes with the karpenter.sh/unregistered taint")
	}

	terraform, err := os.ReadFile(filepath.Join(outDir, "kubernetes.tf"))
	if err != nil {
		t.Fatalf("reading kubernetes.tf: %v", err)
	}
	if strings.Contains(string(terraform), `resource "aws_autoscaling_group" "karpenter-nodes-`) {
		t.Errorf("kops rendered an Auto Scaling group for the Karpenter instance group")
	}
	if !strings.Contains(string(terraform), `resource "aws_autoscaling_group" "nodes-`) {
		t.Errorf("kops did not render the Auto Scaling group of the cloud group instance group")
	}
}
