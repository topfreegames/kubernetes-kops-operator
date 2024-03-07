terraform {
    backend "s3" {
        bucket = "{{ .Bucket }}"
        key = "_terraform/{{ .ClusterName }}.tfstate"
        region = "us-east-1"
    }
}
