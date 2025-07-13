package vm

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"text/template"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/sirupsen/logrus"
	"libvirt.org/go/libvirt"
)

//go:embed domain-template.xml
var domainTemplate string

type VMInLibvirt struct {
	domain              *libvirt.Domain
	libvirtUri          string
	libvirtConn         *libvirt.Connect
	consoleOutput       *bytes.Buffer
	consoleMutex        sync.Mutex
	consoleStream       *libvirt.Stream
	consoleOutputString string
	TestVM
}

func getLibvirtUri() string {
	return "qemu:///session"
}

func NewVM(params TestVM) (vm *VMInLibvirt, err error) {

	if params.LibvirtUri == "" {
		params.LibvirtUri = getLibvirtUri()
	}

	// Set default memory file path if not provided
	if params.MemoryFilePath == "" {
		params.MemoryFilePath = filepath.Join(params.TestDir, params.VMName+".mem")
	}

	vm = &VMInLibvirt{
		libvirtUri: params.LibvirtUri,
		TestVM:     params}

	vm.pidFile = filepath.Join(params.TestDir, params.VMName+".pid")

	return vm, nil
}

// CreateDomain creates the libvirt domain without starting the VM
func (v *VMInLibvirt) CreateDomain() error {
	logrus.Infof("Creating VM domain %s", v.TestVM.VMName)
	conn, err := libvirt.NewConnect(v.libvirtUri)
	if err != nil {
		return err
	}
	v.libvirtConn = conn // Store the connection for reuse

	// Proactively destroy and undefine any existing domain with the same name
	existingDomain, err := conn.LookupDomainByName(v.TestVM.VMName)
	if err == nil {
		state, _, _ := existingDomain.GetState()
		if state == libvirt.DOMAIN_RUNNING || state == libvirt.DOMAIN_PAUSED {
			_ = existingDomain.Destroy()
		}
		_ = existingDomain.UndefineFlags(libvirt.DOMAIN_UNDEFINE_SNAPSHOTS_METADATA | libvirt.DOMAIN_UNDEFINE_MANAGED_SAVE | libvirt.DOMAIN_UNDEFINE_NVRAM)
		_ = existingDomain.Free()
	}

	domainXML, err := v.parseDomainTemplate()
	if err != nil {
		return fmt.Errorf("unable to parse domain template: %w", err)
	}

	logrus.Debugf("domainXML:\n%s\n\n", domainXML)

	v.domain, err = conn.DomainDefineXMLFlags(domainXML, libvirt.DOMAIN_DEFINE_VALIDATE)
	if err != nil {
		return fmt.Errorf("unable to define virtual machine domain: %w", err)
	}

	logrus.Infof("Created VM domain %s", v.TestVM.VMName)
	return nil
}

func (v *VMInLibvirt) Run() error {

	logrus.Infof("Starting VM %s", v.TestVM.VMName)

	// Use existing connection if available, otherwise create new one
	var conn *libvirt.Connect
	var err error
	if v.libvirtConn != nil {
		conn = v.libvirtConn
	} else {
		conn, err = libvirt.NewConnect(v.libvirtUri)
		if err != nil {
			return err
		}
		v.libvirtConn = conn
	}

	// If domain is not created yet, try to get existing one or create it
	if v.domain == nil {
		// First try to lookup existing domain by name
		v.domain, err = conn.LookupDomainByName(v.TestVM.VMName)
		if err != nil {
			// Domain doesn't exist, create it
			domainXML, err := v.parseDomainTemplate()
			if err != nil {
				return fmt.Errorf("unable to parse domain template: %w", err)
			}

			logrus.Debugf("domainXML:\n%s\n\n", domainXML)

			v.domain, err = conn.DomainDefineXMLFlags(domainXML, libvirt.DOMAIN_DEFINE_VALIDATE)
			if err != nil {
				return fmt.Errorf("unable to define virtual machine domain: %w", err)
			}
		} else {
			logrus.Infof("Reusing existing domain %s", v.TestVM.VMName)
		}
	}

	// Check if domain is already running before trying to start it
	state, _, err := v.domain.GetState()
	if err != nil {
		return fmt.Errorf("unable to get domain state: %w", err)
	}

	if state != libvirt.DOMAIN_RUNNING {
		err = v.domain.Create()
		if err != nil {
			return fmt.Errorf("unable to start virtual machine domain: %w", err)
		}
	} else {
		logrus.Infof("Domain %s is already running", v.TestVM.VMName)
	}

	err = v.waitForVMToBeRunning()
	if err != nil {
		return fmt.Errorf("unable to wait for VM to be running: %w", err)
	}

	v.consoleStream, err = conn.NewStream(0)
	if err != nil {
		return fmt.Errorf("unable to create new stream: %w", err)
	}

	err = v.domain.OpenConsole("", v.consoleStream, libvirt.DOMAIN_CONSOLE_FORCE)

	if err != nil {
		return fmt.Errorf("unable to open console: %w", err)
	}

	v.consoleOutput = &bytes.Buffer{}
	v.consoleOutput.Grow(256 * 1024) // grow the buffer to 256kB

	// VM seems to freeze if we request a console and we don't keep reading from it
	go func() {
		defer ginkgo.GinkgoRecover()
		debugConsole := os.Getenv("DEBUG_VM_CONSOLE") == "1"
		if debugConsole {
			fmt.Println("DEBUG_VM_CONSOLE is enabled")
		}

		var buffer [256]byte
		for {
			n, err := v.consoleStream.Recv(buffer[:])
			if err != nil {
				return
			}
			v.consoleMutex.Lock()
			v.consoleOutput.Write(buffer[:n])
			v.consoleMutex.Unlock()

			if debugConsole {
				fmt.Print(string(buffer[:n]))
			}
		}
	}()
	return nil

}

