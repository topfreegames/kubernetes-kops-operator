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
      karpenter/owner: {{ .ClusterName }}/{{ .Name }}
  subnetSelectorTerms:
  - tags:
      kops.k8s.io/instance-group/{{ .Name }}: '*'
      kubernetes.io/cluster/{{ .ClusterName }}: '*'
  tags: 
    k8s.io/cluster-autoscaler/node-template/label/node-role.kubernetes.io/node: ''
  {{- range $key, $value := .Tags }}
    {{ $key }}: {{ $value | quote }}
  {{- end }}
  userData: |
{{ .UserData | indent 4 }}