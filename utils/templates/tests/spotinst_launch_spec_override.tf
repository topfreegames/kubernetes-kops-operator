
resource "spotinst_ocean_aws_launch_spec" "test-ig-test-cluster" {
    lifecycle {
        ignore_changes = [
            security_groups
        ]
    }
}
