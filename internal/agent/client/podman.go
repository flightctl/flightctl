package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
)

const (
	podmanCmd              = "podman"
	defaultPullLogInterval = 30 * time.Second
)

// PodmanInspect represents the overall structure of podman inspect output
type PodmanInspect struct {
	Restarts int                   `json:"RestartCount"`
	State    PodmanContainerState  `json:"State"`
	Config   PodmanContainerConfig `json:"Config"`
}

// ContainerState represents the container state part of the podman inspect output
type PodmanContainerState struct {
	OciVersion  string `json:"OciVersion"`
	Status      string `json:"Status"`
	Running     bool   `json:"Running"`
	Paused      bool   `json:"Paused"`
	Restarting  bool   `json:"Restarting"`
	OOMKilled   bool   `json:"OOMKilled"`
	Dead        bool   `json:"Dead"`
	Pid         int    `json:"Pid"`
	ExitCode    int    `json:"ExitCode"`
	Error       string `json:"Error"`
	StartedAt   string `json:"StartedAt"`
	FinishedAt  string `json:"FinishedAt"`
	Healthcheck string `json:"Healthcheck"`
}

type PodmanContainerConfig struct {
	Labels map[string]string `json:"Labels"`
}

// ArtifactInspect represents the structure of artifact inspect output
type ArtifactInspect struct {
	Manifest ArtifactManifest `json:"Manifest"`
	Name     string           `json:"Name"`
	Digest   string           `json:"Digest"`
}

type ArtifactManifest struct {
	Layers []ArtifactLayer `json:"layers"`
}

type ArtifactLayer struct {
	Annotations map[string]string `json:"annotations,omitempty"`
}

// PodmanEvent represents the structure of a podman event as produced via a CLI events command.
// It should be noted that the CLI represents events differently from libpod. (notably the time properties)
// https://github.com/containers/podman/blob/main/cmd/podman/system/events.go#L81-L96
type PodmanEvent struct {
	ContainerExitCode int               `json:"ContainerExitCode,omitempty"`
	ID                string            `json:"ID"`
	Image             string            `json:"Image"`
	Name              string            `json:"Name"`
	Status            string            `json:"Status"`
	Type              string            `json:"Type"`
	TimeNano          int64             `json:"timeNano"`
	Attributes        map[string]string `json:"Attributes"`
}

type Podman struct {
	exec executer.Executer
	log  *log.PrefixLogger
	// timeout per client call
	timeout    time.Duration
	readWriter fileio.ReadWriter
	backoff    poll.Config
}

func NewPodman(log *log.PrefixLogger, exec executer.Executer, readWriter fileio.ReadWriter, backoff poll.Config) *Podman {
	return &Podman{
		log:        log,
		exec:       exec,
		timeout:    defaultPodmanTimeout,
		readWriter: readWriter,
		backoff:    backoff,
	}
}

// Pull pulls an image from the registry with optional retry and authentication via a pull secret.
// Logs progress periodically while the operation is in progress.
func (p *Podman) Pull(ctx context.Context, image string, opts ...ClientOption) (string, error) {
	options := &clientOptions{}
	for _, opt := range opts {
		opt(options)
	}

	return logProgress(ctx, p.log, "Pulling image, please wait...", func(ctx context.Context) (string, error) {
		return retryWithBackoff(ctx, p.log, p.backoff, func(ctx context.Context) (string, error) {
			return p.pullImage(ctx, image, options)
		})
	})
}

func (p *Podman) pullImage(ctx context.Context, image string, options *clientOptions) (string, error) {
	timeout := p.timeout
	if options.timeout > 0 {
		timeout = options.timeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	pullSecretPath := options.pullSecretPath
	args := []string{"pull", image}
	if pullSecretPath != "" {
		exists, err := p.readWriter.PathExists(pullSecretPath)
		if err != nil {
			return "", fmt.Errorf("check pull secret path: %w", err)
		}
		if !exists {
			p.log.Errorf("Pull secret path %s does not exist", pullSecretPath)
		} else {
			args = append(args, "--authfile", pullSecretPath)
		}
	}
	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("pull image: %w", errors.FromStderr(stderr, exitCode))
	}
	out := strings.TrimSpace(stdout)
	return out, nil
}

