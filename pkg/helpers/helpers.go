package helpers

import (
	"context"
	"fmt"
	"strings"

	g "github.com/onsi/gomega"
	controlplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	infrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kopsapi "k8s.io/kops/pkg/apis/kops"
	"k8s.io/kops/pkg/client/simple"
	"k8s.io/kops/pkg/client/simple/vfsclientset"
	"k8s.io/kops/pkg/pki"
	"k8s.io/kops/upup/pkg/fi"
	"k8s.io/kops/util/pkg/vfs"
	"k8s.io/kubectl/pkg/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func String(v string) *string {
	return &v
}

func NewCluster(name, controlplane, namespace string) *clusterv1.Cluster {
	return &clusterv1.Cluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Cluster",
			APIVersion: "cluster.x-k8s.io/v1beta1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      GetFQDN(name),
			UID:       "12793d7abd2813dnba87e6",
		},
		Spec: clusterv1.ClusterSpec{
			ControlPlaneRef: &corev1.ObjectReference{
				Name:      controlplane,
				Namespace: namespace,
				Kind:      "KopsControlPlane",
			},
		},
	}
}

func NewAWSCredentialSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "11111111-credential",
			Namespace: "kubernetes-kops-operator-system",
		},
		Data: map[string][]byte{
			"AccessKeyID":     []byte("test"),
			"SecretAccessKey": []byte("test"),
		},
	}
}

func NewKopsCluster(name string) *kopsapi.Cluster {
	return &kopsapi.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: GetFQDN(name),
		},
		Spec: kopsapi.ClusterSpec{
			KubernetesVersion: "1.23.12",
			CloudProvider: kopsapi.CloudProviderSpec{
				AWS: &kopsapi.AWSSpec{},
			},
			ConfigBase: fmt.Sprintf("memfs://tests/%s.test.k8s.cluster", name),
			Networking: kopsapi.NetworkingSpec{
				NonMasqueradeCIDR: "172.0.0.0/21",
				NetworkCIDR:       "10.0.0.0/21",
				Subnets: []kopsapi.ClusterSubnetSpec{
					{
						Name: "test-subnet",
						CIDR: "10.0.1.0/24",
						Type: kopsapi.SubnetTypePrivate,
						Zone: "us-east-1",
					},
				},
			},
			EtcdClusters: []kopsapi.EtcdClusterSpec{
				{
					Name:     "main",
					Provider: kopsapi.EtcdProviderTypeManager,
					Members: []kopsapi.EtcdMemberSpec{
						{
							Name:          "a",
							InstanceGroup: String("eu-central-1a"),
						},
						{
							Name:          "b",
							InstanceGroup: String("eu-central-1b"),
						},
						{
							Name:          "c",
							InstanceGroup: String("eu-central-1c"),
						},
					},
				},
			},
			IAM: &kopsapi.IAMSpec{
				Legacy: false,
			},
		},
	}
}

func NewKopsControlPlane(name, namespace string) *controlplanev1alpha1.KopsControlPlane {
	return &controlplanev1alpha1.KopsControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      GetFQDN(name),
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "cluster.x-k8s.io/v1beta1",
					Kind:       "Cluster",
					Name:       GetFQDN(name),
				},
			},
		},
		Spec: controlplanev1alpha1.KopsControlPlaneSpec{
			IdentityRef: controlplanev1alpha1.IdentityRefSpec{
				Kind:      "Secret",
				Name:      "11111111-credential",
				Namespace: "kubernetes-kops-operator-system",
			},
			SSHPublicKey: "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCu7OR4k/qpc6VFqQsMGk7wQcnGzDA/hKABnj3qN85tgIDVsbnOIVgXl4FV1gO+gBjblCLkAmbZYlwhhkosL4xpEc8uk8QWJIzRqalvnLEofdIjClngGqzC40Yu6oVPiqImDazlVNvJ7UdzX02mmYJMe4eRzS1w1dA2hm9uTsaq6CNZuJF2/joV+SKLW88IEXWnb7PdOPZWFy0iN/9JcQKqON7zmR0j1zb4Ydj6Pt9MMIOTRiJpyeTqw0Gy4RWgkKJpwuRhOTnhZ1I8zigXgu4+keMYBgtLLP90Wx6/SI6vt+sG/Zrx5+s0av6vHFH/fDzqX4BSsxY83cOMH6ILLQ1C0hE9ykXx/EAKoou+DT8Doe0wabVxZNMRDOAb0ZnLF1HwUItW+MvgIjtCVpap/jBGmSSqZ5B9cvib7UV+JfLHty7n3AP2SKf52+E3Fp1fP4UiXQ/YUXZksopHLXLtwMdam/qijq5tjk0lVh7j8GGNuejt17+tSOCaP2kNKFyc1u8=",
			KopsClusterSpec: kopsapi.ClusterSpec{
				KubernetesVersion: "1.23.12",
				CloudProvider: kopsapi.CloudProviderSpec{
					AWS: &kopsapi.AWSSpec{},
				},
				Channel:    "none",
				ConfigBase: fmt.Sprintf("memfs://tests/%s.test.k8s.cluster", name),
				Networking: kopsapi.NetworkingSpec{
					NetworkCIDR:       "172.27.0.0/21",
					NonMasqueradeCIDR: "10.0.0.0/21",
					Subnets: []kopsapi.ClusterSubnetSpec{
						{
							Name: "test-subnet",
							CIDR: "172.27.0.0/24",
							Type: kopsapi.SubnetTypePrivate,
							Zone: "us-east-1",
						},
					},
				},

				EtcdClusters: []kopsapi.EtcdClusterSpec{
					{
						Name:     "main",
						Provider: kopsapi.EtcdProviderTypeManager,
						Members: []kopsapi.EtcdMemberSpec{
							{
								Name:          "a",
								InstanceGroup: String("eu-central-1a"),
							},
							{
								Name:          "b",
								InstanceGroup: String("eu-central-1b"),
							},
							{
								Name:          "c",
								InstanceGroup: String("eu-central-1c"),
							},
						},
					},
				},
				IAM: &kopsapi.IAMSpec{
					Legacy: false,
				},
			},
		},
	}
}