// read console output from the VM
func (v *VMInLibvirt) readConsole() string {
	// under some situations we will try to get output from the console
	// but the VM had failed to start.
	if v.consoleOutput == nil {
		return ""
	}
	v.consoleMutex.Lock()
	defer v.consoleMutex.Unlock()
	rdbuf := make([]byte, 1024)
	str := ""

	for v.consoleOutput.Len() > 0 {
		n, err := v.consoleOutput.Read(rdbuf)
		if err != io.EOF && err != nil {
			logrus.Errorf("Error reading console output: %v", err)
			return str
		}
		str = str + string(rdbuf[:n])
	}

	return str
}

// cummulatively read console output from the VM
func (v *VMInLibvirt) GetConsoleOutput() string {
	// Ensure console output is being captured
	if v.consoleOutput == nil {
		logrus.Warnf("Console output buffer is nil for VM %s - console may not be initialized", v.TestVM.VMName)
		return v.consoleOutputString
	}

	// Read any new console output and add it to the accumulated string
	newOutput := v.readConsole()
	if newOutput != "" {
		logrus.Debugf("VM %s: Read %d bytes of new console output", v.TestVM.VMName, len(newOutput))
	}
	v.consoleOutputString += newOutput
	return v.consoleOutputString
}

// EnsureConsoleStream ensures the console stream is properly established
func (v *VMInLibvirt) EnsureConsoleStream() error {
	// If console stream is already established, return
	if v.consoleStream != nil && v.consoleOutput != nil {
		return nil
	}

	// Check if VM is running
	isRunning, err := v.IsRunning()
	if err != nil {
		return fmt.Errorf("failed to check VM state: %w", err)
	}

	if !isRunning {
		return fmt.Errorf("VM is not running, cannot establish console stream")
	}

	// Use existing connection if available, otherwise create new one
	var conn *libvirt.Connect
	if v.libvirtConn != nil {
		conn = v.libvirtConn
	} else {
		conn, err = libvirt.NewConnect(v.libvirtUri)
		if err != nil {
			return fmt.Errorf("failed to connect to libvirt: %w", err)
		}
		v.libvirtConn = conn
	}

	// Create new console stream
	v.consoleStream, err = conn.NewStream(0)
	if err != nil {
		return fmt.Errorf("failed to create new stream: %w", err)
	}

	err = v.domain.OpenConsole("", v.consoleStream, libvirt.DOMAIN_CONSOLE_FORCE)
	if err != nil {
		return fmt.Errorf("failed to open console: %w", err)
	}

	v.consoleOutput = &bytes.Buffer{}
	v.consoleOutput.Grow(256 * 1024) // grow the buffer to 256kB

	// VM seems to freeze if we request a console and we don't keep reading from it
	go func() {
		defer ginkgo.GinkgoRecover()
		debugConsole := os.Getenv("DEBUG_VM_CONSOLE") == "1"
		if debugConsole {
			fmt.Println("DEBUG_VM_CONSOLE is enabled")
		}

		var buffer [256]byte
		for {
			n, err := v.consoleStream.Recv(buffer[:])
			if err != nil {
				return
			}
			v.consoleMutex.Lock()
			v.consoleOutput.Write(buffer[:n])
			v.consoleMutex.Unlock()

			if debugConsole {
				fmt.Print(string(buffer[:n]))
			}
		}
	}()

	return nil
}

