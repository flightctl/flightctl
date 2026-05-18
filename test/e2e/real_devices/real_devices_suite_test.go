package real_devices_test

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var (
	device *e2e.RealDevice
)

func TestRealDevices(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Real Devices E2E Suite")
}

var _ = BeforeSuite(func() {
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())

	_, _, err := e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).ToNot(HaveOccurred(), "failed to setup harness")

	harness := e2e.GetWorkerHarness()

	_, err = login.LoginToAPIWithToken(harness)
	Expect(err).ToNot(HaveOccurred(), "login failed")

	device, err = e2e.NewRealDeviceFromEnv()
	Expect(err).ToNot(HaveOccurred(), "failed to create real device from env")

	if os.Getenv("REAL_DEVICE_ID") == "" {
		provisionFormat := os.Getenv("REAL_DEVICE_PROVISION_FORMAT")
		if provisionFormat == "" {
			provisionFormat = "cloud-init"
		}
		err = device.ProvisionAgent(harness, provisionFormat)
		Expect(err).ToNot(HaveOccurred(), "failed to provision agent on device")
	}

	fleetYAMLPath := os.Getenv("REAL_DEVICE_FLEET_YAML")
	Expect(fleetYAMLPath).ToNot(BeEmpty(), "REAL_DEVICE_FLEET_YAML environment variable is required")

	fleetYAML, err := os.ReadFile(fleetYAMLPath)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("failed to read fleet YAML from %s", fleetYAMLPath))

	Eventually(func() error {
		_, applyErr := harness.CLIWithStdin(string(fleetYAML), "apply", "-f", "-")
		return applyErr
	}).WithTimeout(15 * time.Second).WithPolling(2 * time.Second).Should(Succeed(), "apply fleet YAML")
})

var _ = BeforeEach(func() {
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()
	ctx := util.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)
})

var _ = AfterEach(func() {
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()
	harness.SetTestContext(suiteCtx)
})

var _ = AfterSuite(func() {
	harness := e2e.GetWorkerHarness()

	_, err := login.LoginToAPIWithToken(harness)
	if err != nil {
		logrus.Warnf("Failed to restore admin login for cleanup: %v", err)
	}

	fleetYAMLPath := os.Getenv("REAL_DEVICE_FLEET_YAML")
	if fleetYAMLPath != "" {
		_, err := harness.CLI("delete", "-f", fleetYAMLPath)
		if err != nil {
			logrus.Warnf("Failed to delete fleet: %v", err)
		}
	}

	if device != nil && os.Getenv("REAL_DEVICE_ID") == "" {
		if err := device.UninstallAgent(); err != nil {
			logrus.Warnf("Failed to uninstall agent: %v", err)
		}
	}
})
