package apimetadata

import "time"

// EndpointMetadataVersion contains version-specific information for an endpoint
type EndpointMetadataVersion struct {
	Version      string     // e.g., "v1", "v1beta1"
	DeprecatedAt *time.Time // nil if not deprecated; interpreted as 00:00:00 UTC
}

// EndpointMetadata contains metadata for an API endpoint
type EndpointMetadata struct {
	OperationID string
	Resource    string                    // empty = fixed-contract
	Action      string                    // x-rbac.action, else inferred from method/pattern
	Versions    []EndpointMetadataVersion // Ordered by preference (stable > beta > alpha)
}
