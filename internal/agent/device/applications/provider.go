package applications

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
)

type ApplicationSpec struct {
	// Name of the application
	Name string
	// ID of the application
	ID string
	// Type of the application
	AppType v1alpha1.AppType
	// Path to the application
	Path string
	// EnvVars are the environment variables to be passed to the application
	EnvVars map[string]string
	// Embedded is true if the application is embedded in the device
	Embedded bool
	// ImageProviderr is the spec for the image provider
	ImageProvider *v1alpha1.ImageApplicationProviderSpec
	// InlineProvider is the spec for the inline provider
	InlineProvider *v1alpha1.InlineApplicationProviderSpec
}

type ImageProvider struct {
	podman     *client.Podman
	readWriter fileio.ReadWriter
	log        *log.PrefixLogger
	spec       *ApplicationSpec
}

func NewImageProvider(log *log.PrefixLogger, podman *client.Podman, spec *v1alpha1.ApplicationProviderSpec, readWriter fileio.ReadWriter) (*ImageProvider, error) {
	provider, err := spec.AsImageApplicationProviderSpec()
	if err != nil {
		return nil, fmt.Errorf("getting provider spec:%w", err)
	}

	// set the app name to the image name if not provided
	appName := lo.FromPtr(spec.Name)
	if appName == "" {
		appName = provider.Image
	}
	embedded := false
	path, err := pathFromAppType(v1alpha1.AppTypeCompose, appName, embedded)
	if err != nil {
		return nil, fmt.Errorf("getting app path: %w", err)
	}

	return &ImageProvider{
		log:        log,
		podman:     podman,
		readWriter: readWriter,
		spec: &ApplicationSpec{
			Name:          appName,
			AppType:       lo.FromPtr(spec.AppType),
			Path:          path,
			EnvVars:       lo.FromPtr(spec.EnvVars),
			Embedded:      embedded,
			ImageProvider: &provider,
		},
	}, nil
}

func (p *ImageProvider) Verify(ctx context.Context) error {
	image := p.spec.ImageProvider.Image
	if err := ensureImageExists(ctx, p.log, p.podman, image); err != nil {
		return fmt.Errorf("pulling image: %w", err)
	}

	// type declared in the spec overrides the type from the image
	if p.spec.AppType == "" {
		appType, err := typeFromImage(ctx, p.podman, image)
		if err != nil {
			return fmt.Errorf("getting app type: %w", err)
		}
		p.spec.AppType = appType
	}

	if err := ensureDependenciesFromType(p.spec.AppType); err != nil {
		return fmt.Errorf("%w: ensuring dependencies: %w", errors.ErrNoRetry, err)
	}

	// create a temporary directory to copy the image contents
	tmpAppPath, err := p.readWriter.MkdirTemp("app_temp")
	if err != nil {
		return fmt.Errorf("creating tmp dir: %w", err)
	}

	cleanup := func() {
		if err := p.readWriter.RemoveAll(tmpAppPath); err != nil {
			p.log.Errorf("Cleaning up temporary directory %q: %v", tmpAppPath, err)
		}
	}
	defer cleanup()

	// copy image contents to a tmp directory for further processing
	if err := p.podman.CopyContainerData(ctx, image, tmpAppPath); err != nil {
		return fmt.Errorf("copy image contents: %w", err)
	}

	switch p.spec.AppType {
	case v1alpha1.AppTypeCompose:
		p.spec.ID = newComposeID(p.spec.Name)
		path, err := pathFromAppType(p.spec.AppType, p.spec.Name, p.spec.Embedded)
		if err != nil {
			return fmt.Errorf("getting app path: %w", err)
		}
		p.spec.Path = path

		// ensure the compose application content in tmp dir is valid
		if err := ensureCompose(ctx, p.log, p.podman, p.readWriter, tmpAppPath); err != nil {
			return fmt.Errorf("ensuring compose: %w", err)
		}
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, p.spec.AppType)
	}

	return nil
}

