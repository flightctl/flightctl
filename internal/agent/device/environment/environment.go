package environment

import (
	"os"
	"strings"
)

// EnvVar represents a FlightCtl environment variable name
type EnvVar string

const (
	// DisableConsoleBanner controls whether banner printing to console is disabled
	DisableConsoleBanner EnvVar = "FLIGHTCTL_DISABLE_CONSOLE_BANNER"
	// Simulated controls whether the agent runs in simulation mode
	Simulated EnvVar = "FLIGHTCTL_SIMULATED"
)

// IsEnabled checks if the specified environment variable is enabled.
// It accepts "true" (case-insensitive) or "1" as enabled values.
func IsEnabled(envVar EnvVar) bool {
	value := os.Getenv(string(envVar))
	return strings.EqualFold(value, "true") || value == "1"
}