func NewMockedK8sClient(objects ...client.Object) client.Client {
	err := clusterv1.AddToScheme(scheme.Scheme)
	g.Expect(err).NotTo(g.HaveOccurred())
	fakeClient := fake.NewClientBuilder().WithObjects(objects...).WithScheme(scheme.Scheme).Build()
	return fakeClient
}

func NewFakeKopsClientset() simple.Clientset {
	memFSContext := vfs.NewMemFSContext()
	memfspath := vfs.NewMemFSPath(memFSContext, "memfs://tests")

	return vfsclientset.NewVFSClientset(memfspath)
}

func GetFQDN(name string) string {
	return strings.ToLower(fmt.Sprintf("%s.test.k8s.cluster", name))
}

func NewKopsIG(name, clusterName string) *kopsapi.InstanceGroup {
	return &kopsapi.InstanceGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: kopsapi.InstanceGroupSpec{
			Role: "ControlPlane",
			Subnets: []string{
				"dummy-subnet",
			},
		},
	}
}

func NewKopsMachinePool(name, namespace, clusterName string) *infrastructurev1alpha1.KopsMachinePool {
	return &infrastructurev1alpha1.KopsMachinePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"cluster.x-k8s.io/cluster-name": clusterName,
			},
			ResourceVersion:   "1",
			CreationTimestamp: metav1.Time{},
		},
		Spec: infrastructurev1alpha1.KopsMachinePoolSpec{
			ClusterName: clusterName,
			KopsInstanceGroupSpec: kopsapi.InstanceGroupSpec{
				Role: "ControlPlane",
				Subnets: []string{
					"dummy-subnet",
				},
			},
		},
	}
}

