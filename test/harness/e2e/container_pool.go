package e2e

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/harness/containers"
	"github.com/flightctl/flightctl/test/harness/e2e/vm"
	"github.com/flightctl/flightctl/test/util"
)

// registryHostPort/privateRegistryHostPort mirror the fixed ports the e2e local registry always
// binds to (see test/e2e/infra/auxiliary/registry.go's registryHostPort/privateRegistryHostPort
// constants - not importable here without an e2e -> auxiliary dependency, so duplicated).
const (
	registryHostPort        = "5000"
	privateRegistryHostPort = "5002"
	registryHostname        = "e2e-registry" // mirrors REGISTRY_HOSTNAME in inject_agent_files_into_qcow.sh

	// These source repositories mirror the remaps in
	// test/scripts/inject_agent_files_into_qcow.sh. Auxiliary setup copies flightctl-tests fixtures
	// into the local registry, so container-backed devices use the same local endpoints as VMs.
	containerSourceRepo     = "quay.io/flightctl"
	containerTestSourceRepo = "quay.io/flightctl-tests"

	// defaultContainerDeviceImageRepoPath mirrors test/scripts/agent-images/scripts/build.sh's
	// IMAGE_REPO default, and copyImageFromBundle's local-registry retagging (registry host
	// swapped in, path kept) - see GetContainerDeviceImage.
	defaultContainerDeviceImageRepoPath = "flightctl/flightctl-device"

	// e2eContainerDeviceImageEnv overrides the resolved image; e2eContainerDeviceOSIDEnv overrides
	// just the OS_ID portion of the default (see GetContainerDeviceImage).
	e2eContainerDeviceImageEnv = "E2E_CONTAINER_DEVICE_IMAGE"
	e2eContainerDeviceOSIDEnv  = "AGENT_OS_ID"
)

// ContainerPool creates container-backed devices across all test suites, mirroring VMPool's API
// for the libvirt VM backend (see vm_pool.go). Unlike VMPool, it does not retain devices: every
// request creates a fresh container for that test/spec.
type ContainerPool struct {
	config ContainerPoolConfig
}

// ContainerPoolConfig holds configuration for the container pool.
type ContainerPoolConfig struct {
	// Image is the flightctl-agent bootc image to run for every device container.
	Image string
}

type containerDeviceImageCache struct {
	once    sync.Once
	resolve func() (string, error)
	image   string
	err     error
}

func (c *containerDeviceImageCache) get() (string, error) {
	c.once.Do(func() {
		c.image, c.err = c.resolve()
	})
	return c.image, c.err
}

var (
	globalContainerPool             *ContainerPool
	containerPoolOnce               sync.Once
	containerDeviceID               atomic.Uint64
	globalContainerDeviceImageCache = containerDeviceImageCache{resolve: GetContainerDeviceImage}
)

// GetOrCreateContainerPool returns the global container pool instance, creating it if necessary.
func GetOrCreateContainerPool(config ContainerPoolConfig) *ContainerPool {
	containerPoolOnce.Do(func() {
		globalContainerPool = &ContainerPool{
			config: config,
		}
	})
	return globalContainerPool
}

// GetContainerForWorker creates a fresh container-backed device for the given worker ID.
// Container devices are intentionally not cached: unlike VMs, they are cheap to recreate and each
// spec should start from a newly created container.
func (p *ContainerPool) GetContainerForWorker(workerID int) (vm.TestVMInterface, error) {
	return p.getContainerForWorker(context.Background(), workerID)
}

func (p *ContainerPool) getContainerForWorker(ctx context.Context, workerID int) (*vm.ContainerDevice, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	device, err := p.createContainerForWorker(ctx, workerID)
	if err != nil {
		return nil, fmt.Errorf("failed to create container device for worker %d: %w", workerID, err)
	}
	return device, nil
}

// createContainerForWorker builds and starts a fresh container-backed device for workerID.
func (p *ContainerPool) createContainerForWorker(ctx context.Context, workerID int) (*vm.ContainerDevice, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name := containerDeviceName(workerID)
	fmt.Printf("🔄 [ContainerPool] Worker %d: Creating container device %s\n", workerID, name)

	registryHost, extraHosts := containerDeviceRegistryAccess(containers.GetHostIP())
	files, err := buildAgentIdentityFiles(registryHost)
	if err != nil {
		return nil, err
	}
	files = append(files, buildRegistryRemapFile(registryHost))

	device := vm.NewContainerDevice(vm.ContainerDeviceConfig{
		Name:       name,
		Image:      p.config.Image,
		Files:      files,
		ExtraHosts: extraHosts,
	})
	if err := device.RunAndWaitForSSHContext(ctx); err != nil {
		setupErr := fmt.Errorf("failed to start container device %s: %w", name, err)
		// Clean up a half-started container so a later attempt doesn't leave an orphan behind.
		if cleanupErr := device.ForceDelete(); cleanupErr != nil {
			fmt.Printf("⚠️  [ContainerPool] Worker %d: Failed to cleanup container device %s after start failure: %v\n", workerID, name, cleanupErr)
			return nil, errors.Join(setupErr, fmt.Errorf("failed to clean up container device %s after start failure: %w", name, cleanupErr))
		}
		return nil, setupErr
	}
	fmt.Printf("✅ [ContainerPool] Worker %d: Container device %s started and ready\n", workerID, name)
	return device, nil
}

