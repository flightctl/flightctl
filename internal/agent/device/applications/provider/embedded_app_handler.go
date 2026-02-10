package provider

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

type embeddedAppTypeHandler interface {
	Verify(ctx context.Context) error
	// Install the application content to the device.
	Install(ctx context.Context) error
	// Remove the application content from the device.
	Remove(ctx context.Context) error
	// AppPath returns the path the app is to be installed at
	AppPath() string
	// EmbeddedPath returns the path to the embedded app content for reading specs
	EmbeddedPath() string
	// ID returns the Identifier
	ID() string
	// Volumes returns any Volume that is defined as part of the spec itself
	Volumes() ([]*Volume, error)
	// EnsureDependencies checks that required binaries and versions are available
	EnsureDependencies(ctx context.Context) error
	// CollectOCITargets extracts OCI pull targets from the embedded app spec
	CollectOCITargets(ctx context.Context, configProvider dependency.PullConfigResolver) ([]dependency.OCIPullTarget, error)
}

var _ embeddedAppTypeHandler = (*embeddedQuadletBehavior)(nil)
var _ embeddedAppTypeHandler = (*embeddedComposeBehavior)(nil)

type embeddedQuadletBehavior struct {
	name           string
	rw             fileio.ReadWriter
	log            *log.PrefixLogger
	podman         *client.Podman
	commandChecker commandChecker
	bootTime       string
	installed      bool
}

func (e *embeddedQuadletBehavior) Verify(ctx context.Context) error {
	if err := ensureQuadlet(e.rw, e.EmbeddedPath()); err != nil {
		return err
	}

	version, err := e.podman.Version(ctx)
	if err != nil {
		return fmt.Errorf("podman version: %w", err)
	}
	if err := ensureMinQuadletPodmanVersion(version); err != nil {
		return fmt.Errorf("quadlet app type: %w", err)
	}
	return nil
}

func (e *embeddedQuadletBehavior) Install(ctx context.Context) error {
	if err := e.rw.CopyDir(e.EmbeddedPath(), e.AppPath(), fileio.WithFollowSymlinkWithinRoot()); err != nil {
		return fmt.Errorf("copying embedded directory to real path: %w", err)
	}

	if err := installQuadlet(e.rw, e.log, e.AppPath(), quadletSystemdTargetPath(v1beta1.RootUsername, e.ID()), e.ID()); err != nil {
		return fmt.Errorf("installing quadlet: %w", err)
	}

	markerPath := filepath.Join(e.AppPath(), embeddedQuadletMarkerFile)
	if err := e.rw.WriteFile(markerPath, []byte(e.bootTime), fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("writing embedded marker: %w", err)
	}

	return nil
}

func (e *embeddedQuadletBehavior) Remove(ctx context.Context) error {
	path := quadletSystemdTargetPath(v1beta1.RootUsername, e.ID())
	if err := e.rw.RemoveFile(path); err != nil {
		return fmt.Errorf("removing quadlet target file: %w", err)
	}
	if err := e.rw.RemoveAll(e.AppPath()); err != nil {
		return fmt.Errorf("%w: %w", errors.ErrRemovingApplication, err)
	}
	return nil
}

func (e *embeddedQuadletBehavior) AppPath() string {
	return filepath.Join(lifecycle.RootfulQuadletAppPath, e.name)
}

func (e *embeddedQuadletBehavior) EmbeddedPath() string {
	return filepath.Join(lifecycle.EmbeddedQuadletAppPath, e.name)
}

func (e *embeddedQuadletBehavior) ID() string {
	return lifecycle.GenerateAppID(e.name, v1beta1.CurrentProcessUsername)
}

func (e *embeddedQuadletBehavior) Volumes() ([]*Volume, error) {
	if e.installed {
		return extractQuadletVolumesFromDir(e.ID(), e.rw, e.AppPath())
	}
	return extractQuadletVolumesFromDir(e.ID(), e.rw, e.EmbeddedPath())
}

func (e *embeddedQuadletBehavior) EnsureDependencies(ctx context.Context) error {
	if err := ensureDependenciesFromAppType(quadletBinaryDeps, e.commandChecker); err != nil {
		return err
	}
	version, err := e.podman.Version(ctx)
	if err != nil {
		return fmt.Errorf("%w: podman version: %w", errors.ErrAppDependency, err)
	}
	if err := ensureMinQuadletPodmanVersion(version); err != nil {
		return fmt.Errorf("%w: %w", errors.ErrAppDependency, err)
	}
	return nil
}

func (e *embeddedQuadletBehavior) CollectOCITargets(_ context.Context, configProvider dependency.PullConfigResolver) ([]dependency.OCIPullTarget, error) {
	refs, err := client.ParseQuadletReferencesFromDir(e.rw, e.EmbeddedPath())
	if err != nil {
		return nil, fmt.Errorf("parsing quadlet spec: %w", err)
	}
	var targets []dependency.OCIPullTarget
	for _, ref := range refs {
		targets = append(targets, extractQuadletTargets(ref, configProvider)...)
	}
	return targets, nil
}

type embeddedComposeBehavior struct {
	name           string
	rw             fileio.ReadWriter
	commandChecker commandChecker
}

func (e *embeddedComposeBehavior) Verify(ctx context.Context) error {
	if err := ensureCompose(e.rw, e.AppPath()); err != nil {
		return fmt.Errorf("ensuring compose: %w", err)
	}
	return nil
}

func (e *embeddedComposeBehavior) Install(ctx context.Context) error {
	// no-op
	return nil
}

func (e *embeddedComposeBehavior) Remove(ctx context.Context) error {
	return nil
}

func (e *embeddedComposeBehavior) AppPath() string {
	return filepath.Join(lifecycle.EmbeddedComposeAppPath, e.name)
}

func (e *embeddedComposeBehavior) EmbeddedPath() string {
	return filepath.Join(lifecycle.EmbeddedComposeAppPath, e.name)
}

func (e *embeddedComposeBehavior) ID() string {
	return lifecycle.GenerateAppID(e.name, v1beta1.CurrentProcessUsername)
}
func (e *embeddedComposeBehavior) Volumes() ([]*Volume, error) {
	return nil, nil
}

func (e *embeddedComposeBehavior) EnsureDependencies(_ context.Context) error {
	return ensureDependenciesFromAppType(composeBinaryDeps, e.commandChecker)
}

func (e *embeddedComposeBehavior) CollectOCITargets(_ context.Context, configProvider dependency.PullConfigResolver) ([]dependency.OCIPullTarget, error) {
	spec, err := client.ParseComposeSpecFromDir(e.rw, e.EmbeddedPath())
	if err != nil {
		return nil, fmt.Errorf("parsing compose spec: %w", err)
	}
	var targets []dependency.OCIPullTarget
	for _, svc := range spec.Services {
		if svc.Image != "" {
			targets = append(targets, dependency.OCIPullTarget{
				Type:         dependency.OCITypePodmanImage,
				Reference:    svc.Image,
				PullPolicy:   v1beta1.PullIfNotPresent,
				ClientOptsFn: containerPullOptions(configProvider),
			})
		}
	}
	return targets, nil
}
