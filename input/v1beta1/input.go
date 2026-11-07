// Package v1beta1 contains the input type for this Function
// +kubebuilder:object:generate=true
// +groupName=lifecycle.fn.crossplane.io
// +versionName=v1beta1
package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// This isn't a custom resource, in the sense that we never install its CRD.
// It is a KRM-like object, so we generate a CRD to describe its schema.

// Input configures how function-lifecycle picks the lifecycle mode. The XR's
// lifecycle.fn.crossplane.io/mode annotation always takes precedence.
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:categories=crossplane
type Input struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Mode applies when there is no mode annotation and ModeRef doesn't
	// resolve to a value. Defaults to Full.
	// +kubebuilder:validation:Enum=Full;Provision;ObserveOnly;Deprovision
	// +optional
	Mode string `json:"mode,omitempty"`

	// ModeRef reads the mode from a field of the XR or the pipeline context.
	// It must start with spec., status., or context. Use brackets for keys
	// that contain dots, for example
	// context.[apiextensions.crossplane.io/environment].managementMode.
	// +optional
	ModeRef string `json:"modeRef,omitempty"`

	// RespectExisting keeps the spec.managementPolicies that an earlier
	// pipeline step already set on a composed resource. Without it the
	// function overwrites them.
	// +optional
	RespectExisting bool `json:"respectExisting,omitempty"`
}
