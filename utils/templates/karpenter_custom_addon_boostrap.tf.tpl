resource "aws_s3_object" "custom-addon-bootstrap" {
  bucket  = "{{ .Bucket }}"
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
    manifestHash: "{{ .ManifestHash }}"
    prune:
      kinds:
      - group: karpenter.sh
        kind: Provisioner
        labelSelector: "kops.k8s.io/managed-by=kops-controller"
EOF
  key = "{{ .ClusterName }}/custom-addons/addon.yaml"
  provider = aws.files
}

resource "aws_s3_object" "custom-addon-karpenter-provisioners" {
  bucket  = "{{ .Bucket }}"
  content = file("${path.module}/data/aws_s3_object_karpenter_provisioners_content")
  key = "{{ .ClusterName }}/custom-addons/karpenter-provisioners.wildlife.io/provisioners.yaml"
  provider = aws.files
}


