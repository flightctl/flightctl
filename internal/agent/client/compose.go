package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/util/validation"
	"sigs.k8s.io/yaml"
)

const defaultPodmanTimeout = 2 * time.Minute

type ComposeSpec struct {
	Services map[string]ComposeService `json:"services"`
}

type ComposeService struct {
	Image         string `json:"image"`
	ContainerName string `json:"container_name"`
}

type Compose struct {
	*Podman
}

// UpFromWorkDir runs `docker-compose up -d` or `podman-compose up -d` from the
// given workDir. The last argument is a flag to prevent recreation of existing
// containers.
func (p *Compose) UpFromWorkDir(ctx context.Context, workDir, projectName string, noRecreate bool) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{
		"compose",
		"-p",
		projectName,
		"up",
		"-d",
	}

	if noRecreate {
		args = append(args, "--no-recreate")
	}

	_, stderr, exitCode := p.exec.ExecuteWithContextFromDir(ctx, workDir, podmanCmd, args)
	if exitCode != 0 {
		return fmt.Errorf("podman compose up: %w", errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (p *Compose) Up(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{
		"-f",
		path,
		"up",
		"-d",
	}
	_, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("podman compose up: %w", errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (p *Compose) Down(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{
		"-f",
		path,
		"down",
	}
	_, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode == 0 {
		return nil
	}
	_, err := os.Stat(path)
	if !os.IsNotExist(err) {
		return fmt.Errorf("podman-compose down: %w", errors.FromStderr(stderr, exitCode))
	}
	psStdout, psStderr, psExitCode := p.exec.ExecuteWithContext(ctx, "podman", "ps", "-a", "--format=json")
	if psExitCode != 0 {
		p.log.Errorf("podman ps --all failed: %s", psStderr)
		return fmt.Errorf("podman compose down: %w", errors.FromStderr(stderr, exitCode))
	}
	type psRecord struct {
		Labels map[string]string `json:"Labels"`
	}
	var psRecords []psRecord
	if err = json.Unmarshal([]byte(psStdout), &psRecords); err != nil {
		p.log.WithError(err).Errorf("json unmarshal failed:")
		return fmt.Errorf("podman-compose down failed for path %s with exit code %d: %s", path, exitCode, stderr)
	}

	for _, p := range psRecords {
		if p.Labels != nil && p.Labels["com.docker.compose.project.config_files"] == path {
			return fmt.Errorf("podman-compose down failed for path %s but container created by compose file exists: %s", path, stderr)
		}
	}
	return nil
}

// ParseComposeSpecFromDir reads a compose spec from the given directory and will perform a merge of the base {docker,podman}-compose.yaml and -override.yaml files.
func ParseComposeSpecFromDir(reader fileio.Reader, dir string) (*ComposeSpec, error) {
	// check for docker-compose.yaml or podman-compose.yaml and override files
	//
	// Note: podman nor docker handle merge files from other packages for
	// example podman-compose and docker-compose.override.yaml. for now we will
	// do the same but I will leave this note here for further consideration.
	baseFiles := []string{
		"docker-compose.yaml",
		"docker-compose.yml",
		"podman-compose.yaml",
		"podman-compose.yml",
	}
	overrideFiles := []string{
		"docker-compose.override.yaml",
		"docker-compose.override.yml",
		"podman-compose.override.yaml",
		"podman-compose.override.yml",
	}

	spec := &ComposeSpec{Services: make(map[string]ComposeService)}

	// ensure base
	found, err := readFirstExistingFile(baseFiles, dir, reader, spec)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.ErrNoComposeFile
	}

	// merge override
	_, err = readFirstExistingFile(overrideFiles, dir, reader, spec)
	if err != nil {
		return nil, err
	}

	if len(spec.Services) == 0 {
		return nil, errors.ErrNoComposeServices
	}

	return spec, nil
}

func readFirstExistingFile(files []string, dir string, reader fileio.Reader, spec *ComposeSpec) (bool, error) {
	for _, filename := range files {
		filePath := filepath.Join(dir, filename)

		exists, err := reader.PathExists(filePath)
		if err != nil {
			return false, fmt.Errorf("checking if file exists: %w", err)
		}
		if !exists {
			continue
		}

		// merge file into spec
		if err := mergeFileIntoSpec(filePath, reader, spec); err != nil {
			return false, err
		}
		return true, nil
	}

	// no files found
	return false, nil
}

// mergeFileIntoSpec serializes the compose file at filePath and merges it into the spec.
func mergeFileIntoSpec(filePath string, reader fileio.Reader, spec *ComposeSpec) error {
	content, err := reader.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("reading compose file %s: %w", filePath, err)
	}

	var partial ComposeSpec
	if err := yaml.Unmarshal(content, &partial); err != nil {
		return fmt.Errorf("unmarshaling compose YAML from %s: %w", filePath, err)
	}

	for name, svc := range partial.Services {
		spec.Services[name] = svc
	}
	return nil
}

// Verify validates the compose spec.
func (c *ComposeSpec) Verify() error {
	var errs []error
	for name, service := range c.Services {
		containerName := service.ContainerName
		if service.ContainerName != "" {
			errs = append(errs, fmt.Errorf("service %s has a hard coded container_name %s which is not supported", name, containerName))
		}
		image := service.Image
		if image == "" {
			errs = append(errs, fmt.Errorf("service %s is missing an image", name))
		}
		if err := validation.ValidateOciImageReference(&image, "services."+name+".image"); err != nil {
			errs = append(errs, err...)
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

// Images returns a map of service names to their images.
func (c *ComposeSpec) Images() map[string]string {
	images := make(map[string]string)
	for name, service := range c.Services {
		images[name] = service.Image
	}
	return images
}
