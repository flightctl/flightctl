package e2e

import (
	"errors"
	"io"
	"net/url"
	"slices"
	"strings"
	"testing"
)

const (
	redactionTokenValue     = "value-for-token-flag"
	redactionPasswordValue  = "value-for-p-flag"
	redactionClientKeyValue = "value-for-client-key-flag"
)

// TestIsCLIRateLimitOutputDetectsLoginRateLimit verifies auth rate-limit output is retried.
func TestIsCLIRateLimitOutputDetectsLoginRateLimit(t *testing.T) {
	if !isCLIRateLimitOutput(cliLoginRateLimitExceededOutput) {
		t.Fatalf("expected login rate-limit output to be classified as rate-limited")
	}
}

// TestIsCLIRateLimitOutputPreservesExisting429Checks verifies existing 429 retry detection.
func TestIsCLIRateLimitOutputPreservesExisting429Checks(t *testing.T) {
	cases := []string{
		cliRateLimitResponseStatus,
		cliRateLimitServerReturned,
	}

	for _, output := range cases {
		if !isCLIRateLimitOutput(output) {
			t.Fatalf("expected %q to be classified as rate-limited", output)
		}
	}
}

// TestIsRetryableAPIWriteErrorRequiresTransportError verifies API response errors are not
// retried just because their formatted status message contains transport-looking text.
func TestIsRetryableAPIWriteErrorRequiresTransportError(t *testing.T) {
	apiStatusErr := errors.New("unexpected status creating/updating fleet test: 400: EOF in validation message")
	if isRetryableAPIWriteError(apiStatusErr) {
		t.Fatalf("expected formatted API status error not to be classified as retryable")
	}
}

// TestIsRetryableAPIWriteErrorAllowsTransportErrors verifies marked and typed transport
// failures are still retried.
func TestIsRetryableAPIWriteErrorAllowsTransportErrors(t *testing.T) {
	cases := []error{
		apiWriteTransportError{operation: "replace fleet test", err: io.EOF},
		&url.Error{Op: "Put", URL: "https://flightctl.example/api/v1/fleets/test", Err: io.ErrUnexpectedEOF},
		apiWriteTransportError{operation: "replace fleet test", err: errors.New("server closed idle connection")},
	}

	for _, err := range cases {
		if !isRetryableAPIWriteError(err) {
			t.Fatalf("expected %q to be classified as retryable", err)
		}
	}
}

// TestRedactCommandArgsRemovesSensitiveValues verifies CLI logs do not expose credentials.
func TestRedactCommandArgsRemovesSensitiveValues(t *testing.T) {
	args := []string{
		"flightctl",
		"login",
		"--token",
		redactionTokenValue,
		"-p",
		redactionPasswordValue,
		"--client-key=" + redactionClientKeyValue,
		"get",
		"devices",
	}

	redacted := redactCommandArgs(args)
	redactedLog := strings.Join(redacted, " ")
	for _, value := range []string{redactionTokenValue, redactionPasswordValue, redactionClientKeyValue} {
		if strings.Contains(redactedLog, value) {
			t.Fatalf("expected %q to be redacted from %v", value, redacted)
		}
	}
	if !slices.Contains(redacted, "devices") {
		t.Fatalf("expected non-sensitive args to be preserved in %v", redacted)
	}
}

func TestVMFedoraNoCloudUserDataWhenEnablingPasswordSSHItShouldResetFaillock(t *testing.T) {
	password := t.Name()
	got := VMFedoraNoCloudUserData(password)
	for _, field := range []string{
		"ssh_pwauth",
		"password",
		"faillock-conf",
		"faillock-deny",
		"faillock-disable",
		"faillock-reset",
	} {
		var present bool
		switch field {
		case "ssh_pwauth":
			present = strings.Contains(got, "ssh_pwauth: true")
		case "password":
			present = strings.Contains(got, "password: "+yamlQuote(password))
		case "faillock-conf":
			present = strings.Contains(got, "path: /etc/security/faillock.conf")
		case "faillock-deny":
			present = strings.Contains(got, "deny = 0")
		case "faillock-disable":
			present = strings.Contains(got, "authselect disable-feature with-faillock")
		case "faillock-reset":
			present = strings.Contains(got, "faillock --user "+VMFedoraGuestUser+" --reset")
		}
		if !present {
			t.Fatalf("cloud-init userData is missing required field %s", field)
		}
	}
}

func TestVMFedoraNoCloudUserDataWhenPasswordHasYAMLMetacharactersItShouldQuoteTheScalar(t *testing.T) {
	password := `p:ass"word`
	got := VMFedoraNoCloudUserData(password)
	if !strings.Contains(got, "password: "+yamlQuote(password)) {
		t.Fatal("cloud-init userData is missing a quoted password field")
	}
}

