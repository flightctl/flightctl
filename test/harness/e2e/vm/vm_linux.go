package vm

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
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

type BootcVMLinux struct {
	domain              *libvirt.Domain
	libvirtUri          string
	consoleOutput       *bytes.Buffer
	consoleMutex        sync.Mutex
	consoleStream       *libvirt.Stream
	consoleOutputString string
	BootcVMCommon
}

func getLibvirtUri() string {
	return "qemu:///session"
}

func NewVM(params NewVMParameters) (vm *BootcVMLinux, err error) {

	if params.LibvirtUri == "" {
		params.LibvirtUri = getLibvirtUri()
	}

	vm = &BootcVMLinux{
		libvirtUri: params.LibvirtUri,
		BootcVMCommon: BootcVMCommon{
			vmName:        params.VMName,
			diskImagePath: params.DiskImagePath,
			testDir:       params.TestDir,
			pidFile:       filepath.Join(params.TestDir, params.VMName+".pid"),
		},
	}

	return vm, nil
}

func (v *BootcVMLinux) Run(params RunVMParameters) error {
	v.sshPort = params.SSHPort
	v.removeVm = params.RemoveVm
	v.cmd = params.Cmd
	v.hasCloudInit = params.CloudInitData
	v.cloudInitDir = params.CloudInitDir
	v.vmUsername = params.VMUser
	v.sshPassword = params.SSHPassword

	fmt.Printf("Creating VM %s\n", v.imageID)
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
	// VM seems to freeze if we request a console and we don't keep reading from it
	go func() {
		defer ginkgo.GinkgoRecover()
		var buffer [256]byte
		for {
			n, err := v.consoleStream.Recv(buffer[:])
			if err != nil {
				return
			}
			v.consoleMutex.Lock()
			v.consoleOutput.Write(buffer[:n])
			v.consoleMutex.Unlock()
		}
	}()
	return nil

}

// read console output from the VM
func (v *BootcVMLinux) ReadConsole() string {
	v.consoleMutex.Lock()
	defer v.consoleMutex.Unlock()
	return v.consoleOutput.String()
}

// cummulatively read console output from the VM
func (v *BootcVMLinux) GetConsoleOutput() string {
	v.consoleOutputString += v.ReadConsole()
	return v.consoleOutputString
}

func (v *BootcVMLinux) parseDomainTemplate() (domainXML string, err error) {
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
		DiskImagePath: v.diskImagePath,
		Port:          strconv.Itoa(v.sshPort),
		PIDFile:       v.pidFile,
		Name:          v.vmName,
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

func (v *BootcVMLinux) waitForVMToBeRunning() error {
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
func (v *BootcVMLinux) Delete() (err error) {
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
	}

	return
}

// Shutdown the VM
func (v *BootcVMLinux) Shutdown() (err error) {
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
func (v *BootcVMLinux) ForceDelete() (err error) {
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

func (v *BootcVMLinux) Exists() (bool, error) {
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

		if name == v.vmName {
			return true, nil
		}
	}

	return false, nil
}

func (v *BootcVMLinux) IsRunning() (exists bool, err error) {
	if err != nil {
		return false, fmt.Errorf("unable to load existing libvirt domain: %w", err)
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
