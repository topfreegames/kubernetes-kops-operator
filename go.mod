module github.com/topfreegames/kubernetes-kops-operator

go 1.16

require (
	github.com/aws/aws-sdk-go v1.44.4
	github.com/go-logr/logr v1.2.3
	github.com/hashicorp/go-version v1.4.0
	github.com/hashicorp/hc-install v0.3.1
	github.com/hashicorp/terraform-exec v0.16.1
	github.com/onsi/ginkgo v1.16.5
	github.com/onsi/gomega v1.19.0
	github.com/pkg/errors v0.9.1
	github.com/stretchr/testify v1.7.1
	k8s.io/api v0.23.6
	k8s.io/apimachinery v0.23.6
	k8s.io/client-go v0.23.6
	k8s.io/kops v1.23.1
	k8s.io/kubectl v0.23.6
	sigs.k8s.io/cluster-api v1.1.3
	sigs.k8s.io/controller-runtime v0.11.2
)