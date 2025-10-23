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
    owner: "000000000000"
  metadataOptions:
    httpEndpoint: enabled
    httpProtocolIPv6: disabled
    httpPutResponseHopLimit: 3
    httpTokens: required
  associatePublicIPAddress: false
  blockDeviceMappings:
  - deviceName: /dev/sda1
    ebs:
      volumeSize: 60Gi
      volumeType: gp3
      iops: 3000
      encrypted: true
      throughput: 125
      deleteOnTermination: true
    rootVolume: true
  role: nodes.test-cluster.test.k8s.cluster
  securityGroupSelectorTerms:
  - name: nodes.test-cluster.test.k8s.cluster
  - tags:
      karpenter/test-cluster.test.k8s.cluster/test-machine-pool: "true"
  subnetSelectorTerms:
  - tags:
      kops.k8s.io/instance-group/test-machine-pool: '*'
      kubernetes.io/cluster/test-cluster.test.k8s.cluster: '*'
  tags:
    KubernetesCluster: test-cluster.test.k8s.cluster
    Name: test-cluster.test.k8s.cluster/test-machine-pool
    k8s.io/cluster-autoscaler/node-template/label/node-role.kubernetes.io/node: ''
    kops.k8s.io/instancegroup: test-machine-pool
  userData: |
    dummy content
