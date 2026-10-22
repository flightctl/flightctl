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
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/opencontainers/go-digest"
	"gopkg.in/yaml.v3"
)

const (
	crictlCmd                 = "crictl"
	ctrCmd                    = "ctr"
	defaultCRITimeout         = 2 * time.Minute
	defaultCRIConfigPath      = "/etc/crictl.yaml"
	defaultContainerdEndpoint = "/run/containerd/containerd.sock"
	containerdKubernetesNS    = "k8s.io"
	ociDeltaLayoutImageTag    = "img"
)

const (
	// CRIRuntimeCRIO is the RuntimeName value reported by CRI-O.
	CRIRuntimeCRIO = "cri-o"
	// CRIRuntimeContainerd is the RuntimeName value reported by containerd.
	CRIRuntimeContainerd = "containerd"
)

// CRIRuntimeInfo describes the local CRI implementation and the containerd
// socket used by Kubernetes, when applicable.
type CRIRuntimeInfo struct {
	Name     string
	Endpoint string
}

// CRIImageMetadata is the local image identity and repository metadata reported by CRI.
type CRIImageMetadata struct {
	ID          string
	RepoDigests []string
}

// CRI provides a client for executing crictl CLI commands.
type CRI struct {
	exec       executer.Executer
	log        *log.PrefixLogger
	timeout    time.Duration
	readWriter fileio.ReadWriter
	backoff    poll.Config
}

// NewCRI creates a new CRI client for interacting with container runtimes via crictl.
func NewCRI(log *log.PrefixLogger, exec executer.Executer, readWriter fileio.ReadWriter, backoff poll.Config) *CRI {
	return &CRI{
		log:        log,
		exec:       exec,
		timeout:    defaultCRITimeout,
		readWriter: readWriter,
		backoff:    backoff,
	}
}

// Pull pulls an image using crictl and returns the runtime's image reference.
// Logs progress periodically while the operation is in progress.
func (c *CRI) Pull(ctx context.Context, image string, opts ...ClientOption) (string, error) {
	options := &clientOptions{}
	for _, opt := range opts {
		opt(options)
	}

	return logProgress(ctx, c.log, fmt.Sprintf("Pulling CRI image %s, please wait...", image), func(ctx context.Context) (string, error) {
		return retryWithBackoff(ctx, c.log, c.backoff, func(ctx context.Context) (string, error) {
			return c.pull(ctx, image, options)
		})
	})
}

