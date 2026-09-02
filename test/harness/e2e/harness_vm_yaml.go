package e2e

import (
	"bytes"
	_ "embed"
	"fmt"
	"strconv"
	"strings"
	"text/template"
)

//go:embed vm-template.yaml
var vmYAMLTemplateText string

//go:embed cloud-config-fedora.yaml.tmpl
var fedoraCloudConfigTemplateText string

var vmTemplateFuncs = template.FuncMap{
	"indent":    indentLines,
	"yamlQuote": yamlQuote,
}

var vmYAMLTemplate = template.Must(template.New("vm.yaml").Funcs(vmTemplateFuncs).Parse(vmYAMLTemplateText))

var fedoraCloudConfigTemplate = template.Must(
	template.New("cloud-config-fedora").Funcs(vmTemplateFuncs).Parse(fedoraCloudConfigTemplateText),
)

type vmYAMLParams struct {
	Name           string
	GuestMemory    string
	Image          string
	CPUCores       int
	UserData       string
	UserDataBase64 string
}

type fedoraCloudConfigParams struct {
	Password        string
	FaillockCommand string
}

func renderTemplate(tmpl *template.Template, data any) string {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		panic("rendering " + tmpl.Name() + ": " + err.Error())
	}
	return buf.String()
}

func renderVMYAML(p vmYAMLParams) string {
	return renderTemplate(vmYAMLTemplate, p)
}

func indentLines(spaces int, s string) string {
	if s == "" {
		return ""
	}
	prefix := strings.Repeat(" ", spaces)
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func yamlQuote(s string) string {
	return strconv.Quote(s)
}

// VMYAML builds a KubeVirt VirtualMachine manifest for e2e tests using cloudInitNoCloud userData.
func VMYAML(name, guestMemory, image, cloudInitUserData string) string {
	return renderVMYAML(vmYAMLParams{
		Name:        name,
		GuestMemory: guestMemory,
		Image:       image,
		UserData:    cloudInitUserData,
	})
}

// VMGuestDisableFaillockCommand returns a cloud-init runcmd that removes pam_faillock
// from the PAM stack via authselect and clears any lockout already recorded for user.
// write_files deny=0 in faillock.conf is the persistent setting; this runcmd still
// resets a lock taken in the first-boot window before that file exists. Failures are
// ignored so non-authselect images still boot.
func VMGuestDisableFaillockCommand(user string) string {
	return fmt.Sprintf(`bash -lc "authselect disable-feature with-faillock >/dev/null 2>&1 || true; faillock --user %s --reset >/dev/null 2>&1 || true"`, user)
}

// VMFedoraNoCloudUserData returns cloud-init userData that enables password SSH for the fedora user.
func VMFedoraNoCloudUserData(password string) string {
	return renderTemplate(fedoraCloudConfigTemplate, fedoraCloudConfigParams{
		Password:        password,
		FaillockCommand: VMGuestDisableFaillockCommand(VMFedoraGuestUser),
	})
}

// VMYAMLWithCPU builds a KubeVirt VirtualMachine manifest. cpuCores <= 0 omits the cpu block.
func VMYAMLWithCPU(name, guestMemory, image string, cpuCores int, cloudInitUserData string) string {
	return renderVMYAML(vmYAMLParams{
		Name:        name,
		GuestMemory: guestMemory,
		Image:       image,
		CPUCores:    cpuCores,
		UserData:    cloudInitUserData,
	})
}

// VMYAMLWithConfigDrive builds a VM manifest using cloudInitConfigDrive userDataBase64.
func VMYAMLWithConfigDrive(name, guestMemory, image, userDataBase64 string) string {
	return renderVMYAML(vmYAMLParams{
		Name:           name,
		GuestMemory:    guestMemory,
		Image:          image,
		UserDataBase64: userDataBase64,
	})
}
