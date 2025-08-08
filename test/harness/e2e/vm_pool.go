package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/flightctl/flightctl/test/harness/e2e/vm"
)

// VMPool manages VMs across all test suites
type VMPool struct {
	vms             map[int]vm.TestVMInterface
	mutex           sync.RWMutex
	config          VMPoolConfig
	sharedDiskOnce  sync.Once
	sharedDiskError error
}

// VMPoolConfig holds configuration for the VM pool
type VMPoolConfig struct {
	BaseDiskPath string
	TempDir      string
	SSHPortBase  int
}

var (
	globalVMPool *VMPool
	poolOnce     sync.Once
)

// GetVMPool returns the global VM pool instance
func GetVMPool() *VMPool {
	poolOnce.Do(func() {
		globalVMPool = &VMPool{
			vms: make(map[int]vm.TestVMInterface),
		}
	})

	return globalVMPool
}

// Initialize initializes the VM pool with configuration
func (p *VMPool) Initialize(config VMPoolConfig) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.config = config

	// For external snapshots, no storage pool manager needed
	// The storage pool manager was designed for internal snapshots

	return nil
}

// GetVMForWorker returns a VM for the given worker ID, creating it if necessary
func (p *VMPool) GetVMForWorker(workerID int) (vm.TestVMInterface, error) {
	p.mutex.RLock()
	if vm, exists := p.vms[workerID]; exists {
		p.mutex.RUnlock()
		return vm, nil
	}
	p.mutex.RUnlock()

	// Create new VM for this worker (outside of lock)
	newVM, err := p.createVMForWorker(workerID)
	if err != nil {
		return nil, fmt.Errorf("failed to create VM for worker %d: %w", workerID, err)
	}

	// Lock only when accessing the map
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// Double-check after acquiring write lock in case another goroutine created it
	if vm, exists := p.vms[workerID]; exists {
		// Another goroutine created the VM while we were creating ours
		// Clean up our VM and return the existing one
		if cleanupErr := newVM.ForceDelete(); cleanupErr != nil {
			// Log cleanup error but don't fail the operation
			fmt.Printf("⚠️  [VMPool] Worker %d: Failed to cleanup redundant VM: %v\n", workerID, cleanupErr)
		}
		return vm, nil
	}

	p.vms[workerID] = newVM
	return newVM, nil
}

