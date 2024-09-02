resource "aws_s3_object" "custom-addon-bootstrap" {
  bucket  = "tests"
  content = <<EOF
kind: Addons
metadata:
  name: addons
spec:
  addons:
  - name: karpenter-resources.wildlife.io
    version: 0.0.1
    selector:
      k8s-addon: karpenter-resources.wildlife.io
    manifest: karpenter-resources.wildlife.io/resources.yaml
    manifestHash: "{{ .ManifestHash }}"
    prune:
      kinds:
      - group: karpenter.sh
        kind: Provisioner
        labelSelector: "kops.k8s.io/managed-by=kops-controller"
      - group: karpenter.sh
        kind: NodePool
        labelSelector: "kops.k8s.io/managed-by=kops-controller"
      - group: karpenter.k8s.aws
        kind: EC2NodeClass
        labelSelector: "kops.k8s.io/managed-by=kops-controller"
EOF
  key = "test-cluster.test.k8s.cluster/custom-addons/addon.yaml"
  provider = aws.files
}

resource "aws_s3_object" "custom-addon-karpenter-resources" {
  bucket  = "tests"
  content = file("${path.module}/data/aws_s3_object_karpenter_resources_content")
  key = "test-cluster.test.k8s.cluster/custom-addons/karpenter-resources.wildlife.io/resources.yaml"
  provider = aws.files
}


