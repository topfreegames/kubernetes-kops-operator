
resource "aws_launch_template" "test-ig-test-cluster" {
    lifecycle {
        ignore_changes = [
            network_interfaces.0.security_groups
        ]
    }
}
