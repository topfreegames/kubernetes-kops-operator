/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	karpenter "github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kops "k8s.io/kops/pkg/apis/kops"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

const (
	// KopsMachinePoolStateReadyCondition reports on the successful management of the Kops state.
	KopsMachinePoolStateReadyCondition clusterv1.ConditionType = "KopsMachinePoolStateReady"
)

const (
	// KopsMachinePoolStateReconciliationFailedReason (Severity=Error) indicates that Kops state couldn't be created/updated.
	KopsMachinePoolStateReconciliationFailedReason = "KopsMachinePoolStateReconciliationFailed"

	// KopsMachinePoolFinalizer allows the controller to clean up resources on delete.
	KopsMachinePoolFinalizer = "kopsmachinepool.infrastructure.cluster.x-k8s.io"

	// FailedToUpdateKopsMachinePool (Severity=Warn) indicates that controller failed to update the custom resource.
	FailedToUpdateKopsMachinePool = "FailedToUpdateKopsMachinePool"
)

// KopsMachinePoolSpec defines the desired state of KopsMachinePool
type KopsMachinePoolSpec struct {
	// ProviderID is the ARN of the associated ASG
	// +optional
	ProviderID string `json:"providerID,omitempty"`

	// ProviderIDList are the identification IDs of machine instances provided by the provider.
	// This field must match the provider IDs as seen on the node objects corresponding to a machine pool's machine instances.
	// +optional
	ProviderIDList []string `json:"providerIDList,omitempty"`

	// ClusterName is the name of the Cluster this object belongs to.
	// +kubebuilder:validation:MinLength=1
	ClusterName string `json:"clusterName"`

	// KarpenterProvisioners is the list of provisioners to be applied.
	// +optional
	KarpenterProvisioners []karpenter.Provisioner `json:"karpenterProvisioners,omitempty"`

	// KopsInstanceGroupSpec declare a desired InstanceGroup Kops resource: https://kops.sigs.k8s.io/instance_groups/
	KopsInstanceGroupSpec kops.InstanceGroupSpec `json:"kopsInstanceGroupSpec"`

	// Spot.io metadata labels: https://kops.sigs.k8s.io/getting_started/spot-ocean/
	SpotInstOptions map[string]string `json:"spotInstOptions,omitempty"`
}

// KopsMachinePoolStatus defines the observed state of KopsMachinePool
type KopsMachinePoolStatus struct {
	// Ready denotes that the API Server is ready to
	// receive requests.
	// +kubebuilder:default=false
	Ready bool `json:"ready,omitempty"`

	// Replicas is the most recently observed number of replicas
	// +optional
	Replicas int32 `json:"replicas"`

	// ErrorMessage indicates that there is a terminal problem reconciling the
	// state, and will be set to a descriptive error message.
	// +optional
	FailureMessage *string `json:"failureMessage,omitempty"`

	// Conditions defines current service state of the KopsMachinePool.
	// +optional
	Conditions clusterv1.Conditions `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:path=kopsmachinepools,scope=Namespaced,shortName=kmp
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// KopsMachinePool is the Schema for the kopsmachinepools API
type KopsMachinePool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KopsMachinePoolSpec   `json:"spec,omitempty"`
	Status KopsMachinePoolStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// KopsMachinePoolList contains a list of KopsMachinePool
type KopsMachinePoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KopsMachinePool `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KopsMachinePool{}, &KopsMachinePoolList{})
}

// GetConditions returns the set of conditions for this object.
func (cp *KopsMachinePool) GetConditions() clusterv1.Conditions {
	return cp.Status.Conditions
}

// SetConditions sets the conditions on this object.
func (cp *KopsMachinePool) SetConditions(conditions clusterv1.Conditions) {
	cp.Status.Conditions = conditions
}