// createVMForWorker creates a new VM for the specified worker
func (p *VMPool) createVMForWorker(workerID int) (vm.TestVMInterface, error) {
	vmName := fmt.Sprintf("flightctl-e2e-worker-%d", workerID)

	fmt.Printf("🔄 [VMPool] Worker %d: Creating VM %s\n", workerID, vmName)

	// Create worker-specific temp directory
	workerDir := filepath.Join(p.config.TempDir, fmt.Sprintf("flightctl-e2e-worker-%d", workerID))
	if err := os.MkdirAll(workerDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create worker directory: %w", err)
	}

	// Ensure the worker directory has the correct permissions for file creation
	if err := os.Chmod(workerDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to set permissions on worker directory: %w", err)
	}

	// Create a shared base disk in /tmp using sync.Once to prevent race conditions
	// This saves massive disk space while working around libvirt session mode security restrictions
	sharedBaseDisk := filepath.Join(p.config.TempDir, "shared-base-disk.qcow2")

	// Use sync.Once to ensure the shared base disk is created exactly once
	p.sharedDiskOnce.Do(func() {
		fmt.Printf("🔄 [VMPool] Worker %d: Creating shared base disk at %s\n", workerID, sharedBaseDisk)
		cmd := exec.Command("cp", "--sparse=always", p.config.BaseDiskPath, sharedBaseDisk) //nolint:gosec
		if output, err := cmd.CombinedOutput(); err != nil {
			p.sharedDiskError = fmt.Errorf("failed to create shared base disk: %w, output: %s", err, string(output))
			return
		}
		if err := os.Chmod(sharedBaseDisk, 0644); err != nil {
			p.sharedDiskError = fmt.Errorf("failed to set permissions on shared base disk: %w", err)
			return
		}
		fmt.Printf("✅ [VMPool] Worker %d: Shared base disk created successfully\n", workerID)
	})

	// Check if there was an error during shared disk creation
	if p.sharedDiskError != nil {
		return nil, p.sharedDiskError
	}

	workerDiskPath := filepath.Join(workerDir, fmt.Sprintf("worker-%d-disk.qcow2", workerID))

	if _, err := os.Stat(workerDiskPath); err == nil {
		fmt.Printf("🔄 [VMPool] Worker %d: Overlay disk already exists at %s, skipping creation\n", workerID, workerDiskPath)
	} else {
		fmt.Printf("🔄 [VMPool] Worker %d: Creating qcow2 overlay disk with shared backing file at %s\n", workerID, workerDiskPath)
		cmd := exec.Command("qemu-img", "create", "-f", "qcow2", "-b", sharedBaseDisk, "-F", "qcow2", workerDiskPath)
		if output, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("failed to create overlay disk for worker %d: %w, output: %s", workerID, err, string(output))
		}
	}

	// Ensure the copied file has the correct permissions for libvirt access
	if err := os.Chmod(workerDiskPath, 0644); err != nil {
		return nil, fmt.Errorf("failed to set permissions on disk copy for worker %d: %w", workerID, err)
	}

	fmt.Printf("✅ [VMPool] Worker %d: Overlay disk created successfully\n", workerID)

	// Create VM using the worker-specific overlay disk
	newVM, err := vm.NewVM(vm.TestVM{
		TestDir:       workerDir,
		VMName:        vmName,
		DiskImagePath: workerDiskPath, // Use worker-specific overlay disk
		VMUser:        "user",
		SSHPassword:   "user",
		SSHPort:       p.config.SSHPortBase + workerID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create VM: %w", err)
	}
	fmt.Printf("✅ [VMPool] Worker %d: VM struct created\n", workerID)

	// Start the VM and wait for SSH to be ready
	fmt.Printf("🔄 [VMPool] Worker %d: Starting VM and waiting for SSH\n", workerID)
	if err := newVM.RunAndWaitForSSH(); err != nil {
		return nil, fmt.Errorf("failed to start VM: %w", err)
	}
	fmt.Printf("✅ [VMPool] Worker %d: VM started and SSH ready\n", workerID)

	// Take a snapshot of the running state (VM stayso running)
	exists, err := newVM.HasSnapshot("pristine")
	if err != nil {
		return nil, fmt.Errorf("failed to check if VM has snapshot: %w", err)
	}
	if exists {
		fmt.Printf("✅ [VMPool] Worker %d: Pristine snapshot already exists, skipping creation\n", workerID)
		return newVM, nil
	}

	fmt.Printf("🔄 [VMPool] Worker %d: Creating pristine snapshot\n", workerID)
	if err := newVM.CreateSnapshot("pristine"); err != nil {
		// Clean up on failure
		_ = newVM.ForceDelete()
		return nil, fmt.Errorf("failed to create pristine snapshot: %w", err)
	}
	fmt.Printf("✅ [VMPool] Worker %d: Pristine snapshot created successfully\n", workerID)

	// VM stays running - no shutdown
	fmt.Printf("✅ [VMPool] Worker %d: VM setup completed, VM is running\n", workerID)
	return newVM, nil
}

// cleanupWorkerDirectory removes the entire worker directory and all its contents
func (p *VMPool) cleanupWorkerDirectory(workerID int) {
	workerDir := filepath.Join(p.config.TempDir, fmt.Sprintf("flightctl-e2e-worker-%d", workerID))
	if err := os.RemoveAll(workerDir); err != nil {
		fmt.Printf("⚠️  [VMPool] Worker %d: Failed to remove worker directory: %v\n", workerID, err)
	} else {
		fmt.Printf("✅ [VMPool] Worker %d: Worker directory cleaned up\n", workerID)
	}
}

