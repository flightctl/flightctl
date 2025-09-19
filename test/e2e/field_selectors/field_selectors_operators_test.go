package field_selectors

import (
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/e2e/resources"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Field Selectors Extension Operators", Label("integration", "82198"), func() {

	Context("Filter devices by field name, operator and value", func() {
		DescribeTable("Evaluate filtered devices based on a combination of field name, operator and value",
			func(field string, operator string, valueTemplate string, expectedCount int) {
				// Get harness directly - no shared package-level variable
				harness := e2e.GetWorkerHarness()

				Expect(resources.DevicesAreListed(harness, 0)).To(Succeed())
				Expect(resources.FleetsAreListed(harness, 0)).To(Succeed())

				Expect(createDevicesWithNamePrefixAndFleet(harness, resourceCount, devicePrefix, fmt.Sprintf("fleet-%s-1", harness.GetTestIDFromContext()), &[]*api.Device{})).To(Succeed())
				Expect(resources.DevicesAreListed(harness, resourceCount)).To(Succeed())

				Expect(createFleet(harness, fmt.Sprintf("fleet-%s-1", harness.GetTestIDFromContext()), templateImage, &[]*api.Fleet{})).To(Succeed())
				Expect(resources.FleetsAreListed(harness, 1)).To(Succeed())

				// Generate the actual value by replacing placeholders with test-id
				value := generateValueWithTestID(harness, valueTemplate)
				filteringDevicesResponse, _, err := filteringDevicesWithFieldSelectorAndOperator(harness, field, operator, value)
				Expect(err).ShouldNot(HaveOccurred())

				Expect(responseShouldContainExpectedResources(filteringDevicesResponse, err, expectedCount)).To(Succeed())
			},
			// Metadata name examples
			Entry("metadata.name NotEquals device-<test-id>-1", "metadata.name", "NotEquals", "device-<test-id>-1", resourceCount-1),
			Entry("metadata.name DoubleEquals device-<test-id>-2", "metadata.name", "DoubleEquals", "device-<test-id>-2", 1),
			Entry("metadata.name Contains device-<test-id>-", "metadata.name", "Contains", "device-<test-id>-", resourceCount),
			Entry("metadata.name Contains vice-<test-id>-3", "metadata.name", "Contains", "vice-<test-id>-3", 1),
			Entry("metadata.name NotContains vice-<test-id>-4", "metadata.name", "NotContains", "vice-<test-id>-4", resourceCount-1),
			Entry("metadata.name NotContains device-<test-id>-", "metadata.name", "NotContains", "device-<test-id>-", 0),
			Entry("metadata.name GreaterThan 10", "metadata.name", "GreaterThan", "10", 0),
			Entry("metadata.name GreaterThanOrEquals 20", "metadata.name", "GreaterThanOrEquals", "20", 0),
			Entry("metadata.name LessThan 30", "metadata.name", "LessThan", "30", 0),
			Entry("metadata.name LessThanOrEquals 40", "metadata.name", "LessThanOrEquals", "40", 0),
			Entry("metadata.name In (some-name)", "metadata.name", "In", "(some-name)", 0),
			Entry("metadata.name NotIn (some-name)", "metadata.name", "NotIn", "(some-name)", resourceCount),

			// Metadata owner examples
			Entry("metadata.owner NotEquals fleet-<test-id>-11", "metadata.owner", "NotEquals", "fleet-<test-id>-11", resourceCount),
			Entry("metadata.owner Contains Fleet/", "metadata.owner", "Contains", "Fleet/", resourceCount),
			Entry("metadata.owner Contains fleet-<test-id>-", "metadata.owner", "Contains", "fleet-<test-id>-", resourceCount),
			Entry("metadata.owner NotContains fleet-<test-id>-11", "metadata.owner", "NotContains", "fleet-<test-id>-11", resourceCount),
			Entry("metadata.owner GreaterThan 11", "metadata.owner", "GreaterThan", "11", 0),
			Entry("metadata.owner GreaterThanOrEquals 21", "metadata.owner", "GreaterThanOrEquals", "21", 0),
			Entry("metadata.owner LessThan 31", "metadata.owner", "LessThan", "31", 0),
			Entry("metadata.owner LessThanOrEquals 41", "metadata.owner", "LessThanOrEquals", "41", 0),
			Entry("metadata.owner In (some-name)", "metadata.owner", "In", "(some-name)", 0),
			Entry("metadata.owner NotIn (some-name)", "metadata.owner", "NotIn", "(some-name)", resourceCount),

			// Status updated status examples
			Entry("status.updated.status Equals UpToDate", "status.updated.status", "Equals", "UpToDate", resourceCount),
			Entry("status.updated.status Equals Unknown", "status.updated.status", "Equals", "Unknown", 0),
			Entry("status.updated.status NotEquals UpToDate", "status.updated.status", "NotEquals", "UpToDate", 0),
			Entry("status.updated.status DoubleEquals UpToDate", "status.updated.status", "DoubleEquals", "UpToDate", resourceCount),
			Entry("status.updated.status In (UpToDate)", "status.updated.status", "In", "(UpToDate)", resourceCount),
			Entry("status.updated.status NotIn (Unknown)", "status.updated.status", "NotIn", "(Unknown)", resourceCount),

			// Applications summary examples
			Entry("status.applicationsSummary.status Equals Unknown", "status.applicationsSummary.status", "Equals", "Unknown", resourceCount),
			Entry("status.applicationsSummary.status NotEquals Unknown", "status.applicationsSummary.status", "NotEquals", "Unknown", 0),
			Entry("status.applicationsSummary.status NotEquals UpToDate", "status.applicationsSummary.status", "NotEquals", "UpToDate", resourceCount),

			// Last seen examples
			Entry("lastSeen Equals 0001-01-01T00:00:00Z", "lastSeen", "Equals", "0001-01-01T00:00:00Z", resourceCount),
			Entry("lastSeen NotEquals 0001-01-01T00:00:00Z", "lastSeen", "NotEquals", "0001-01-01T00:00:00Z", 0),

			// Summary status examples
			Entry("status.summary.status Equals Unknown", "status.summary.status", "Equals", "Unknown", resourceCount),
			Entry("status.summary.status NotEquals Unknown", "status.summary.status", "NotEquals", "Unknown", 0),
			Entry("status.summary.status DoubleEquals Unknown", "status.summary.status", "DoubleEquals", "Unknown", resourceCount),
			Entry("status.summary.status In (UpToDate)", "status.summary.status", "In", "(UpToDate)", 0),
			Entry("status.summary.status NotIn (UpToDate)", "status.summary.status", "NotIn", "(UpToDate)", resourceCount),

			// Lifecycle status examples
			Entry("status.lifecycle.status Equals Unknown", "status.lifecycle.status", "Equals", "Unknown", resourceCount),
			Entry("status.lifecycle.status NotEquals Unknown", "status.lifecycle.status", "NotEquals", "Unknown", 0),
			Entry("status.lifecycle.status DoubleEquals Unknown", "status.lifecycle.status", "DoubleEquals", "Unknown", resourceCount),
			Entry("status.lifecycle.status In (UpToDate)", "status.lifecycle.status", "In", "(UpToDate)", 0),
			Entry("status.lifecycle.status NotIn (UpToDate)", "status.lifecycle.status", "NotIn", "(UpToDate)", resourceCount),
		)

		// Individual tests for fleet-specific metadata.owner entries
		It("should handle metadata.owner NotEquals Fleet/fleet-1", func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			Expect(resources.DevicesAreListed(harness, 0)).To(Succeed())
			Expect(resources.FleetsAreListed(harness, 0)).To(Succeed())

			fleetName := fmt.Sprintf("fleet-%s-1", harness.GetTestIDFromContext())

			Expect(createDevicesWithNamePrefixAndFleet(harness, resourceCount, devicePrefix, fleetName, &[]*api.Device{})).To(Succeed())
			Expect(resources.DevicesAreListed(harness, resourceCount)).To(Succeed())

			Expect(createFleet(harness, fleetName, templateImage, &[]*api.Fleet{})).To(Succeed())
			Expect(resources.FleetsAreListed(harness, 1)).To(Succeed())

			filteringDevicesResponse, _, err := filteringDevicesWithFieldSelectorAndOperator(harness, "metadata.owner", "NotEquals", fmt.Sprintf("Fleet/%s", fleetName))
			Expect(err).ShouldNot(HaveOccurred())

			Expect(responseShouldContainExpectedResources(filteringDevicesResponse, err, 0)).To(Succeed())
		})

		It("should handle metadata.owner DoubleEquals Fleet/fleet-1", func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			Expect(resources.DevicesAreListed(harness, 0)).To(Succeed())
			Expect(resources.FleetsAreListed(harness, 0)).To(Succeed())

			fleetName := fmt.Sprintf("fleet-%s-1", harness.GetTestIDFromContext())

			Expect(createDevicesWithNamePrefixAndFleet(harness, resourceCount, devicePrefix, fleetName, &[]*api.Device{})).To(Succeed())
			Expect(resources.DevicesAreListed(harness, resourceCount)).To(Succeed())

			Expect(createFleet(harness, fleetName, templateImage, &[]*api.Fleet{})).To(Succeed())
			Expect(resources.FleetsAreListed(harness, 1)).To(Succeed())

			filteringDevicesResponse, _, err := filteringDevicesWithFieldSelectorAndOperator(harness, "metadata.owner", "DoubleEquals", fmt.Sprintf("Fleet/%s", fleetName))
			Expect(err).ShouldNot(HaveOccurred())

			Expect(responseShouldContainExpectedResources(filteringDevicesResponse, err, resourceCount)).To(Succeed())
		})

		It("should handle metadata.owner NotContains Fleet/fleet-1", func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			Expect(resources.DevicesAreListed(harness, 0)).To(Succeed())
			Expect(resources.FleetsAreListed(harness, 0)).To(Succeed())

			fleetName := fmt.Sprintf("fleet-%s-1", harness.GetTestIDFromContext())

			Expect(createDevicesWithNamePrefixAndFleet(harness, resourceCount, devicePrefix, fleetName, &[]*api.Device{})).To(Succeed())
			Expect(resources.DevicesAreListed(harness, resourceCount)).To(Succeed())

			Expect(createFleet(harness, fleetName, templateImage, &[]*api.Fleet{})).To(Succeed())
			Expect(resources.FleetsAreListed(harness, 1)).To(Succeed())

			filteringDevicesResponse, _, err := filteringDevicesWithFieldSelectorAndOperator(harness, "metadata.owner", "NotContains", fmt.Sprintf("Fleet/%s", fleetName))
			Expect(err).ShouldNot(HaveOccurred())

			Expect(responseShouldContainExpectedResources(filteringDevicesResponse, err, 0)).To(Succeed())
		})
	})

	Context("Filter fleets by field name, operator and value", func() {
		DescribeTable("Evaluate filtered fleets based on a combination of field name, operator and value",
			func(field string, operator string, valueTemplate string, expectedCount int) {
				// Get harness directly - no shared package-level variable
				harness := e2e.GetWorkerHarness()

				Expect(resources.FleetsAreListed(harness, 0)).To(Succeed())

				Expect(createFleetsWithNamePrefix(harness, resourceCount, fleetPrefix, templateImage, &[]*api.Fleet{})).To(Succeed())

				Expect(resources.FleetsAreListed(harness, resourceCount)).To(Succeed())

				// Generate the actual value by replacing placeholders with test-id
				value := generateValueWithTestID(harness, valueTemplate)
				filteringFleetsResponse, _, err := filteringFleetsWithFieldSelectorAndOperator(harness, field, operator, value)

				Expect(responseShouldContainExpectedResources(filteringFleetsResponse, err, expectedCount)).To(Succeed())
			},
			// Fleet metadata.name examples
			Entry("metadata.name Contains fleet-<test-id>-", "metadata.name", "Contains", "fleet-<test-id>-", resourceCount),
			Entry("metadata.name Contains leet-<test-id>-3", "metadata.name", "Contains", "leet-<test-id>-3", 1),
			Entry("metadata.name NotContains leet-<test-id>-4", "metadata.name", "NotContains", "leet-<test-id>-4", resourceCount-1),
			Entry("metadata.name NotContains fleet-<test-id>-", "metadata.name", "NotContains", "fleet-<test-id>-", 0),
			Entry("metadata.name GreaterThan 12", "metadata.name", "GreaterThan", "12", 0),
			Entry("metadata.name GreaterThanOrEquals 22", "metadata.name", "GreaterThanOrEquals", "22", 0),
			Entry("metadata.name LessThan 32", "metadata.name", "LessThan", "32", 0),
			Entry("metadata.name LessThanOrEquals 42", "metadata.name", "LessThanOrEquals", "42", 0),
			Entry("metadata.name In (some-name)", "metadata.name", "In", "(some-name)", 0),
			Entry("metadata.name NotIn (some-name)", "metadata.name", "NotIn", "(some-name)", resourceCount),
		)

		// Individual tests for fleet-specific metadata.name entries
		It("should handle metadata.name NotEquals fleet-1", func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			Expect(resources.FleetsAreListed(harness, 0)).To(Succeed())

			Expect(createFleetsWithNamePrefix(harness, resourceCount, fleetPrefix, templateImage, &[]*api.Fleet{})).To(Succeed())
			Expect(resources.FleetsAreListed(harness, resourceCount)).To(Succeed())

			// Generate the expected fleet name with test-id for index 1
			expectedFleetName := getExpectedFleetName(harness, fleetPrefix, 1)
			filteringFleetsResponse, _, err := filteringFleetsWithFieldSelectorAndOperator(harness, "metadata.name", "NotEquals", expectedFleetName)

			Expect(responseShouldContainExpectedResources(filteringFleetsResponse, err, resourceCount-1)).To(Succeed())
		})

		It("should handle metadata.name DoubleEquals fleet-2", func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			Expect(resources.FleetsAreListed(harness, 0)).To(Succeed())

			Expect(createFleetsWithNamePrefix(harness, resourceCount, fleetPrefix, templateImage, &[]*api.Fleet{})).To(Succeed())
			Expect(resources.FleetsAreListed(harness, resourceCount)).To(Succeed())

			// Generate the expected fleet name with test-id for index 2
			expectedFleetName := getExpectedFleetName(harness, fleetPrefix, 2)
			filteringFleetsResponse, _, err := filteringFleetsWithFieldSelectorAndOperator(harness, "metadata.name", "DoubleEquals", expectedFleetName)

			Expect(responseShouldContainExpectedResources(filteringFleetsResponse, err, 1)).To(Succeed())
		})
	})

	Context("Filter repositories by field name, operator and value", func() {
		DescribeTable("Evaluate filtered repositories based on a combination of field name, operator and value",
			func(field string, operator string, valueTemplate string, expectedCount int) {
				// Get harness directly - no shared package-level variable
				harness := e2e.GetWorkerHarness()

				Expect(resources.RepositoriesAreListed(harness, 0)).To(Succeed())

				Expect(createRepositoriesWithNamePrefix(harness, resourceCount, repositoryPrefix, repositoryUrl, &[]*api.Repository{})).To(Succeed())

				Expect(resources.RepositoriesAreListed(harness, resourceCount)).To(Succeed())

				// Generate the actual value by replacing placeholders with test-id
				value := generateValueWithTestID(harness, valueTemplate)
				filteringRepositoriesResponse, _, err := filteringRepositoriesWithFieldSelectorAndOperator(harness, field, operator, value)

				Expect(responseShouldContainExpectedResources(filteringRepositoriesResponse, err, expectedCount)).To(Succeed())
			},
			// Repository metadata.name examples
			Entry("metadata.name NotEquals repository-<test-id>-1", "metadata.name", "NotEquals", "repository-<test-id>-1", resourceCount-1),
			Entry("metadata.name DoubleEquals repository-<test-id>-2", "metadata.name", "DoubleEquals", "repository-<test-id>-2", 1),
			Entry("metadata.name Contains pository-<test-id>-", "metadata.name", "Contains", "pository-<test-id>-", resourceCount),
			Entry("metadata.name Contains pository-<test-id>-3", "metadata.name", "Contains", "pository-<test-id>-3", 1),
			Entry("metadata.name NotContains pository-<test-id>-4", "metadata.name", "NotContains", "pository-<test-id>-4", resourceCount-1),
			Entry("metadata.name NotContains repository-<test-id>-", "metadata.name", "NotContains", "repository-<test-id>-", 0),
			Entry("metadata.name GreaterThan 13", "metadata.name", "GreaterThan", "13", 0),
			Entry("metadata.name GreaterThanOrEquals 23", "metadata.name", "GreaterThanOrEquals", "23", 0),
			Entry("metadata.name LessThan 33", "metadata.name", "LessThan", "33", 0),
			Entry("metadata.name LessThanOrEquals 43", "metadata.name", "LessThanOrEquals", "43", 0),
			Entry("metadata.name In (some-name)", "metadata.name", "In", "(some-name)", 0),
			Entry("metadata.name NotIn (some-name)", "metadata.name", "NotIn", "(some-name)", resourceCount),
		)
	})
})
