# Kubernetes Kops Operator

## Overview
The Kubernetes Kops Operator is a Kubernetes operator that manages kOps clusters using the Kubernetes API. It follows the [Cluster API](https://github.com/kubernetes-sigs/cluster-api) pattern to provide a declarative way to manage Kubernetes clusters using kOps as the infrastructure provider.

## Features
- Declarative cluster management using Kubernetes custom resources
- Compatibility with Cluster API project:
  - Implements Cluster API's control plane and infrastructure provider interfaces
  - Supports Cluster API's cluster lifecycle management
  - Integrates with Cluster API's machine deployment and machine pool concepts
- Support for AWS infrastructure
- Karpenter integration for node provisioning (v1 NodePools)
- SpotInst integration for cost optimization
- Custom resource management for kOps clusters
- Automated cluster validation and health checks

## Architecture
The operator consists of several key components:

### Custom Resources
1. **KopsControlPlane** (`controlplane.cluster.x-k8s.io/v1alpha1`)
   - Manages the control plane of kOps clusters
   - Handles cluster configuration and lifecycle
   - Manages worker node pools through KopsMachinePool resources
   - Supports SpotInst integration
   - Integrates with Karpenter for node provisioning

2. **KopsMachinePool** (`infrastructure.cluster.x-k8s.io/v1alpha1`)
   - Defines worker node pool configurations
   - Supports Karpenter NodePools for node provisioning
   - Configures instance groups and node templates

### Controllers
- **KopsControlPlane Controller**: 
  - Manages the complete lifecycle of kOps clusters
  - Handles both control plane and worker node pool management
  - Integrates with Karpenter for node provisioning
  - Manages SpotInst resources when enabled

## Prerequisites
- Kubernetes cluster (for running the operator)
- Cluster API core components installed:
  - cluster-api-controller
  - cluster-api-bootstrap-controller
  - cluster-api-control-plane-controller
- AWS credentials configured
- kOps CLI installed (for development)
- Go 1.23.5 or later
- kubebuilder v3

## Usage

### Creating a Cluster
1. Define your KopsControlPlane resource
2. Define KopsMachinePool resources for worker nodes
3. Apply the resources to your Kubernetes cluster

### Managing Node Pools
The operator supports two methods for node management:
1. Traditional kOps instance groups
2. Karpenter NodePools (recommended)

### SpotInst Integration
Enable SpotInst by configuring the KopsControlPlane resource:
```yaml
spec:
  spotInst:
    enabled: true
    featureFlags: "Spotinst,SpotinstOcean"
```

## Contributing
1. Fork the repository
2. Create a feature branch
3. Submit a pull request

## License
Apache License 2.0

## Support
For issues and feature requests, please use the GitHub issue tracker.