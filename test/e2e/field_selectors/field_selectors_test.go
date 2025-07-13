package field_selectors

import (
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/e2e/resources"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Field Selectors Extension", Label("integration", "82219"), func() {
	var (
		expectedDevices      []*api.Device
		expectedFleets       []*api.Fleet
		expectedRepositories []*api.Repository
	)

	BeforeEach(func() {
		expectedDevices = nil
		expectedFleets = nil
		expectedRepositories = nil
	})

	Context("Supported fields validation", func() {
		It("should return a list of supported fields when providing invalid field selectors", func() {
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
				"status.lastSeen",
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
			err := resources.DevicesAreListed(harness, 0)
			Expect(err).ShouldNot(HaveOccurred())

			filteringDevicesResponse, _, err := filteringDevicesWithFieldSelectorAndOperator(harness, "@invalid-selector", "Equals", "invalid-value")
			Expect(err).ShouldNot(HaveOccurred())

			Expect(responseShouldContainExpectedResources(filteringDevicesResponse, err, 0)).To(Succeed())
		})
	})

	Context("Filter devices by name", func() {
		DescribeTable("Filter a selected device from a list of devices",
			func(value string, expectedCount int) {
				Expect(resources.DevicesAreListed(harness, 0)).To(Succeed())

				Expect(createDevicesWithNamePrefixAndFleet(harness, resourceCount, devicePrefix, fleetName, &expectedDevices)).To(Succeed())

				Expect(resources.DevicesAreListed(harness, resourceCount)).To(Succeed())

				filteringDevicesResponse, _, err := filteringDevicesWithFieldSelectorAndOperator(harness, "metadata.name", "Equals", value)
				Expect(err).ShouldNot(HaveOccurred())

				Expect(responseShouldContainExpectedResources(filteringDevicesResponse, err, expectedCount)).To(Succeed())
			},
			Entry("should match device-1", "device-1", 1),
			Entry("should match device-5", "device-5", 1),
			Entry("should match device-9", "device-9", 1),
			Entry("should not match device-20", "device-20", 0),
		)
	})

	Context("Filter devices by owner (fleet)", func() {
		DescribeTable("Filter selected devices from a list of devices assigned to a specific owner (fleet)",
			func(value string, expectedCount int) {
				Expect(resources.DevicesAreListed(harness, 0)).To(Succeed())
				Expect(resources.FleetsAreListed(harness, 0)).To(Succeed())

				Expect(createDevicesWithNamePrefixAndFleet(harness, resourceCount/2, "device-a-", "fleet-1", &expectedDevices)).To(Succeed())
				Expect(resources.DevicesAreListed(harness, resourceCount/2)).To(Succeed())

				Expect(createDevicesWithNamePrefixAndFleet(harness, resourceCount/2, "device-b-", "fleet-2", &expectedDevices)).To(Succeed())
				Expect(resources.DevicesAreListed(harness, resourceCount)).To(Succeed())

				Expect(createFleet(harness, "fleet-1", templateImage, &expectedFleets)).To(Succeed())
				Expect(resources.FleetsAreListed(harness, 1)).To(Succeed())

				Expect(createFleet(harness, "fleet-2", templateImage, &expectedFleets)).To(Succeed())
				Expect(resources.FleetsAreListed(harness, 2)).To(Succeed())

				filteringDevicesResponse, _, err := filteringDevicesWithFieldSelectorAndOperator(harness, "metadata.owner", "Equals", value)
				Expect(err).ShouldNot(HaveOccurred())

				Expect(responseShouldContainExpectedResources(filteringDevicesResponse, err, expectedCount)).To(Succeed())
			},
			Entry("should match Fleet/fleet-1", "Fleet/fleet-1", resourceCount/2),
			Entry("should match Fleet/fleet-2", "Fleet/fleet-2", resourceCount/2),
			Entry("should not match Fleet/default", "Fleet/default", 0),
		)
	})

	Context("Filter devices by creation timestamp", func() {
		It("should filter devices from a list of devices created during current year", func() {
			Expect(resources.DevicesAreListed(harness, 0)).To(Succeed())

			Expect(createDevicesWithNamePrefixAndFleet(harness, resourceCount, devicePrefix, fleetName, &expectedDevices)).To(Succeed())

			Expect(resources.DevicesAreListed(harness, resourceCount)).To(Succeed())

			filteringDevicesResponse, err := filterDevicesWithCreationTimeDuringCurrentYear(harness, "metadata.creationTimestamp")
			Expect(err).ShouldNot(HaveOccurred())

			Expect(responseShouldContainExpectedResources(filteringDevicesResponse, err, resourceCount)).To(Succeed())
		})
	})

	Context("Filter fleets by name", func() {
		DescribeTable("Filter a selected fleet from a list of fleets",
			func(value string, expectedCount int) {
				Expect(resources.FleetsAreListed(harness, 0)).To(Succeed())

				Expect(createFleetsWithNamePrefix(harness, resourceCount, fleetPrefix, templateImage, &expectedFleets)).To(Succeed())

				Expect(resources.FleetsAreListed(harness, resourceCount)).To(Succeed())

				filteringFleetsResponse, _, err := filteringFleetsWithFieldSelectorAndOperator(harness, "metadata.name", "Equals", value)
				Expect(err).ShouldNot(HaveOccurred())

				Expect(responseShouldContainExpectedResources(filteringFleetsResponse, err, expectedCount)).To(Succeed())
			},
			Entry("should match fleet-1", "fleet-1", 1),
			Entry("should match fleet-5", "fleet-5", 1),
			Entry("should not match fleet-20", "fleet-20", 0),
		)
	})

	Context("Filter fleets by creation timestamp", func() {
		It("should filter fleets from a list of fleets created during current year", func() {
			Expect(resources.FleetsAreListed(harness, 0)).To(Succeed())

			Expect(createFleetsWithNamePrefix(harness, resourceCount, fleetPrefix, templateImage, &expectedFleets)).To(Succeed())

			Expect(resources.FleetsAreListed(harness, resourceCount)).To(Succeed())

			filteringFleetsResponse, err := filterFleetsWithCreationTimeDuringCurrentYear(harness, "metadata.creationTimestamp")
			Expect(err).ShouldNot(HaveOccurred())

			Expect(responseShouldContainExpectedResources(filteringFleetsResponse, err, resourceCount)).To(Succeed())
		})
	})

	Context("Filter repositories by name", func() {
		DescribeTable("Filter a selected repository from a list of repositories",
			func(value string, expectedCount int) {
				Expect(resources.RepositoriesAreListed(harness, 0)).To(Succeed())

				Expect(createRepositoriesWithNamePrefix(harness, resourceCount, repositoryPrefix, repositoryUrl, &expectedRepositories)).To(Succeed())

				Expect(resources.RepositoriesAreListed(harness, resourceCount)).To(Succeed())

				filteringRepositoriesResponse, _, err := filteringRepositoriesWithFieldSelectorAndOperator(harness, "metadata.name", "Equals", value)
				Expect(err).ShouldNot(HaveOccurred())

				Expect(responseShouldContainExpectedResources(filteringRepositoriesResponse, err, expectedCount)).To(Succeed())
			},
			Entry("should match repository-1", "repository-1", 1),
			Entry("should match repository-5", "repository-5", 1),
			Entry("should not match repository-20", "repository-20", 0),
		)
	})

	Context("Filter repositories by creation timestamp", func() {
		It("should filter repositories created during current year", func() {
			Expect(resources.RepositoriesAreListed(harness, 0)).To(Succeed())

			Expect(createRepositoriesWithNamePrefix(harness, resourceCount, repositoryPrefix, repositoryUrl, &expectedRepositories)).To(Succeed())

			Expect(resources.RepositoriesAreListed(harness, resourceCount)).To(Succeed())

			filteringRepositoriesResponse, err := filterRepositoriesWithCreationTimeDuringCurrentYear(harness, "metadata.creationTimestamp")
			Expect(err).ShouldNot(HaveOccurred())

			Expect(responseShouldContainExpectedResources(filteringRepositoriesResponse, err, resourceCount)).To(Succeed())
		})
	})
})
