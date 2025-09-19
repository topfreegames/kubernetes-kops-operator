locals {
  cluster_name                 = "test-cluster.us-east-1.k8s.example.com"
  region                       = "us-east-1"
  vpc_id                       = aws_vpc.test-cluster-us-east-1-k8s-example-com.id
}

output "cluster_name" {
  value = "test-cluster.us-east-1.k8s.example.com"
}

output "region" {
  value = "us-east-1"
}

provider "aws" {
  region = "us-east-1"
}

provider "aws" {
  alias  = "files"
  region = "us-east-1"
}

terraform {
  required_version = ">= 0.15.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 3.71.0"
    }
  }
}
