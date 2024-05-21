package vm

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"text/template"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
	"github.com/sirupsen/logrus"
)

//go:embed domain-template.xml
var domainTemplate string

type VMInLibvirt struct {
	domain              libvirt.Domain
	libvirt             *libvirt.Libvirt
	libvirtUri          string
	consoleOutput       *bytes.Buffer
	consoleMutex        sync.Mutex
	consoleOutputString string
	TestVM
}

func getLibvirtUri() string {
	return  "qemu:///session"
}

func NewVM(params TestVM) (*VMInLibvirt, error) {

	if params.LibvirtUri == "" {
		params.LibvirtUri = getLibvirtUri()
	}

	vm := &VMInLibvirt{
		libvirtUri: params.LibvirtUri,
		TestVM:     params,
	}

	vm.pidFile = filepath.Join(params.TestDir, params.VMName+".pid")

	return vm, nil
}

func (v *VMInLibvirt) Run() (error) {

	logrus.Infof("Creating VM %s", v.TestVM.VMName)
	logrus.Infof("libvirt URI: %s", v.libvirtUri)

	uri, err := url.Parse(v.libvirtUri)
	if err != nil {
		return fmt.Errorf("unable to parse libvirt URI: %w", err)
	}

	v.libvirt, err = libvirt.ConnectToURI(uri);
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}

	defer func (){
		if err := v.libvirt.Disconnect(); err != nil {
			logrus.Errorf("Error disconnecting: %v", err)
		}
	}()

	domainXML, err := v.parseDomainTemplate()
	if err != nil {
		return fmt.Errorf("unable to parse domain template: %w", err)
	}

	logrus.Debugf("domainXML:\n%s\n\n", domainXML)

	v.domain, err = v.libvirt.DomainDefineXML(domainXML)
	if err != nil {
		return fmt.Errorf("unable to define virtual machine domain: %w", err)
	}

	if err := v.libvirt.DomainCreate(v.domain); err != nil {
		return fmt.Errorf("unable to start virtual machine domain: %w", err)
	}

	err = v.waitForVMToBeRunning()
	if err != nil {
		return fmt.Errorf("unable to wait for VM to be running: %w", err)
	}

	v.consoleOutput = &bytes.Buffer{}

	if debugConsole := os.Getenv("DEBUG_VM_CONSOLE"); debugConsole == "1" {
		logrus.Infof("Streaming console output for VM %s", v.TestVM.VMName)
		go streamConsole(v.libvirt, v.domain, "", v.consoleOutput)
	}

	return nil
}

func streamConsole(l *libvirt.Libvirt, domain libvirt.Domain, alias string, stream io.Writer) error {
	return l.DomainOpenConsole(domain, libvirt.OptString{alias}, stream, uint32(libvirt.DomainConsoleForce))
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
		state, _, err := v.libvirt.DomainGetState(v.domain, 0)
		if err != nil {
			return fmt.Errorf("unable to get VM state: %w", err)
		}

		if libvirt.DomainState(state) == libvirt.DomainRunning {
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

	if v.Exists() {
		err := v.libvirt.DomainShutdownFlags(v.domain, libvirt.DomainShutdownDefault)
		if err != nil {
			return fmt.Errorf("unable to shutdown VM: %w", err)
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
	if v.IsRunning() {
		err := v.libvirt.DomainShutdown(v.domain)
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

func (v *VMInLibvirt) Exists() bool {
	_, err := v.libvirt.DomainLookupByName(v.TestVM.VMName)
	return err == nil
}

func (v *VMInLibvirt) IsRunning() bool {
	state, _, err := v.libvirt.DomainGetState(v.domain, 0)
	if err != nil {
		logrus.Errorf("unable to get VM state: %v", err)
		return false
	}

	if libvirt.DomainState(state) == libvirt.DomainRunning {
		return true
	} else {
		return false
	}
}

func (v *VMInLibvirt) RunAndWaitForSSH() error {
	err := v.Run()
	if err != nil {
		return fmt.Errorf("failed to run VM: %w", err)
	}

	err = v.WaitForSSHToBeReady()
	if err != nil {
		return fmt.Errorf("waiting for SSH: %w", err)
	}

	return nil
}
