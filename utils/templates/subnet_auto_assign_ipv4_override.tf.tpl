{{- range . -}}
resource "aws_subnet" "{{ .Name }}-{{stringReplace .ClusterName "." "-" -1}}" {
    map_public_ip_on_launch = true
}

{{ end }}