// PullArtifact pulls an artifact from the registry with optional retry and authentication via a pull secret.
// Logs progress periodically while the operation is in progress.
func (p *Podman) PullArtifact(ctx context.Context, artifact string, opts ...ClientOption) (string, error) {
	options := &clientOptions{}
	for _, opt := range opts {
		opt(options)
	}

	return logProgress(ctx, p.log, "Pulling artifact, please wait...", func(ctx context.Context) (string, error) {
		return retryWithBackoff(ctx, p.log, p.backoff, func(ctx context.Context) (string, error) {
			return p.pullArtifact(ctx, artifact, options)
		})
	})
}

func (p *Podman) pullArtifact(ctx context.Context, artifact string, options *clientOptions) (string, error) {
	timeout := p.timeout
	if options.timeout > 0 {
		timeout = options.timeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := p.EnsureArtifactSupport(ctx); err != nil {
		return "", err
	}

	pullSecretPath := options.pullSecretPath
	args := []string{"artifact", "pull", artifact}
	if pullSecretPath != "" {
		exists, err := p.readWriter.PathExists(pullSecretPath)
		if err != nil {
			return "", fmt.Errorf("check pull secret path: %w", err)
		}
		if !exists {
			p.log.Errorf("Pull secret path %s does not exist", pullSecretPath)
		} else {
			args = append(args, "--authfile", pullSecretPath)
		}
	}

	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("pull artifact: %w", errors.FromStderr(stderr, exitCode))
	}
	return strings.TrimSpace(stdout), nil
}

// ExtractArtifact to the given destination, which should be an already existing directory if the artifact contains multiple layers, otherwise podman will extract a single layer in the artifact to the destination path directly as a file.
//
// See
// https://github.com/opencontainers/image-spec/blob/main/manifest.md#guidelines-for-artifact-usage
// for details on the expected structure of artifacts. Regular images are considered artifacts by
// podman due to the intentional looseness of the spec.
func (p *Podman) ExtractArtifact(ctx context.Context, artifact, destination string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	if err := p.EnsureArtifactSupport(ctx); err != nil {
		return "", err
	}

	args := []string{"artifact", "extract", artifact, destination}
	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("artifact extract: %w", errors.FromStderr(stderr, exitCode))
	}
	out := strings.TrimSpace(stdout)
	return out, nil
}

// Inspect returns the JSON output of the image inspection. The expectation is
// that the image exists in local container storage.
func (p *Podman) Inspect(ctx context.Context, image string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"inspect", image}
	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("inspect image: %s: %w", image, errors.FromStderr(stderr, exitCode))
	}
	out := strings.TrimSpace(stdout)
	return out, nil
}

func (p *Podman) ImageExists(ctx context.Context, image string) bool {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"image", "exists", image}
	_, _, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	return exitCode == 0
}

// ImageDigest returns the digest of the specified image.
// Returns empty string and error if the image does not exist or cannot be inspected.
func (p *Podman) ImageDigest(ctx context.Context, image string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"image", "inspect", "--format", "{{.Digest}}", image}
	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("get image digest: %s: %w", image, errors.FromStderr(stderr, exitCode))
	}
	digest := strings.TrimSpace(stdout)
	return digest, nil
}

// ArtifactExists returns true if the artifact exists in storage otherwise false.
func (p *Podman) ArtifactExists(ctx context.Context, artifact string) bool {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"artifact", "inspect", artifact}
	_, _, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	return exitCode == 0
}

func (p *Podman) artifactInspect(ctx context.Context, reference string) (*ArtifactInspect, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	args := []string{"artifact", "inspect", reference}
	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return nil, fmt.Errorf("artifact inspect: %w", errors.FromStderr(stderr, exitCode))
	}

	var inspectResult ArtifactInspect
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &inspectResult); err != nil {
		return nil, fmt.Errorf("unmarshal artifact inspect output: %w", err)
	}

	return &inspectResult, nil
}

