// Command flightctl-dev-vm is a developer tool for starting, connecting to,
// and deleting a single flightctl agent VM.  It uses the exact same
// libvirt/qcow2 infrastructure as the e2e test suite (user-session libvirt,
// qcow2 overlays, QEMU user-networking SSH port forwarding) and requires no
// root privileges for VM management.
//
// Usage:
//
//	bin/flightctl-dev-vm start   [--name NAME] [--base-disk PATH] [--ssh-port PORT] [--mem MiB] [--work-dir DIR]
//	bin/flightctl-dev-vm console [--name NAME] [--ssh-port PORT]
//	bin/flightctl-dev-vm delete  [--name NAME] [--work-dir DIR]
//
// Prerequisites: run 'make deploy' first so the flightctl API is up and the
// base qcow2 image has been built and injected with agent config.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/flightctl/flightctl/test/harness/e2e/vm"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

const (
	defaultVMName    = "flightctl-device-default"
	defaultSSHPort   = 2222
	defaultMemoryMiB = 2048
	vmUser           = "user"
	vmPassword       = "user"
)

func defaultWorkDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/flightctl-dev-vm"
	}
	return filepath.Join(home, ".local", "share", "flightctl", "dev-vm")
}

// findBaseDisk locates bin/output/qcow2/disk.qcow2 relative to the current
// working directory (repo root when invoked via make) or relative to the
// binary's own directory (bin/).
func findBaseDisk() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting working directory: %w", err)
	}
	candidate := filepath.Join(cwd, "bin", "output", "qcow2", "disk.qcow2")
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}

	exe, err := os.Executable()
	if err == nil {
		// Binary lives in <repo>/bin/, so base disk is at <repo>/bin/output/qcow2/disk.qcow2
		candidate = filepath.Join(filepath.Dir(exe), "output", "qcow2", "disk.qcow2")
		if abs, err := filepath.Abs(candidate); err == nil {
			if _, err := os.Stat(abs); err == nil {
				return abs, nil
			}
		}
	}

	return "", fmt.Errorf("base disk not found; run 'make e2e-agent-images' or pass --base-disk")
}

func overlayDiskPath(workDir, vmName string) string {
	return filepath.Join(workDir, vmName+"-disk.qcow2")
}