// CleanupWorkerVM cleans up the VM for a specific worker
func (p *VMPool) CleanupWorkerVM(workerID int) error {
	// Only lock for map access
	p.mutex.Lock()
	vm, exists := p.vms[workerID]
	if exists {
		delete(p.vms, workerID) // Remove from map immediately
		fmt.Printf("🔍 [VMPool] Worker %d: VM removed from pool map (total VMs in map: %d)\n", workerID, len(p.vms))
	} else {
		fmt.Printf("🔍 [VMPool] Worker %d: No VM found in pool map to remove\n", workerID)
	}
	p.mutex.Unlock() // 🔓 Release mutex before VM operations

	if !exists {
		fmt.Printf("✅ [VMPool] Worker %d: No VM found to cleanup\n", workerID)
		return nil
	}

	fmt.Printf("🔄 [VMPool] Worker %d: Starting VM cleanup\n", workerID)

	// Now do VM operations without holding the mutex
	vmExists, err := vm.Exists()
	if err != nil {
		fmt.Printf("⚠️  [VMPool] Worker %d: Failed to check if VM exists: %v\n", workerID, err)
	}

	if vmExists {
		// VM operations that can hang - no mutex held
		if err := vm.Shutdown(); err != nil {
			fmt.Printf("⚠️  [VMPool] Worker %d: Failed to shutdown VM: %v\n", workerID, err)
		}
		// ... rest of cleanup
	}

	// Clean up entire worker directory
	p.cleanupWorkerDirectory(workerID)

	return nil
}

// CleanupAll cleans up all VMs in the pool
func (p *VMPool) CleanupAll() error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	var lastErr error
	for workerID, vm := range p.vms {
		fmt.Printf("🔄 [VMPool] Worker %d: Cleaning up VM\n", workerID)

		// Check if VM exists before trying to clean it up
		vmExists, err := vm.Exists()
		if err != nil {
			fmt.Printf("⚠️  [VMPool] Worker %d: Failed to check if VM exists: %v\n", workerID, err)
		}

		if vmExists {
			// Delete snapshot first (ignore errors)
			_ = vm.DeleteSnapshot("pristine")

			// Delete VM
			if err := vm.ForceDelete(); err != nil {
				fmt.Printf("⚠️  [VMPool] Worker %d: Failed to delete VM: %v\n", workerID, err)
				if lastErr != nil {
					lastErr = fmt.Errorf("multiple errors: %w, worker %d: %w", lastErr, workerID, err)
				} else {
					lastErr = fmt.Errorf("failed to delete VM for worker %d: %w", workerID, err)
				}
			}
		} else {
			fmt.Printf("ℹ️  [VMPool] Worker %d: VM no longer exists, skipping cleanup\n", workerID)
		}

		// Clean up worker directory
		p.cleanupWorkerDirectory(workerID)
	}
	fmt.Printf("🔍 [VMPool] Clearing VM pool map (was %d VMs)\n", len(p.vms))
	p.vms = make(map[int]vm.TestVMInterface)

	// For external snapshots, no overlays to clean up
	// The storage pool manager was designed for internal snapshots

	return lastErr
}

// GetBaseDiskPath finds the base qcow2 disk path
func GetBaseDiskPath() (string, error) {

	currentWorkDirectory, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %w", err)
	}

	parts := strings.Split(currentWorkDirectory, "/")
	topDir := ""
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == "test" {
			topDir = strings.Join(parts[:i], "/")
			break
		}
	}

	if topDir == "" {
		return "", fmt.Errorf("could not find top-level directory")
	}

	baseDisk := filepath.Join(topDir, "bin/output/qcow2/disk.qcow2")
	if _, err := os.Stat(baseDisk); os.IsNotExist(err) {
		return "", fmt.Errorf("base disk not found at %s", baseDisk)
	}

	return baseDisk, nil
}

// SetupVMForWorker is a convenience function that initializes the VM pool and returns a VM for the worker
func SetupVMForWorker(workerID int, tempDir string, sshPortBase int) (vm.TestVMInterface, error) {
	vmPool := GetVMPool()

	baseDiskPath, err := GetBaseDiskPath()
	if err != nil {
		return nil, fmt.Errorf("failed to get base disk path: %w", err)
	}

	if err := vmPool.Initialize(VMPoolConfig{
		BaseDiskPath: baseDiskPath,
		TempDir:      tempDir,
		SSHPortBase:  sshPortBase,
	}); err != nil {
		return nil, fmt.Errorf("failed to initialize VM pool: %w", err)
	}

	return vmPool.GetVMForWorker(workerID)
}
