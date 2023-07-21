terraform {
    backend "s3" {
        bucket = "tests"
        key = "_terraform/test-cluster.test.k8s.cluster.tfstate"
        region = "us-east-1"
    }
}