func main() {
	var (
		vmName    string
		baseDisk  string
		sshPort   int
		memoryMiB int
		workDir   string
	)

	root := &cobra.Command{
		Use:   "flightctl-dev-vm",
		Short: "Manage a flightctl agent development VM",
		Long: `Developer tool for starting, connecting to, and deleting a single
flightctl agent VM.  Uses user-session libvirt (qemu:///session) and qcow2
overlays — no root required.

Prerequisites: run 'make deploy' first.`,
	}

	addCommonFlags := func(cmd *cobra.Command) {
		cmd.Flags().StringVar(&vmName, "name", defaultVMName, "VM domain name")
		cmd.Flags().IntVar(&sshPort, "ssh-port", defaultSSHPort, "Host port forwarded to VM SSH (127.0.0.1:<port>)")
		cmd.Flags().StringVar(&workDir, "work-dir", defaultWorkDir(), "Directory for the overlay disk and VM state")
	}

	// start subcommand
	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Create a qcow2 overlay disk and start the VM",
		RunE: func(cmd *cobra.Command, args []string) error {
			if baseDisk == "" {
				var err error
				baseDisk, err = findBaseDisk()
				if err != nil {
					return err
				}
			}
			return runStart(vmName, baseDisk, sshPort, memoryMiB, workDir)
		},
	}
	addCommonFlags(startCmd)
	startCmd.Flags().StringVar(&baseDisk, "base-disk", "", "Path to base qcow2 image (default: auto-detect bin/output/qcow2/disk.qcow2)")
	startCmd.Flags().IntVar(&memoryMiB, "mem", defaultMemoryMiB, "VM memory in MiB")
	root.AddCommand(startCmd)

	// console subcommand
	consoleCmd := &cobra.Command{
		Use:   "console",
		Short: "Attach to the VM serial console via virsh (exit with Ctrl+])",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConsole(vmName)
		},
	}
	consoleCmd.Flags().StringVar(&vmName, "name", defaultVMName, "VM domain name")
	root.AddCommand(consoleCmd)

	// delete subcommand
	deleteCmd := &cobra.Command{
		Use:   "delete",
		Short: "Stop the VM and remove its overlay disk",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDelete(vmName, workDir)
		},
	}
	addCommonFlags(deleteCmd)
	root.AddCommand(deleteCmd)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func runStart(vmName, baseDisk string, sshPort, memoryMiB int, workDir string) error {
	logrus.Infof("Starting VM %s (base: %s, ssh-port: %d, mem: %dMiB)", vmName, baseDisk, sshPort, memoryMiB)

	if err := os.MkdirAll(workDir, 0755); err != nil {
		return fmt.Errorf("creating work directory %s: %w", workDir, err)
	}

	overlayPath := overlayDiskPath(workDir, vmName)
	if _, err := os.Stat(overlayPath); os.IsNotExist(err) {
		logrus.Infof("Creating overlay disk at %s", overlayPath)
		cmd := exec.Command("qemu-img", "create", "-f", "qcow2", "-b", baseDisk, "-F", "qcow2", overlayPath) //nolint:gosec
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("creating overlay disk: %w\noutput: %s", err, string(out))
		}
	} else {
		logrus.Infof("Reusing existing overlay disk at %s", overlayPath)
	}

	if err := os.Chmod(overlayPath, 0644); err != nil {
		return fmt.Errorf("setting overlay disk permissions: %w", err)
	}

	testVM, err := vm.NewVM(vm.TestVM{
		TestDir:       workDir,
		VMName:        vmName,
		DiskImagePath: overlayPath,
		VMUser:        vmUser,
		SSHPassword:   vmPassword,
		SSHPort:       sshPort,
		MemoryMiB:     memoryMiB,
	})
	if err != nil {
		return fmt.Errorf("initializing VM: %w", err)
	}

	if err := testVM.Run(); err != nil {
		return fmt.Errorf("starting VM: %w", err)
	}

	fmt.Printf("\nVM %q is running.\n", vmName)
	fmt.Printf("  SSH:     ssh -p %d -o StrictHostKeyChecking=no %s@127.0.0.1  (password: %s)\n", sshPort, vmUser, vmPassword)
	fmt.Printf("  Console: make agent-vm-console VMNAME=%s\n", vmName)
	fmt.Printf("  Delete:  make clean-agent-vm VMNAME=%s\n\n", vmName)

	return nil
}

// runConsole attaches to the VM's serial console via virsh using the
// user-session libvirt URI (no root required).  The serial console works
// regardless of SSH or network availability, making it useful for debugging
// boot issues.  Exit the session with Ctrl+].
func runConsole(vmName string) error {
	virshPath, err := exec.LookPath("virsh")
	if err != nil {
		return fmt.Errorf("virsh not found in PATH")
	}

	args := []string{"virsh", "-c", "qemu:///session", "console", vmName}

	fmt.Println("Attaching to serial console (exit with Ctrl+])")
	return syscall.Exec(virshPath, args, os.Environ()) //nolint:gosec
}

func runDelete(vmName, workDir string) error {
	logrus.Infof("Deleting VM %s", vmName)

	overlayPath := overlayDiskPath(workDir, vmName)

	testVM, err := vm.NewVM(vm.TestVM{
		TestDir:       workDir,
		VMName:        vmName,
		DiskImagePath: overlayPath,
		VMUser:        vmUser,
		SSHPassword:   vmPassword,
	})
	if err != nil {
		return fmt.Errorf("initializing VM handle: %w", err)
	}

	if err := testVM.ForceDelete(); err != nil {
		return fmt.Errorf("deleting VM domain: %w", err)
	}

	if err := os.Remove(overlayPath); err != nil && !os.IsNotExist(err) {
		logrus.Warnf("Failed to remove overlay disk %s: %v", overlayPath, err)
	}

	fmt.Printf("VM %q deleted.\n", vmName)
	return nil
}