func (p *ImageProvider) Install(ctx context.Context) error {
	if p.spec.ImageProvider == nil {
		return fmt.Errorf("image application spec is nil")
	}

	if err := p.podman.CopyContainerData(ctx, p.spec.ImageProvider.Image, p.spec.Path); err != nil {
		return fmt.Errorf("copy image contents: %w", err)
	}

	if err := writeENVFile(p.spec.Path, p.readWriter, p.spec.EnvVars); err != nil {
		return fmt.Errorf("writing env file: %w", err)
	}

	return nil
}

func (p *ImageProvider) Remove(ctx context.Context) error {
	if err := p.readWriter.RemoveAll(p.spec.Path); err != nil {
		return fmt.Errorf("removing application: %w", err)
	}
	return nil
}

func (p *ImageProvider) Name() string {
	return p.spec.Name
}

func (p *ImageProvider) Spec() *ApplicationSpec {
	return p.spec
}

type InlineProvider struct {
	podman     *client.Podman
	readWriter fileio.ReadWriter
	log        *log.PrefixLogger
	spec       *ApplicationSpec
}

func NewInlineProvider(log *log.PrefixLogger, podman *client.Podman, spec *v1alpha1.ApplicationProviderSpec, readWriter fileio.ReadWriter) (*InlineProvider, error) {
	provider, err := spec.AsInlineApplicationProviderSpec()
	if err != nil {
		return nil, fmt.Errorf("getting provider spec:%w", err)
	}

	p := &InlineProvider{
		log:        log,
		podman:     podman,
		readWriter: readWriter,
		spec: &ApplicationSpec{
			Name:           lo.FromPtr(spec.Name),
			AppType:        lo.FromPtr(spec.AppType),
			EnvVars:        lo.FromPtr(spec.EnvVars),
			Embedded:       false,
			InlineProvider: &provider,
		},
	}

	path, err := pathFromAppType(p.spec.AppType, p.spec.Name, p.spec.Embedded)
	if err != nil {
		return nil, fmt.Errorf("getting app path: %w", err)
	}

	p.spec.Path = path
	p.spec.ID = newComposeID(p.spec.Name)

	return p, nil

}

func (p *InlineProvider) Verify(ctx context.Context) error {
	if err := ensureDependenciesFromType(p.spec.AppType); err != nil {
		return fmt.Errorf("%w: ensuring dependencies: %w", errors.ErrNoRetry, err)
	}
	// create a temporary directory to copy the image contents
	tmpAppPath, err := p.readWriter.MkdirTemp("app_temp")
	if err != nil {
		return fmt.Errorf("creating tmp dir: %w", err)
	}

	cleanup := func() {
		if err := p.readWriter.RemoveAll(tmpAppPath); err != nil {
			p.log.Errorf("Cleaning up temporary directory %q: %v", tmpAppPath, err)
		}
	}
	defer cleanup()

	// copy image contents to a tmp directory for further processing
	if err := p.writeInlineContent(tmpAppPath, p.spec.InlineProvider.Inline); err != nil {
		return err
	}

	switch p.spec.AppType {
	case v1alpha1.AppTypeCompose:
		if err := ensureCompose(ctx, p.log, p.podman, p.readWriter, tmpAppPath); err != nil {
			return fmt.Errorf("ensuring compose: %w", err)
		}
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, p.spec.AppType)
	}
	return nil
}

func (p *InlineProvider) Install(ctx context.Context) error {
	if err := p.writeInlineContent(p.spec.Path, p.spec.InlineProvider.Inline); err != nil {
		return err
	}

	if err := writeENVFile(p.spec.Path, p.readWriter, p.spec.EnvVars); err != nil {
		return fmt.Errorf("writing env file: %w", err)
	}

	return nil
}

func (p *InlineProvider) writeInlineContent(appPath string, contents []v1alpha1.ApplicationContent) error {
	if err := p.readWriter.MkdirAll(appPath, fileio.DefaultDirectoryPermissions); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}
	for _, content := range contents {
		contentBytes, err := fileio.DecodeContent(lo.FromPtr(content.Content), content.ContentEncoding)
		if err != nil {
			return fmt.Errorf("decoding application content: %w", err)
		}
		if err := p.readWriter.WriteFile(filepath.Join(appPath, content.Path), contentBytes, fileio.DefaultFilePermissions); err != nil {
			return fmt.Errorf("writing application content: %w", err)
		}
	}
	return nil
}

