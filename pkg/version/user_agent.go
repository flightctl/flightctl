package version

import (
	"fmt"
	"runtime"
)

// UserAgent represents the user agent information for HTTP requests
type UserAgent struct {
	// Name is the application name (e.g., "flightctl-agent", "flightctl-cli")
	Name string
	// Version is the version string
	Version string
	// OS is the operating system
	OS string
	// Arch is the architecture
	Arch string
}

// NewUserAgent creates a new UserAgent with the given application name
func NewUserAgent(name string) UserAgent {
	info := Get()
	return UserAgent{
		Name:    name,
		Version: info.GitVersion,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}
}

func (ua UserAgent) String() string {
	return fmt.Sprintf("%s/%s (%s/%s)",
		ua.Name,
		ua.Version,
		ua.OS,
		ua.Arch,
	)
}
