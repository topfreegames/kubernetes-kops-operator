resource "spotinst_ocean_aws" "nodes-test-cluster-test-k8s-cluster" {
    lifecycle {
      ignore_changes = [autoscaler, desired_capacity, associate_public_ip_address, spot_percentage]
    }
    use_as_template_only = true
}