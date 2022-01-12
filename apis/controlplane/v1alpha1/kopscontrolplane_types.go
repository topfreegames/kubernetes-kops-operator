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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kops/pkg/apis/kops"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

const (
	// KopsControlPlaneStateReadyCondition reports on the successful management of the Kops state.
	KopsControlPlaneStateReadyCondition clusterv1.ConditionType = "KopsControlPlaneStateReady"

	// KopsterraformGenerationReadyCondition reports on the successful generation of Terraform files by Kops.
	KopsTerraformGenerationReadyCondition clusterv1.ConditionType = "KopsTerraformGenerationReady"

	// TerraformApplyReadyCondition reports on the successful apply of the Terraform files.
	TerraformApplyReadyCondition clusterv1.ConditionType = "TerraformApplyReady"

	// KopsControlPlaneFinalizer allows the controller to clean up resources on delete.
	KopsControlPlaneFinalizer = "kopscontrolplane.controlplane.cluster.x-k8s.io"
)

const (

	// KopsControlPlaneStateReconciliationFailedReason (Severity=Error) indicates that Kops state couldn't be created/updated.
	KopsControlPlaneStateReconciliationFailedReason = "KopsControlPlaneStateReconciliationFailed"

	// KopsTerraformGenerationReconciliationFailedReason (Severity=Error) indicates that Terraform files couldn't be generated.
	KopsTerraformGenerationReconciliationFailedReason = "KopsTerraformGenerationReconciliationFailed"

	// TerraformApplyReconciliationFailedReason (Severity=Error) indicates that Terraform files couldn't be applied.
	TerraformApplyReconciliationFailedReason = "TerraformApplyReconciliationFailed"
)

// KopsControlPlaneSpec defines the desired state of KopsControlPlane
type KopsControlPlaneSpec struct {
	// SSHPublicKey is the SSH public key added in the nodes; required on AWS
	SSHPublicKey string `json:"SSHPublicKey"`
	// KopsClusterSpec declare the desired Cluster Kops resource: https://kops.sigs.k8s.io/cluster_spec/
	KopsClusterSpec kops.ClusterSpec `json:"kopsClusterSpec"`
}

// KopsControlPlaneStatus defines the observed state of KopsControlPlane
type KopsControlPlaneStatus struct {
	// Ready denotes that the API Server is ready to
	// receive requests.
	// +kubebuilder:default=false
	Ready bool `json:"ready,omitempty"`

	// ErrorMessage indicates that there is a terminal problem reconciling the
	// state, and will be set to a descriptive error message.
	// +optional
	FailureMessage *string `json:"failureMessage,omitempty"`

	// Conditions defines current service state of the KubeadmConfig.
	// +optional
	Conditions clusterv1.Conditions `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// KopsControlPlane is the Schema for the kopscontrolplanes API
type KopsControlPlane struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KopsControlPlaneSpec   `json:"spec,omitempty"`
	Status KopsControlPlaneStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// KopsControlPlaneList contains a list of KopsControlPlane
type KopsControlPlaneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KopsControlPlane `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KopsControlPlane{}, &KopsControlPlaneList{})
}

// GetConditions returns the set of conditions for this object.
func (cp *KopsControlPlane) GetConditions() clusterv1.Conditions {
	return cp.Status.Conditions
}

// SetConditions sets the conditions on this object.
func (cp *KopsControlPlane) SetConditions(conditions clusterv1.Conditions) {
	cp.Status.Conditions = conditions
}