func (c *CRI) pull(ctx context.Context, image string, options *clientOptions) (string, error) {
	timeout := c.timeout
	if options.timeout > 0 {
		timeout = options.timeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	c.log.Debugf("Pulling CRI image %s (timeout: %v)", image, timeout)

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

	c.log.Debugf("CRI image %s pulled successfully", image)
	const imageRefPrefix = "Image is up to date for "
	imageRef, ok := strings.CutPrefix(strings.TrimSpace(stdout), imageRefPrefix)
	if !ok {
		return "", nil
	}
	return strings.TrimSpace(imageRef), nil
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

// ImageInventory returns the CRI runtime's image list with repository digests.
// It is used by delta investigation diagnostics to compare the imported image
// identity with a digest-pinned target before the canonical reference refresh.
func (c *CRI) ImageInventory(ctx context.Context, opts ...ClientOption) (string, error) {
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

	args := []string{}
	if options.criConfigPath != "" {
		exists, err := c.readWriter.PathExists(options.criConfigPath)
		if err != nil {
			return "", fmt.Errorf("check CRI config path: %w", err)
		}
		if exists {
			args = append(args, "--config", options.criConfigPath)
		}
	}
	args = append(args, "images", "--digests", "--no-trunc")
	stdout, stderr, exitCode := c.exec.ExecuteWithContext(ctx, crictlCmd, args...)
	if exitCode != 0 {
		return strings.TrimSpace(stdout), fmt.Errorf("crictl images: %w", errors.FromStderr(stderr, exitCode))
	}
	return strings.TrimSpace(stdout), nil
}

// ImageRepoDigests returns the registry digests recorded for a local CRI image.
func (c *CRI) ImageRepoDigests(ctx context.Context, image string, opts ...ClientOption) ([]string, error) {
	metadata, err := c.ImageMetadata(ctx, image, opts...)
	if err != nil {
		return nil, err
	}
	return metadata.RepoDigests, nil
}

// ImageMetadata returns the runtime identity and repository digests for a local CRI image.
func (c *CRI) ImageMetadata(ctx context.Context, image string, opts ...ClientOption) (CRIImageMetadata, error) {
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

	args := []string{}
	if options.criConfigPath != "" {
		exists, err := c.readWriter.PathExists(options.criConfigPath)
		if err != nil {
			return CRIImageMetadata{}, fmt.Errorf("check CRI config path: %w", err)
		}
		if !exists {
			c.log.Errorf("CRI config path does not exist: %s", options.criConfigPath)
		} else {
			args = append(args, "--config", options.criConfigPath)
		}
	}
	args = append(args, "inspecti", "--output", "json", image)

	stdout, stderr, exitCode := c.exec.ExecuteWithContext(ctx, crictlCmd, args...)
	if exitCode != 0 {
		return CRIImageMetadata{}, fmt.Errorf("inspect CRI image %s: %w", image, errors.FromStderr(stderr, exitCode))
	}

	var response struct {
		Status struct {
			ID          string   `json:"id"`
			RepoDigests []string `json:"repoDigests"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &response); err != nil {
		return CRIImageMetadata{}, fmt.Errorf("parse CRI image status for %s: %w", image, err)
	}
	return CRIImageMetadata{ID: response.Status.ID, RepoDigests: response.Status.RepoDigests}, nil
}

// ImageReferences finds registry references keyed by repository digest in the
// configured CRI store with one image-list request.
func (c *CRI) ImageReferences(ctx context.Context, opts ...ClientOption) (map[string]string, error) {
	options := &clientOptions{}
	for _, opt := range opts {
		opt(options)
	}
	references := make(map[string]string)
	timeout := c.timeout
	if options.timeout > 0 {
		timeout = options.timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{}
	if options.criConfigPath != "" {
		exists, err := c.readWriter.PathExists(options.criConfigPath)
		if err != nil {
			return nil, fmt.Errorf("check CRI config path: %w", err)
		}
		if exists {
			args = append(args, "--config", options.criConfigPath)
		}
	}
	args = append(args, "images", "--output", "json")
	stdout, stderr, exitCode := c.exec.ExecuteWithContext(ctx, crictlCmd, args...)
	if exitCode != 0 {
		return nil, fmt.Errorf("list CRI images: %w", errors.FromStderr(stderr, exitCode))
	}
	var response struct {
		Images []struct {
			RepoDigests []string `json:"repoDigests"`
		} `json:"images"`
	}
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		return nil, fmt.Errorf("parse CRI images: %w", err)
	}
	for _, image := range response.Images {
		for _, repoDigest := range image.RepoDigests {
			named, err := reference.ParseNormalizedNamed(repoDigest)
			if err != nil {
				continue
			}
			pinned, ok := named.(reference.Digested)
			if !ok {
				continue
			}
			repositoryDigest := pinned.Digest().String()
			if selected := references[repositoryDigest]; selected == "" || named.String() < selected {
				references[repositoryDigest] = named.String()
			}
		}
	}
	return references, nil
}

// RuntimeInfo returns the runtime connected to crictl and, for containerd, its
// socket path for ctr imports.
func (c *CRI) RuntimeInfo(ctx context.Context, opts ...ClientOption) (*CRIRuntimeInfo, error) {
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

	args := []string{}
	if options.criConfigPath != "" {
		exists, err := c.readWriter.PathExists(options.criConfigPath)
		if err != nil {
			return nil, fmt.Errorf("check crictl config path: %w", err)
		}
		if exists {
			args = append(args, "--config", options.criConfigPath)
		}
	}
	args = append(args, "version")

	stdout, stderr, exitCode := c.exec.ExecuteWithContext(ctx, crictlCmd, args...)
	if exitCode != 0 {
		return nil, fmt.Errorf("crictl version: %w", errors.FromStderr(stderr, exitCode))
	}

	var runtimeName string
	for _, line := range strings.Split(stdout, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(key) == "RuntimeName" {
			runtimeName = strings.ToLower(strings.TrimSpace(value))
			break
		}
	}
	if runtimeName == "" {
		return nil, fmt.Errorf("crictl version did not report RuntimeName")
	}

	info := &CRIRuntimeInfo{Name: runtimeName}
	if runtimeName == CRIRuntimeContainerd {
		endpoint, err := c.containerdEndpoint(options)
		if err != nil {
			return nil, err
		}
		info.Endpoint = endpoint
	}
	return info, nil
}

type crictlConfig struct {
	RuntimeEndpoint string `yaml:"runtime-endpoint"`
	ImageEndpoint   string `yaml:"image-endpoint"`
}

func (c *CRI) containerdEndpoint(options *clientOptions) (string, error) {
	configPath := options.criConfigPath
	if configPath == "" {
		configPath = defaultCRIConfigPath
	}

	endpoint := ""
	configPaths := []string{configPath}
	if configPath != defaultCRIConfigPath {
		configPaths = append(configPaths, defaultCRIConfigPath)
	}
	for _, path := range configPaths {
		exists, err := c.readWriter.PathExists(path)
		if err != nil {
			return "", fmt.Errorf("check crictl config path: %w", err)
		}
		if !exists {
			continue
		}
		data, err := c.readWriter.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read crictl config: %w", err)
		}
		var config crictlConfig
		if err := yaml.Unmarshal(data, &config); err != nil {
			return "", fmt.Errorf("parse crictl config: %w", err)
		}
		endpoint = config.RuntimeEndpoint
		if endpoint == "" {
			endpoint = config.ImageEndpoint
		}
		break
	}
	if endpoint == "" {
		endpoint = "unix://" + defaultContainerdEndpoint
	}

	if strings.HasPrefix(endpoint, "unix://") {
		endpoint = strings.TrimPrefix(endpoint, "unix://")
	} else if strings.Contains(endpoint, "://") {
		return "", fmt.Errorf("unsupported containerd endpoint %q", endpoint)
	}
	if endpoint == "" || !strings.HasPrefix(endpoint, "/") {
		return "", fmt.Errorf("containerd endpoint must be an absolute unix socket path: %q", endpoint)
	}
	return endpoint, nil
}

// MountContainerdImage mounts a locally cached image from containerd so
// oci-delta can use it as source data when reconstructing changed layers.
func (c *CRI) MountContainerdImage(ctx context.Context, endpoint, image, mountPath string, opts ...ClientOption) error {
	return c.executeContainerd(ctx, endpoint, []string{"images", "mount", image, mountPath}, opts...)
}

// UnmountContainerdImage releases a source image mount created by
// MountContainerdImage.
func (c *CRI) UnmountContainerdImage(ctx context.Context, endpoint, mountPath string, opts ...ClientOption) error {
	return c.executeContainerd(ctx, endpoint, []string{"images", "unmount", mountPath}, opts...)
}

// ImportContainerdImage imports a reconstructed OCI archive into Kubernetes'
// containerd namespace, then assigns the reference used by the workload.
func (c *CRI) ImportContainerdImage(ctx context.Context, endpoint, archivePath, importBase, targetImage string, opts ...ClientOption) error {
	temporaryRef := importBase + ":" + ociDeltaLayoutImageTag
	if err := c.executeContainerd(
		ctx,
		endpoint,
		[]string{
			"images", "import",
			"--base-name", importBase,
			"--index-name", temporaryRef,
			archivePath,
		},
		opts...,
	); err != nil {
		c.cleanupContainerdImage(ctx, endpoint, temporaryRef, opts...)
		return err
	}

	if err := c.executeContainerd(ctx, endpoint, []string{"images", "tag", "--force", temporaryRef, targetImage}, opts...); err != nil {
		c.cleanupContainerdImage(ctx, endpoint, temporaryRef, opts...)
		return err
	}
	c.cleanupContainerdImage(ctx, endpoint, temporaryRef, opts...)
	return nil
}

// TagContainerdImage force-binds an existing containerd image reference in the
// Kubernetes namespace to another reference.
func (c *CRI) TagContainerdImage(ctx context.Context, endpoint, source, target string, opts ...ClientOption) error {
	return c.executeContainerd(ctx, endpoint, []string{"images", "tag", "--force", source, target}, opts...)
}

// ContainerdImagePlatformDigest resolves a locally cached image index to the
// manifest for this host without contacting its source registry.
func (c *CRI) ContainerdImagePlatformDigest(ctx context.Context, endpoint, repositoryDigest string, opts ...ClientOption) (string, error) {
	expectedDigest, err := digest.Parse(repositoryDigest)
	if err != nil {
		return "", fmt.Errorf("parse containerd image digest: %w", err)
	}
	raw, err := c.executeContainerdOutput(ctx, endpoint, []string{"content", "get", repositoryDigest}, opts...)
	if err != nil {
		return "", err
	}
	return platformDigestFromRawManifest(raw, expectedDigest)
}

func (c *CRI) executeContainerd(ctx context.Context, endpoint string, args []string, opts ...ClientOption) error {
	_, err := c.executeContainerdOutput(ctx, endpoint, args, opts...)
	return err
}

func (c *CRI) executeContainerdOutput(ctx context.Context, endpoint string, args []string, opts ...ClientOption) (string, error) {
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

	commandArgs := []string{"--address", endpoint, "--namespace", containerdKubernetesNS}
	commandArgs = append(commandArgs, args...)
	stdout, stderr, exitCode := c.exec.ExecuteWithContext(ctx, ctrCmd, commandArgs...)
	if exitCode != 0 {
		return "", fmt.Errorf("ctr %s: %w", strings.Join(args[:2], " "), errors.FromStderr(stderr, exitCode))
	}
	return stdout, nil
}

func (c *CRI) cleanupContainerdImage(ctx context.Context, endpoint, image string, opts ...ClientOption) {
	cleanupCtx := context.WithoutCancel(ctx)
	if err := c.executeContainerd(cleanupCtx, endpoint, []string{"images", "rm", image}, opts...); err != nil {
		c.log.Warnf("remove temporary containerd image reference %s: %v", image, err)
	}
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
