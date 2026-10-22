package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/containerd/platforms"
	"github.com/containers/image/v5/docker/reference"
	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	skopeoCmd            = "skopeo"
	defaultSkopeoTimeout = 2 * time.Minute
)

// OCIManifest represents the minimal OCI manifest structure needed for type detection
type OCIManifest struct {
	MediaType    string            `json:"mediaType"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Subject      *OCIDescriptor    `json:"subject,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	Config       *OCIDescriptor    `json:"config,omitempty"`
	Layers       []OCIDescriptor   `json:"layers,omitempty"`
	Manifests    json.RawMessage   `json:"manifests,omitempty"`
}

// OCIDescriptor represents a content descriptor in an OCI manifest
type OCIDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

// OCIIndex is an OCI image index, including Referrers results.
type OCIIndex struct {
	SchemaVersion int           `json:"schemaVersion"`
	MediaType     string        `json:"mediaType,omitempty"`
	Manifests     []OCIReferrer `json:"manifests"`
}

// OCIPlatformReferrers ties a referrer index to the exact platform manifest
// digest used to query it.
type OCIPlatformReferrers struct {
	TargetDigest string
	Referrers    *OCIIndex
}

// OCIReferrer is a descriptor in a Referrers index.
type OCIReferrer struct {
	MediaType    string            `json:"mediaType"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

type Skopeo struct {
	exec       executer.Executer
	log        *log.PrefixLogger
	timeout    time.Duration
	readWriter fileio.ReadWriter
}

// SkopeoFactory creates a skopeo client. A blank username means to use the process user.
type SkopeoFactory func(v1beta1.Username) (*Skopeo, error)

func NewSkopeoFactory(log *log.PrefixLogger, rwFactory fileio.ReadWriterFactory) SkopeoFactory {
	return func(username v1beta1.Username) (*Skopeo, error) {
		readWriter, err := rwFactory(username)
		if err != nil {
			return nil, err
		}

		exec, err := ExecuterForUser(username)
		if err != nil {
			return nil, err
		}

		return NewSkopeo(
			log,
			exec,
			readWriter,
		), nil
	}
}

func NewSkopeo(log *log.PrefixLogger, exec executer.Executer, readWriter fileio.ReadWriter) *Skopeo {
	return &Skopeo{
		log:        log,
		exec:       exec,
		timeout:    defaultSkopeoTimeout,
		readWriter: readWriter,
	}
}

// InspectManifest inspects an OCI image or artifact and returns the deserialized manifest.
// This is used to determine if a reference is an image or an artifact.
func (s *Skopeo) InspectManifest(ctx context.Context, image string, opts ...ClientOption) (*OCIManifest, error) {
	options := &clientOptions{}
	for _, opt := range opts {
		opt(options)
	}

	timeout := s.timeout
	if options.timeout > 0 {
		timeout = options.timeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"inspect", "--raw", dockerRef(image)}
	args, err := s.appendAuthArgs(args, options)
	if err != nil {
		return nil, err
	}

	stdout, stderr, exitCode := s.exec.ExecuteWithContext(ctx, skopeoCmd, args...)
	if exitCode != 0 {
		return nil, fmt.Errorf("inspect manifest: %w", errors.FromStderr(stderr, exitCode))
	}

	var manifest OCIManifest
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &manifest); err != nil {
		return nil, fmt.Errorf("parsing manifest JSON: %w", err)
	}

	return &manifest, nil
}

func (s *Skopeo) Copy(ctx context.Context, src, dest string, opts ...ClientOption) error {
	return s.copy(ctx, src, dest, false, opts...)
}

// CopyPreservingDigests copies an image while requiring its manifest digest to
// remain unchanged. It is used for local activation of already pinned content.
func (s *Skopeo) CopyPreservingDigests(ctx context.Context, src, dest string, opts ...ClientOption) error {
	return s.copy(ctx, src, dest, true, opts...)
}

func (s *Skopeo) copy(ctx context.Context, src, dest string, preserveDigests bool, opts ...ClientOption) error {
	options := &clientOptions{}
	for _, opt := range opts {
		opt(options)
	}

	timeout := s.timeout
	if options.timeout > 0 {
		timeout = options.timeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"copy"}
	operation := "copy"
	if preserveDigests {
		args = append(args, "--preserve-digests")
		operation = "copy preserving digests"
	}
	args = append(args, src, dest)
	if options.pullSecretPath != "" {
		exists, err := s.readWriter.PathExists(options.pullSecretPath)
		if err != nil {
			return fmt.Errorf("check pull secret path: %w", err)
		}
		if !exists {
			return fmt.Errorf("pull secret path %s does not exist", options.pullSecretPath)
		}
		args = append(args, "--src-authfile", options.pullSecretPath)
	} else if !options.defaultAuth {
		args = append(args, "--src-no-creds")
	}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, skopeoCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("%s: %w", operation, errors.FromStderr(stderr, exitCode))
	}
	return nil
}

// InspectDigest returns the manifest digest of image.
func (s *Skopeo) InspectDigest(ctx context.Context, image string, opts ...ClientOption) (string, error) {
	options := &clientOptions{}
	for _, opt := range opts {
		opt(options)
	}
	return s.inspectDigest(ctx, image, options)
}

// InspectPlatformDigest resolves an image index to the manifest for this host.
// Single-image manifests return their own content digest.
func (s *Skopeo) InspectPlatformDigest(ctx context.Context, image string, opts ...ClientOption) (string, error) {
	options := &clientOptions{}
	for _, opt := range opts {
		opt(options)
	}
	timeout := s.timeout
	if options.timeout > 0 {
		timeout = options.timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := s.inspectRaw(ctx, image, options)
	if err != nil {
		return "", err
	}
	expectedDigest, err := pinnedManifestDigest(image)
	if err != nil {
		return "", err
	}
	return platformDigestFromRawManifest(raw, expectedDigest)
}

func pinnedManifestDigest(image string) (digest.Digest, error) {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return "", fmt.Errorf("parse image reference: %w", err)
	}
	digested, ok := named.(reference.Digested)
	if !ok {
		return "", nil
	}
	if err := digested.Digest().Validate(); err != nil {
		return "", fmt.Errorf("invalid pinned image digest: %w", err)
	}
	return digested.Digest(), nil
}

func platformDigestFromRawManifest(raw string, expectedDigest digest.Digest) (string, error) {
	var index ocispec.Index
	if err := json.Unmarshal([]byte(raw), &index); err != nil {
		return "", fmt.Errorf("parse image manifest: %w", err)
	}
	if index.MediaType != ocispec.MediaTypeImageIndex && index.MediaType != "application/vnd.docker.distribution.manifest.list.v2+json" {
		if expectedDigest != "" {
			verifier := expectedDigest.Verifier()
			if _, err := verifier.Write([]byte(raw)); err != nil {
				return "", fmt.Errorf("verify pinned image manifest: %w", err)
			}
			if !verifier.Verified() {
				return "", fmt.Errorf("pinned image manifest digest mismatch")
			}
			return expectedDigest.String(), nil
		}
		return digest.FromBytes([]byte(raw)).String(), nil
	}
	hostPlatform := platforms.Default()
	var selected *ocispec.Descriptor
	for i := range index.Manifests {
		descriptor := &index.Manifests[i]
		if descriptor.Platform == nil || !hostPlatform.Match(*descriptor.Platform) {
			continue
		}
		if selected == nil || hostPlatform.Less(*descriptor.Platform, *selected.Platform) {
			selected = descriptor
		}
	}
	if selected == nil {
		return "", fmt.Errorf("image index has no manifest for the host platform")
	}
	if err := selected.Digest.Validate(); err != nil {
		return "", fmt.Errorf("invalid platform manifest digest: %w", err)
	}
	return selected.Digest.String(), nil
}

// ListReferrers returns the referrers index for image.
// On Referrers-API 404, unknown subcommand, or manifest unknown, it inspects the
// Referrers Tag Schema tag on the same repository.
func (s *Skopeo) ListReferrers(ctx context.Context, image string, opts ...ClientOption) (*OCIIndex, error) {
	options := &clientOptions{}
	for _, opt := range opts {
		opt(options)
	}

	timeout := s.timeout
	if options.timeout > 0 {
		timeout = options.timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"list-referrers", dockerRef(image)}
	args, err := s.appendAuthArgs(args, options)
	if err != nil {
		return nil, err
	}

	stdout, stderr, exitCode := s.exec.ExecuteWithContext(ctx, skopeoCmd, args...)
	if exitCode == 0 {
		return parseOCIIndex(stdout)
	}
	if !isReferrersFallback(stderr) {
		return nil, fmt.Errorf("list referrers: %w", errors.FromStderr(stderr, exitCode))
	}

	name, digest := parseImageNameAndDigest(image)
	if digest == "" {
		digest, err = s.inspectDigest(ctx, image, options)
		if err != nil {
			return nil, err
		}
	}
	if name == "" || digest == "" {
		return nil, fmt.Errorf("list referrers: cannot resolve Tag Schema for %q", image)
	}

	raw, err := s.inspectRaw(ctx, tagSchemaRef(name, digest), options)
	if err != nil {
		return nil, err
	}
	return parseOCIIndex(raw)
}

// ListPlatformReferrers resolves image to the host platform manifest and lists
// referrers for that exact manifest digest.
func (s *Skopeo) ListPlatformReferrers(ctx context.Context, image string, opts ...ClientOption) (*OCIPlatformReferrers, error) {
	repository, _ := parseImageNameAndDigest(image)
	if repository == "" {
		return nil, fmt.Errorf("list platform referrers: invalid image reference %q", image)
	}
	platformDigest, err := s.InspectPlatformDigest(ctx, image, opts...)
	if err != nil {
		return nil, fmt.Errorf("inspect target platform digest: %w", err)
	}
	referrers, err := s.ListReferrers(ctx, repository+"@"+platformDigest, opts...)
	if err != nil {
		return nil, err
	}
	return &OCIPlatformReferrers{TargetDigest: platformDigest, Referrers: referrers}, nil
}

func (s *Skopeo) inspectDigest(ctx context.Context, image string, options *clientOptions) (string, error) {
	args := []string{"inspect", "--format", "{{.Digest}}", dockerRef(image)}
	args, err := s.appendAuthArgs(args, options)
	if err != nil {
		return "", err
	}
	stdout, stderr, exitCode := s.exec.ExecuteWithContext(ctx, skopeoCmd, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("inspect digest: %w", errors.FromStderr(stderr, exitCode))
	}
	return strings.TrimSpace(stdout), nil
}

func (s *Skopeo) inspectRaw(ctx context.Context, image string, options *clientOptions) (string, error) {
	args := []string{"inspect", "--raw", dockerRef(image)}
	args, err := s.appendAuthArgs(args, options)
	if err != nil {
		return "", err
	}
	stdout, stderr, exitCode := s.exec.ExecuteWithContext(ctx, skopeoCmd, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("inspect manifest: %w", errors.FromStderr(stderr, exitCode))
	}
	return stdout, nil
}

func (s *Skopeo) appendAuthArgs(args []string, options *clientOptions) ([]string, error) {
	pullSecretPath := options.pullSecretPath
	if pullSecretPath == "" {
		if options.defaultAuth {
			return args, nil
		}
		// Skopeo does not behave well when looking up default credentials as a non-root user without a proper systemd session
		// running, so disable default credentials when none were explicitly provided. This
		// means any credentials required have to be specified in the options.
		return append(args, "--no-creds"), nil
	}
	exists, err := s.readWriter.PathExists(pullSecretPath)
	if err != nil {
		return nil, fmt.Errorf("check pull secret path: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("pull secret path %s does not exist", pullSecretPath)
	}
	return append(args, "--authfile", pullSecretPath), nil
}

func dockerRef(image string) string {
	return fmt.Sprintf("docker://%s", image)
}

func parseOCIIndex(stdout string) (*OCIIndex, error) {
	var index OCIIndex
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &index); err != nil {
		return nil, fmt.Errorf("parsing referrers JSON: %w", err)
	}
	return &index, nil
}

func parseImageNameAndDigest(image string) (name, digest string) {
	matches := validation.OciImageReferenceRegexp.FindStringSubmatch(image)
	if len(matches) == 0 {
		return "", ""
	}
	return matches[1], matches[3]
}

func tagSchemaRef(name, digest string) string {
	return name + ":" + strings.Replace(digest, ":", "-", 1)
}

func isReferrersFallback(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "manifest unknown") ||
		strings.Contains(s, "404") ||
		strings.Contains(s, "unknown command") ||
		strings.Contains(s, "unrecognized command") ||
		strings.Contains(s, "unknown flag")
}