// ArtifactDigest returns the digest of the specified artifact.
func (p *Podman) ArtifactDigest(ctx context.Context, reference string) (string, error) {
	if err := p.EnsureArtifactSupport(ctx); err != nil {
		return "", err
	}

	inspect, err := p.artifactInspect(ctx, reference)
	if err != nil {
		return "", err
	}

	if inspect.Digest == "" {
		return "", fmt.Errorf("artifact digest empty for %s", reference)
	}

	return inspect.Digest, nil
}

// InspectArtifactAnnotations inspects an OCI artifact and returns its annotations map.
func (p *Podman) InspectArtifactAnnotations(ctx context.Context, reference string) (map[string]string, error) {
	if err := p.EnsureArtifactSupport(ctx); err != nil {
		return nil, err
	}

	inspect, err := p.artifactInspect(ctx, reference)
	if err != nil {
		return nil, err
	}

	return extractArtifactAnnotations(inspect), nil
}

// extractArtifactAnnotations parses the podman artifact inspect JSON output and extracts annotations
func extractArtifactAnnotations(inspect *ArtifactInspect) map[string]string {
	// Merge annotations from all layers in the manifest
	annotations := make(map[string]string)
	for _, layer := range inspect.Manifest.Layers {
		for key, value := range layer.Annotations {
			annotations[key] = value
		}
	}

	return annotations
}

// EventsSinceCmd returns a command to get podman events since the given time. After creating the command, it should be started with exec.Start().
// When the events are in sync with the current time a sync event is emitted.
func (p *Podman) EventsSinceCmd(ctx context.Context, events []string, sinceTime string) *exec.Cmd {
	args := []string{"events", "--format", "json", "--since", sinceTime}
	for _, event := range events {
		args = append(args, "--filter", fmt.Sprintf("event=%s", event))
	}

	return p.exec.CommandContext(ctx, podmanCmd, args...)
}

func (p *Podman) Mount(ctx context.Context, image string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{
		"image",
		"mount",
		image,
	}
	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("mount image: %s: %w", image, errors.FromStderr(stderr, exitCode))
	}

	out := strings.TrimSpace(stdout)
	return out, nil
}

func (p *Podman) Unmount(ctx context.Context, image string) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{
		"image",
		"unmount",
		image,
	}
	_, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("unmount image: %s: %w", image, errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (p *Podman) Copy(ctx context.Context, src, dst string) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"cp", src, dst}
	_, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("copy %s to %s: %w", src, dst, errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (p *Podman) InspectLabels(ctx context.Context, image string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	resp, err := p.Inspect(ctx, image)
	if err != nil {
		return nil, err
	}

	var inspectData []PodmanInspect
	if err := json.Unmarshal([]byte(resp), &inspectData); err != nil {
		return nil, fmt.Errorf("parse image inspect response: %w", err)
	}

	if len(inspectData) == 0 {
		return nil, fmt.Errorf("no image config found")
	}

	return inspectData[0].Config.Labels, nil
}

func (p *Podman) StopContainers(ctx context.Context, labels []string) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"stop"}
	for _, label := range labels {
		args = append(args, "--filter", fmt.Sprintf("label=%s", label))
	}
	_, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("stop containers: %w", errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (p *Podman) RemoveContainer(ctx context.Context, labels []string) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"rm"}
	for _, label := range labels {
		args = append(args, "--filter", fmt.Sprintf("label=%s", label))
	}
	_, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("remove containers: %w", errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (p *Podman) CreateVolume(ctx context.Context, name string, labels []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"volume", "create", name}
	for _, label := range labels {
		args = append(args, "--label", label)
	}

	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("create volume: %s: %w", strings.TrimSpace(stdout), errors.FromStderr(stderr, exitCode))
	}

	inspectArgs := []string{"volume", "inspect", name, "--format", "{{.Mountpoint}}"}
	mountpointOut, inspectStderr, inspectExit := p.exec.ExecuteWithContext(ctx, podmanCmd, inspectArgs...)
	if inspectExit != 0 {
		return "", fmt.Errorf("inspect volume mountpoint: %w", errors.FromStderr(inspectStderr, inspectExit))
	}

	mountpoint := strings.TrimSpace(mountpointOut)

	return mountpoint, nil
}

