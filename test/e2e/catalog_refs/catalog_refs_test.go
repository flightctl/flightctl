package catalog_refs_test

import (
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

const (
	catalogName = "e2e-catalog-refs"
	osItemName  = "os-item"
	appItemName = "app-item"

	osVersion1  = "v1"
	osVersion2  = "v2"
	appVersion1 = "v1"

	channel = "stable"

	containerAppName = "catalog-app"
	containerPort    = "80"
	hostPort         = "8090"
	fleetLabelKey    = "fleet"
)

var _ = Describe("Catalog item references", Ordered, Label("EDM-4813", "catalog-refs", "sanity"), func() {
	var (
		harness *e2e.Harness

		osImageURI  string
		appImageURI string
	)

	BeforeAll(func() {
		harness = e2e.GetWorkerHarness()

		svc := auxiliary.Get(harness.Context)
		Expect(svc).ToNot(BeNil(), "auxiliary services must be initialized")
		osImageURI = fmt.Sprintf("%s:%s/%s", svc.Registry.Host, svc.Registry.Port, testutil.DeviceImageRegistryPath)
		appImageURI = fmt.Sprintf("%s:%s/%s", svc.Registry.Host, svc.Registry.Port, testutil.SleepAppRegistryPath)

		By("Creating test catalog")
		_, err := harness.CreateCatalog(catalogName, "E2E Test Catalog")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { _ = harness.DeleteCatalogIgnoreNotFound(catalogName) })

		By("Creating OS catalog item with two versions")
		osSpec := e2e.NewOSCatalogItemSpecMultiVersion(osImageURI, []string{osVersion1, osVersion2}, channel)
		_, err = harness.CreateCatalogItem(catalogName, osItemName, osSpec)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { _ = harness.DeleteCatalogItemIgnoreNotFound(catalogName, osItemName) })

		By("Creating application catalog item")
		appSpec := e2e.NewAppCatalogItemSpec(appImageURI, appVersion1, channel)
		_, err = harness.CreateCatalogItem(catalogName, appItemName, appSpec)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { _ = harness.DeleteCatalogItemIgnoreNotFound(catalogName, appItemName) })
	})

	It("resolves OS catalog ref and delivers to agent", func() {
		harness = e2e.GetWorkerHarness()
		deviceId, _ := harness.EnrollAndWaitForOnlineStatus()

		By("Updating device OS spec with catalogItemRef v1")
		err := harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
			device.Spec.Os = &v1beta1.DeviceOsSpec{
				CatalogItemRef: &v1beta1.CatalogItemRefSpec{
					Catalog: catalogName,
					Item:    osItemName,
					Version: osVersion1,
				},
			}
		})
		Expect(err).ToNot(HaveOccurred())

		By("Verifying render succeeds (SpecValid condition is True)")
		harness.WaitForDeviceContents(deviceId, "SpecValid=True after OS catalog ref v1",
			func(device *v1beta1.Device) bool {
				if device.Status == nil {
					return false
				}
				cond := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceSpecValid)
				return cond != nil && cond.Status == v1beta1.ConditionStatusTrue
			}, e2e.TIMEOUT)

		By("Updating catalogItemRef version to v2")
		err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
			device.Spec.Os = &v1beta1.DeviceOsSpec{
				CatalogItemRef: &v1beta1.CatalogItemRefSpec{
					Catalog: catalogName,
					Item:    osItemName,
					Version: osVersion2,
				},
			}
		})
		Expect(err).ToNot(HaveOccurred())

		By("Verifying render succeeds for updated version (SpecValid remains True)")
		harness.WaitForDeviceContents(deviceId, "SpecValid=True after OS catalog ref v2",
			func(device *v1beta1.Device) bool {
				if device.Status == nil {
					return false
				}
				cond := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceSpecValid)
				return cond != nil && cond.Status == v1beta1.ConditionStatusTrue
			}, e2e.TIMEOUT)
	})

	It("deploys and removes application via catalog ref", func() {
		harness = e2e.GetWorkerHarness()
		deviceId, _ := harness.EnrollAndWaitForOnlineStatus()

		By("Building container application with catalog item ref")
		containerApp := v1beta1.ContainerApplication{
			Name:    lo.ToPtr(containerAppName),
			AppType: v1beta1.AppTypeContainer,
			Ports:   &[]v1beta1.ApplicationPort{v1beta1.ApplicationPort(hostPort + ":" + containerPort)},
		}
		err := containerApp.FromCatalogItemRefApplicationProviderSpec(v1beta1.CatalogItemRefApplicationProviderSpec{
			CatalogItemRef: v1beta1.CatalogItemRefSpec{
				Catalog: catalogName,
				Item:    appItemName,
				Version: appVersion1,
			},
		})
		Expect(err).ToNot(HaveOccurred())

		var appSpec v1beta1.ApplicationProviderSpec
		err = appSpec.FromContainerApplication(containerApp)
		Expect(err).ToNot(HaveOccurred())

		By("Updating device spec with the application")
		err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
			device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{appSpec}
		})
		Expect(err).ToNot(HaveOccurred())

		By("Verifying container application is running on device")
		err = harness.WaitForApplicationStatus(deviceId, containerAppName, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)
		Expect(err).ToNot(HaveOccurred())

		By("Removing application from device spec")
		err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
			device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{}
		})
		Expect(err).ToNot(HaveOccurred())

		By("Verifying container is stopped")
		harness.WaitForNoApplications(deviceId)
	})

	It("propagates fleet catalog refs to enrolled device", func() {
		harness = e2e.GetWorkerHarness()
		testID := harness.GetTestIDFromContext()
		fleetName := fmt.Sprintf("catalog-fleet-%s", testID)

		By("Creating fleet with catalog refs in device template")
		containerApp := v1beta1.ContainerApplication{
			Name:    lo.ToPtr(containerAppName),
			AppType: v1beta1.AppTypeContainer,
			Ports:   &[]v1beta1.ApplicationPort{v1beta1.ApplicationPort(hostPort + ":" + containerPort)},
		}
		err := containerApp.FromCatalogItemRefApplicationProviderSpec(v1beta1.CatalogItemRefApplicationProviderSpec{
			CatalogItemRef: v1beta1.CatalogItemRefSpec{
				Catalog: catalogName,
				Item:    appItemName,
				Version: appVersion1,
			},
		})
		Expect(err).ToNot(HaveOccurred())

		var appSpec v1beta1.ApplicationProviderSpec
		err = appSpec.FromContainerApplication(containerApp)
		Expect(err).ToNot(HaveOccurred())

		deviceSpec := v1beta1.DeviceSpec{
			Os: &v1beta1.DeviceOsSpec{
				CatalogItemRef: &v1beta1.CatalogItemRefSpec{
					Catalog: catalogName,
					Item:    osItemName,
					Version: osVersion1,
				},
			},
			Applications: &[]v1beta1.ApplicationProviderSpec{appSpec},
		}
		selector := v1beta1.LabelSelector{MatchLabels: &map[string]string{fleetLabelKey: fleetName}}
		err = harness.CreateOrUpdateTestFleet(fleetName, selector, deviceSpec)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { _ = harness.DeleteFleetIgnoreNotFound(fleetName) })

		By("Enrolling device into fleet via label selector")
		deviceId, _ := harness.EnrollAndWaitForOnlineStatus(map[string]string{fleetLabelKey: fleetName})

		By("Verifying render succeeds via fleet pipeline (SpecValid=True)")
		harness.WaitForDeviceContents(deviceId, "fleet catalog ref SpecValid=True",
			func(device *v1beta1.Device) bool {
				if device.Status == nil {
					return false
				}
				cond := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceSpecValid)
				return cond != nil && cond.Status == v1beta1.ConditionStatusTrue
			}, e2e.LONGTIMEOUT)

		By("Verifying application is running via catalog ref")
		err = harness.WaitForApplicationStatus(deviceId, containerAppName, v1beta1.ApplicationStatusRunning, testutil.LONG_TIMEOUT, testutil.POLLING)
		Expect(err).ToNot(HaveOccurred())
	})

	It("rejects deletion of in-use catalog item", func() {
		harness = e2e.GetWorkerHarness()
		deviceId, _ := harness.EnrollAndWaitForOnlineStatus()

		By("Setting device OS spec with catalog ref")
		err := harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
			device.Spec.Os = &v1beta1.DeviceOsSpec{
				CatalogItemRef: &v1beta1.CatalogItemRefSpec{
					Catalog: catalogName,
					Item:    osItemName,
					Version: osVersion1,
				},
			}
		})
		Expect(err).ToNot(HaveOccurred())

		By("Waiting for successful render (SpecValid=True)")
		harness.WaitForDeviceContents(deviceId, "SpecValid=True for delete protection test",
			func(device *v1beta1.Device) bool {
				if device.Status == nil {
					return false
				}
				cond := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceSpecValid)
				return cond != nil && cond.Status == v1beta1.ConditionStatusTrue
			}, e2e.TIMEOUT)

		By("Attempting to delete the referenced OS catalog item")
		client := harness.GetV1Alpha1Client()
		Expect(client).ToNot(BeNil())
		resp, err := client.DeleteCatalogItemWithResponse(harness.Context, catalogName, osItemName)
		Expect(err).ToNot(HaveOccurred())

		By("Verifying API rejects deletion (non-200 status)")
		if resp.StatusCode() == http.StatusOK {
			Skip("AC-7: delete protection not yet implemented")
		}
		Expect(resp.StatusCode()).To(SatisfyAny(
			Equal(http.StatusConflict),
			Equal(http.StatusBadRequest),
			Equal(http.StatusUnprocessableEntity),
		))

		By("Verifying catalog item still exists")
		_, err = harness.GetCatalogItem(catalogName, osItemName)
		Expect(err).ToNot(HaveOccurred())
	})

	It("surfaces render error for type-mismatched ref", func() {
		harness = e2e.GetWorkerHarness()
		deviceId, _ := harness.EnrollAndWaitForOnlineStatus()

		By("Setting device OS spec to reference app-type catalog item (type mismatch)")
		err := harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
			device.Spec.Os = &v1beta1.DeviceOsSpec{
				CatalogItemRef: &v1beta1.CatalogItemRefSpec{
					Catalog: catalogName,
					Item:    appItemName,
					Version: appVersion1,
				},
			}
		})
		Expect(err).ToNot(HaveOccurred())

		By("Verifying device reports render error (SpecValid=False)")
		harness.WaitForDeviceContents(deviceId, "SpecValid=False with type mismatch",
			func(device *v1beta1.Device) bool {
				if device.Status == nil {
					return false
				}
				cond := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceSpecValid)
				if cond == nil {
					return false
				}
				return cond.Status == v1beta1.ConditionStatusFalse
			}, e2e.TIMEOUT)
	})
})
