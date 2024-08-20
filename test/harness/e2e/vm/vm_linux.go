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

	vm = &VMInLibvirt{
		libvirtUri: params.LibvirtUri,
		TestVM:     params}

	vm.pidFile = filepath.Join(params.TestDir, params.VMName+".pid")

	return vm, nil
}

func (v *VMInLibvirt) Run() error {

	logrus.Infof("Creating VM %s", v.TestVM.VMName)
	conn, err := libvirt.NewConnect(v.libvirtUri)
	if err != nil {
		return err
	}
	defer conn.Close()

	domainXML, err := v.parseDomainTemplate()
	if err != nil {
		return fmt.Errorf("unable to parse domain template: %w", err)
	}

	logrus.Debugf("domainXML:\n%s\n\n", domainXML)

	v.domain, err = conn.DomainDefineXMLFlags(domainXML, libvirt.DOMAIN_DEFINE_VALIDATE)
	if err != nil {
		return fmt.Errorf("unable to define virtual machine domain: %w", err)
	}

	err = v.domain.Create()
	if err != nil {
		return fmt.Errorf("unable to start virtual machine domain: %w", err)
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
	v.consoleOutputString += v.readConsole()
	return v.consoleOutputString
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
	if err != nil {
		return fmt.Errorf("unable to load existing libvirt domain: %w", err)
	}

	domainExists, err := v.Exists()
	if err != nil {
		return fmt.Errorf("unable to check if VM exists: %w", err)
	}

	if domainExists {
		err = v.domain.UndefineFlags(libvirt.DOMAIN_UNDEFINE_NVRAM)
		if errors.As(err, &libvirt.Error{Code: libvirt.ERR_INVALID_ARG}) {
			err = v.domain.Undefine()
		}

		if err != nil {
			return fmt.Errorf("unable to undefine VM: %w", err)
		}
		logrus.Infof("Deleted VM: %s", v.TestVM.VMName)
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
	err = v.Shutdown()
	if err != nil {
		return fmt.Errorf("unable to shutdown VM: %w", err)
	}

	err = v.Delete()
	if err != nil {
		return fmt.Errorf("unable to remove VM: %w", err)
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
	err := v.Run()
	if err != nil {
		return fmt.Errorf("failed to run VM: %w", err)
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
