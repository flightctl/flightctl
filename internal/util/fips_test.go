package util

import (
	"crypto/fips140"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsFIPSEnabled(t *testing.T) {
	if os.Getenv("FLIGHTCTL_FIPS_TEST_CHILD") == "1" {
		expected := os.Getenv("FLIGHTCTL_FIPS_TEST_EXPECTED") == "true"
		require.Equal(t, expected, fips140.Enabled())
		require.Equal(t, expected, IsFIPSEnabled())
		return
	}

	tests := []struct {
		name    string
		mode    string
		legacy  string
		enabled bool
	}{
		{"When native FIPS is enabled it should report enabled", "on", "", true},
		{"When native FIPS is disabled it should report disabled", "off", "", false},
		{"When only legacy OpenSSL switches are set it should report the runtime state", "off", "1", false},
		{"When strict native FIPS is enabled it should report enabled", "only", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Native FIPS settings are read before main, so use a fresh process.
			executable, err := os.Executable()
			require.NoError(t, err)
			cmd := exec.Command(executable, "-test.run=^TestIsFIPSEnabled$")
			for _, entry := range os.Environ() {
				if strings.HasPrefix(entry, "GODEBUG=") || strings.HasPrefix(entry, "GOLANG_FIPS=") || strings.HasPrefix(entry, "OPENSSL_FORCE_FIPS_MODE=") {
					continue
				}
				cmd.Env = append(cmd.Env, entry)
			}
			cmd.Env = append(cmd.Env, "FLIGHTCTL_FIPS_TEST_CHILD=1", "FLIGHTCTL_FIPS_TEST_EXPECTED="+strconv.FormatBool(tt.enabled),
				"GODEBUG=fips140="+tt.mode, "GOLANG_FIPS="+tt.legacy, "OPENSSL_FORCE_FIPS_MODE="+tt.legacy)
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
		})
	}
}
