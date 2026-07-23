package model

import "github.com/flightctl/flightctl/internal/domain"

// CapabilityCountUnknown is the DevicesSummary capabilities map key for devices
// that have not reported a given capability (missing status.capabilities field).
const CapabilityCountUnknown = "unknown"

// NormalizeCapabilityCounts moves empty-string keys (from SQL NULL / missing JSON
// paths) into CapabilityCountUnknown. Other keys are left unchanged.
func NormalizeCapabilityCounts(counts map[string]int64) map[string]int64 {
	if counts == nil {
		counts = make(map[string]int64)
	}
	if n, ok := counts[""]; ok {
		counts[CapabilityCountUnknown] += n
		delete(counts, "")
	}
	return counts
}

func deviceOsModeCountKey(status *domain.DeviceStatus) string {
	if status == nil || status.Capabilities == nil || status.Capabilities.OsMode == nil {
		return CapabilityCountUnknown
	}
	return string(*status.Capabilities.OsMode)
}

// NewDevicesSummaryCapabilities builds the capabilities breakdown for a DevicesSummary,
// normalizing missing osMode values to CapabilityCountUnknown.
func NewDevicesSummaryCapabilities(osMode map[string]int64) *domain.DevicesSummaryCapabilities {
	normalized := NormalizeCapabilityCounts(osMode)
	return &domain.DevicesSummaryCapabilities{
		OsMode: &normalized,
	}
}
