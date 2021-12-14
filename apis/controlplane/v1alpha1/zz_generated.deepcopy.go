//go:build !ignore_autogenerated
// +build !ignore_autogenerated

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

// Code generated by controller-gen. DO NOT EDIT.

package v1alpha1

import (
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *KopsControlPlane) DeepCopyInto(out *KopsControlPlane) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	out.Status = in.Status
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new KopsControlPlane.
func (in *KopsControlPlane) DeepCopy() *KopsControlPlane {
	if in == nil {
		return nil
	}
	out := new(KopsControlPlane)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *KopsControlPlane) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *KopsControlPlaneList) DeepCopyInto(out *KopsControlPlaneList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]KopsControlPlane, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new KopsControlPlaneList.
func (in *KopsControlPlaneList) DeepCopy() *KopsControlPlaneList {
	if in == nil {
		return nil
	}
	out := new(KopsControlPlaneList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *KopsControlPlaneList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *KopsControlPlaneSpec) DeepCopyInto(out *KopsControlPlaneSpec) {
	*out = *in
	in.KopsClusterSpec.DeepCopyInto(&out.KopsClusterSpec)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new KopsControlPlaneSpec.
func (in *KopsControlPlaneSpec) DeepCopy() *KopsControlPlaneSpec {
	if in == nil {
		return nil
	}
	out := new(KopsControlPlaneSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *KopsControlPlaneStatus) DeepCopyInto(out *KopsControlPlaneStatus) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new KopsControlPlaneStatus.
func (in *KopsControlPlaneStatus) DeepCopy() *KopsControlPlaneStatus {
	if in == nil {
		return nil
	}
	out := new(KopsControlPlaneStatus)
	in.DeepCopyInto(out)
	return out
}
