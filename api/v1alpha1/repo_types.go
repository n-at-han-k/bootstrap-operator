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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RepoSpec defines the desired state of a bare git repository served over HTTP.
type RepoSpec struct {
	// CredentialsSecretRef references a Secret with "username" and "password"
	// keys used for HTTP basic auth.
	CredentialsSecretRef corev1.LocalObjectReference `json:"credentialsSecretRef"`

	// Storage configures the PVC backing the repository data.
	Storage StorageSpec `json:"storage"`

	// Image is the container image to use for the git HTTP server.
	// +kubebuilder:default="alpine:3.21"
	Image string `json:"image,omitempty"`
}

// StorageSpec configures persistent storage.
type StorageSpec struct {
	// Size is the requested PVC size (e.g. "1Gi").
	// +kubebuilder:default="1Gi"
	Size resource.Quantity `json:"size,omitempty"`

	// StorageClassName is the name of the StorageClass to use.
	StorageClassName *string `json:"storageClassName,omitempty"`
}

// RepoStatus reflects the observed state of the Repo.
type RepoStatus struct {
	Ready      bool               `json:"ready,omitempty"`
	URL        string             `json:"url,omitempty"`
	Message    string             `json:"message,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.url`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:resource:plural=repos,shortName=repo
type Repo struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RepoSpec   `json:"spec,omitempty"`
	Status            RepoStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type RepoList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Repo `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Repo{}, &RepoList{})
}