type podmanVolume struct {
	Name string `json:"Name"`
}

func (p *Podman) ListVolumes(ctx context.Context, labels []string, filters []string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{
		"volume",
		"ls",
		"--format",
		"json",
	}
	args = applyFilters(args, labels, filters)
	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return nil, fmt.Errorf("list volumes: %w", errors.FromStderr(stderr, exitCode))
	}
	var podVols []podmanVolume
	err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &podVols)
	if err != nil {
		return nil, fmt.Errorf("unmarshal volumes: %w", err)
	}
	volumesSeen := make(map[string]struct{})
	volumes := make([]string, 0, len(podVols))
	for _, volume := range podVols {
		if _, ok := volumesSeen[volume.Name]; !ok {
			volumesSeen[volume.Name] = struct{}{}
			volumes = append(volumes, volume.Name)
		}
	}
	return volumes, nil
}

func (p *Podman) VolumeExists(ctx context.Context, name string) bool {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"volume", "exists", name}
	_, _, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	return exitCode == 0
}

func (p *Podman) inspectVolumeProperty(ctx context.Context, name string, property string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"volume", "inspect", name, "--format", property}
	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("inspect volume property %s: %w", property, errors.FromStderr(stderr, exitCode))
	}

	return strings.TrimSpace(stdout), nil
}

func (p *Podman) InspectVolumeDriver(ctx context.Context, name string) (string, error) {
	return p.inspectVolumeProperty(ctx, name, "{{.Driver}}")
}

func (p *Podman) InspectVolumeMount(ctx context.Context, name string) (string, error) {
	return p.inspectVolumeProperty(ctx, name, "{{.Mountpoint}}")
}

func (p *Podman) RemoveVolumes(ctx context.Context, volumes ...string) error {
	for _, volume := range volumes {
		nctx, cancel := context.WithTimeout(ctx, p.timeout)
		args := []string{"volume", "rm", volume}
		_, stderr, exitCode := p.exec.ExecuteWithContext(nctx, podmanCmd, args...)
		cancel()
		if exitCode != 0 {
			return fmt.Errorf("remove volumes: %w", errors.FromStderr(stderr, exitCode))
		}
		p.log.Infof("Removed volume %s", volume)
	}
	return nil
}

func applyFilters(args, labels, filters []string) []string {
	for _, label := range labels {
		args = append(args, "--filter", fmt.Sprintf("label=%s", label))
	}

	for _, filter := range filters {
		args = append(args, "--filter", filter)
	}
	return args
}

func (p *Podman) ListNetworks(ctx context.Context, labels []string, filters []string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{
		"network",
		"ls",
		"--format",
		"{{.Network.ID}}",
	}
	args = applyFilters(args, labels, filters)

	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return nil, fmt.Errorf("list networks: %w", errors.FromStderr(stderr, exitCode))
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	networkSeen := make(map[string]struct{})
	for _, line := range lines {
		// handle multiple networks comma separated
		networks := strings.Split(line, ",")
		for _, network := range networks {
			network = strings.TrimSpace(network)
			if network != "" {
				networkSeen[network] = struct{}{}
			}
		}
	}

	var networks []string
	for network := range networkSeen {
		networks = append(networks, network)
	}
	return networks, nil
}

func (p *Podman) RemoveNetworks(ctx context.Context, networks ...string) error {
	for _, network := range networks {
		nctx, cancel := context.WithTimeout(ctx, p.timeout)
		args := []string{"network", "rm", network}
		_, stderr, exitCode := p.exec.ExecuteWithContext(nctx, podmanCmd, args...)
		cancel()
		if exitCode != 0 {
			return fmt.Errorf("remove networks: %w", errors.FromStderr(stderr, exitCode))
		}
		p.log.Infof("Removed network %s", network)
	}
	return nil
}

