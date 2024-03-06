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
    manifestHash: "b3d32a0baf397c2f500b8936bad9e0581c6b234b7549d5aa624bc383722daaae"
    prune:
      kinds:
      - group: karpenter.sh
        kind: Provisioner
        labelSelector: "kops.k8s.io/managed-by=kops-controller"
  - name: karpenter-nodepool.wildlife.io
    version: 0.0.1
    selector:
      k8s-addon: karpenter-nodepool.wildlife.io
    manifest: karpenter-nodepool.wildlife.io/nodepool.yaml
    manifestHash: "b3d32a0baf397c2f500b8936bad9e0581c6b234b7549d5aa624bc383722daaae"
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
