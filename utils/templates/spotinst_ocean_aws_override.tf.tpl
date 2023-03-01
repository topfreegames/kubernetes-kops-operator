resource "spotinst_ocean_aws" "nodes-{{ stringReplace . "." "-" -1 }}" {
    lifecycle {
      ignore_changes = [autoscaler, desired_capacity, associate_public_ip_address, spot_percentage]
    }
    use_as_template_only = true
}