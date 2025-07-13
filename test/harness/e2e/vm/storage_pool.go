package vm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	StoragePoolName = "flightctl-e2e-storage"
	StoragePoolPath = "/tmp/flightctl-e2e-storage"
)

// StoragePoolManager manages the libvirt storage pool for persistent disks
type StoragePoolManager struct {
	poolName string
	poolPath string
}

// NewStoragePoolManager creates a new storage pool manager
func NewStoragePoolManager() *StoragePoolManager {
	return &StoragePoolManager{
		poolName: StoragePoolName,
		poolPath: StoragePoolPath,
	}
}

// EnsureStoragePoolExists creates the storage pool if it doesn't exist
func (spm *StoragePoolManager) EnsureStoragePoolExists() error {
	fmt.Printf("🔄 [StoragePool] Checking if storage pool %s exists\n", spm.poolName)

	// Always ensure the directory exists first
	if err := os.MkdirAll(spm.poolPath, 0755); err != nil {
		return fmt.Errorf("failed to create storage pool directory: %w", err)
	}

	// Check if pool exists
	cmd := exec.Command("virsh", "pool-info", spm.poolName)
	if err := cmd.Run(); err == nil {
		fmt.Printf("✅ [StoragePool] Storage pool %s already exists\n", spm.poolName)

		// Check if pool is already active
		infoCmd := exec.Command("virsh", "pool-info", spm.poolName)
		if infoOutput, infoErr := infoCmd.CombinedOutput(); infoErr == nil {
			if strings.Contains(string(infoOutput), "State:          running") || strings.Contains(string(infoOutput), "State:          active") {
				fmt.Printf("✅ [StoragePool] Storage pool %s is already active\n", spm.poolName)
				return nil
			}
		}

		// Pool exists but is not active, try to start it
		startCmd := exec.Command("virsh", "pool-start", spm.poolName)
		if output, err := startCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to start existing storage pool: %w, output: %s", err, string(output))
		}
		fmt.Printf("✅ [StoragePool] Storage pool %s started successfully\n", spm.poolName)
		return nil
	}

	fmt.Printf("🔄 [StoragePool] Creating storage pool %s at %s\n", spm.poolName, spm.poolPath)

	// Define storage pool
	defineCmd := exec.Command("virsh", "pool-define-as",
		"--name", spm.poolName,
		"--type", "dir",
		"--target", spm.poolPath)

	if output, err := defineCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to define storage pool: %w, output: %s", err, string(output))
	}

	// Build storage pool
	buildCmd := exec.Command("virsh", "pool-build", spm.poolName)
	if output, err := buildCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to build storage pool: %w, output: %s", err, string(output))
	}

	// Start storage pool
	startCmd := exec.Command("virsh", "pool-start", spm.poolName)
	if output, err := startCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to start storage pool: %w, output: %s", err, string(output))
	}

	// Enable autostart
	autostartCmd := exec.Command("virsh", "pool-autostart", spm.poolName)
	if output, err := autostartCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to enable autostart for storage pool: %w, output: %s", err, string(output))
	}

	fmt.Printf("✅ [StoragePool] Storage pool %s created and started successfully\n", spm.poolName)
	return nil
}

// EnsureDefaultNetworkExists creates the default libvirt network if it doesn't exist
func (spm *StoragePoolManager) EnsureDefaultNetworkExists() error {
	fmt.Printf("🔄 [StoragePool] Checking if default network exists\n")

	// Check if default network exists
	cmd := exec.Command("virsh", "net-info", "default")
	if err := cmd.Run(); err == nil {
		fmt.Printf("✅ [StoragePool] Default network already exists\n")
		return nil
	}

	fmt.Printf("🔄 [StoragePool] Creating default network\n")

	// Define default network
	defineCmd := exec.Command("virsh", "net-define", "/usr/share/libvirt/networks/default.xml")
	if output, err := defineCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to define default network: %w, output: %s", err, string(output))
	}

	// Start default network
	startCmd := exec.Command("virsh", "net-start", "default")
	if output, err := startCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to start default network: %w, output: %s", err, string(output))
	}

	// Enable autostart
	autostartCmd := exec.Command("virsh", "net-autostart", "default")
	if output, err := autostartCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to enable autostart for default network: %w, output: %s", err, string(output))
	}

	fmt.Printf("✅ [StoragePool] Default network created and started successfully\n")
	return nil
}

// CreateOverlayInPool creates a qcow2 overlay in the storage pool
func (spm *StoragePoolManager) CreateOverlayInPool(workerID int, baseDiskPath string) (string, error) {
	overlayName := fmt.Sprintf("flightctl-e2e-worker-%d-disk.qcow2", workerID)
	overlayPath := filepath.Join(spm.poolPath, overlayName)

	fmt.Printf("🔄 [StoragePool] Creating overlay %s in storage pool\n", overlayName)

	// Create qcow2 overlay with backing file
	cmd := exec.Command("qemu-img", "create",
		"-f", "qcow2",
		"-b", baseDiskPath,
		"-F", "qcow2",
		overlayPath)

	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to create overlay disk: %w, output: %s", err, string(output))
	}

	fmt.Printf("✅ [StoragePool] Overlay %s created successfully\n", overlayName)
	return overlayPath, nil
}

// CleanupWorkerOverlay removes the overlay for a specific worker
func (spm *StoragePoolManager) CleanupWorkerOverlay(workerID int) error {
	overlayName := fmt.Sprintf("flightctl-e2e-worker-%d-disk.qcow2", workerID)
	overlayPath := filepath.Join(spm.poolPath, overlayName)

	fmt.Printf("🔄 [StoragePool] Cleaning up overlay %s\n", overlayName)

	if err := os.Remove(overlayPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove overlay %s: %w", overlayName, err)
	}

	fmt.Printf("✅ [StoragePool] Overlay %s cleaned up\n", overlayName)
	return nil
}

// CleanupAllOverlays removes all worker overlays
func (spm *StoragePoolManager) CleanupAllOverlays() error {
	fmt.Printf("🔄 [StoragePool] Cleaning up all overlays\n")

	entries, err := os.ReadDir(spm.poolPath)
	if err != nil {
		return fmt.Errorf("failed to read storage pool directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "flightctl-e2e-worker-") && strings.HasSuffix(entry.Name(), "-disk.qcow2") {
			overlayPath := filepath.Join(spm.poolPath, entry.Name())
			if err := os.Remove(overlayPath); err != nil {
				fmt.Printf("⚠️  [StoragePool] Failed to remove overlay %s: %v\n", entry.Name(), err)
			} else {
				fmt.Printf("✅ [StoragePool] Removed overlay %s\n", entry.Name())
			}
		}
	}

	return nil
}

// GetStoragePoolPath returns the storage pool path
func (spm *StoragePoolManager) GetStoragePoolPath() string {
	return spm.poolPath
}
