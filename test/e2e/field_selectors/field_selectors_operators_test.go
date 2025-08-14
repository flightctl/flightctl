package field_selectors

import (
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/e2e/resources"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Field Selectors Extension Operators", Label("integration", "82198"), func() {
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

	AfterEach(func() {
		Expect(resources.DeleteAll(harness, expectedDevices, expectedFleets, expectedRepositories)).To(Succeed())
	})

	Context("Filter devices by field name, operator and value", func() {
		DescribeTable("Evaluate filtered devices based on a combination of field name, operator and value",
			func(field string, operator string, value string, expectedCount int) {
				Expect(resources.DevicesAreListed(harness, 0)).To(Succeed())
				Expect(resources.FleetsAreListed(harness, 0)).To(Succeed())

				Expect(createDevicesWithNamePrefixAndFleet(harness, resourceCount, devicePrefix, fleetName, &expectedDevices)).To(Succeed())
				Expect(resources.DevicesAreListed(harness, resourceCount)).To(Succeed())

				Expect(createFleet(harness, fleetName, templateImage, &expectedFleets)).To(Succeed())
				Expect(resources.FleetsAreListed(harness, 1)).To(Succeed())

				filteringDevicesResponse, _, err := filteringDevicesWithFieldSelectorAndOperator(harness, field, operator, value)
				Expect(err).ShouldNot(HaveOccurred())

				Expect(responseShouldContainExpectedResources(filteringDevicesResponse, err, expectedCount)).To(Succeed())
			},
			// Metadata name examples
			Entry("metadata.name NotEquals device-1", "metadata.name", "NotEquals", "device-1", resourceCount-1),
			Entry("metadata.name DoubleEquals device-2", "metadata.name", "DoubleEquals", "device-2", 1),
			Entry("metadata.name Contains device-", "metadata.name", "Contains", "device-", resourceCount),
			Entry("metadata.name Contains vice-3", "metadata.name", "Contains", "vice-3", 1),
			Entry("metadata.name NotContains vice-4", "metadata.name", "NotContains", "vice-4", resourceCount-1),
			Entry("metadata.name NotContains device-", "metadata.name", "NotContains", "device-", 0),
			Entry("metadata.name GreaterThan 10", "metadata.name", "GreaterThan", "10", 0),
			Entry("metadata.name GreaterThanOrEquals 20", "metadata.name", "GreaterThanOrEquals", "20", 0),
			Entry("metadata.name LessThan 30", "metadata.name", "LessThan", "30", 0),
			Entry("metadata.name LessThanOrEquals 40", "metadata.name", "LessThanOrEquals", "40", 0),
			Entry("metadata.name In (some-name)", "metadata.name", "In", "(some-name)", 0),
			Entry("metadata.name NotIn (some-name)", "metadata.name", "NotIn", "(some-name)", resourceCount),

			// Metadata owner examples
			Entry("metadata.owner NotEquals Fleet/fleet-1", "metadata.owner", "NotEquals", "Fleet/fleet-1", 0),
			Entry("metadata.owner NotEquals fleet-11", "metadata.owner", "NotEquals", "fleet-11", resourceCount),
			Entry("metadata.owner DoubleEquals Fleet/fleet-1", "metadata.owner", "DoubleEquals", "Fleet/fleet-1", resourceCount),
			Entry("metadata.owner Contains Fleet/", "metadata.owner", "Contains", "Fleet/", resourceCount),
			Entry("metadata.owner Contains fleet-", "metadata.owner", "Contains", "fleet-", resourceCount),
			Entry("metadata.owner NotContains fleet-11", "metadata.owner", "NotContains", "fleet-11", resourceCount),
			Entry("metadata.owner NotContains Fleet/fleet-1", "metadata.owner", "NotContains", "Fleet/fleet-1", 0),
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
			Entry("status.lastSeen Equals 0001-01-01T00:00:00Z", "status.lastSeen", "Equals", "0001-01-01T00:00:00Z", resourceCount),
			Entry("status.lastSeen NotEquals 0001-01-01T00:00:00Z", "status.lastSeen", "NotEquals", "0001-01-01T00:00:00Z", 0),

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
	})

	Context("Filter fleets by field name, operator and value", func() {
		DescribeTable("Evaluate filtered fleets based on a combination of field name, operator and value",
			func(field string, operator string, value string, expectedCount int) {
				Expect(resources.FleetsAreListed(harness, 0)).To(Succeed())

				Expect(createFleetsWithNamePrefix(harness, resourceCount, fleetPrefix, templateImage, &expectedFleets)).To(Succeed())

				Expect(resources.FleetsAreListed(harness, resourceCount)).To(Succeed())

				filteringFleetsResponse, _, err := filteringFleetsWithFieldSelectorAndOperator(harness, field, operator, value)

				Expect(responseShouldContainExpectedResources(filteringFleetsResponse, err, expectedCount)).To(Succeed())
			},
			// Fleet metadata.name examples
			Entry("metadata.name NotEquals fleet-1", "metadata.name", "NotEquals", "fleet-1", resourceCount-1),
			Entry("metadata.name DoubleEquals fleet-2", "metadata.name", "DoubleEquals", "fleet-2", 1),
			Entry("metadata.name Contains fleet-", "metadata.name", "Contains", "fleet-", resourceCount),
			Entry("metadata.name Contains leet-3", "metadata.name", "Contains", "leet-3", 1),
			Entry("metadata.name NotContains leet-4", "metadata.name", "NotContains", "leet-4", resourceCount-1),
			Entry("metadata.name NotContains fleet-", "metadata.name", "NotContains", "fleet-", 0),
			Entry("metadata.name GreaterThan 12", "metadata.name", "GreaterThan", "12", 0),
			Entry("metadata.name GreaterThanOrEquals 22", "metadata.name", "GreaterThanOrEquals", "22", 0),
			Entry("metadata.name LessThan 32", "metadata.name", "LessThan", "32", 0),
			Entry("metadata.name LessThanOrEquals 42", "metadata.name", "LessThanOrEquals", "42", 0),
			Entry("metadata.name In (some-name)", "metadata.name", "In", "(some-name)", 0),
			Entry("metadata.name NotIn (some-name)", "metadata.name", "NotIn", "(some-name)", resourceCount),
		)
	})

	Context("Filter repositories by field name, operator and value", func() {
		DescribeTable("Evaluate filtered repositories based on a combination of field name, operator and value",
			func(field string, operator string, value string, expectedCount int) {
				Expect(resources.RepositoriesAreListed(harness, 0)).To(Succeed())

				Expect(createRepositoriesWithNamePrefix(harness, resourceCount, repositoryPrefix, repositoryUrl, &expectedRepositories)).To(Succeed())

				Expect(resources.RepositoriesAreListed(harness, resourceCount)).To(Succeed())

				filteringRepositoriesResponse, _, err := filteringRepositoriesWithFieldSelectorAndOperator(harness, field, operator, value)

				Expect(responseShouldContainExpectedResources(filteringRepositoriesResponse, err, expectedCount)).To(Succeed())
			},
			// Repository metadata.name examples
			Entry("metadata.name NotEquals repository-1", "metadata.name", "NotEquals", "repository-1", resourceCount-1),
			Entry("metadata.name DoubleEquals repository-2", "metadata.name", "DoubleEquals", "repository-2", 1),
			Entry("metadata.name Contains pository-", "metadata.name", "Contains", "pository-", resourceCount),
			Entry("metadata.name Contains pository-3", "metadata.name", "Contains", "pository-3", 1),
			Entry("metadata.name NotContains pository-4", "metadata.name", "NotContains", "pository-4", resourceCount-1),
			Entry("metadata.name NotContains repository-", "metadata.name", "NotContains", "repository-", 0),
			Entry("metadata.name GreaterThan 13", "metadata.name", "GreaterThan", "13", 0),
			Entry("metadata.name GreaterThanOrEquals 23", "metadata.name", "GreaterThanOrEquals", "23", 0),
			Entry("metadata.name LessThan 33", "metadata.name", "LessThan", "33", 0),
			Entry("metadata.name LessThanOrEquals 43", "metadata.name", "LessThanOrEquals", "43", 0),
			Entry("metadata.name In (some-name)", "metadata.name", "In", "(some-name)", 0),
			Entry("metadata.name NotIn (some-name)", "metadata.name", "NotIn", "(some-name)", resourceCount),
		)
	})
})
