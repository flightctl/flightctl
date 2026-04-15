package os

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
)

const osReleasePath = "/etc/os-release"

// ParseOSRelease reads /etc/os-release and returns a map of key-value pairs.
// Keys include NAME, VERSION_ID, ID, VERSION, PRETTY_NAME.
func ParseOSRelease(reader fileio.Reader) (map[string]string, error) {
	data, err := reader.ReadFile(osReleasePath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", osReleasePath, err)
	}

	result := make(map[string]string)
	lines := strings.Split(string(data), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := parts[0]
		value := strings.Trim(parts[1], "\"'")
		result[key] = value
	}

	return result, nil
}
