package e2e

import (
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
