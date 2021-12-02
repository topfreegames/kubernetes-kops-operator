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
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// KopsControlPlaneSpec defines the desired state of KopsControlPlane
type KopsControlPlaneSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// KopsClusterSpec declare the desired Cluster Kops resource: https://kops.sigs.k8s.io/cluster_spec/
	KopsClusterSpec kops.ClusterSpec `json:"kopsClusterSpec"`
}

// KopsControlPlaneStatus defines the observed state of KopsControlPlane
type KopsControlPlaneStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
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
