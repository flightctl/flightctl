// Package v1alpha1 provides primitives to interact with the openapi HTTP API.
//
// Code generated by github.com/deepmap/oapi-codegen version v1.15.0 DO NOT EDIT.
package v1alpha1

import (
	"encoding/json"

	"github.com/oapi-codegen/runtime"
)

// ContainerStatus defines model for ContainerStatus.
type ContainerStatus struct {
	// Id ID of the container.
	Id string `json:"id"`

	// Image Image of the container.
	Image string `json:"image"`

	// Name Name of the container.
	Name string `json:"name"`

	// Status Status of the container (e.g., running, stopped, etc.).
	Status string `json:"status"`
}

// Device Device represents a physical device.
type Device struct {
	// ApiVersion APIVersion defines the versioned schema of this representation of an object. Servers should convert recognized schemas to the latest internal value, and may reject unrecognized values. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources
	ApiVersion string `json:"apiVersion"`

	// Kind Kind is a string value representing the REST resource this object represents. Servers may infer this from the endpoint the client submits requests to. Cannot be updated. In CamelCase. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds
	Kind string `json:"kind"`

	// Metadata ObjectMeta is metadata that all persisted resources must have, which includes all objects users must create.
	Metadata ObjectMeta `json:"metadata"`

	// Spec DeviceSpec is a description of a device's target state.
	Spec DeviceSpec `json:"spec"`

	// Status DeviceStatus represents information about the status of a device. Status may trail the actual state of a device, especially if the device has not contacted the management service in a while.
	Status *DeviceStatus `json:"status,omitempty"`
}

// DeviceCondition DeviceCondition contains condition information for a device.
type DeviceCondition struct {
	LastHeartbeatTime  *string `json:"lastHeartbeatTime,omitempty"`
	LastTransitionTime *string `json:"lastTransitionTime,omitempty"`

	// Message Human readable message indicating details about last transition.
	Message *string `json:"message,omitempty"`

	// Reason (brief) reason for the condition's last transition.
	Reason *string `json:"reason,omitempty"`

	// Status Status of the condition, one of True, False, Unknown.
	Status string `json:"status"`

	// Type Type of device condition.
	Type string `json:"type"`
}

// DeviceList DeviceList is a list of Devices.
type DeviceList struct {
	// ApiVersion APIVersion defines the versioned schema of this representation of an object. Servers should convert recognized schemas to the latest internal value, and may reject unrecognized values. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources
	ApiVersion string `json:"apiVersion"`

	// Items List of Devices.
	Items []Device `json:"items"`

	// Kind Kind is a string value representing the REST resource this object represents. Servers may infer this from the endpoint the client submits requests to. Cannot be updated. In CamelCase. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds
	Kind string `json:"kind"`

	// Metadata ListMeta describes metadata that synthetic resources must have, including lists and various status objects. A resource may have only one of {ObjectMeta, ListMeta}.
	Metadata ListMeta `json:"metadata"`
}

// DeviceOSSpec defines model for DeviceOSSpec.
type DeviceOSSpec struct {
	// Image ostree image name or URL.
	Image string `json:"image"`
}

// DeviceSpec DeviceSpec is a description of a device's target state.
type DeviceSpec struct {
	// Config List of config resources.
	Config     *[]DeviceSpec_Config_Item `json:"config,omitempty"`
	Containers *struct {
		MatchPattern *[]string `json:"matchPattern,omitempty"`
	} `json:"containers,omitempty"`
	Os      *DeviceOSSpec `json:"os,omitempty"`
	Systemd *struct {
		MatchPatterns *[]string `json:"matchPatterns,omitempty"`
	} `json:"systemd,omitempty"`
}

// DeviceSpec_Config_Item defines model for DeviceSpec.config.Item.
type DeviceSpec_Config_Item struct {
	union json.RawMessage
}

// DeviceStatus DeviceStatus represents information about the status of a device. Status may trail the actual state of a device, especially if the device has not contacted the management service in a while.
type DeviceStatus struct {
	// Conditions Current state of the device.
	Conditions *[]DeviceCondition `json:"conditions,omitempty"`

	// Containers Statuses of containers in the device.
	Containers *[]ContainerStatus `json:"containers,omitempty"`

	// SystemInfo DeviceSystemInfo is a set of ids/uuids to uniquely identify the device.
	SystemInfo *DeviceSystemInfo `json:"systemInfo,omitempty"`

	// SystemdUnits Current state of systemd units on the device.
	SystemdUnits *[]DeviceSystemdUnitStatus `json:"systemdUnits,omitempty"`
}

