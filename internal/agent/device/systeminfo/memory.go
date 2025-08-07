package systeminfo

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

func collectMemoryInfo(log *log.PrefixLogger, reader fileio.Reader) (*MemoryInfo, error) {
	data, err := reader.ReadFile(memInfoPath)
	if err != nil {
		return nil, fmt.Errorf("read memory info: %w", err)
	}

	memInfo := &MemoryInfo{}
	lines := strings.Split(string(data), "\n")

	for _, line := range lines {
		if strings.HasPrefix(line, "MemTotal:") {
			// split by whitespace
			parts := strings.Fields(line)
			if len(parts) < 2 {
				return nil, fmt.Errorf("invalid MemTotal format")
			}

			valueKB, err := strconv.ParseUint(parts[1], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse memory value: %w", err)
			}

			memInfo.TotalKB = valueKB
			break
		}
	}

	if memInfo.TotalKB == 0 {
		log.Warnf("MemTotal not found in %s", memInfoPath)
	}

	return memInfo, nil
}
