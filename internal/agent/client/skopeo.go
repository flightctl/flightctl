package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	skopeoCmd            = "skopeo"
	defaultSkopeoTimeout = 2 * time.Minute
)

// OCIManifest represents the minimal OCI manifest structure needed for type detection
type OCIManifest struct {
	MediaType    string          `json:"mediaType"`
	ArtifactType string          `json:"artifactType,omitempty"`
	Config       *OCIDescriptor  `json:"config,omitempty"`
	Manifests    json.RawMessage `json:"manifests,omitempty"`
}

// OCIDescriptor represents a content descriptor in an OCI manifest
type OCIDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
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

	args := []string{"inspect", "--raw", fmt.Sprintf("docker://%s", image)}

	pullSecretPath := options.pullSecretPath
	if pullSecretPath != "" {
		exists, err := s.readWriter.PathExists(pullSecretPath)
		if err != nil {
			return nil, fmt.Errorf("check pull secret path: %w", err)
		}
		if !exists {
			return nil, fmt.Errorf("pull secret path %s does not exist", pullSecretPath)
		}
		args = append(args, "--authfile", pullSecretPath)
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
