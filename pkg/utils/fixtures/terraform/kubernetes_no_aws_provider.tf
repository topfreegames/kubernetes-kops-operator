locals {
  cluster_name = "azure-cluster.eastus.k8s.example.com"
  region       = "eastus"
}

output "cluster_name" {
  value = "azure-cluster.eastus.k8s.example.com"
}

provider "azurerm" {
  features {}
}

terraform {
  required_version = ">= 0.15.0"
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = ">= 3.0.0"
    }
  }
}
