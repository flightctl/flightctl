package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/test/harness/e2e/vm"
)

// VMPool manages VMs across all test suites
type VMPool struct {
	vms    map[int]vm.TestVMInterface
	mutex  sync.RWMutex
	config VMPoolConfig
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

	// Need to create a new VM
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// Double-check after acquiring write lock
	if vm, exists := p.vms[workerID]; exists {
		return vm, nil
	}

	// Create new VM for this worker
	newVM, err := p.createVMForWorker(workerID)
	if err != nil {
		return nil, fmt.Errorf("failed to create VM for worker %d: %w", workerID, err)
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

	// Create a fast copy of the base disk for this worker using cp with sparse support
	// This is the fastest method and doesn't hurt VM performance
	workerDiskPath := filepath.Join(workerDir, fmt.Sprintf("worker-%d-disk.qcow2", workerID))

	// Remove any existing disk file to avoid permission issues
	if err := os.Remove(workerDiskPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to remove existing disk file for worker %d: %w", workerID, err)
	}

	fmt.Printf("🔄 [VMPool] Worker %d: Creating fast disk copy at %s\n", workerID, workerDiskPath)

	// Use cp with sparse file support for maximum speed
	cmd := exec.Command("cp", "--sparse=always", p.config.BaseDiskPath, workerDiskPath)

	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to create fast disk copy for worker %d: %w, output: %s", workerID, err, string(output))
	}

	// Ensure the copied file has the correct permissions for libvirt access
	if err := os.Chmod(workerDiskPath, 0644); err != nil {
		return nil, fmt.Errorf("failed to set permissions on disk copy for worker %d: %w", workerID, err)
	}

	fmt.Printf("✅ [VMPool] Worker %d: Fast disk copy created successfully\n", workerID)

	// Create VM using the worker-specific disk copy
	newVM, err := vm.NewVM(vm.TestVM{
		TestDir:       workerDir,
		VMName:        vmName,
		DiskImagePath: workerDiskPath, // Use worker-specific disk copy
		VMUser:        "user",
		SSHPassword:   "user",
		SSHPort:       p.config.SSHPortBase + workerID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create VM: %w", err)
	}
	fmt.Printf("✅ [VMPool] Worker %d: VM struct created\n", workerID)

	// Clean up any existing VM with the same name
	fmt.Printf("🔄 [VMPool] Worker %d: Checking for existing VM\n", workerID)
	if err := p.cleanupExistingVM(newVM); err != nil {
		return nil, fmt.Errorf("failed to cleanup existing VM: %w", err)
	}
	fmt.Printf("✅ [VMPool] Worker %d: Existing VM cleanup completed\n", workerID)

	// Start the VM and wait for SSH to be ready
	fmt.Printf("🔄 [VMPool] Worker %d: Starting VM and waiting for SSH\n", workerID)
	if err := newVM.RunAndWaitForSSH(); err != nil {
		return nil, fmt.Errorf("failed to start VM: %w", err)
	}
	fmt.Printf("✅ [VMPool] Worker %d: VM started and SSH ready\n", workerID)

	// Take a snapshot of the running state (VM stays running)
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

// cleanupExistingVM removes any existing VM with the same name
func (p *VMPool) cleanupExistingVM(newVM vm.TestVMInterface) error {
	// Check if VM exists
	exists, err := newVM.Exists()
	if err != nil {
		return fmt.Errorf("failed to check if VM exists: %w", err)
	}

	if exists {
		fmt.Printf("🔄 [VMPool] Found existing VM, deleting it\n")
		// Force delete the existing VM
		if err := newVM.ForceDelete(); err != nil {
			return fmt.Errorf("failed to delete existing VM: %w", err)
		}

		// Wait a moment for cleanup to complete
		time.Sleep(1 * time.Second)
		fmt.Printf("✅ [VMPool] Existing VM deleted successfully\n")
	} else {
		fmt.Printf("✅ [VMPool] No existing VM found\n")
	}

	return nil
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
	p.vms = make(map[int]vm.TestVMInterface)

	// For external snapshots, no overlays to clean up
	// The storage pool manager was designed for internal snapshots

	return lastErr
}

// GetBaseDiskPath finds the base qcow2 disk path
func GetBaseDiskPath() (string, error) {
	// Use the user-level libvirt images directory for better compatibility
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	baseDisk := filepath.Join(homeDir, ".local/share/libvirt/images/base-disk.qcow2")
	if _, err := os.Stat(baseDisk); os.IsNotExist(err) {
		// Fallback to the original location if the new location doesn't exist
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

		baseDisk = filepath.Join(topDir, "bin/output/qcow2/disk.qcow2")
		if _, err := os.Stat(baseDisk); os.IsNotExist(err) {
			return "", fmt.Errorf("base disk not found at %s", baseDisk)
		}
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

// CleanupVMForWorker is a convenience function to clean up a worker's VM
func CleanupVMForWorker(workerID int) error {
	vmPool := GetVMPool()
	return vmPool.CleanupWorkerVM(workerID)
}

// RegisterVMPoolCleanup sets up a signal handler to clean up all VMs on process exit
var cleanupRegistered sync.Once

func RegisterVMPoolCleanup() {
	cleanupRegistered.Do(func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-c
			GetVMPool().CleanupAll()
			os.Exit(1)
		}()
	})
}
