package debugdestination

import "fmt"

// Verbosity controls how much detail the debug destination logs for each
// snapshot.
type Verbosity string

const (
	// VerbosityBasic logs a summary containing the pipeline ID, revision,
	// and resource counts.
	VerbosityBasic Verbosity = "basic"

	// VerbosityNormal adds sorted catalog names and catalog item identities
	// to the basic summary.
	VerbosityNormal Verbosity = "normal"

	// VerbosityDetailed logs a basic summary followed by one log entry per
	// Catalog and CatalogItem, including each resource's JSON representation.
	// Enable deliberately because catalog metadata may contain sensitive
	// operational information.
	VerbosityDetailed Verbosity = "detailed"
)

// Config holds the configuration for the debug destination.
type Config struct {
	Verbosity Verbosity `json:"verbosity,omitempty"`
}

// Validate verifies that the configured verbosity is supported.
//
// The factory supplies VerbosityBasic as the default, so an empty value is
// invalid here. This also ensures that an explicitly configured empty value
// does not silently select undocumented behavior.
func (c *Config) Validate() error {
	switch c.Verbosity {
	case VerbosityBasic, VerbosityNormal, VerbosityDetailed:
		return nil
	default:
		return fmt.Errorf(
			"unsupported verbosity %q: must be one of %q, %q, or %q",
			c.Verbosity,
			VerbosityBasic,
			VerbosityNormal,
			VerbosityDetailed,
		)
	}
}