// DeviceSystemInfo DeviceSystemInfo is a set of ids/uuids to uniquely identify the device.
type DeviceSystemInfo struct {
	// Architecture The Architecture reported by the device.
	Architecture string `json:"architecture"`

	// BootID Boot ID reported by the device.
	BootID string `json:"bootID"`

	// MachineID MachineID reported by the device.
	MachineID string `json:"machineID"`

	// Measurements The integrity measurements of the system.
	Measurements map[string]string `json:"measurements"`

	// OperatingSystem The Operating System reported by the device.
	OperatingSystem string `json:"operatingSystem"`
}

// DeviceSystemdUnitStatus The status of the systemd unit.
type DeviceSystemdUnitStatus struct {
	// ActiveState The active state of the systemd unit.
	ActiveState string `json:"activeState"`

	// LoadState The load state of the systemd unit.
	LoadState string `json:"loadState"`

	// Name The name of the systemd unit.
	Name interface{} `json:"name"`
}

// EnrollmentRequest EnrollmentRequest represents a request for approval to enroll a device.
type EnrollmentRequest struct {
	// ApiVersion APIVersion defines the versioned schema of this representation of an object. Servers should convert recognized schemas to the latest internal value, and may reject unrecognized values. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources
	ApiVersion string `json:"apiVersion"`

	// Kind Kind is a string value representing the REST resource this object represents. Servers may infer this from the endpoint the client submits requests to. Cannot be updated. In CamelCase. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds
	Kind string `json:"kind"`

	// Metadata ObjectMeta is metadata that all persisted resources must have, which includes all objects users must create.
	Metadata ObjectMeta `json:"metadata"`

	// Spec EnrollmentRequestSpec is a description of a EnrollmentRequest's target state.
	Spec EnrollmentRequestSpec `json:"spec"`

	// Status EnrollmentRequestStatus represents information about the status of a EnrollmentRequest.
	Status *EnrollmentRequestStatus `json:"status,omitempty"`
}

// EnrollmentRequestCondition EnrollmentRequestCondition contains condition information for a EnrollmentRequest.
type EnrollmentRequestCondition struct {
	LastTransitionTime *string `json:"lastTransitionTime,omitempty"`

	// Message Human readable message indicating details about last transition.
	Message *string `json:"message,omitempty"`

	// Reason (brief) reason for the condition's last transition.
	Reason *string `json:"reason,omitempty"`

	// Status Status of the condition, one of True, False, Unknown.
	Status string `json:"status"`

	// Type Type of fleet condition.
	Type string `json:"type"`
}

// EnrollmentRequestList EnrollmentRequestList is a list of EnrollmentRequest.
type EnrollmentRequestList struct {
	// ApiVersion APIVersion defines the versioned schema of this representation of an object. Servers should convert recognized schemas to the latest internal value, and may reject unrecognized values. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources
	ApiVersion string `json:"apiVersion"`

	// Items List of EnrollmentRequest.
	Items []EnrollmentRequest `json:"items"`

	// Kind Kind is a string value representing the REST resource this object represents. Servers may infer this from the endpoint the client submits requests to. Cannot be updated. In CamelCase. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds
	Kind string `json:"kind"`

	// Metadata ListMeta describes metadata that synthetic resources must have, including lists and various status objects. A resource may have only one of {ObjectMeta, ListMeta}.
	Metadata ListMeta `json:"metadata"`
}

// EnrollmentRequestSpec EnrollmentRequestSpec is a description of a EnrollmentRequest's target state.
type EnrollmentRequestSpec struct {
	// Csr csr is a PEM-encoded PKCS#10 certificate signing request.
	Csr string `json:"csr"`

	// DeviceStatus DeviceStatus represents information about the status of a device. Status may trail the actual state of a device, especially if the device has not contacted the management service in a while.
	DeviceStatus *DeviceStatus `json:"deviceStatus,omitempty"`
}

// EnrollmentRequestStatus EnrollmentRequestStatus represents information about the status of a EnrollmentRequest.
type EnrollmentRequestStatus struct {
	// Certificate certificate is a PEM-encoded signed certificate.
	Certificate *string `json:"certificate,omitempty"`

	// Conditions Current state of the EnrollmentRequest.
	Conditions *[]EnrollmentRequestCondition `json:"conditions,omitempty"`
}

