package e2e

import (
	"bytes"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/test/harness/e2e/vm"
	"github.com/onsi/gomega"
)

// recordingDevice captures the commands a harness runs on the device instead of executing them.
type recordingDevice struct {
	vm.TestVMInterface
	commands []string
}

func (device *recordingDevice) RunSSH(inputArgs []string, _ *bytes.Buffer) (*bytes.Buffer, error) {
	device.commands = append(device.commands, strings.Join(inputArgs, " "))
	return &bytes.Buffer{}, nil
}

// unwrapDeviceCommand strips the "bash -lc '<command>'" wrapper vmShellCommandArgs adds.
func unwrapDeviceCommand(t *testing.T, recorded string) string {
	t.Helper()
	quoted, found := strings.CutPrefix(recorded, "bash -lc ")
	if !found {
		t.Fatalf("recorded device command %q is not wrapped by vmShellCommandArgs", recorded)
	}
	return strings.ReplaceAll(strings.Trim(quoted, "'"), `'"'"'`, "'")
}

// TestCaptureStandardEvidenceRunsDeviceCommandsAsRoot pins every device-side evidence command to
// root. The agent runs as root, so its container images and /var/lib/flightctl state are only
// visible to root's podman and filesystem view; and on a container-backed device an unprivileged
// podman additionally cannot allocate a nested user namespace at all (the device container is
// itself started by the CI runner's rootless podman), so a missing sudo fails the whole capture
// rather than merely reading the wrong store.
func TestCaptureStandardEvidenceRunsDeviceCommandsAsRoot(t *testing.T) {
	// CaptureHostCLI reaches Harness.ApiEndpoint, which makes a Gomega assertion on API_ENDPOINT.
	gomega.RegisterTestingT(t)
	t.Setenv("API_ENDPOINT", "https://api-endpoint.invalid")
	device := &recordingDevice{}
	harness := &Harness{VM: device}

	// CaptureHostCLI shells out to the flightctl binary, which a unit test has no deployment for.
	// Every device command under test runs before that point, so its error is not relevant here.
	_ = harness.CaptureStandardEvidence(t.TempDir(), "device-under-test")

	if len(device.commands) == 0 {
		t.Fatal("CaptureStandardEvidence ran no device commands")
	}
	var sawPodman bool
	for _, recorded := range device.commands {
		if strings.Contains(recorded, "podman") {
			sawPodman = true
		}
	}
	if !sawPodman {
		t.Fatal("CaptureStandardEvidence no longer captures podman images; this test guards that command's privileges")
	}
	for _, recorded := range device.commands {
		command := unwrapDeviceCommand(t, recorded)
		if !strings.HasPrefix(command, "sudo ") {
			t.Errorf("device evidence command %q must run as root, because the agent's images and state are root-owned and rootless podman is unavailable on container-backed devices", command)
		}
	}
}
