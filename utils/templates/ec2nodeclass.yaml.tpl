apiVersion: karpenter.k8s.aws/v1beta1
kind: EC2NodeClass
metadata:
  name: {{ .Name }}
  labels:
    kops.k8s.io/managed-by: kops-controller
spec:
  amiFamily: Custom
  amiSelectorTerms:
  - name: {{ .AmiName }}
  metadataOptions:
    httpEndpoint: enabled
    httpProtocolIPv6: disabled
    httpPutResponseHopLimit: 2
    httpTokens: required
  role: nodes.{{ .ClusterName }}
  securityGroupSelectorTerms:
  - name: nodes.{{ .ClusterName }}
  - tags:
      karpenter/owner: {{ .ClusterName }}/{{ .IGName }}
  subnetSelectorTerms:
  - tags:
      kops.k8s.io/instance-group/{{ .IGName }}: '*'
      kubernetes.io/cluster/{{ .ClusterName }}: '*'
  tags: 
    Name: {{ .ClusterName }}/{{ .IGName }}
    k8s.io/cluster-autoscaler/node-template/label/node-role.kubernetes.io/node: ''
    kops.k8s.io/instancegroup: {{ .IGName }}
  {{- range $key, $value := .Tags }}
    {{ $key }}: {{ $value | quote }}
  {{- end }}
  userData: |
{{ .UserData | indent 4 }}