// Fleet Fleet represents a set of devices.
type Fleet struct {
	// ApiVersion APIVersion defines the versioned schema of this representation of an object. Servers should convert recognized schemas to the latest internal value, and may reject unrecognized values. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources
	ApiVersion string `json:"apiVersion"`

	// Kind Kind is a string value representing the REST resource this object represents. Servers may infer this from the endpoint the client submits requests to. Cannot be updated. In CamelCase. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds
	Kind string `json:"kind"`

	// Metadata ObjectMeta is metadata that all persisted resources must have, which includes all objects users must create.
	Metadata ObjectMeta `json:"metadata"`

	// Spec FleetSpec is a description of a fleet's target state.
	Spec FleetSpec `json:"spec"`

	// Status FleetStatus represents information about the status of a fleet. Status may trail the actual state of a fleet, especially if devices of a fleet have not contacted the management service in a while.
	Status *FleetStatus `json:"status,omitempty"`
}

// FleetCondition DeviceCondition contains condition information for a device.
type FleetCondition struct {
	LastTransitionTime *string `json:"lastTransitionTime,omitempty"`

	// Message Human readable message indicating details about last transition.
	Message *string `json:"message,omitempty"`

	// Reason (brief) reason for the condition's last transition.
	Reason *string `json:"reason,omitempty"`

	// Status Status of the condition, one of True, False, Unknown.
	Status string `json:"status"`

	// Type Type of fleet condition.
	Type string `json:"type"`
}

// FleetList FleetList is a list of Fleets.
type FleetList struct {
	// ApiVersion APIVersion defines the versioned schema of this representation of an object. Servers should convert recognized schemas to the latest internal value, and may reject unrecognized values. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources
	ApiVersion string `json:"apiVersion"`

	// Items List of Fleets.
	Items []Fleet `json:"items"`

	// Kind Kind is a string value representing the REST resource this object represents. Servers may infer this from the endpoint the client submits requests to. Cannot be updated. In CamelCase. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds
	Kind string `json:"kind"`

	// Metadata ListMeta describes metadata that synthetic resources must have, including lists and various status objects. A resource may have only one of {ObjectMeta, ListMeta}.
	Metadata ListMeta `json:"metadata"`
}

// FleetSpec FleetSpec is a description of a fleet's target state.
type FleetSpec struct {
	Selector *LabelSelector `json:"selector,omitempty"`
	Template struct {
		// Metadata ObjectMeta is metadata that all persisted resources must have, which includes all objects users must create.
		Metadata *ObjectMeta `json:"metadata,omitempty"`

		// Spec DeviceSpec is a description of a device's target state.
		Spec DeviceSpec `json:"spec"`
	} `json:"template"`
}

// FleetStatus FleetStatus represents information about the status of a fleet. Status may trail the actual state of a fleet, especially if devices of a fleet have not contacted the management service in a while.
type FleetStatus struct {
	// Conditions Current state of the fleet.
	Conditions *[]FleetCondition `json:"conditions,omitempty"`
}

// GitConfigProviderSpec defines model for GitConfigProviderSpec.
type GitConfigProviderSpec struct {
	GitRef struct {
		Path           string `json:"path"`
		RepoURL        string `json:"repoURL"`
		TargetRevision string `json:"targetRevision"`
	} `json:"gitRef"`
	Name string `json:"name"`
}

// InlineConfigProviderSpec defines model for InlineConfigProviderSpec.
type InlineConfigProviderSpec struct {
	Inline map[string]interface{} `json:"inline"`
	Name   string                 `json:"name"`
}

