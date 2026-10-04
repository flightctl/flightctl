package bootc

import (
	"strings"

	"github.com/coreos/go-semver/semver"
)

const downloadOnlySwitchMinimumVersion = "1.16.10"

// SupportsDownloadOnlySwitch reports whether the version from `bootc --version`
// supports `bootc switch --download-only`.
func SupportsDownloadOnlySwitch(version string) bool {
	fields := strings.Fields(version)
	if len(fields) != 2 || fields[0] != "bootc" {
		return false
	}

	current, err := semver.NewVersion(fields[1])
	if err != nil {
		return false
	}
	minimum, err := semver.NewVersion(downloadOnlySwitchMinimumVersion)
	if err != nil {
		return false
	}
	return current.Compare(*minimum) >= 0
}