func (p *Podman) ListPods(ctx context.Context, labels []string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	// pods created by podman-compose don't have the compose project label,
	// so we need to get pod IDs from the containers that do have the label
	args := []string{
		"ps",
		"-a",
		"--format",
		"{{.Pod}}",
	}
	args = applyFilters(args, labels, []string{})

	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return nil, fmt.Errorf("list pods: %w", errors.FromStderr(stderr, exitCode))
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	podSeen := make(map[string]struct{})
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// skip empty lines and containers not in a pod
		if line == "" || line == "--" {
			continue
		}
		podSeen[line] = struct{}{}
	}

	var pods []string
	for pod := range podSeen {
		pods = append(pods, pod)
	}
	return pods, nil
}

func (p *Podman) RemovePods(ctx context.Context, pods ...string) error {
	for _, pod := range pods {
		nctx, cancel := context.WithTimeout(ctx, p.timeout)
		args := []string{"pod", "rm", pod}
		_, stderr, exitCode := p.exec.ExecuteWithContext(nctx, podmanCmd, args...)
		cancel()
		if exitCode != 0 {
			return fmt.Errorf("remove pods: %w", errors.FromStderr(stderr, exitCode))
		}
		p.log.Infof("Removed pod %s", pod)
	}
	return nil
}

func (p *Podman) Unshare(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args = append([]string{"unshare"}, args...)
	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("unshare: %w", errors.FromStderr(stderr, exitCode))
	}
	out := strings.TrimSpace(stdout)
	return out, nil
}

func (p *Podman) CopyContainerData(ctx context.Context, image, destPath string) error {
	return copyContainerData(ctx, p.log, p.readWriter, p, image, destPath)
}

func (p *Podman) Compose() *Compose {
	return &Compose{
		Podman: p,
	}
}

type PodmanVersion struct {
	Major int
	Minor int
}

// EnsureArtifactSupport verifies the local podman version can execute artifact commands.
func (p *Podman) EnsureArtifactSupport(ctx context.Context) error {
	version, err := p.Version(ctx)
	if err != nil {
		return fmt.Errorf("%w: checking podman version: %w", errors.ErrNoRetry, err)
	}
	if !version.GreaterOrEqual(5, 5) {
		return fmt.Errorf("%w: OCI artifact operations require podman >= 5.5, found %d.%d", errors.ErrNoRetry, version.Major, version.Minor)
	}
	return nil
}

func (v PodmanVersion) GreaterOrEqual(major, minor int) bool {
	if v.Major > major {
		return true
	}
	if v.Major == major && v.Minor >= minor {
		return true
	}
	return false
}

// Version returns the major and monor versions of podman.
func (p *Podman) Version(ctx context.Context) (*PodmanVersion, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"--version"} // podman version --format "{{.Version}}" has some unexpectecd failure cases in testing
	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return nil, fmt.Errorf("podman --version: %w", errors.FromStderr(stderr, exitCode))
	}

	// Example: "podman version 5.4.2"
	fields := strings.Fields(stdout)
	if len(fields) < 3 {
		return nil, fmt.Errorf("unexpected podman version output: %q", stdout)
	}

	versionStr := fields[len(fields)-1]
	parts := strings.SplitN(versionStr, ".", 3)

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, fmt.Errorf("parse major version: %w", err)
	}

	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, fmt.Errorf("parse minor version: %w", err)
	}

	return &PodmanVersion{Major: major, Minor: minor}, nil
}

// GetImageCopyTmpDir returns the image copy tmp dir exposed by the podman info API.
func (p *Podman) GetImageCopyTmpDir(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	args := []string{"info", "--format", "{{.Store.ImageCopyTmpDir}}"}
	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, podmanCmd, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("get image copy tmpdir: %w", errors.FromStderr(stderr, exitCode))
	}

	tmpDir := strings.TrimSpace(stdout)
	return tmpDir, nil
}

func IsPodmanRootless() bool {
	return os.Geteuid() != 0
}

