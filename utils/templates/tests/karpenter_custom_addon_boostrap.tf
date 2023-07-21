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
    manifestHash: "785169a7d4ba0221779cd95ad7701652642e46b8408e641d54bacc100ab5fe9e"
EOF
  key = "test-cluster.test.k8s.cluster/custom-addons/addon.yaml"
}

resource "aws_s3_object" "custom-addon-karpenter-provisioners" {
  bucket  = "tests"
  content = file("${path.module}/data/aws_s3_object_karpenter_provisioners_content")
  key = "test-cluster.test.k8s.cluster/custom-addons/karpenter-provisioners.wildlife.io/provisioners.yaml"
}


