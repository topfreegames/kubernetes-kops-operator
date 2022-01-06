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
	kops "k8s.io/kops/pkg/apis/kops"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// KopsMachinePoolSpec defines the desired state of KopsMachinePool
type KopsMachinePoolSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// ClusterName is the name of the Cluster this object belongs to.
	// +kubebuilder:validation:MinLength=1
	ClusterName string `json:"clusterName"`

	// KopsInstanceGroupSpec declare a desired InstanceGroup Kops resource: https://kops.sigs.k8s.io/instance_groups/
	KopsInstanceGroupSpec kops.InstanceGroupSpec `json:"kopsInstanceGroupSpec"`
}

// KopsMachinePoolStatus defines the observed state of KopsMachinePool
type KopsMachinePoolStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

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
