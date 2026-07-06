package service_test

import (
	"encoding/json"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/rendered"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("Device application lifecycle", func() {
	var (
		suite          *ServiceTestSuite
		testKvStore    kvstore.KVStore
		queuesProvider queues.Provider
	)

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()

		// SetDeviceApplicationDesiredState/RestartDeviceApplication notify rendered.Bus on
		// success, so it must be backed by a real KV store + queue for
		// this suite. Reuse one instance for the whole context, same as the GetRenderedDevice
		// AwaitingReconnect suite in device_test.go.
		var err error
		if testKvStore == nil {
			testKvStore, err = kvstore.NewKVStore(suite.Ctx, suite.Log, redisHost, redisPort, redisPassword)
			Expect(err).ToNot(HaveOccurred())
			processID := fmt.Sprintf("device-application-lifecycle-test-%s", uuid.New().String())
			queuesProvider, err = queues.NewRedisProvider(suite.Ctx, suite.Log, processID, redisHost, redisPort, redisPassword, queues.DefaultRetryConfig())
			Expect(err).ToNot(HaveOccurred())
			Expect(rendered.Bus.Initialize(suite.Ctx, testKvStore, queuesProvider, 10*time.Second, suite.Log)).To(Succeed())
		}
	})

	AfterEach(func() {
		suite.Teardown()
	})

	containerApp := func(name string) api.ApplicationProviderSpec {
		containerApp := api.ContainerApplication{
			AppType: api.AppTypeContainer,
			Name:    lo.ToPtr(name),
			Image:   "quay.io/test/app:v1",
		}
		var spec api.ApplicationProviderSpec
		Expect(spec.FromContainerApplication(containerApp)).To(Succeed())
		return spec
	}

	It("overlays a device-level lifecycle override onto the rendered application spec", func() {
		deviceName := "lifecycle-standalone-device"
		device := api.Device{
			Metadata: api.ObjectMeta{Name: lo.ToPtr(deviceName)},
			Spec: &api.DeviceSpec{
				Os:           &api.DeviceOsSpec{Image: "quay.io/fedora/fedora-coreos:stable"},
				Applications: &[]api.ApplicationProviderSpec{containerApp("app-1")},
			},
		}
		_, status := suite.Handler.CreateDevice(suite.Ctx, suite.OrgID, device)
		Expect(status.Code).To(Equal(int32(201)))

		appsJSON, err := json.Marshal([]api.ApplicationProviderSpec{containerApp("app-1")})
		Expect(err).ToNot(HaveOccurred())
		_, err = suite.Store.Device().UpdateRendered(suite.Ctx, suite.OrgID, deviceName, "[]", string(appsJSON), "hash1", nil)
		Expect(err).ToNot(HaveOccurred())

		By("before any lifecycle override, the rendered application should have no desired state")
		renderedDevice, status := suite.Handler.GetRenderedDevice(suite.Ctx, suite.OrgID, deviceName, api.GetRenderedDeviceParams{})
		Expect(status.Code).To(Equal(int32(200)))
		Expect(renderedDevice.Spec.Applications).ToNot(BeNil())
		Expect(*renderedDevice.Spec.Applications).To(HaveLen(1))
		app1, err := (*renderedDevice.Spec.Applications)[0].AsContainerApplication()
		Expect(err).ToNot(HaveOccurred())
		Expect(app1.DesiredState).To(BeNil())

		By("setting the desired state should not require re-rendering to be reflected")
		lifecycle, setStatus := suite.Handler.SetDeviceApplicationDesiredState(suite.Ctx, suite.OrgID, deviceName, "app-1", api.ApplicationDesiredStateStopped)
		Expect(setStatus.Code).To(Equal(int32(200)))
		Expect(lifecycle.DesiredState).ToNot(BeNil())
		Expect(*lifecycle.DesiredState).To(Equal(api.ApplicationDesiredStateStopped))

		renderedDevice, status = suite.Handler.GetRenderedDevice(suite.Ctx, suite.OrgID, deviceName, api.GetRenderedDeviceParams{})
		Expect(status.Code).To(Equal(int32(200)))
		Expect(*renderedDevice.Spec.Applications).To(HaveLen(1))
		app1, err = (*renderedDevice.Spec.Applications)[0].AsContainerApplication()
		Expect(err).ToNot(HaveOccurred())
		Expect(app1.DesiredState).ToNot(BeNil())
		Expect(*app1.DesiredState).To(Equal(api.ApplicationDesiredStateStopped))

		By("the device's declarative spec must remain untouched by the lifecycle override")
		plainDevice, status := suite.Handler.GetDevice(suite.Ctx, suite.OrgID, deviceName)
		Expect(status.Code).To(Equal(int32(200)))
		Expect(*plainDevice.Spec.Applications).To(HaveLen(1))
		plainApp1, err := (*plainDevice.Spec.Applications)[0].AsContainerApplication()
		Expect(err).ToNot(HaveOccurred())
		Expect(plainApp1.DesiredState).To(BeNil())

		By("restarting the application should bump its restart generation independent of desired state")
		lifecycle, restartStatus := suite.Handler.RestartDeviceApplication(suite.Ctx, suite.OrgID, deviceName, "app-1")
		Expect(restartStatus.Code).To(Equal(int32(200)))
		Expect(lifecycle.RestartGeneration).ToNot(BeNil())
		// restartGeneration is set to the device's next rendered version, not a simple +1
		// from a prior value, so it should reflect the version bump from the SetDeviceApplicationDesiredState
		// call above plus this call's own bump.
		Expect(*lifecycle.RestartGeneration).To(BeNumerically(">", 0))
		Expect(lifecycle.DesiredState).ToNot(BeNil())
		Expect(*lifecycle.DesiredState).To(Equal(api.ApplicationDesiredStateStopped))

		By("restarting again should strictly increase the restart generation")
		lifecycle2, restartStatus2 := suite.Handler.RestartDeviceApplication(suite.Ctx, suite.OrgID, deviceName, "app-1")
		Expect(restartStatus2.Code).To(Equal(int32(200)))
		Expect(lifecycle2.RestartGeneration).ToNot(BeNil())
		Expect(*lifecycle2.RestartGeneration).To(BeNumerically(">", *lifecycle.RestartGeneration))

		By("setting the desired state back to running should clear the lifecycle override entirely and revert the rendered application to the declarative spec")
		_, runStatus := suite.Handler.SetDeviceApplicationDesiredState(suite.Ctx, suite.OrgID, deviceName, "app-1", api.ApplicationDesiredStateRunning)
		Expect(runStatus.Code).To(Equal(int32(200)))

		renderedDevice, status = suite.Handler.GetRenderedDevice(suite.Ctx, suite.OrgID, deviceName, api.GetRenderedDeviceParams{})
		Expect(status.Code).To(Equal(int32(200)))
		app1, err = (*renderedDevice.Spec.Applications)[0].AsContainerApplication()
		Expect(err).ToNot(HaveOccurred())
		Expect(app1.DesiredState).To(BeNil())
		Expect(app1.RestartGeneration).To(BeNil())

		_, getStatus := suite.Handler.GetDeviceApplicationLifecycle(suite.Ctx, suite.OrgID, deviceName, "app-1")
		Expect(getStatus.Code).To(Equal(int32(404)))
	})

	It("rejects lifecycle changes for applications that are not part of the device's spec", func() {
		deviceName := "lifecycle-missing-app-device"
		device := api.Device{
			Metadata: api.ObjectMeta{Name: lo.ToPtr(deviceName)},
			Spec: &api.DeviceSpec{
				Os: &api.DeviceOsSpec{Image: "quay.io/fedora/fedora-coreos:stable"},
			},
		}
		_, status := suite.Handler.CreateDevice(suite.Ctx, suite.OrgID, device)
		Expect(status.Code).To(Equal(int32(201)))

		_, setStatus := suite.Handler.SetDeviceApplicationDesiredState(suite.Ctx, suite.OrgID, deviceName, "does-not-exist", api.ApplicationDesiredStateStopped)
		Expect(setStatus.Code).To(Equal(int32(404)))
	})
})
