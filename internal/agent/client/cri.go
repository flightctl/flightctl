package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/containers/image/v5/docker/reference"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	crictlCmd         = "crictl"
	defaultCRITimeout = 2 * time.Minute
)

// CRI provides a client for executing crictl CLI commands.
type CRI struct {
	exec       executer.Executer
	log        *log.PrefixLogger
	timeout    time.Duration
	readWriter fileio.ReadWriter
}

// NewCRI creates a new CRI client for interacting with container runtimes via crictl.
func NewCRI(log *log.PrefixLogger, exec executer.Executer, readWriter fileio.ReadWriter) *CRI {
	return &CRI{
		log:        log,
		exec:       exec,
		timeout:    defaultCRITimeout,
		readWriter: readWriter,
	}
}

// Pull pulls an image using crictl with optional authentication.
func (c *CRI) Pull(ctx context.Context, image string, opts ...ClientOption) (string, error) {
	options := &clientOptions{}
	for _, opt := range opts {
		opt(options)
	}

	timeout := c.timeout
	if options.timeout > 0 {
		timeout = options.timeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var args []string

	if options.criConfigPath != "" {
		exists, err := c.readWriter.PathExists(options.criConfigPath)
		if err != nil {
			return "", fmt.Errorf("check crictl config path: %w", err)
		}
		if !exists {
			c.log.Errorf("CRI config path does not exist: %s", options.criConfigPath)
		} else {
			args = append(args, "--config", options.criConfigPath)
		}
	}

	args = append(args, "pull")

	// We use the CRICTL_AUTH environment variable instead of the --creds or --auth
	// command-line flags for security: environment variables are not visible in the
	// process list (ps), whereas command-line arguments expose credentials to any
	// user who can list processes on the system.
	//
	// When setting cmd.Env in Go, the child process does NOT inherit the parent's
	// environment - it only receives the explicitly provided variables. Therefore,
	// we must include os.Environ() to preserve PATH and other necessary variables.
	env := os.Environ()
	if options.pullSecretPath != "" {
		authString, err := c.getAuthStringForImage(image, options.pullSecretPath)
		if err != nil {
			return "", fmt.Errorf("get credentials for %s: %w", image, err)
		}
		if authString != "" {
			env = append(env, fmt.Sprintf("CRICTL_AUTH=%s", authString))
		}
	}

	args = append(args, image)

	stdout, stderr, exitCode := c.exec.ExecuteWithContextFromDir(ctx, "", crictlCmd, args, env...)
	if exitCode != 0 {
		return "", fmt.Errorf("crictl pull: %w", errors.FromStderr(stderr, exitCode))
	}

	return strings.TrimSpace(stdout), nil
}

// RemoveImage removes an image using crictl rmi.
func (c *CRI) RemoveImage(ctx context.Context, image string, opts ...ClientOption) error {
	options := &clientOptions{}
	for _, opt := range opts {
		opt(options)
	}

	timeout := c.timeout
	if options.timeout > 0 {
		timeout = options.timeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var args []string

	if options.criConfigPath != "" {
		exists, err := c.readWriter.PathExists(options.criConfigPath)
		if err != nil {
			return fmt.Errorf("check crictl config path: %w", err)
		}
		if !exists {
			c.log.Errorf("CRI config path does not exist: %s", options.criConfigPath)
		} else {
			args = append(args, "--config", options.criConfigPath)
		}
	}

	args = append(args, "rmi", image)

	_, stderr, exitCode := c.exec.ExecuteWithContext(ctx, crictlCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("crictl rmi: %w", errors.FromStderr(stderr, exitCode))
	}

	return nil
}

// ImageExists checks if an image exists in the CRI runtime.
func (c *CRI) ImageExists(ctx context.Context, image string, opts ...ClientOption) bool {
	options := &clientOptions{}
	for _, opt := range opts {
		opt(options)
	}

	timeout := c.timeout
	if options.timeout > 0 {
		timeout = options.timeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var args []string

	if options.criConfigPath != "" {
		exists, err := c.readWriter.PathExists(options.criConfigPath)
		if err != nil {
			c.log.Errorf("Failed to check CRI config path %s: %v", options.criConfigPath, err)
			return false
		}
		if !exists {
			c.log.Errorf("CRI config path does not exist: %s", options.criConfigPath)
		} else {
			args = append(args, "--config", options.criConfigPath)
		}
	}

	args = append(args, "images", image)

	stdout, _, exitCode := c.exec.ExecuteWithContext(ctx, crictlCmd, args...)
	if exitCode != 0 {
		return false
	}

	output := strings.TrimSpace(stdout)
	lines := strings.Split(output, "\n")
	return len(lines) > 1
}

// getAuthStringForImage retrieves the base64-encoded auth string for a specific image from an auth file.
// This returns the auth string in the format expected by crictl --auth flag.
func (c *CRI) getAuthStringForImage(image, authPath string) (string, error) {
	config, exists, err := parseAuthFile(c.readWriter, authPath)
	if err != nil {
		return "", err
	}
	if !exists {
		c.log.Errorf("Pull secret path does not exist: %s", authPath)
		return "", nil
	}
	if config == nil {
		return "", nil
	}

	authString, _ := config.getAuthString(image)
	return authString, nil
}

// The following types and functions implement Docker/containers auth.json parsing
// and registry credential matching following podman's authentication logic.
// This ensures compatibility with podman's credential resolution behavior,
// including registry key normalization and hierarchical path matching.
//
// Reference: https://github.com/containers/podman/blob/main/pkg/auth/auth.go

// dockerAuthConfig represents the structure of a Docker/containers auth.json file
type dockerAuthConfig struct {
	Auths map[string]authEntry `json:"auths"`
}

// authEntry represents a single registry authentication entry
type authEntry struct {
	Auth     string `json:"auth,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// parseAuthFile reads and parses a Docker/containers auth.json file.
// Returns the config, whether the file exists, and any error.
func parseAuthFile(rw fileio.ReadWriter, path string) (*dockerAuthConfig, bool, error) {
	exists, err := rw.PathExists(path)
	if err != nil {
		return nil, false, fmt.Errorf("%w: auth file path: %w", errors.ErrCheckingFileExists, err)
	}
	if !exists {
		return nil, false, nil
	}

	data, err := rw.ReadFile(path)
	if err != nil {
		return nil, true, fmt.Errorf("%w: %w", errors.ErrReadingAuthFile, err)
	}

	var rawConfig dockerAuthConfig
	if err := json.Unmarshal(data, &rawConfig); err != nil {
		return nil, true, fmt.Errorf("%w: %w", errors.ErrParsingAuthFile, err)
	}

	config := &dockerAuthConfig{
		Auths: make(map[string]authEntry),
	}
	for key, entry := range rawConfig.Auths {
		normalizedKey := normalizeAuthFileKey(key)
		config.Auths[normalizedKey] = entry
	}

	return config, true, nil
}

// getAuthString returns the base64-encoded auth string for an image reference.
// It follows the podman/containers registry matching rules, searching from
// most specific to least specific:
//   - registry.io/namespace/user/image
//   - registry.io/namespace/user
//   - registry.io/namespace
//   - registry.io
//
// If the auth entry has an Auth field, it returns that directly (already base64 encoded).
// If only Username/Password are available, it encodes them as base64(username:password).
func (a *dockerAuthConfig) getAuthString(imageRef string) (string, bool) {
	if a == nil || len(a.Auths) == 0 {
		return "", false
	}

	ref := normalizeImageRef(imageRef)
	paths := buildRegistryPaths(ref)

	for _, path := range paths {
		if entry, ok := a.Auths[path]; ok {
			authString := getAuthStringFromEntry(entry)
			if authString != "" {
				return authString, true
			}
		}
	}

	return "", false
}

// normalizeImageRef removes the tag/digest and any scheme prefix from an image reference
func normalizeImageRef(imageRef string) string {
	ref := imageRef

	if idx := strings.Index(ref, "://"); idx != -1 {
		ref = ref[idx+3:]
	}

	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return ref
	}

	return reference.TrimNamed(named).Name()
}

// buildRegistryPaths generates all possible registry paths from most specific to least
func buildRegistryPaths(ref string) []string {
	parts := strings.Split(ref, "/")
	if len(parts) == 0 {
		return nil
	}

	var paths []string
	for i := len(parts); i > 0; i-- {
		paths = append(paths, strings.Join(parts[:i], "/"))
	}

	return paths
}

// getAuthStringFromEntry returns the base64-encoded auth string from an auth entry.
// If the entry has an Auth field, it returns that directly.
// If only Username/Password are available, it encodes them as base64(username:password).
func getAuthStringFromEntry(entry authEntry) string {
	if entry.Auth != "" {
		return entry.Auth
	}

	if entry.Username != "" {
		return base64.StdEncoding.EncodeToString(
			[]byte(fmt.Sprintf("%s:%s", entry.Username, entry.Password)))
	}

	return ""
}

// normalizeAuthFileKey takes an auth file key and converts it into a canonical format.
// This follows podman's normalization logic to ensure consistent matching with image references.
func normalizeAuthFileKey(authFileKey string) string {
	stripped := strings.TrimPrefix(authFileKey, "http://")
	stripped = strings.TrimPrefix(stripped, "https://")

	if stripped != authFileKey {
		stripped, _, _ = strings.Cut(stripped, "/")
	}

	switch stripped {
	case "registry-1.docker.io", "index.docker.io":
		return "docker.io"
	default:
		return stripped
	}
}
