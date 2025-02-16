package device_selection

import "time"

const (
	RolloutDeviceSelectionInterval = 30 * time.Second
	DefaultSuccessThreshold        = 90
	DefaultUpdateTimeout           = 24 * time.Hour
)
