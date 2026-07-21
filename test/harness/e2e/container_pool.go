package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

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

	// containerSourceRepo/containerTestsSourceRepo mirror SOURCE_REPO/TESTS_SOURCE_REPO in
	// test/scripts/inject_agent_files_into_qcow.sh, so container-backed devices resolve
	// quay.io/flightctl(-private)/... and quay.io/flightctl-tests/... to the same local mirrors
	// VM-backed devices use.
	containerSourceRepo      = "quay.io/flightctl"
	containerTestsSourceRepo = "quay.io/flightctl-tests"

	// defaultContainerDeviceImageRepoPath/defaultContainerDeviceOSID mirror
	// test/scripts/agent-images/scripts/build.sh's IMAGE_REPO/OS_ID defaults, and
	// copyImageFromBundle's local-registry retagging (registry host swapped in, path kept) - see
	// GetContainerDeviceImage.
	defaultContainerDeviceImageRepoPath = "flightctl/flightctl-device"
	defaultContainerDeviceOSID          = "cs9-bootc"

	// e2eContainerDeviceImageEnv overrides the resolved image; e2eContainerDeviceOSIDEnv overrides
	// just the OS_ID portion of the default (see GetContainerDeviceImage).
	e2eContainerDeviceImageEnv = "E2E_CONTAINER_DEVICE_IMAGE"
	e2eContainerDeviceOSIDEnv  = "AGENT_OS_ID"
)

// ContainerPool manages container-backed devices across all test suites, mirroring VMPool's role
// for the libvirt VM backend (see vm_pool.go). There is exactly one device per worker - unlike
// VMPool there's no separate "fresh" pool, since every container-backed revert already recreates
// the container from scratch (see vm.ContainerDevice.RevertToSnapshot).
type ContainerPool struct {
	devices map[int]vm.TestVMInterface
	mutex   sync.RWMutex
	config  ContainerPoolConfig
}

// ContainerPoolConfig holds configuration for the container pool.
type ContainerPoolConfig struct {
	// Image is the flightctl-agent bootc image to run for every device container.
	Image string
}

var (
	globalContainerPool *ContainerPool
	containerPoolOnce   sync.Once
)

// GetOrCreateContainerPool returns the global container pool instance, creating it if necessary.
func GetOrCreateContainerPool(config ContainerPoolConfig) *ContainerPool {
	containerPoolOnce.Do(func() {
		globalContainerPool = &ContainerPool{
			devices: make(map[int]vm.TestVMInterface),
			config:  config,
		}
	})
	return globalContainerPool
}

// GetContainerForWorker returns a container-backed device for the given worker ID, creating it
// on-demand if it doesn't exist.
func (p *ContainerPool) GetContainerForWorker(workerID int) (vm.TestVMInterface, error) {
	p.mutex.RLock()
	if d, exists := p.devices[workerID]; exists {
		p.mutex.RUnlock()
		return d, nil
	}
	p.mutex.RUnlock()

	newDevice, err := p.createContainerForWorker(workerID)
	if err != nil {
		return nil, fmt.Errorf("failed to create container device for worker %d: %w", workerID, err)
	}

	p.mutex.Lock()
	defer p.mutex.Unlock()
	if d, exists := p.devices[workerID]; exists {
		// Another goroutine created the device while we were creating ours.
		if cleanupErr := newDevice.ForceDelete(); cleanupErr != nil {
			fmt.Printf("⚠️  [ContainerPool] Worker %d: Failed to cleanup redundant container device: %v\n", workerID, cleanupErr)
		}
		return d, nil
	}
	p.devices[workerID] = newDevice
	return newDevice, nil
}

// createContainerForWorker builds and starts a fresh container-backed device for workerID.
func (p *ContainerPool) createContainerForWorker(workerID int) (vm.TestVMInterface, error) {
	name := fmt.Sprintf("flightctl-e2e-container-worker-%d", workerID)
	fmt.Printf("🔄 [ContainerPool] Worker %d: Creating container device %s\n", workerID, name)

	files, err := buildAgentIdentityFiles()
	if err != nil {
		return nil, err
	}
	files = append(files, buildRegistryRemapFile())

	device := vm.NewContainerDevice(vm.ContainerDeviceConfig{
		Name:  name,
		Image: p.config.Image,
		Files: files,
	})
	if err := device.RunAndWaitForSSH(); err != nil {
		return nil, fmt.Errorf("failed to start container device %s: %w", name, err)
	}
	fmt.Printf("✅ [ContainerPool] Worker %d: Container device %s started and ready\n", workerID, name)
	return device, nil
}

