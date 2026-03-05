/*
Copyright 2026.

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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ImageSpec defines a container image build from a Repo, pushed to a Registry.
type ImageSpec struct {
	// RepoRef references a Repo CR in the same namespace.
	RepoRef corev1.LocalObjectReference `json:"repoRef"`

	// RegistryRef references a Registry CR in the same namespace.
	RegistryRef corev1.LocalObjectReference `json:"registryRef"`

	// FlakeOutput is the Nix flake output to build (e.g. "image").
	FlakeOutput string `json:"flakeOutput"`

	// Destination is the image path within the registry (e.g. "operators/my-app:v1").
	Destination string `json:"destination"`

	// Branch is the git branch to clone.
	// +kubebuilder:default="main"
	Branch string `json:"branch,omitempty"`
}

// ImageStatus reflects the observed state of the Image build.
type ImageStatus struct {
	Built      bool               `json:"built,omitempty"`
	JobName    string             `json:"jobName,omitempty"`
	Message    string             `json:"message,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Built",type=boolean,JSONPath=`.status.built`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:resource:shortName=img
type Image struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ImageSpec   `json:"spec,omitempty"`
	Status            ImageStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ImageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Image `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Image{}, &ImageList{})
}