func CreateFakeKopsKeyPair(ctx context.Context, keyStore fi.CAStore) error {
	certData := `-----BEGIN CERTIFICATE-----
MIIC2DCCAcCgAwIBAgIRALJXAkVj964tq67wMSI8oJQwDQYJKoZIhvcNAQELBQAw
FTETMBEGA1UEAxMKa3ViZXJuZXRlczAeFw0xNzEyMjcyMzUyNDBaFw0yNzEyMjcy
MzUyNDBaMBUxEzARBgNVBAMTCmt1YmVybmV0ZXMwggEiMA0GCSqGSIb3DQEBAQUA
A4IBDwAwggEKAoIBAQDgnCkSmtnmfxEgS3qNPaUCH5QOBGDH/inHbWCODLBCK9gd
XEcBl7FVv8T2kFr1DYb0HVDtMI7tixRVFDLgkwNlW34xwWdZXB7GeoFgU1xWOQSY
OACC8JgYTQ/139HBEvgq4sej67p+/s/SNcw34Kk7HIuFhlk1rRk5kMexKIlJBKP1
YYUYetsJ/QpUOkqJ5HW4GoetE76YtHnORfYvnybviSMrh2wGGaN6r/s4ChOaIbZC
An8/YiPKGIDaZGpj6GXnmXARRX/TIdgSQkLwt0aTDBnPZ4XvtpI8aaL8DYJIqAzA
NPH2b4/uNylat5jDo0b0G54agMi97+2AUrC9UUXpAgMBAAGjIzAhMA4GA1UdDwEB
/wQEAwIBBjAPBgNVHRMBAf8EBTADAQH/MA0GCSqGSIb3DQEBCwUAA4IBAQBVGR2r
hzXzRMU5wriPQAJScszNORvoBpXfZoZ09FIupudFxBVU3d4hV9StKnQgPSGA5XQO
HE97+BxJDuA/rB5oBUsMBjc7y1cde/T6hmi3rLoEYBSnSudCOXJE4G9/0f8byAJe
rN8+No1r2VgZvZh6p74TEkXv/l3HBPWM7IdUV0HO9JDhSgOVF1fyQKJxRuLJR8jt
O6mPH2UX0vMwVa4jvwtkddqk2OAdYQvH9rbDjjbzaiW0KnmdueRo92KHAN7BsDZy
VpXHpqo1Kzg7D3fpaXCf5si7lqqrdJVXH4JC72zxsPehqgi8eIuqOBkiDWmRxAxh
8yGeRx9AbknHh4Ia
-----END CERTIFICATE-----
`
	privatekeyData := `-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEA4JwpEprZ5n8RIEt6jT2lAh+UDgRgx/4px21gjgywQivYHVxH
AZexVb/E9pBa9Q2G9B1Q7TCO7YsUVRQy4JMDZVt+McFnWVwexnqBYFNcVjkEmDgA
gvCYGE0P9d/RwRL4KuLHo+u6fv7P0jXMN+CpOxyLhYZZNa0ZOZDHsSiJSQSj9WGF
GHrbCf0KVDpKieR1uBqHrRO+mLR5zkX2L58m74kjK4dsBhmjeq/7OAoTmiG2QgJ/
P2IjyhiA2mRqY+hl55lwEUV/0yHYEkJC8LdGkwwZz2eF77aSPGmi/A2CSKgMwDTx
9m+P7jcpWreYw6NG9BueGoDIve/tgFKwvVFF6QIDAQABAoIBAA0ktjaTfyrAxsTI
Bezb7Zr5NBW55dvuII299cd6MJo+rI/TRYhvUv48kY8IFXp/hyUjzgeDLunxmIf9
/Zgsoic9Ol44/g45mMduhcGYPzAAeCdcJ5OB9rR9VfDCXyjYLlN8H8iU0734tTqM
0V13tQ9zdSqkGPZOIcq/kR/pylbOZaQMe97BTlsAnOMSMKDgnftY4122Lq3GYy+t
vpr+bKVaQZwvkLoSU3rECCaKaghgwCyX7jft9aEkhdJv+KlwbsGY6WErvxOaLWHd
cuMQjGapY1Fa/4UD00mvrA260NyKfzrp6+P46RrVMwEYRJMIQ8YBAk6N6Hh7dc0G
8Z6i1m0CgYEA9HeCJR0TSwbIQ1bDXUrzpftHuidG5BnSBtax/ND9qIPhR/FBW5nj
22nwLc48KkyirlfIULd0ae4qVXJn7wfYcuX/cJMLDmSVtlM5Dzmi/91xRiFgIzx1
AsbBzaFjISP2HpSgL+e9FtSXaaqeZVrflitVhYKUpI/AKV31qGHf04sCgYEA6zTV
99Sb49Wdlns5IgsfnXl6ToRttB18lfEKcVfjAM4frnkk06JpFAZeR+9GGKUXZHqs
z2qcplw4d/moCC6p3rYPBMLXsrGNEUFZqBlgz72QA6BBq3X0Cg1Bc2ZbK5VIzwkg
ST2SSux6ccROfgULmN5ZiLOtdUKNEZpFF3i3qtsCgYADT/s7dYFlatobz3kmMnXK
sfTu2MllHdRys0YGHu7Q8biDuQkhrJwhxPW0KS83g4JQym+0aEfzh36bWcl+u6R7
KhKj+9oSf9pndgk345gJz35RbPJYh+EuAHNvzdgCAvK6x1jETWeKf6btj5pF1U1i
Q4QNIw/QiwIXjWZeubTGsQKBgQCbduLu2rLnlyyAaJZM8DlHZyH2gAXbBZpxqU8T
t9mtkJDUS/KRiEoYGFV9CqS0aXrayVMsDfXY6B/S/UuZjO5u7LtklDzqOf1aKG3Q
dGXPKibknqqJYH+bnUNjuYYNerETV57lijMGHuSYCf8vwLn3oxBfERRX61M/DU8Z
worz/QKBgQDCTJI2+jdXg26XuYUmM4XXfnocfzAXhXBULt1nENcogNf1fcptAVtu
BAiz4/HipQKqoWVUYmxfgbbLRKKLK0s0lOWKbYdVjhEm/m2ZU8wtXTagNwkIGoyq
Y/C1Lox4f1ROJnCjc/hfcOjcxX5M8A8peecHWlVtUPKTJgxQ7oMKcw==
-----END RSA PRIVATE KEY-----
`

	cert, _ := pki.ParsePEMCertificate([]byte(certData))
	key, _ := pki.ParsePEMPrivateKey([]byte(privatekeyData))
	keyset := &fi.Keyset{
		Items: map[string]*fi.KeysetItem{
			"1": {
				Id:          "1",
				Certificate: cert,
				PrivateKey:  key,
			},
		},
		Primary: &fi.KeysetItem{
			Id:          "1",
			Certificate: cert,
			PrivateKey:  key,
		},
	}
	err := keyStore.StoreKeyset(ctx, fi.CertificateIDCA, keyset)
	return err
}
