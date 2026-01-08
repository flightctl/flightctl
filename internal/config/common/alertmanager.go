package common

// AlertmanagerConfig holds Alertmanager client configuration.
type AlertmanagerConfig struct {
	Hostname   string `json:"hostname,omitempty"`
	Port       uint   `json:"port,omitempty"`
	MaxRetries int    `json:"maxRetries,omitempty"`
	BaseDelay  string `json:"baseDelay,omitempty"`
	MaxDelay   string `json:"maxDelay,omitempty"`
}

// NewDefaultAlertmanager returns a default Alertmanager configuration.
func NewDefaultAlertmanager() *AlertmanagerConfig {
	return &AlertmanagerConfig{
		Hostname:   "localhost",
		Port:       9093,
		MaxRetries: 3,
		BaseDelay:  "500ms",
		MaxDelay:   "10s",
	}
}