func copyContainerData(ctx context.Context, log *log.PrefixLogger, writer fileio.Writer, podman *Podman, image, destPath string) (err error) {
	var mountPoint string

	rootless := IsPodmanRootless()
	if rootless {
		log.Warnf("Running in rootless mode this is for testing only")
		mountPoint, err = podman.Unshare(ctx, "podman", "image", "mount", image)
		if err != nil {
			return fmt.Errorf("failed to execute podman share: %w", err)
		}
	} else {
		mountPoint, err = podman.Mount(ctx, image)
		if err != nil {
			return fmt.Errorf("failed to mount image: %w", err)
		}
	}

	if err := writer.MkdirAll(destPath, fileio.DefaultDirectoryPermissions); err != nil {
		return fmt.Errorf("failed to dest create directory: %w", err)
	}

	defer func() {
		if err := podman.Unmount(ctx, image); err != nil {
			log.Errorf("failed to unmount image: %s %v", image, err)
		}
	}()

	// recursively copy image files to agent destination
	if err := copyData(ctx, log, writer, mountPoint, destPath); err != nil {
		return fmt.Errorf("error during copy: %w", err)
	}

	return nil
}

func copyData(ctx context.Context, log *log.PrefixLogger, writer fileio.Writer, srcRoot, destRoot string) error {
	walkRoot := writer.PathFor(srcRoot)
	return filepath.Walk(walkRoot, func(walkedSrc string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		relPath, err := filepath.Rel(walkRoot, walkedSrc)
		if err != nil {
			return fmt.Errorf("computing relative path: %w", err)
		}

		realSrc := filepath.Join(srcRoot, relPath)
		relDest := filepath.Join(destRoot, relPath)

		if info.IsDir() {
			if info.Name() == "merged" {
				log.Tracef("Skipping merged directory: %s", walkedSrc)
				return nil
			}

			// create the directory in the destination
			log.Tracef("Creating directory: %s", relDest)

			// ensure any directories in the image are also created
			return writer.MkdirAll(relDest, fileio.DefaultDirectoryPermissions)
		}

		log.Tracef("Copying file from %s to %s", realSrc, relDest)
		return writer.CopyFile(realSrc, relDest)
	})
}

// SanitizePodmanLabel sanitizes a string to be used as a label in Podman.
// Podman labels must be lowercase and can only contain alpha numeric
// characters, hyphens, and underscores. Any other characters are replaced with
// an underscore.
func SanitizePodmanLabel(name string) string {
	var result strings.Builder
	result.Grow(len(name))

	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		// lower case alpha numeric characters, hyphen, and underscore are allowed
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_':
			result.WriteByte(c)
		// upper case alpha characters are converted to lower case
		case c >= 'A' && c <= 'Z':
			// add 32 to ascii value convert to lower case
			result.WriteByte(c + 32)
		// any special characters are replaced with an underscore
		default:
			result.WriteByte('_')
		}
	}

	return result.String()
}

func retryWithBackoff(ctx context.Context, log *log.PrefixLogger, backoff poll.Config, operation func(context.Context) (string, error)) (string, error) {
	var result string
	var retriableErr error
	err := poll.BackoffWithContext(ctx, backoff, func(ctx context.Context) (bool, error) {
		var err error
		retriableErr = nil
		result, err = operation(ctx)
		if err != nil {
			if !errors.IsRetryable(err) {
				log.Error(err)
				return false, err
			}
			retriableErr = err
			log.Warnf("A retriable error occurred: %s", err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		if retriableErr != nil {
			err = fmt.Errorf("%w: %w", retriableErr, err)
		}
		return "", err
	}
	return result, nil
}

func logProgress(ctx context.Context, log *log.PrefixLogger, msg string, fn func(ctx context.Context) (string, error)) (string, error) {
	doneCh := make(chan struct{})
	defer close(doneCh)

	startTime := time.Now()
	go func() {
		ticker := time.NewTicker(defaultPullLogInterval)
		defer ticker.Stop()

		for {
			select {
			case <-doneCh:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				elapsed := time.Since(startTime)
				log.Infof("%s (elapsed: %v)", msg, elapsed)
			}
		}
	}()

	return fn(ctx)
}
