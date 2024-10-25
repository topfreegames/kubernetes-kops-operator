{{ range . }}
resource "spotinst_ocean_aws_launch_spec" "{{ stringReplace . "." "-" -1 }}" {
    lifecycle {
        ignore_changes = [
            security_groups
        ]
    }
}
{{ end }}