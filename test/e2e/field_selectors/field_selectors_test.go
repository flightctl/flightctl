package field_selectors

import (
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/e2e/resources"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Field Selectors Extension", Label("integration", "82219"), func() {

	Context("Supported fields validation", func() {
		It("should return a list of supported fields when providing invalid field selectors", func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			Expect(resources.DevicesAreListed(harness, 0)).To(Succeed())

			_, actualSupportedFields, err := filteringDevicesWithFieldSelectorAndOperator(harness, "invalid-selector", "Equals", "invalid-value")
			Expect(err).ShouldNot(HaveOccurred())

			supportedFields := []string{
				"metadata.alias",
				"metadata.creationTimestamp",
				"metadata.name",
				"metadata.nameOrAlias",
				"metadata.owner",
				"status.applicationsSummary.status",
				"lastSeen",
				"status.lifecycle.status",
				"status.summary.status",
				"status.updated.status",
			}

			Expect(len(actualSupportedFields)).To(Equal(len(supportedFields)))
			for _, field := range supportedFields {
				Expect(contains(actualSupportedFields, field)).To(BeTrue())
			}
		})
	})

	Context("Invalid field selector syntax validation", func() {
		It("should return an error message when providing invalid syntax for field selector name", func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			err := resources.DevicesAreListed(harness, 0)
			Expect(err).ShouldNot(HaveOccurred())

			filteringDevicesResponse, _, err := filteringDevicesWithFieldSelectorAndOperator(harness, "@invalid-selector", "Equals", "invalid-value")
			Expect(err).ShouldNot(HaveOccurred())

			Expect(responseShouldContainExpectedResources(filteringDevicesResponse, err, 0)).To(Succeed())
		})
	})

	Context("Filter devices by name", func() {
		DescribeTable("Filter a selected device from a list of devices",
			func(deviceIndex int, expectedCount int) {
				// Get harness directly - no shared package-level variable
				harness := e2e.GetWorkerHarness()

				Expect(resources.DevicesAreListed(harness, 0)).To(Succeed())

				Expect(createDevicesWithNamePrefixAndFleet(harness, resourceCount, devicePrefix, fmt.Sprintf("fleet-%s-1", harness.GetTestIDFromContext()), &[]*api.Device{})).To(Succeed())

				Expect(resources.DevicesAreListed(harness, resourceCount)).To(Succeed())

				// Generate the expected device name with test-id
				expectedDeviceName := getExpectedDeviceName(harness, devicePrefix, deviceIndex)
				filteringDevicesResponse, _, err := filteringDevicesWithFieldSelectorAndOperator(harness, "metadata.name", "Equals", expectedDeviceName)
				Expect(err).ShouldNot(HaveOccurred())

				Expect(responseShouldContainExpectedResources(filteringDevicesResponse, err, expectedCount)).To(Succeed())
			},
			Entry("should match device-1", 1, 1),
			Entry("should match device-5", 5, 1),
			Entry("should match device-9", 9, 1),
			Entry("should not match device-20", 20, 0),
		)
	})

	Context("Filter devices by owner (fleet)", func() {
		It("should match Fleet/fleet-1", func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			Expect(resources.DevicesAreListed(harness, 0)).To(Succeed())
			Expect(resources.FleetsAreListed(harness, 0)).To(Succeed())

			fleet1Name := fmt.Sprintf("fleet-%s-1", harness.GetTestIDFromContext())
			fleet2Name := fmt.Sprintf("fleet-%s-2", harness.GetTestIDFromContext())

			Expect(createDevicesWithNamePrefixAndFleet(harness, resourceCount/2, "device-a-", fleet1Name, &[]*api.Device{})).To(Succeed())
			Expect(resources.DevicesAreListed(harness, resourceCount/2)).To(Succeed())

			Expect(createDevicesWithNamePrefixAndFleet(harness, resourceCount/2, "device-b-", fleet2Name, &[]*api.Device{})).To(Succeed())
			Expect(resources.DevicesAreListed(harness, resourceCount)).To(Succeed())

			Expect(createFleet(harness, fleet1Name, templateImage, &[]*api.Fleet{})).To(Succeed())
			Expect(resources.FleetsAreListed(harness, 1)).To(Succeed())

			Expect(createFleet(harness, fleet2Name, templateImage, &[]*api.Fleet{})).To(Succeed())
			Expect(resources.FleetsAreListed(harness, 2)).To(Succeed())

			filteringDevicesResponse, _, err := filteringDevicesWithFieldSelectorAndOperator(harness, "metadata.owner", "Equals", fmt.Sprintf("Fleet/%s", fleet1Name))
			Expect(err).ShouldNot(HaveOccurred())

			Expect(responseShouldContainExpectedResources(filteringDevicesResponse, err, resourceCount/2)).To(Succeed())
		})

		It("should match Fleet/fleet-2", func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			Expect(resources.DevicesAreListed(harness, 0)).To(Succeed())
			Expect(resources.FleetsAreListed(harness, 0)).To(Succeed())

			fleet1Name := fmt.Sprintf("fleet-%s-1", harness.GetTestIDFromContext())
			fleet2Name := fmt.Sprintf("fleet-%s-2", harness.GetTestIDFromContext())

			Expect(createDevicesWithNamePrefixAndFleet(harness, resourceCount/2, "device-a-", fleet1Name, &[]*api.Device{})).To(Succeed())
			Expect(resources.DevicesAreListed(harness, resourceCount/2)).To(Succeed())

			Expect(createDevicesWithNamePrefixAndFleet(harness, resourceCount/2, "device-b-", fleet2Name, &[]*api.Device{})).To(Succeed())
			Expect(resources.DevicesAreListed(harness, resourceCount)).To(Succeed())

			Expect(createFleet(harness, fleet1Name, templateImage, &[]*api.Fleet{})).To(Succeed())
			Expect(resources.FleetsAreListed(harness, 1)).To(Succeed())

			Expect(createFleet(harness, fleet2Name, templateImage, &[]*api.Fleet{})).To(Succeed())
			Expect(resources.FleetsAreListed(harness, 2)).To(Succeed())

			filteringDevicesResponse, _, err := filteringDevicesWithFieldSelectorAndOperator(harness, "metadata.owner", "Equals", fmt.Sprintf("Fleet/%s", fleet2Name))
			Expect(err).ShouldNot(HaveOccurred())

			Expect(responseShouldContainExpectedResources(filteringDevicesResponse, err, resourceCount/2)).To(Succeed())
		})

		It("should not match Fleet/default", func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			Expect(resources.DevicesAreListed(harness, 0)).To(Succeed())
			Expect(resources.FleetsAreListed(harness, 0)).To(Succeed())

			fleet1Name := fmt.Sprintf("fleet-%s-1", harness.GetTestIDFromContext())
			fleet2Name := fmt.Sprintf("fleet-%s-2", harness.GetTestIDFromContext())

			Expect(createDevicesWithNamePrefixAndFleet(harness, resourceCount/2, "device-a-", fleet1Name, &[]*api.Device{})).To(Succeed())
			Expect(resources.DevicesAreListed(harness, resourceCount/2)).To(Succeed())

			Expect(createDevicesWithNamePrefixAndFleet(harness, resourceCount/2, "device-b-", fleet2Name, &[]*api.Device{})).To(Succeed())
			Expect(resources.DevicesAreListed(harness, resourceCount)).To(Succeed())

			Expect(createFleet(harness, fleet1Name, templateImage, &[]*api.Fleet{})).To(Succeed())
			Expect(resources.FleetsAreListed(harness, 1)).To(Succeed())

			Expect(createFleet(harness, fleet2Name, templateImage, &[]*api.Fleet{})).To(Succeed())
			Expect(resources.FleetsAreListed(harness, 2)).To(Succeed())

			filteringDevicesResponse, _, err := filteringDevicesWithFieldSelectorAndOperator(harness, "metadata.owner", "Equals", "Fleet/default")
			Expect(err).ShouldNot(HaveOccurred())

			Expect(responseShouldContainExpectedResources(filteringDevicesResponse, err, 0)).To(Succeed())
		})
	})

	Context("Filter devices by creation timestamp", func() {
		It("should filter devices from a list of devices created during current year", func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			Expect(resources.DevicesAreListed(harness, 0)).To(Succeed())

			Expect(createDevicesWithNamePrefixAndFleet(harness, resourceCount, devicePrefix, fmt.Sprintf("fleet-%s-1", harness.GetTestIDFromContext()), &[]*api.Device{})).To(Succeed())

			Expect(resources.DevicesAreListed(harness, resourceCount)).To(Succeed())

			filteringDevicesResponse, err := filterDevicesWithCreationTimeDuringCurrentYear(harness, "metadata.creationTimestamp")
			Expect(err).ShouldNot(HaveOccurred())

			Expect(responseShouldContainExpectedResources(filteringDevicesResponse, err, resourceCount)).To(Succeed())
		})
	})

	Context("Filter fleets by name", func() {
		DescribeTable("Filter a selected fleet from a list of fleets",
			func(fleetIndex int, expectedCount int) {
				// Get harness directly - no shared package-level variable
				harness := e2e.GetWorkerHarness()

				Expect(resources.FleetsAreListed(harness, 0)).To(Succeed())

				Expect(createFleetsWithNamePrefix(harness, resourceCount, fleetPrefix, templateImage, &[]*api.Fleet{})).To(Succeed())

				Expect(resources.FleetsAreListed(harness, resourceCount)).To(Succeed())

				// Generate the expected fleet name with test-id
				expectedFleetName := getExpectedFleetName(harness, fleetPrefix, fleetIndex)
				filteringFleetsResponse, _, err := filteringFleetsWithFieldSelectorAndOperator(harness, "metadata.name", "Equals", expectedFleetName)
				Expect(err).ShouldNot(HaveOccurred())

				Expect(responseShouldContainExpectedResources(filteringFleetsResponse, err, expectedCount)).To(Succeed())
			},
			Entry("should match fleet-1", 1, 1),
			Entry("should match fleet-5", 5, 1),
			Entry("should not match fleet-20", 20, 0),
		)
	})

	Context("Filter fleets by creation timestamp", func() {
		It("should filter fleets from a list of fleets created during current year", func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			Expect(resources.FleetsAreListed(harness, 0)).To(Succeed())

			Expect(createFleetsWithNamePrefix(harness, resourceCount, fleetPrefix, templateImage, &[]*api.Fleet{})).To(Succeed())

			Expect(resources.FleetsAreListed(harness, resourceCount)).To(Succeed())

			filteringFleetsResponse, err := filterFleetsWithCreationTimeDuringCurrentYear(harness, "metadata.creationTimestamp")
			Expect(err).ShouldNot(HaveOccurred())

			Expect(responseShouldContainExpectedResources(filteringFleetsResponse, err, resourceCount)).To(Succeed())
		})
	})

	Context("Filter repositories by name", func() {
		DescribeTable("Filter a selected repository from a list of repositories",
			func(repositoryIndex int, expectedCount int) {
				// Get harness directly - no shared package-level variable
				harness := e2e.GetWorkerHarness()

				Expect(resources.RepositoriesAreListed(harness, 0)).To(Succeed())

				Expect(createRepositoriesWithNamePrefix(harness, resourceCount, repositoryPrefix, repositoryUrl, &[]*api.Repository{})).To(Succeed())

				Expect(resources.RepositoriesAreListed(harness, resourceCount)).To(Succeed())

				// Generate the expected repository name with test-id
				expectedRepositoryName := getExpectedRepositoryName(harness, repositoryPrefix, repositoryIndex)
				filteringRepositoriesResponse, _, err := filteringRepositoriesWithFieldSelectorAndOperator(harness, "metadata.name", "Equals", expectedRepositoryName)
				Expect(err).ShouldNot(HaveOccurred())

				Expect(responseShouldContainExpectedResources(filteringRepositoriesResponse, err, expectedCount)).To(Succeed())
			},
			Entry("should match repository-1", 1, 1),
			Entry("should match repository-5", 5, 1),
			Entry("should not match repository-20", 20, 0),
		)
	})

	Context("Filter repositories by creation timestamp", func() {
		It("should filter repositories created during current year", func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			Expect(resources.RepositoriesAreListed(harness, 0)).To(Succeed())

			Expect(createRepositoriesWithNamePrefix(harness, resourceCount, repositoryPrefix, repositoryUrl, &[]*api.Repository{})).To(Succeed())

			Expect(resources.RepositoriesAreListed(harness, resourceCount)).To(Succeed())

			filteringRepositoriesResponse, err := filterRepositoriesWithCreationTimeDuringCurrentYear(harness, "metadata.creationTimestamp")
			Expect(err).ShouldNot(HaveOccurred())

			Expect(responseShouldContainExpectedResources(filteringRepositoriesResponse, err, resourceCount)).To(Succeed())
		})
	})
})
