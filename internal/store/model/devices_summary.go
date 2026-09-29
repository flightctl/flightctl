package model

import "github.com/flightctl/flightctl/internal/domain"

// CapabilityCountUnknown is the DevicesSummary capabilities map key for devices
// that have not reported a given capability (missing status.capabilities field).
const CapabilityCountUnknown = "unknown"

// NormalizeCapabilityCounts returns a new map with empty-string keys (from SQL
// NULL / missing JSON paths) moved into CapabilityCountUnknown. The input map
// is not modified.
func NormalizeCapabilityCounts(counts map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(counts))
	var emptyCount int64
	hasEmpty := false
	for k, v := range counts {
		if k == "" {
			emptyCount = v
			hasEmpty = true
			continue
		}
		out[k] = v
	}
	if hasEmpty {
		out[CapabilityCountUnknown] += emptyCount
	}
	return out
}

func deviceOsModeCountKey(status *domain.DeviceStatus) string {
	// Prefer status.systemInfo.osMode, falling back to the deprecated
	// status.capabilities.osMode for devices reported by older agents.
	mode := domain.DeviceStatusOsMode(status)
	if mode == nil || *mode == "" {
		return CapabilityCountUnknown
	}
	return string(*mode)
}

// NewDevicesSummaryCapabilities builds the capabilities breakdown for a DevicesSummary,
// normalizing missing osMode values to CapabilityCountUnknown.
func NewDevicesSummaryCapabilities(osMode map[string]int64) *domain.DevicesSummaryCapabilities {
	normalizedCounts := NormalizeCapabilityCounts(osMode)
	return &domain.DevicesSummaryCapabilities{
		OsMode: &normalizedCounts,
	}
}
