apiVersion: karpenter.k8s.aws/v1beta1
kind: EC2NodeClass
metadata:
  name: test-machine-pool
  labels:
    kops.k8s.io/managed-by: kops-controller
spec:
  amiFamily: Custom
  amiSelectorTerms:
  - name: ubuntu-v1
  metadataOptions:
    httpEndpoint: enabled
    httpProtocolIPv6: disabled
    httpPutResponseHopLimit: 2
    httpTokens: required
  role: nodes.test-cluster.test.k8s.cluster
  securityGroupSelectorTerms:
  - name: nodes.test-cluster.test.k8s.cluster
  - tags:
      karpenter/owner: test-cluster.test.k8s.cluster/test-machine-pool
  subnetSelectorTerms:
  - tags:
      kops.k8s.io/instance-group/test-machine-pool: '*'
      kubernetes.io/cluster/test-cluster.test.k8s.cluster: '*'
  tags: 
    k8s.io/cluster-autoscaler/node-template/label/node-role.kubernetes.io/node: ''
  userData: |
    dummy content