// containerDeviceName returns a runtime-unique name, even when two specs for the same worker
// create devices concurrently. This avoids the containers racing to remove or replace each other.
func containerDeviceName(workerID int) string {
	return fmt.Sprintf("flightctl-e2e-container-worker-%d-%d-%d", workerID, os.Getpid(), containerDeviceID.Add(1))
}

// registryAccessHost returns the host component for host-side image references to the local e2e
// registry. Mirrors registry.go: bare IPv6 literals in image names are parsed as transport
// prefixes (e.g. "fd2e:" looks like "docker:"), so fall back to localhost. Do not use this for
// config copied into a device, where localhost refers to the device itself.
func registryAccessHost() string {
	host := containers.GetHostIP()
	if strings.Contains(host, ":") {
		return "localhost"
	}
	return host
}

// containerDeviceRegistryAccess mirrors the VM image's registry setup: in IPv6 mode the device
// uses the stable e2e-registry hostname, mapped to the host IP so both published registry ports
// (5000 and 5002) resolve to the host instead of the device's or registry container's namespace.
func containerDeviceRegistryAccess(hostIP string) (string, []string) {
	if strings.Contains(hostIP, ":") {
		return registryHostname, []string{fmt.Sprintf("%s:%s", registryHostname, hostIP)}
	}
	return hostIP, nil
}

// GetContainerDeviceImage resolves the flightctl-agent bootc image to run for container-backed
// devices: the same "base" image test/scripts/agent-images/scripts/build.sh builds for the qcow2,
// but pointed at the local registry mirror - see copyImageFromBundle in
// test/e2e/infra/auxiliary/images.go, which is what actually pushes it there during CI (registry
// host swapped in, image path/tag unchanged).
//
// The tag isn't just "base-${OS_ID}": build_and_qcow2.sh's bundle filter only bundles/pushes the
// base-${OS_ID}-${TAG} alias (TAG being a git-describe string not otherwise available to this test
// binary), so the exact tag is read back out of the agent image bundle itself via
// auxiliary.ResolveAgentDeviceImageTag - see its doc comment. The bare "base-${OS_ID}" alias is
// never pushed to the registry (see ResolveAgentDeviceImageTag's doc comment), so a resolution
// failure is returned rather than guessed at - silently falling back to that tag would just trade
// a clear error here for a confusing "manifest unknown" pull failure once the device starts.
//
// Override with E2E_CONTAINER_DEVICE_IMAGE for local runs to bypass all of this.
func GetContainerDeviceImage() (string, error) {
	if img := os.Getenv(e2eContainerDeviceImageEnv); img != "" {
		return img, nil
	}
	osID := os.Getenv(e2eContainerDeviceOSIDEnv)
	tag, err := auxiliary.ResolveAgentDeviceImageTag(osID)
	if err != nil {
		return "", fmt.Errorf("failed to resolve agent device image tag from bundle: %w", err)
	}
	return fmt.Sprintf("%s:%s/%s:%s", registryAccessHost(), registryHostPort, defaultContainerDeviceImageRepoPath, tag), nil
}

// GetAgentIdentityDir returns the directory holding the agent's enrollment bootstrap config and
// certs (config.yaml + certs/*), generated once per e2e run by
// test/scripts/agent-images/prepare_agent_config.sh and shared by every device - VM or container
// alike (VMs get it injected into their qcow2 by inject_agent_files_into_qcow.sh; containers get
// it copied in directly, see buildAgentIdentityFiles). Errors if it hasn't been generated yet.
func GetAgentIdentityDir() (string, error) {
	dir := filepath.Join(util.GetTopLevelDir(), "bin", "agent", "etc", "flightctl")
	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); err != nil {
		return "", fmt.Errorf("agent config not found at %s (run prepare_agent_config.sh / the agent-images build pipeline first): %w", dir, err)
	}
	return dir, nil
}

