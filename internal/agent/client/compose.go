package client

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/flightctl/flightctl/internal/util/validation"
)

const (
	ComposeOverrideFilename = "99-compose-flightctl-agent.override.yaml"
	defaultPodmanTimeout    = 10 * time.Minute
)

var (
	BaseComposeFiles = []string{
		"docker-compose.yaml",
		"docker-compose.yml",
		"podman-compose.yaml",
		"podman-compose.yml",
	}

	OverrideComposeFiles = []string{
		"docker-compose.override.yaml",
		"docker-compose.override.yml",
		"podman-compose.override.yaml",
		"podman-compose.override.yml",
	}
)

type Compose struct {
	*Podman
}

// UpFromWorkDir runs `podman compose up -d` from the given workDir using Compose file layering.
//
// It searches for Compose files in the following order:
//  1. One base file (required), chosen from BaseComposeFiles.
//  2. One standard override file (optional), chosen from OverrideComposeFiles.
//  3. An optional flightctl override file (ComposeOverrideFilename) if present.
//
// The method builds the final compose command by layering the discovered files in order.
// The noRecreate flag, if true, adds `--no-recreate` to prevent recreating existing containers.
func (p *Compose) UpFromWorkDir(ctx context.Context, workDir, projectName string, noRecreate bool) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"compose", "-p", projectName}

	// base compose file is required
	baseFound := false
	for _, file := range BaseComposeFiles {
		path := filepath.Join(workDir, file)
		found, err := p.readWriter.PathExists(path)
		if err != nil {
			return fmt.Errorf("checking base compose file %q existence: %w", path, err)
		}
		if found {
			args = append(args, "-f", file)
			baseFound = true
			break
		}
	}
	if !baseFound {
		return fmt.Errorf("no base compose file found in: %s", workDir)
	}

	// check for override (optional)
	for _, file := range OverrideComposeFiles {
		path := filepath.Join(workDir, file)
		found, err := p.readWriter.PathExists(path)
		if err != nil {
			return fmt.Errorf("checking override compose file %q existence: %w", path, err)
		}
		if found {
			args = append(args, "-f", file)
			break
		}
	}

	// check for agent override file (optional)
	flightctlPath := filepath.Join(workDir, ComposeOverrideFilename)
	found, err := p.readWriter.PathExists(flightctlPath)
	if err != nil {
		return fmt.Errorf("checking flightctl override file %q existence: %w", flightctlPath, err)
	}
	if found {
		args = append(args, "-f", ComposeOverrideFilename)
	}

	args = append(args, "up", "-d")
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
func ParseComposeSpecFromDir(reader fileio.Reader, dir string) (*common.ComposeSpec, error) {
	spec := &common.ComposeSpec{
		Services: make(map[string]common.ComposeService),
		Volumes:  make(map[string]common.ComposeVolume),
	}

	// ensure base
	found, err := readFirstExistingFile(BaseComposeFiles, dir, reader, spec)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%w found in: %s supported file names: %s", errors.ErrNoComposeFile, dir, strings.Join(BaseComposeFiles, ", "))
	}

	// merge override
	_, err = readFirstExistingFile(OverrideComposeFiles, dir, reader, spec)
	if err != nil {
		return nil, err
	}

	if len(spec.Services) == 0 {
		return nil, errors.ErrNoComposeServices
	}

	return spec, nil
}

func readFirstExistingFile(files []string, dir string, reader fileio.Reader, spec *common.ComposeSpec) (bool, error) {
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
func mergeFileIntoSpec(filePath string, reader fileio.Reader, spec *common.ComposeSpec) error {
	content, err := reader.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("reading compose file %s: %w", filePath, err)
	}

	partial, err := common.ParseComposeSpec(content)
	if err != nil {
		return fmt.Errorf("parsing compose: %s: %w", filePath, err)
	}

	// merge services
	maps.Copy(spec.Services, partial.Services)

	// merge volumes
	maps.Copy(spec.Volumes, partial.Volumes)

	return nil
}

// NewComposeID generates a deterministic, lowercase, DNS-compatible ID with a fixed-length hash suffix.
func NewComposeID(input string) string {
	const suffixLength = 6
	id := SanitizePodmanLabel(input)
	hashValue := crc32.ChecksumIEEE([]byte(id))
	suffix := strconv.AppendUint(nil, uint64(hashValue), 10)
	maxLength := validation.DNS1123MaxLength - suffixLength - 1
	if len(id) > maxLength {
		id = id[:maxLength]
	}

	var builder strings.Builder
	builder.Grow(len(id) + 1 + suffixLength)

	builder.WriteString(id)
	builder.WriteByte('-')
	builder.WriteString(string(suffix[:suffixLength]))

	return builder.String()
}

// ComposeVolumeName generates a unique Compose-compatible volume name
// based on the application and volume names.
func ComposeVolumeName(appName, volumeName string) string {
	return NewComposeID(appName + "-" + volumeName)
}