// KubernetesSecretProviderSpec defines model for KubernetesSecretProviderSpec.
type KubernetesSecretProviderSpec struct {
	Name      string `json:"name"`
	SecretRef struct {
		MountPath string `json:"mountPath"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"secretRef"`
}

// LabelSelector defines model for LabelSelector.
type LabelSelector struct {
	MatchLabels map[string]string `json:"matchLabels"`
}

// ListMeta ListMeta describes metadata that synthetic resources must have, including lists and various status objects. A resource may have only one of {ObjectMeta, ListMeta}.
type ListMeta struct {
	// Continue continue may be set if the user set a limit on the number of items returned, and indicates that the server has more data available. The value is opaque and may be used to issue another request to the endpoint that served this list to retrieve the next set of available objects. Continuing a consistent list may not be possible if the server configuration has changed or more than a few minutes have passed. The resourceVersion field returned when using this continue value will be identical to the value in the first response, unless you have received this token from an error message.
	Continue *string `json:"continue,omitempty"`

	// RemainingItemCount remainingItemCount is the number of subsequent items in the list which are not included in this list response. If the list request contained label or field selectors, then the number of remaining items is unknown and the field will be left unset and omitted during serialization. If the list is complete (either because it is not chunking or because this is the last chunk), then there are no more remaining items and this field will be left unset and omitted during serialization. Servers older than v1.15 do not set this field. The intended use of the remainingItemCount is *estimating* the size of a collection. Clients should not rely on the remainingItemCount to be set or to be exact.
	RemainingItemCount *int64 `json:"remainingItemCount,omitempty"`
}

// ObjectMeta ObjectMeta is metadata that all persisted resources must have, which includes all objects users must create.
type ObjectMeta struct {
	CreationTimestamp *string `json:"creationTimestamp,omitempty"`
	DeletionTimestamp *string `json:"deletionTimestamp,omitempty"`

	// Labels Map of string keys and values that can be used to organize and categorize (scope and select) objects.
	Labels *map[string]string `json:"labels,omitempty"`

	// Name name of the object
	Name *string `json:"name,omitempty"`
}

// Status Status is a return value for calls that don't return other objects.
type Status struct {
	// Message A human-readable description of the status of this operation.
	Message *string `json:"message,omitempty"`

	// Reason A machine-readable description of why this operation is in the "Failure" status. If this value is empty there is no information available. A Reason clarifies an HTTP status code but does not override it.
	Reason *string `json:"reason,omitempty"`

	// Status Status of the operation. One of: "Success" or "Failure". More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#spec-and-status
	Status *string `json:"status,omitempty"`
}

// ListDevicesParams defines parameters for ListDevices.
type ListDevicesParams struct {
	// Continue An optional parameter to query more results from the server. The value of the paramter must match the value of the 'continue' field in the previous list response.
	Continue *string `form:"continue,omitempty" json:"continue,omitempty"`

	// LabelSelector A selector to restrict the list of returned objects by their labels. Defaults to everything.
	LabelSelector *string `form:"labelSelector,omitempty" json:"labelSelector,omitempty"`

	// Limit The maximum number of results returned in the list response. The server will set the 'continue' field in the list response if more results exist. The continue value may then be specified as parameter in a subesquent query.
	Limit *int32 `form:"limit,omitempty" json:"limit,omitempty"`
}

// ListEnrollmentRequestsParams defines parameters for ListEnrollmentRequests.
type ListEnrollmentRequestsParams struct {
	// Continue An optional parameter to query more results from the server. The value of the paramter must match the value of the 'continue' field in the previous list response.
	Continue *string `form:"continue,omitempty" json:"continue,omitempty"`

	// LabelSelector A selector to restrict the list of returned objects by their labels. Defaults to everything.
	LabelSelector *string `form:"labelSelector,omitempty" json:"labelSelector,omitempty"`

	// Limit The maximum number of results returned in the list response. The server will set the 'continue' field in the list response if more results exist. The continue value may then be specified as parameter in a subesquent query.
	Limit *int32 `form:"limit,omitempty" json:"limit,omitempty"`
}

// ListFleetsParams defines parameters for ListFleets.
type ListFleetsParams struct {
	// Continue An optional parameter to query more results from the server. The value of the paramter must match the value of the 'continue' field in the previous list response.
	Continue *string `form:"continue,omitempty" json:"continue,omitempty"`

	// LabelSelector A selector to restrict the list of returned objects by their labels. Defaults to everything.
	LabelSelector *string `form:"labelSelector,omitempty" json:"labelSelector,omitempty"`

	// Limit The maximum number of results returned in the list response. The server will set the 'continue' field in the list response if more results exist. The continue value may then be specified as parameter in a subesquent query.
	Limit *int32 `form:"limit,omitempty" json:"limit,omitempty"`
}

// CreateDeviceJSONRequestBody defines body for CreateDevice for application/json ContentType.
type CreateDeviceJSONRequestBody = Device

// ReplaceDeviceJSONRequestBody defines body for ReplaceDevice for application/json ContentType.
type ReplaceDeviceJSONRequestBody = Device

// ReplaceDeviceStatusJSONRequestBody defines body for ReplaceDeviceStatus for application/json ContentType.
type ReplaceDeviceStatusJSONRequestBody = Device

// CreateEnrollmentRequestJSONRequestBody defines body for CreateEnrollmentRequest for application/json ContentType.
type CreateEnrollmentRequestJSONRequestBody = EnrollmentRequest

// ReplaceEnrollmentRequestJSONRequestBody defines body for ReplaceEnrollmentRequest for application/json ContentType.
type ReplaceEnrollmentRequestJSONRequestBody = EnrollmentRequest

// ReplaceEnrollmentRequestApprovalJSONRequestBody defines body for ReplaceEnrollmentRequestApproval for application/json ContentType.
type ReplaceEnrollmentRequestApprovalJSONRequestBody = EnrollmentRequest

// ReplaceEnrollmentRequestStatusJSONRequestBody defines body for ReplaceEnrollmentRequestStatus for application/json ContentType.
type ReplaceEnrollmentRequestStatusJSONRequestBody = EnrollmentRequest

// CreateFleetJSONRequestBody defines body for CreateFleet for application/json ContentType.
type CreateFleetJSONRequestBody = Fleet

// ReplaceFleetJSONRequestBody defines body for ReplaceFleet for application/json ContentType.
type ReplaceFleetJSONRequestBody = Fleet

// ReplaceFleetStatusJSONRequestBody defines body for ReplaceFleetStatus for application/json ContentType.
type ReplaceFleetStatusJSONRequestBody = Fleet

// AsGitConfigProviderSpec returns the union data inside the DeviceSpec_Config_Item as a GitConfigProviderSpec
func (t DeviceSpec_Config_Item) AsGitConfigProviderSpec() (GitConfigProviderSpec, error) {
	var body GitConfigProviderSpec
	err := json.Unmarshal(t.union, &body)
	return body, err
}

// FromGitConfigProviderSpec overwrites any union data inside the DeviceSpec_Config_Item as the provided GitConfigProviderSpec
func (t *DeviceSpec_Config_Item) FromGitConfigProviderSpec(v GitConfigProviderSpec) error {
	b, err := json.Marshal(v)
	t.union = b
	return err
}

// MergeGitConfigProviderSpec performs a merge with any union data inside the DeviceSpec_Config_Item, using the provided GitConfigProviderSpec
func (t *DeviceSpec_Config_Item) MergeGitConfigProviderSpec(v GitConfigProviderSpec) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}

	merged, err := runtime.JsonMerge(t.union, b)
	t.union = merged
	return err
}

