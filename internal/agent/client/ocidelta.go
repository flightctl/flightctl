package client

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	ociDeltaCmd    = "oci-delta"
	ostreeRepoPath = "/ostree/repo"
)

type OCIDelta struct {
	exec    executer.Executer
	log     *log.PrefixLogger
	timeout time.Duration
}

// OCIDeltaFactory creates an OCI delta client for the given storage owner.
type OCIDeltaFactory func(v1beta1.Username) (*OCIDelta, error)

// NewOCIDeltaFactory creates OCI delta clients that run as the storage owner.
func NewOCIDeltaFactory(log *log.PrefixLogger, timeout time.Duration) OCIDeltaFactory {
	return func(username v1beta1.Username) (*OCIDelta, error) {
		exec, err := ExecuterForUser(username)
		if err != nil {
			return nil, fmt.Errorf("create oci-delta executor for user %s: %w", username, err)
		}
		return NewOCIDelta(log, exec, timeout), nil
	}
}

func NewOCIDelta(log *log.PrefixLogger, exec executer.Executer, timeout time.Duration) *OCIDelta {
	return &OCIDelta{
		log:     log,
		exec:    exec,
		timeout: timeout,
	}
}

// Apply reconstructs an OCI image from a pulled delta artifact into dest.
// dest is an oci-delta output reference (oci:PATH or oci-archive:PATH).
func (d *OCIDelta) Apply(ctx context.Context, deltaRef, dest string) error {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	args := []string{
		"apply",
		"--ostree-repo",
		ostreeRepoPath,
		deltaRef,
		dest,
	}
	_, stderr, exitCode := d.exec.ExecuteWithContext(ctx, ociDeltaCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("oci-delta apply: %w", errors.FromStderr(stderr, exitCode))
	}
	return nil
}

// ApplyFromDirectory reconstructs an OCI image from a delta using source
// content in an unpacked root filesystem. dest is an oci-delta output
// reference (oci:PATH or oci-archive:PATH).
func (d *OCIDelta) ApplyFromDirectory(ctx context.Context, sourceDir, deltaRef, dest string) error {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	_, stderr, exitCode := d.exec.ExecuteWithContext(
		ctx,
		ociDeltaCmd,
		"apply",
		"--directory",
		sourceDir,
		deltaRef,
		dest,
	)
	if exitCode != 0 {
		return fmt.Errorf("oci-delta directory apply: %w", errors.FromStderr(stderr, exitCode))
	}
	return nil
}

// Import reconstructs an OCI image directly in containers/storage. An empty
// targetRef imports the image without assigning it a name.
func (d *OCIDelta) Import(ctx context.Context, deltaRef, targetRef string) error {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	args := []string{"import"}
	if targetRef != "" {
		args = append(args, "--tag", targetRef)
	}
	args = append(args, deltaRef)

	_, stderr, exitCode := d.exec.ExecuteWithContext(ctx, ociDeltaCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("oci-delta import: %w", errors.FromStderr(stderr, exitCode))
	}
	return nil
}

// SourceImageDigests returns cached host image digests in the OSTree repository
// used by Apply. It does not consult the separate application
// containers/storage store.
func (d *OCIDelta) SourceImageDigests(ctx context.Context) (map[string]struct{}, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	stdout, stderr, exitCode := d.exec.ExecuteWithContext(ctx, "ostree", "refs", "--list", "--repo", ostreeRepoPath, "ostree/container/image")
	if exitCode != 0 {
		return nil, fmt.Errorf("list bootc images: %w", errors.FromStderr(stderr, exitCode))
	}
	sources := make(map[string]struct{})
	for _, ref := range strings.Fields(stdout) {
		metadata, _, exitCode := d.exec.ExecuteWithContext(ctx, "ostree", "show", "--repo", ostreeRepoPath, "--print-metadata-key=ostree.manifest-digest", ref)
		if exitCode != 0 {
			continue
		}
		if source := strings.Trim(strings.TrimSpace(metadata), "'\""); source != "" {
			sources[source] = struct{}{}
		}
	}
	return sources, nil
}

// SourceImageExists checks cached host images in the OSTree repository used by Apply.
func (d *OCIDelta) SourceImageExists(ctx context.Context, sourceDigest string) (bool, error) {
	sources, err := d.SourceImageDigests(ctx)
	if err != nil {
		return false, err
	}
	_, exists := sources[sourceDigest]
	return exists, nil
}