// CleanupWorkerContainer removes the container device for a specific worker, if any.
func (p *ContainerPool) CleanupWorkerContainer(workerID int) error {
	p.mutex.Lock()
	d, exists := p.devices[workerID]
	if exists {
		delete(p.devices, workerID)
	}
	p.mutex.Unlock()

	if !exists {
		return nil
	}
	return d.ForceDelete()
}

// CleanupAll removes every container device in the pool.
func (p *ContainerPool) CleanupAll() error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	var lastErr error
	for workerID, d := range p.devices {
		if err := d.ForceDelete(); err != nil {
			lastErr = fmt.Errorf("failed to delete container device for worker %d: %w", workerID, err)
			fmt.Printf("⚠️  [ContainerPool] Worker %d: %v\n", workerID, err)
		}
	}
	p.devices = make(map[int]vm.TestVMInterface)
	return lastErr
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
// auxiliary.ResolveAgentDeviceImageTag - see its doc comment. Falls back to the bare "base-${OS_ID}"
// guess (best-effort, may not exist in the registry) only if that resolution fails, e.g. the bundle
// isn't present for some local dev setup.
//
// Override with E2E_CONTAINER_DEVICE_IMAGE for local runs to bypass all of this.
func GetContainerDeviceImage() string {
	if img := os.Getenv(e2eContainerDeviceImageEnv); img != "" {
		return img
	}
	osID := os.Getenv(e2eContainerDeviceOSIDEnv)
	tag, err := auxiliary.ResolveAgentDeviceImageTag(osID)
	if err != nil {
		if osID == "" {
			osID = defaultContainerDeviceOSID
		}
		tag = "base-" + osID
		fmt.Printf("⚠️  [ContainerPool] Failed to resolve exact agent device image tag from bundle (%v); falling back to %q\n", err, tag)
	}
	return fmt.Sprintf("%s:%s/%s:%s", containers.GetHostIP(), registryHostPort, defaultContainerDeviceImageRepoPath, tag)
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
// inject_agent_files_into_qcow.sh writes into every VM-backed device's qcow2.
func buildAgentIdentityFiles() ([]vm.ContainerFile, error) {
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
	if _, err := os.Stat(caCertPath); err == nil {
		regHostPort := containers.GetHostIP() + ":" + registryHostPort
		files = append(files,
			vm.ContainerFile{HostPath: caCertPath, ContainerPath: "/etc/pki/ca-trust/source/anchors/flightctl-e2e-registry.crt", Mode: 0644},
			vm.ContainerFile{HostPath: caCertPath, ContainerPath: "/etc/containers/certs.d/" + regHostPort + "/ca.crt", Mode: 0644},
		)
	}

	return files, nil
}

// buildRegistryRemapFile generates the same registries.conf.d remap
// inject_agent_files_into_qcow.sh's write_registry_remap writes into the qcow2, as an in-memory
// ContainerFile (there's no standalone host-side file to point at - the bash version writes it
// inline too).
func buildRegistryRemapFile() vm.ContainerFile {
	host := containers.GetHostIP()
	dest := fmt.Sprintf("%s:%s/flightctl", host, registryHostPort)
	privateDest := fmt.Sprintf("%s:%s/flightctl", host, privateRegistryHostPort)
	testsDest := fmt.Sprintf("%s:%s/flightctl-tests", host, registryHostPort)

	content := fmt.Sprintf(`[[registry]]
prefix = "%s"
location = "%s"

[[registry]]
prefix = "%s-private"
location = "%s"

[[registry]]
prefix = "%s"
location = "%s"
`, containerSourceRepo, dest, containerSourceRepo, privateDest, containerTestsSourceRepo, testsDest)

	return vm.ContainerFile{
		Content:       []byte(content),
		ContainerPath: "/etc/containers/registries.conf.d/flightctl-remap.conf",
		Mode:          0644,
	}
}

// SetupContainerForWorker is a convenience function that initializes the container pool and
// returns a container-backed device for the worker. Devices are created on-demand if they don't
// already exist in the pool.
func SetupContainerForWorker(workerID int) (vm.TestVMInterface, error) {
	pool := GetOrCreateContainerPool(ContainerPoolConfig{Image: GetContainerDeviceImage()})
	return pool.GetContainerForWorker(workerID)
}

// RemoveContainerFromPool removes a container device from the global pool for the given worker
// ID. Note: this should be called AFTER harness.Cleanup(), which handles device destruction.
func RemoveContainerFromPool(workerID int) error {
	if globalContainerPool == nil {
		return nil
	}
	globalContainerPool.mutex.Lock()
	_, exists := globalContainerPool.devices[workerID]
	if exists {
		delete(globalContainerPool.devices, workerID)
	}
	globalContainerPool.mutex.Unlock()
	return nil
}
