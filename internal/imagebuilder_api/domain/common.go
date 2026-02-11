package domain

import (
	corev1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
)

// ========== Common Types from Core API ==========

// Status is the response status type
type Status = corev1beta1.Status

// ObjectMeta is metadata for persisted resources
type ObjectMeta = corev1beta1.ObjectMeta

// ListMeta is metadata for list responses
type ListMeta = corev1beta1.ListMeta

// ConditionStatus represents the status of a condition
type ConditionStatus = corev1beta1.ConditionStatus

// Condition status constants
const (
	ConditionStatusTrue    = corev1beta1.ConditionStatusTrue
	ConditionStatusFalse   = corev1beta1.ConditionStatusFalse
	ConditionStatusUnknown = corev1beta1.ConditionStatusUnknown
)

// ========== Repository Types (needed for validation) ==========

// OciRepoSpec represents OCI repository specification
type OciRepoSpec = corev1beta1.OciRepoSpec

// RepoSpecType represents the repository spec type
type RepoSpecType = corev1beta1.RepoSpecType

// Repository spec type constants
const (
	RepoSpecTypeOci = corev1beta1.RepoSpecTypeOci
)

// Access mode type
type AccessMode = corev1beta1.OciRepoSpecAccessMode

// Access mode constants
const (
	Read      = corev1beta1.Read
	ReadWrite = corev1beta1.ReadWrite
)

// ========== Event Types ==========

// Event represents an event resource
type Event = corev1beta1.Event

// EventReason represents the reason for an event
type EventReason = corev1beta1.EventReason

// Event reason constants
const (
	EventReasonResourceCreated = corev1beta1.EventReasonResourceCreated
	EventReasonResourceUpdated = corev1beta1.EventReasonResourceUpdated
	EventReasonResourceDeleted = corev1beta1.EventReasonResourceDeleted
)

// ResourceKind represents the kind of resource (from core API)
type CoreResourceKind = corev1beta1.ResourceKind