// AsKubernetesSecretProviderSpec returns the union data inside the DeviceSpec_Config_Item as a KubernetesSecretProviderSpec
func (t DeviceSpec_Config_Item) AsKubernetesSecretProviderSpec() (KubernetesSecretProviderSpec, error) {
	var body KubernetesSecretProviderSpec
	err := json.Unmarshal(t.union, &body)
	return body, err
}

// FromKubernetesSecretProviderSpec overwrites any union data inside the DeviceSpec_Config_Item as the provided KubernetesSecretProviderSpec
func (t *DeviceSpec_Config_Item) FromKubernetesSecretProviderSpec(v KubernetesSecretProviderSpec) error {
	b, err := json.Marshal(v)
	t.union = b
	return err
}

// MergeKubernetesSecretProviderSpec performs a merge with any union data inside the DeviceSpec_Config_Item, using the provided KubernetesSecretProviderSpec
func (t *DeviceSpec_Config_Item) MergeKubernetesSecretProviderSpec(v KubernetesSecretProviderSpec) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}

	merged, err := runtime.JsonMerge(t.union, b)
	t.union = merged
	return err
}

// AsInlineConfigProviderSpec returns the union data inside the DeviceSpec_Config_Item as a InlineConfigProviderSpec
func (t DeviceSpec_Config_Item) AsInlineConfigProviderSpec() (InlineConfigProviderSpec, error) {
	var body InlineConfigProviderSpec
	err := json.Unmarshal(t.union, &body)
	return body, err
}

// FromInlineConfigProviderSpec overwrites any union data inside the DeviceSpec_Config_Item as the provided InlineConfigProviderSpec
func (t *DeviceSpec_Config_Item) FromInlineConfigProviderSpec(v InlineConfigProviderSpec) error {
	b, err := json.Marshal(v)
	t.union = b
	return err
}

// MergeInlineConfigProviderSpec performs a merge with any union data inside the DeviceSpec_Config_Item, using the provided InlineConfigProviderSpec
func (t *DeviceSpec_Config_Item) MergeInlineConfigProviderSpec(v InlineConfigProviderSpec) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}

	merged, err := runtime.JsonMerge(t.union, b)
	t.union = merged
	return err
}

func (t DeviceSpec_Config_Item) MarshalJSON() ([]byte, error) {
	b, err := t.union.MarshalJSON()
	return b, err
}

func (t *DeviceSpec_Config_Item) UnmarshalJSON(b []byte) error {
	err := t.union.UnmarshalJSON(b)
	return err
}