func (p *InlineProvider) Remove(ctx context.Context) error {
	if err := p.readWriter.RemoveAll(p.spec.Path); err != nil {
		return fmt.Errorf("removing application: %w", err)
	}
	return nil
}

func (p *InlineProvider) Name() string {
	return p.spec.Name
}

func (p *InlineProvider) Spec() *ApplicationSpec {
	return p.spec
}

type EmbeddedProvider struct {
	podman     *client.Podman
	readWriter fileio.ReadWriter
	log        *log.PrefixLogger
	spec       *ApplicationSpec
}

func NewEmbeddedProvider(log *log.PrefixLogger, podman *client.Podman, readWriter fileio.ReadWriter, name string, appType v1alpha1.AppType) (Provider, error) {
	return &EmbeddedProvider{
		log:        log,
		podman:     podman,
		readWriter: readWriter,
		spec: &ApplicationSpec{
			Name:     name,
			AppType:  appType,
			Embedded: true,
			EnvVars:  make(map[string]string),
		},
	}, nil
}

func (p *EmbeddedProvider) Verify(ctx context.Context) error {
	appPath, err := pathFromAppType(p.spec.AppType, p.spec.Name, p.spec.Embedded)
	if err != nil {
		return fmt.Errorf("getting app path: %w", err)
	}
	switch p.spec.AppType {
	case v1alpha1.AppTypeCompose:
		p.spec.ID = newComposeID(p.spec.Name)
		p.spec.Path = appPath
		if err := ensureCompose(ctx, p.log, p.podman, p.readWriter, appPath); err != nil {
			return fmt.Errorf("ensuring compose: %w", err)
		}
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, p.spec.AppType)
	}
	return nil
}
func (e *EmbeddedProvider) Name() string {
	return e.spec.Name
}

func (e *EmbeddedProvider) Spec() *ApplicationSpec {
	return e.spec
}

func (e *EmbeddedProvider) Install(ctx context.Context) error {
	return nil
}

func (e *EmbeddedProvider) Remove(ctx context.Context) error {
	return nil
}

func pathFromAppType(appType v1alpha1.AppType, name string, embedded bool) (string, error) {
	var typePath string
	switch appType {
	case v1alpha1.AppTypeCompose:
		if embedded {
			typePath = lifecycle.EmbeddedComposeAppPath
			break
		}
		typePath = lifecycle.ComposeAppPath
	default:
		return "", fmt.Errorf("unsupported application type: %s", appType)
	}
	return filepath.Join(typePath, name), nil
}

func ensureCompose(ctx context.Context, log *log.PrefixLogger, podman *client.Podman, readWriter fileio.ReadWriter, appPath string) error {
	// note: errors like "error converting YAML to JSON: yaml: line 5: found
	// character that cannot start any token" is often improperly formatted yaml
	// (double check the yaml spacing)
	spec, err := client.ParseComposeSpecFromDir(readWriter, appPath)
	if err != nil {
		return fmt.Errorf("parsing compose spec: %w", err)
	}

	if err := spec.Verify(); err != nil {
		return fmt.Errorf("validating compose spec: %w", err)
	}

	for _, image := range spec.Images() {
		if err := ensureImageExists(ctx, log, podman, image); err != nil {
			return fmt.Errorf("pulling service image: %w", err)
		}
	}
	return nil
}

// writeENVFile writes the environment variables to a .env file in the appPath
func writeENVFile(appPath string, writer fileio.Writer, envVars map[string]string) error {
	if len(envVars) > 0 {
		var env strings.Builder
		for k, v := range envVars {
			env.WriteString(fmt.Sprintf("%s=%s\n", k, v))
		}
		envPath := fmt.Sprintf("%s/.env", appPath)
		if err := writer.WriteFile(envPath, []byte(env.String()), fileio.DefaultFilePermissions); err != nil {
			return err
		}
	}
	return nil
}