// buildAgentIdentityFiles returns the ContainerFiles needed to give a fresh device container the
// same enrollment bootstrap identity, and (if present) the same trusted registry CA, that
// inject_agent_files_into_qcow.sh writes into every VM-backed device's qcow2. registryHost is the
// address as resolved from inside the device container.
func buildAgentIdentityFiles(registryHost string) ([]vm.ContainerFile, error) {
	dir, err := GetAgentIdentityDir()
	if err != nil {
		return nil, err
	}

	files := []vm.ContainerFile{
		{HostPath: filepath.Join(dir, "config.yaml"), ContainerPath: "/etc/flightctl/config.yaml", Mode: 0644},
	}

	certsDir := filepath.Join(dir, "certs")
	entries, err := os.ReadDir(certsDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read agent certs dir %s: %w", certsDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		mode := int64(0644)
		if strings.HasSuffix(entry.Name(), ".key") {
			mode = 0600
		}
		files = append(files, vm.ContainerFile{
			HostPath:      filepath.Join(certsDir, entry.Name()),
			ContainerPath: "/etc/flightctl/certs/" + entry.Name(),
			Mode:          mode,
		})
	}

	// CA anchor: same file inject_agent_files_into_qcow.sh installs to both the trust anchors dir
	// (system-wide TLS trust, needs update-ca-trust - see ContainerDevice.Run) and containers/certs.d
	// (podman/skopeo's own registry-specific trust store, no update-ca-trust needed).
	caCertPath := filepath.Join(util.GetTopLevelDir(), "bin", "e2e-certs", "pki", "CA", "ca.crt")
	switch _, err := os.Stat(caCertPath); {
	case err == nil:
		regHostPort := fmt.Sprintf("%s:%s", registryHost, registryHostPort)
		files = append(files,
			vm.ContainerFile{HostPath: caCertPath, ContainerPath: "/etc/pki/ca-trust/source/anchors/flightctl-e2e-registry.crt", Mode: 0644},
			vm.ContainerFile{HostPath: caCertPath, ContainerPath: "/etc/containers/certs.d/" + regHostPort + "/ca.crt", Mode: 0644},
		)
	case os.IsNotExist(err):
		// Expected for local/dev runs that haven't generated e2e certs yet - the registry-trust
		// files above are best-effort, mirroring inject_agent_files_into_qcow.sh's own handling.
	default:
		return nil, fmt.Errorf("failed to stat CA cert %s: %w", caCertPath, err)
	}

	return files, nil
}

// buildRegistryRemapFile generates the same registries.conf.d remap
// inject_agent_files_into_qcow.sh's write_registry_remap writes into the qcow2, as an in-memory
// ContainerFile (there's no standalone host-side file to point at - the bash version writes it
// inline too). host is the registry address as resolved from inside the device container.
func buildRegistryRemapFile(host string) vm.ContainerFile {
	dest := fmt.Sprintf("%s:%s/flightctl", host, registryHostPort)
	privateDest := fmt.Sprintf("%s:%s/flightctl", host, privateRegistryHostPort)
	testDest := fmt.Sprintf("%s:%s/flightctl-tests", host, registryHostPort)

	content := fmt.Sprintf(`[[registry]]
prefix = "%s"
location = "%s"

[[registry]]
prefix = "%s-private"
location = "%s"

[[registry]]
prefix = "%s"
location = "%s"
`, containerSourceRepo, dest, containerSourceRepo, privateDest, containerTestSourceRepo, testDest)

	return vm.ContainerFile{
		Content:       []byte(content),
		ContainerPath: "/etc/containers/registries.conf.d/flightctl-remap.conf",
		Mode:          0644,
	}
}

func getOrCreateContainerPool() (*ContainerPool, error) {
	image, err := globalContainerDeviceImageCache.get()
	if err != nil {
		return nil, err
	}
	return GetOrCreateContainerPool(ContainerPoolConfig{Image: image}), nil
}

// validateContainerDevicePrerequisites resolves the device image and validates the agent identity
// files without creating a container. Call it during suite setup so missing inputs fail once at
// the suite boundary rather than once per spec.
func validateContainerDevicePrerequisites() error {
	if _, err := getOrCreateContainerPool(); err != nil {
		return fmt.Errorf("failed to resolve container device image: %w", err)
	}
	registryHost, _ := containerDeviceRegistryAccess(containers.GetHostIP())
	files, err := buildAgentIdentityFiles(registryHost)
	if err != nil {
		return fmt.Errorf("failed to validate container device agent identity: %w", err)
	}
	if err := validateContainerIdentityFiles(files); err != nil {
		return fmt.Errorf("failed to validate container device agent identity: %w", err)
	}
	return nil
}

func validateContainerIdentityFiles(files []vm.ContainerFile) error {
	for _, file := range files {
		if file.HostPath == "" {
			continue
		}
		hostFile, err := os.Open(file.HostPath)
		if err != nil {
			return fmt.Errorf("failed to open identity file %s: %w", file.HostPath, err)
		}
		info, statErr := hostFile.Stat()
		closeErr := hostFile.Close()
		if statErr != nil {
			return fmt.Errorf("failed to stat identity file %s: %w", file.HostPath, statErr)
		}
		if closeErr != nil {
			return fmt.Errorf("failed to close identity file %s: %w", file.HostPath, closeErr)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("identity file %s is not a regular file", file.HostPath)
		}
	}
	return nil
}

// SetupContainerForWorker is a convenience function that initializes the container factory and
// returns a newly created container-backed device for the worker.
func SetupContainerForWorker(workerID int) (vm.TestVMInterface, error) {
	ctx, cancel := context.WithTimeout(context.Background(), setupSnapshotRestoreTimeout)
	defer cancel()
	return setupContainerForWorker(ctx, workerID)
}

func setupContainerForWorker(ctx context.Context, workerID int) (*vm.ContainerDevice, error) {
	pool, err := getOrCreateContainerPool()
	if err != nil {
		return nil, err
	}
	return pool.getContainerForWorker(ctx, workerID)
}