func TestVMYAMLWhenBuildingNoCloudManifestItShouldEmbedIndentedUserData(t *testing.T) {
	userData := VMFedoraNoCloudUserData(t.Name())
	got := VMYAML("test-vm", "1024M", "quay.io/example/fedora:40", userData)
	for _, field := range []string{
		"kind",
		"guest-memory",
		"cloud-init-nocloud",
		"user-data",
		"no-cpu",
	} {
		var present bool
		switch field {
		case "kind":
			present = strings.Contains(got, "kind: VirtualMachine")
		case "guest-memory":
			present = strings.Contains(got, "guest: 1024M")
		case "cloud-init-nocloud":
			present = strings.Contains(got, "cloudInitNoCloud:")
		case "user-data":
			present = strings.Contains(got, "ssh_pwauth: true")
		case "no-cpu":
			present = !strings.Contains(got, "cpu:")
		}
		if !present {
			t.Fatalf("vm.yaml is missing required field %s", field)
		}
	}
}

func TestVMYAMLWithCPUWhenCoresAreSetItShouldRenderCPUBlock(t *testing.T) {
	got := VMYAMLWithCPU("test-vm", "2G", "quay.io/example/fedora:41", 2, VMFedoraNoCloudUserData(t.Name()))
	if !strings.Contains(got, "cores: 2") {
		t.Fatal("vm.yaml is missing cpu cores")
	}
}

func TestVMYAMLWithConfigDriveWhenUserDataIsBase64ItShouldUseConfigDriveVolume(t *testing.T) {
	got := VMYAMLWithConfigDrive("test-vm", "1024M", "quay.io/example/fedora:40", "YWJj")
	if !strings.Contains(got, "cloudInitConfigDrive:") {
		t.Fatal("vm.yaml is missing cloudInitConfigDrive volume")
	}
	if !strings.Contains(got, "userDataBase64: YWJj") {
		t.Fatal("vm.yaml is missing config-drive userDataBase64")
	}
	if strings.Contains(got, "cloudInitNoCloud:") {
		t.Fatal("vm.yaml included cloudInitNoCloud for a config-drive manifest")
	}
}

func TestVMYAMLWithHostVolumesWhenDisksAreSetItShouldRenderHostAndDataVolumes(t *testing.T) {
	got := VMYAMLWithHostVolumes(
		"test-vm",
		"1024M",
		"quay.io/example/fedora:40",
		VMFedoraNoCloudUserData(t.Name()),
		"/var/lib/flightctl/vm-data/test-vm.img",
		"10M",
	)
	for _, field := range []string{
		"host-data-disk",
		"host-data-volume",
		"extradata-disk",
		"extradata-volume",
		"data-volume-template",
	} {
		var present bool
		switch field {
		case "host-data-disk":
			present = strings.Contains(got, "name: host-data")
		case "host-data-volume":
			present = strings.Contains(got, "path: /var/lib/flightctl/vm-data/test-vm.img")
		case "extradata-disk":
			present = strings.Contains(got, "name: extradata")
		case "extradata-volume":
			present = strings.Contains(got, "dataVolume:")
		case "data-volume-template":
			present = strings.Contains(got, "storage: 10M")
		}
		if !present {
			t.Fatalf("vm.yaml is missing required field %s", field)
		}
	}
}

func TestVMYAMLWithHostVolumesWhenPathsAreEmptyItShouldOmitExtraDisks(t *testing.T) {
	got := VMYAMLWithHostVolumes("test-vm", "1024M", "quay.io/example/fedora:40", VMFedoraNoCloudUserData(t.Name()), "", "")
	if strings.Contains(got, "host-data") {
		t.Fatal("vm.yaml included host-data when hostDiskPath was empty")
	}
	if strings.Contains(got, "extradata") {
		t.Fatal("vm.yaml included extradata when extraDataSize was empty")
	}
}

func TestVMFedoraNoCloudUserDataWithWhenExtraFilesAreSetItShouldKeepFaillockAndAppendEntries(t *testing.T) {
	got := VMFedoraNoCloudUserDataWith(t.Name(), []VMCloudInitWriteFile{{
		Path:        "/etc/sudoers.d/fedora",
		Owner:       "root:root",
		Permissions: "0440",
		Content:     "fedora ALL=(ALL) NOPASSWD:ALL",
	}}, []string{"/usr/local/bin/verify-vm-host-volumes.sh"})
	for _, field := range []string{
		"faillock-conf",
		"sudoers",
		"setup-script-runcmd",
	} {
		var present bool
		switch field {
		case "faillock-conf":
			present = strings.Contains(got, "path: /etc/security/faillock.conf")
		case "sudoers":
			present = strings.Contains(got, "path: /etc/sudoers.d/fedora")
		case "setup-script-runcmd":
			present = strings.Contains(got, "/usr/local/bin/verify-vm-host-volumes.sh")
		}
		if !present {
			t.Fatalf("cloud-init userData is missing required field %s", field)
		}
	}
}
