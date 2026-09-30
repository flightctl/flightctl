package healthcheckextension

import (
	"fmt"
	"net"
	"strings"
)

const (
	defaultEndpoint  = "localhost:13133"
	defaultLivePath  = "/livez"
	defaultReadyPath = "/readyz"
)

// Config holds the configuration for the healthcheck extension.
type Config struct {
	// Endpoint is the host:port address on which the health HTTP server
	// listens. The default is "localhost:13133".
	Endpoint string `json:"endpoint,omitempty"`

	// LivePath is the HTTP path for the liveness probe.
	// The default is "/livez".
	LivePath string `json:"livePath,omitempty"`

	// ReadyPath is the HTTP path for the readiness probe.
	// The default is "/readyz".
	ReadyPath string `json:"readyPath,omitempty"`
}

// Validate checks that the configured endpoint and probe paths are usable.
//
// Defaults are supplied by the component factory before configuration is
// decoded, so validation does not modify the configuration.
func (c *Config) Validate() error {
	if c.Endpoint == "" {
		return fmt.Errorf("\"endpoint\" must not be empty")
	}

	host, port, err := net.SplitHostPort(c.Endpoint)
	if err != nil {
		return fmt.Errorf(
			"\"endpoint\" %q is not a valid host:port address: %w",
			c.Endpoint,
			err,
		)
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf(
			"\"endpoint\" %q has an empty host",
			c.Endpoint,
		)
	}
	if strings.TrimSpace(port) == "" {
		return fmt.Errorf(
			"\"endpoint\" %q has an empty port",
			c.Endpoint,
		)
	}

	if err := validateProbePath("livePath", c.LivePath); err != nil {
		return err
	}
	if err := validateProbePath("readyPath", c.ReadyPath); err != nil {
		return err
	}

	if c.LivePath == c.ReadyPath {
		return fmt.Errorf(
			"\"livePath\" and \"readyPath\" must not be the same path (%q)",
			c.LivePath,
		)
	}

	return nil
}

func validateProbePath(field, path string) error {
	if path == "" {
		return fmt.Errorf("%q must not be empty", field)
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("%q %q must start with /", field, path)
	}
	if strings.ContainsAny(path, "?#") {
		return fmt.Errorf(
			"%q %q must not contain a query string or fragment",
			field,
			path,
		)
	}
	if strings.Contains(path, "%") {
		return fmt.Errorf(
			"%q %q must not contain percent-encoded characters",
			field,
			path,
		)
	}
	if strings.ContainsAny(path, "{}") {
		return fmt.Errorf(
			"%q %q must not contain '{' or '}' (ServeMux wildcards are not supported)",
			field,
			path,
		)
	}
	if strings.ContainsAny(path, " \t\r\n") {
		return fmt.Errorf(
			"%q %q must not contain whitespace",
			field,
			path,
		)
	}

	return nil
}
