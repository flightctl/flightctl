package util

import "crypto/fips140"

// IsFIPSEnabled reports whether the Go cryptographic module is operating in FIPS
// mode. The runtime initializes this state at process startup, including host
// FIPS detection provided by the Red Hat toolchain.
func IsFIPSEnabled() bool {
	return fips140.Enabled()
}
