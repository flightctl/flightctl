package healthcheckextension

import (
	"fmt"
	"net"
	"net/http"
	"path"
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

	if err := validateWithServeMux(c.LivePath, c.ReadyPath); err != nil {
		return err
	}

	return nil
}

func validateProbePath(field, p string) error {
	if p == "" {
		return fmt.Errorf("%q must not be empty", field)
	}
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("%q %q must start with /", field, p)
	}
	if strings.ContainsAny(p, "?#") {
		return fmt.Errorf(
			"%q %q must not contain a query string or fragment",
			field,
			p,
		)
	}
	if strings.Contains(p, "%") {
		return fmt.Errorf(
			"%q %q must not contain percent-encoded characters",
			field,
			p,
		)
	}
	if strings.ContainsAny(p, "{}") {
		return fmt.Errorf(
			"%q %q must not contain '{' or '}' (ServeMux wildcards are not supported)",
			field,
			p,
		)
	}

	// Reject non-canonical paths (double slashes, dot segments, trailing slashes).
	cleaned := path.Clean(p)
	if cleaned != p {
		return fmt.Errorf("%s %q must be a canonical path (use %q instead)", field, p, cleaned)
	}

	return nil
}

func validateWithServeMux(paths ...string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("invalid healthcheck route: %v", r)
		}
	}()
	mux := http.NewServeMux()
	noop := func(http.ResponseWriter, *http.Request) {}
	for _, p := range paths {
		mux.HandleFunc(p, noop)
	}
	return nil
}
