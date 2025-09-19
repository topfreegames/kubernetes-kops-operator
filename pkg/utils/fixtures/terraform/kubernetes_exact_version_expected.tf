locals {
  cluster_name                 = "prod-cluster.us-east-1.k8s.example.com"
  region                       = "us-east-1"
  vpc_id                       = aws_vpc.prod-cluster-us-east-1-k8s-example-com.id
  masters_role_arn             = aws_iam_role.masters-prod-cluster-us-east-1-k8s-example-com.arn
  nodes_role_arn               = aws_iam_role.nodes-prod-cluster-us-east-1-k8s-example-com.arn
}

output "cluster_name" {
  value = "prod-cluster.us-east-1.k8s.example.com"
}

output "masters_role_arn" {
  value = aws_iam_role.masters-prod-cluster-us-east-1-k8s-example-com.arn
}

output "nodes_role_arn" {
  value = aws_iam_role.nodes-prod-cluster-us-east-1-k8s-example-com.arn
}

provider "aws" {
  region = "us-east-1"
}

terraform {
  required_version = ">= 0.15.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "5.0.0"
    }
  }
}
