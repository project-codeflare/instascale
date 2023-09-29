/*
Copyright 2022.

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

package config

import corev1 "k8s.io/api/core/v1"

// InstaScaleConfiguration defines the InstaScale configuration.
type InstaScaleConfiguration struct {
	// ocmSecretRef is reference to the authentication Secret for connecting to OCM.
	// If provided, MachinePools are used to auto-scale the cluster.
	// +optional
	OCMSecretRef *corev1.SecretReference `json:"ocmSecretRef,omitempty"`
	// maxScaleoutAllowed defines the upper limit for the number of cluster nodes
	// that can be scaled out by InstaScale.
	MaxScaleoutAllowed int32 `json:"maxScaleoutAllowed"`
	// machineSetsStrategy is a string which is used to decide if machineSets are created from a copy
	// or reused by instascale by using either the Reuse or Duplicate string.
	MachineSetsStrategy string `json:"machineSetsStrategy,omitempty"`
}
