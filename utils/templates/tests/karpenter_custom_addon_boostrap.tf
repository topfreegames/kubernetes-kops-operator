resource "aws_s3_object" "custom-addon-bootstrap" {
  bucket  = "tests"
  content = <<EOF
kind: Addons
metadata:
  name: addons
spec:
  addons:
  - name: karpenter-provisioners.wildlife.io
    version: 0.0.1
    selector:
      k8s-addon: karpenter-provisioners.wildlife.io
    manifest: karpenter-provisioners.wildlife.io/provisioners.yaml
    manifestHash: "5bac255e7afd3072f86c42ca20bb87137010affde05fb0bf933a9af7a180f891"
    prune:
      kinds:
      - group: karpenter.sh
        kind: Provisioner
        labelSelector: "kops.k8s.io/managed-by=kops-controller"
  - name: karpenter-nodepools.wildlife.io
    version: 0.0.1
    selector:
      k8s-addon: karpenter-nodepools.wildlife.io
    manifest: karpenter-nodepools.wildlife.io/nodepools.yaml
    manifestHash: "5bac255e7afd3072f86c42ca20bb87137010affde05fb0bf933a9af7a180f891"
    prune:
      kinds:
      - group: karpenter.sh
        kind: NodePool
        labelSelector: "kops.k8s.io/managed-by=kops-controller"
EOF
  key = "test-cluster.test.k8s.cluster/custom-addons/addon.yaml"
  provider = aws.files
}

resource "aws_s3_object" "custom-addon-karpenter-provisioners" {
  bucket  = "tests"
  content = file("${path.module}/data/aws_s3_object_karpenter_provisioners_content")
  key = "test-cluster.test.k8s.cluster/custom-addons/karpenter-provisioners.wildlife.io/provisioners.yaml"
  provider = aws.files
}

resource "aws_s3_object" "custom-addon-karpenter-nodepools" {
  bucket  = "tests"
  content = file("${path.module}/data/aws_s3_object_karpenter_nodepools_content")
  key = "test-cluster.test.k8s.cluster/custom-addons/karpenter-nodepools.wildlife.io/nodepools.yaml"
  provider = aws.files
}