func (v *VMInLibvirt) parseDomainTemplate() (domainXML string, err error) {
	tmpl, err := template.New("domain-template").Parse(domainTemplate)
	if err != nil {
		return "", fmt.Errorf("unable to parse domain template: %w", err)
	}

	var domainXMLBuf bytes.Buffer

	type TemplateParams struct {
		DiskImagePath   string
		Port            string
		PIDFile         string
		SMBios          string
		Name            string
		CloudInitCDRom  string
		CloudInitSMBios string
	}

	templateParams := TemplateParams{
		DiskImagePath: v.TestVM.DiskImagePath,
		Port:          strconv.Itoa(v.TestVM.SSHPort),
		PIDFile:       v.pidFile,
		Name:          v.TestVM.VMName,
	}

	err = v.ParseCloudInit()
	if err != nil {
		return "", fmt.Errorf("unable to set cloud-init: %w", err)
	}

	if v.hasCloudInit {
		templateParams.CloudInitCDRom = fmt.Sprintf(`
			<disk type="file" device="cdrom">
				<driver name="qemu" type="raw"/>
				<source file="%s"></source>
				<target dev="sda" bus="sata"/>
				<readonly/>
			</disk>
		`, v.cloudInitArgs)
	}

	err = tmpl.Execute(&domainXMLBuf, templateParams)
	if err != nil {
		return "", fmt.Errorf("unable to execute domain template: %w", err)
	}

	return domainXMLBuf.String(), nil
}

func (v *VMInLibvirt) waitForVMToBeRunning() error {
	timeout := 60 * time.Second
	elapsed := 0 * time.Second

	for elapsed < timeout {
		state, _, err := v.domain.GetState()

		if err != nil {
			return fmt.Errorf("unable to get VM state: %w", err)
		}

		if state == libvirt.DOMAIN_RUNNING {
			return nil
		}

		time.Sleep(1 * time.Second)
		elapsed += 1 * time.Second
	}

	return fmt.Errorf("VM did not start in %s seconds", timeout)
}

// Delete the VM definition
func (v *VMInLibvirt) Delete() (err error) {
	if v.domain == nil {
		// Try to look up the domain by name
		conn, err := libvirt.NewConnect(v.libvirtUri)
		if err != nil {
			return fmt.Errorf("unable to connect to libvirt: %w", err)
		}
		defer conn.Close()
		domain, err := conn.LookupDomainByName(v.TestVM.VMName)
		if err != nil {
			// If not found, nothing to delete
			return nil
		}
		v.domain = domain
	}

	domainExists, err := v.Exists()
	if err != nil {
		return fmt.Errorf("unable to check if VM exists: %w", err)
	}

	if domainExists {
		err = v.domain.UndefineFlags(libvirt.DOMAIN_UNDEFINE_SNAPSHOTS_METADATA | libvirt.DOMAIN_UNDEFINE_MANAGED_SAVE | libvirt.DOMAIN_UNDEFINE_NVRAM)
		if errors.As(err, &libvirt.Error{Code: libvirt.ERR_INVALID_ARG}) {
			err = v.domain.Undefine()
		}
		if err != nil {
			return fmt.Errorf("unable to undefine VM: %w", err)
		}
	}

	return
}

// Shutdown the VM
func (v *VMInLibvirt) Shutdown() (err error) {
	if err != nil {
		return fmt.Errorf("unable to load existing libvirt domain: %w", err)
	}

	//check if domain is running and shut it down
	isRunning, err := v.IsRunning()
	if err != nil {
		return fmt.Errorf("unable to check if VM is running: %w", err)
	}

	if isRunning {
		err := v.domain.Destroy()
		if err != nil {
			return fmt.Errorf("unable to destroy VM: %w", err)
		}
	}

	return
}

