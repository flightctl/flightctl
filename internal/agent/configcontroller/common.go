package configcontroller

// The helpers in this file are intended to be migrated to a shared library such as library-go.

import (
	"fmt"

	ignv3 "github.com/coreos/ignition/v2/config/v3_4"
	ignv3types "github.com/coreos/ignition/v2/config/v3_4/types"
)

// ParseAndConvertConfig parses rawIgn V3 ignition bytes and returns
// a V3 config or an error.
func ParseAndConvertConfig(rawIgn []byte) (ignv3types.Config, error) {
	ignconfigi, err := IgnParseWrapper(rawIgn)
	if err != nil {
		return ignv3types.Config{}, fmt.Errorf("failed to parse Ignition config: %w", err)
	}

	switch typedConfig := ignconfigi.(type) {
	case ignv3types.Config:
		return ignconfigi.(ignv3types.Config), nil
	default:
		return ignv3types.Config{}, fmt.Errorf("unexpected type for ignition config: %v", typedConfig)
	}
}

func IgnParseWrapper(rawIgn []byte) (interface{}, error) {
	ignCfgV3, rptV3, errV3 := ignv3.ParseCompatibleVersion(rawIgn)
	if errV3 == nil && !rptV3.IsFatal() {
		return ignCfgV3, nil
	}

	return ignv3types.Config{}, fmt.Errorf("parsing Ignition config spec v3 failed with error: %v\nReport: %v", errV3, rptV3)
}
