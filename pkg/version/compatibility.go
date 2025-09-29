package version

import (
	"fmt"
	"strconv"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

// Client should be within 2 minor versions of the server
const MinorVersionCompatibility = 2

// VersionCompatibilityChecker handles version compatibility checking between client and server
type VersionCompatibilityChecker struct {
	clientVersion Info
}

// NewVersionCompatibilityChecker creates a new version compatibility checker
func NewVersionCompatibilityChecker() *VersionCompatibilityChecker {
	return &VersionCompatibilityChecker{
		clientVersion: Get(),
	}
}

// CheckCompatibility checks if the client version is compatible with the server version
func (v *VersionCompatibilityChecker) CheckCompatibility(serverVersion *api.Version) error {
	if serverVersion == nil {
		return nil
	}

	clientMajor, clientMinor, err := v.parseVersion(v.clientVersion.GitVersion)
	if err != nil {
		return nil
	}

	serverMajor, serverMinor, err := v.parseVersion(serverVersion.Version)
	if err != nil {
		return nil
	}

	if clientMajor != serverMajor {
		return fmt.Errorf("version incompatibility detected: client %s vs server %s (different major versions). Please align major versions maximum of %d versions apart",
			v.clientVersion.GitVersion, serverVersion.Version, MinorVersionCompatibility)
	}

	if delta := clientMinor - serverMinor; delta > MinorVersionCompatibility || delta < -MinorVersionCompatibility {
		return fmt.Errorf("version incompatibility detected: client %s vs server %s (minor delta exceeds ±%d). Please use a compatible client/server",
			v.clientVersion.GitVersion, serverVersion.Version, MinorVersionCompatibility)
	}

	return nil
}

// parseVersion parses a version string and returns major and minor version numbers
func (v *VersionCompatibilityChecker) parseVersion(versionStr string) (major, minor int, err error) {
	// Handle version strings like "0.9.1-rc.0", "0.5", "v1.2.3", etc.
	versionStr = strings.TrimSpace(versionStr)
	versionStr = strings.TrimPrefix(versionStr, "v")

	parts := strings.Split(versionStr, ".")
	const minSemverParts = 2
	if len(parts) < minSemverParts {
		return 0, 0, fmt.Errorf("invalid version format: %s", versionStr)
	}

	// Parse major version
	majorStr := strings.Split(parts[0], "-")[0] // Remove any suffix like "-rc.0"
	major, err = strconv.Atoi(majorStr)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid major version: %s", majorStr)
	}

	// Parse minor version
	minorStr := strings.Split(parts[1], "-")[0]
	minor, err = strconv.Atoi(minorStr)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid minor version: %s", minorStr)
	}

	return major, minor, nil
}