// ForceDelete stops and removes the VM
func (v *VMInLibvirt) ForceDelete() (err error) {
	if v.domain == nil {
		// Try to look up the domain by name
		conn, err := libvirt.NewConnect(v.libvirtUri)
		if err != nil {
			return fmt.Errorf("unable to connect to libvirt: %w", err)
		}
		defer conn.Close()
		domain, err := conn.LookupDomainByName(v.TestVM.VMName)
		if err != nil {
			// If not found, nothing to delete
			return nil
		}
		v.domain = domain
	}

	err = v.Shutdown()
	if err != nil {
		return fmt.Errorf("unable to shutdown VM: %w", err)
	}

	err = v.Delete()
	if err != nil {
		return fmt.Errorf("unable to remove VM: %w", err)
	}

	// Clean up the connection
	if v.libvirtConn != nil {
		v.libvirtConn.Close()
		v.libvirtConn = nil
	}

	return
}

func (v *VMInLibvirt) Exists() (bool, error) {
	conn, err := libvirt.NewConnect(v.libvirtUri)
	if err != nil {
		return false, err
	}
	defer conn.Close()

	var flags libvirt.ConnectListAllDomainsFlags
	domains, err := conn.ListAllDomains(flags)
	if err != nil {
		return false, err

	}
	for _, domain := range domains {
		name, err := domain.GetName()
		if err != nil {
			return false, err
		}

		if name == v.TestVM.VMName {
			return true, nil
		}
	}

	return false, nil
}

func (v *VMInLibvirt) IsRunning() (exists bool, err error) {

	if v.domain == nil {
		return false, nil
	}

	state, _, err := v.domain.GetState()
	if err != nil {
		return false, fmt.Errorf("unable to get VM state: %w", err)
	}

	if state == libvirt.DOMAIN_RUNNING {
		return true, nil
	} else {
		return false, nil
	}
}

func (v *VMInLibvirt) RunAndWaitForSSH() error {
	//check if its running first if it is do not run it again:
	isRunning, err := v.IsRunning()
	if err != nil {
		return fmt.Errorf("failed to check if VM is running: %w", err)
	}

	if !isRunning {
		err := v.Run()
		if err != nil {
			return fmt.Errorf("failed to run VM: %w", err)
		}
	}

	err = v.WaitForSSHToBeReady()
	if err != nil {
		fmt.Println("============ Console output ============")
		fmt.Println(v.GetConsoleOutput())
		fmt.Println("========================================")
		return fmt.Errorf("waiting for SSH: %w", err)
	}
	return nil
}

// CreateSnapshot creates an external snapshot of the VM with memory state
func (v *VMInLibvirt) CreateSnapshot(name string) error {
	if v.domain == nil {
		return fmt.Errorf("VM domain is not initialized")
	}

	// Create external snapshot XML with memory state
	snapshotXML := fmt.Sprintf(`
		<domainsnapshot>
			<name>%s</name>
			<description>Test snapshot for %s</description>
			<memory file="%s" snapshot="external"/>
		</domainsnapshot>
	`, name, v.TestVM.VMName, v.TestVM.MemoryFilePath)

	_, err := v.domain.CreateSnapshotXML(snapshotXML, 0)
	if err != nil {
		return fmt.Errorf("failed to create external snapshot %s: %w", name, err)
	}

	logrus.Infof("Created external snapshot %s for VM %s with memory file %s", name, v.TestVM.VMName, v.TestVM.MemoryFilePath)
	return nil
}

