{{ range . }}
resource "aws_launch_template" "{{ stringReplace . "." "-" -1 }}" {
    lifecycle {
        ignore_changes = [
            network_interfaces.0.security_groups
        ]
    }
}
{{ end }}