package vm

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestVMDemo(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "VM Demo Tests")
}

var _ = Describe("VM Demo Tests", func() {
	It("should demonstrate VM creation and snapshot workflow", func() {
		// This test demonstrates the actual VM creation and snapshot workflow
		// It will run if you have a real disk image available

		// Check if we have a real disk image to work with
		realDiskImagePath := os.Getenv("TEST_DISK_IMAGE_PATH")
		if realDiskImagePath == "" {
			Skip("TEST_DISK_IMAGE_PATH not set - set this to a real QCOW2 disk image to run the demo")
		}

		// Check if the disk image actually exists
		if _, err := os.Stat(realDiskImagePath); os.IsNotExist(err) {
			Skip("Disk image does not exist: " + realDiskImagePath)
		}

		// 1. Create a temporary directory for the test
		testDir, err := os.MkdirTemp("", "vm-demo-test-*")
		Expect(err).ToNot(HaveOccurred())
		defer os.RemoveAll(testDir)

		// 2. Create a test VM configuration
		testVM := TestVM{
			TestDir:       testDir,
			VMName:        "demo-vm",
			DiskImagePath: realDiskImagePath,
			VMUser:        "user",
			SSHPassword:   "user",
			SSHPort:       2228,
		}

		// 3. Create the VM instance
		vm, err := NewVM(testVM)
		Expect(err).ToNot(HaveOccurred())
		Expect(vm).ToNot(BeNil())

		// Verify memory file path is set correctly
		expectedMemPath := filepath.Join(testDir, "demo-vm.mem")
		Expect(vm.MemoryFilePath).To(Equal(expectedMemPath))

		// 4. Create the VM domain (without starting it)
		err = vm.CreateDomain()
		Expect(err).ToNot(HaveOccurred())

		// 5. Start the VM
		err = vm.RunAndWaitForSSH()
		Expect(err).ToNot(HaveOccurred())

		// 6. Wait for VM to be running
		isRunning, err := vm.IsRunning()
		Expect(err).ToNot(HaveOccurred())
		Expect(isRunning).To(BeTrue())

		// 7. Create a snapshot
		snapshotName := "demo-snapshot-1"
		err = vm.CreateSnapshot(snapshotName)
		Expect(err).ToNot(HaveOccurred())

		// 8. Create a file in the VM
		_, err = vm.RunSSH([]string{"touch", "/tmp/test-file"}, nil)
		Expect(err).ToNot(HaveOccurred())

		// 9. Verify the file exists
		_, err = vm.RunSSH([]string{"cat", "/tmp/test-file"}, nil)
		Expect(err).ToNot(HaveOccurred())
		//add something to ram
		_, err = vm.RunSSH([]string{"echo", "test", ">", "/dev/shm/test-file"}, nil)
		Expect(err).ToNot(HaveOccurred())
		//lets verify that the memory is loaded
		_, err = vm.RunSSH([]string{"cat", "/dev/shm/test-file"}, nil)
		Expect(err).ToNot(HaveOccurred())

		// 10. Revert to the snapshot
		err = vm.RevertToSnapshot(snapshotName)
		Expect(err).ToNot(HaveOccurred())

		// 10. Verify the file does not exist
		_, err = vm.RunSSH([]string{"cat", "/tmp/test-file"}, nil)
		Expect(err).To(HaveOccurred())
		//lets verify that the memory is not loaded
		_, err = vm.RunSSH([]string{"cat", "/dev/shm/test-file"}, nil)
		Expect(err).To(HaveOccurred())

		// 10. Verify VM is still running after revert
		isRunning, err = vm.IsRunning()
		Expect(err).ToNot(HaveOccurred())
		Expect(isRunning).To(BeTrue())

		// 11. Final cleanup - force delete the VM
		err = vm.ForceDelete()
		Expect(err).ToNot(HaveOccurred())

		// 12. Verify VM is deleted
		exists, err := vm.Exists()
		Expect(err).ToNot(HaveOccurred())
		Expect(exists).To(BeFalse())
	})
})