// RevertToSnapshot reverts the VM to a specific snapshot
func (v *VMInLibvirt) RevertToSnapshot(name string) error {
	if v.domain == nil {
		return fmt.Errorf("VM domain is not initialized")
	}

	// Check if VM exists before trying to pause it
	vmExists, err := v.Exists()
	if err != nil {
		return fmt.Errorf("failed to check if VM exists: %w", err)
	}

	if !vmExists {
		return fmt.Errorf("VM %s does not exist, cannot revert to snapshot", v.TestVM.VMName)
	}

	// First, pause the VM
	err = v.Pause()
	if err != nil {
		return fmt.Errorf("failed to pause VM before revert: %w", err)
	}

	// Get the snapshot
	snapshot, err := v.domain.SnapshotLookupByName(name, 0)
	if err != nil {
		return fmt.Errorf("failed to find snapshot %s: %w", name, err)
	}

	// Revert to the snapshot
	err = snapshot.RevertToSnapshot(libvirt.DOMAIN_SNAPSHOT_REVERT_RUNNING)
	if err != nil {
		return fmt.Errorf("failed to revert to snapshot %s: %w", name, err)
	}

	// Wait a moment for the VM to stabilize after revert
	time.Sleep(2 * time.Second)

	// Ensure console stream is properly established after revert
	if err := v.EnsureConsoleStream(); err != nil {
		logrus.Warnf("Failed to ensure console stream after revert: %v", err)
		// Don't fail the revert operation, just log the warning
	}

	logrus.Infof("Reverted VM %s to snapshot %s", v.TestVM.VMName, name)
	return nil
}

// DeleteSnapshot deletes a specific snapshot
func (v *VMInLibvirt) DeleteSnapshot(name string) error {
	if v.domain == nil {
		return fmt.Errorf("VM domain is not initialized")
	}

	// Get the snapshot
	snapshot, err := v.domain.SnapshotLookupByName(name, 0)
	if err != nil {
		return fmt.Errorf("failed to find snapshot %s: %w", name, err)
	}

	// Delete the snapshot
	err = snapshot.Delete(libvirt.DOMAIN_SNAPSHOT_DELETE_CHILDREN)
	if err != nil {
		return fmt.Errorf("failed to delete snapshot %s: %w", name, err)
	}

	logrus.Infof("Deleted snapshot %s from VM %s", name, v.TestVM.VMName)
	return nil
}

// Pause pauses the VM
func (v *VMInLibvirt) Pause() error {
	if v.domain == nil {
		return fmt.Errorf("VM domain is not initialized")
	}

	state, _, err := v.domain.GetState()
	if err != nil {
		return fmt.Errorf("failed to get VM state: %w", err)
	}

	if state == libvirt.DOMAIN_PAUSED {
		logrus.Debugf("VM %s is already paused", v.TestVM.VMName)
		return nil
	}

	err = v.domain.Suspend()
	if err != nil {
		return fmt.Errorf("failed to pause VM %s: %w", v.TestVM.VMName, err)
	}

	logrus.Infof("Paused VM %s", v.TestVM.VMName)
	return nil
}

// Resume resumes the VM
func (v *VMInLibvirt) Resume() error {
	if v.domain == nil {
		return fmt.Errorf("VM domain is not initialized")
	}

	state, _, err := v.domain.GetState()
	if err != nil {
		return fmt.Errorf("failed to get VM state: %w", err)
	}

	if state == libvirt.DOMAIN_RUNNING {
		logrus.Debugf("VM %s is already running", v.TestVM.VMName)
		return nil
	}

	err = v.domain.Resume()
	if err != nil {
		return fmt.Errorf("failed to resume VM %s: %w", v.TestVM.VMName, err)
	}

	logrus.Infof("Resumed VM %s", v.TestVM.VMName)
	return nil
}

// HasSnapshot checks if a snapshot exists
func (v *VMInLibvirt) HasSnapshot(name string) (bool, error) {
	if v.domain == nil {
		return false, fmt.Errorf("VM domain is not initialized")
	}

	// For external snapshots with NO_METADATA flag, check for the actual snapshot files
	// Check for disk snapshot file (external snapshots create new disk files)
	diskSnapshotPath := v.TestVM.DiskImagePath + "." + name
	if _, err := os.Stat(diskSnapshotPath); err == nil {
		logrus.Debugf("Found disk snapshot file: %s", diskSnapshotPath)
		return true, nil
	}

	// Check for memory snapshot file
	if v.TestVM.MemoryFilePath != "" {
		memSnapshotPath := v.TestVM.MemoryFilePath + "." + name
		if _, err := os.Stat(memSnapshotPath); err == nil {
			logrus.Debugf("Found memory snapshot file: %s", memSnapshotPath)
			return true, nil
		}
	}

	// Fallback to libvirt metadata check
	_, err := v.domain.SnapshotLookupByName(name, 0)
	if err != nil {
		if errors.As(err, &libvirt.Error{Code: libvirt.ERR_NO_DOMAIN_SNAPSHOT}) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check snapshot %s: %w", name, err)
	}

	return true, nil
}
