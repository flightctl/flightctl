package service_test

import (
	"encoding/json"
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

// Suite-level tracer is initialized once in service_suite_test.go.

var _ = Describe("Fleet create", func() {
	var suite *ServiceTestSuite

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
	})

	AfterEach(func() {
		suite.Teardown()
	})
	DescribeTable("ReplaceFleet owner handling",
		func(replace func(fleet api.Fleet) (*api.Fleet, api.Status), expectedOwner string) {
			fleet := api.Fleet{
				Metadata: api.ObjectMeta{
					Name:  lo.ToPtr("test-fleet"),
					Owner: lo.ToPtr("test-owner"),
				},
				Spec: api.FleetSpec{},
			}
			createdFleet, status := replace(fleet)
			Expect(status.Code).To(Equal(int32(http.StatusCreated)))
			Expect(lo.FromPtr(createdFleet.Metadata.Name)).To(Equal("test-fleet"))
			Expect(lo.FromPtr(createdFleet.Metadata.Owner)).To(Equal(expectedOwner))

			retrievedFleet, status := suite.Fleet.GetFleet(suite.Ctx, suite.OrgID, "test-fleet", api.GetFleetParams{})
			Expect(status.Code).To(Equal(int32(http.StatusOK)))
			Expect(lo.FromPtr(retrievedFleet.Metadata.Name)).To(Equal("test-fleet"))
			Expect(lo.FromPtr(retrievedFleet.Metadata.Owner)).To(Equal(expectedOwner))
		},
		Entry("Trusted caller (ReplaceFleet) should keep owner as given",
			func(fleet api.Fleet) (*api.Fleet, api.Status) {
				return suite.Fleet.ReplaceFleet(suite.Ctx, suite.OrgID, "test-fleet", fleet, true)
			},
			"test-owner",
		),
		Entry("Untrusted caller (ReplaceFleetFromUntrusted) should nil owner",
			func(fleet api.Fleet) (*api.Fleet, api.Status) {
				return fleetservice.ReplaceFleetFromUntrusted(suite.Ctx, suite.Fleet, suite.OrgID, "test-fleet", fleet, false)
			},
			"",
		),
	)
})

var _ = Describe("Fleet schedule validation and persistence", func() {
	var suite *ServiceTestSuite

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
	})

	AfterEach(func() {
		suite.Teardown()
	})

	It("accepts and persists independent download and update schedules", func() {
		fleet := fleetWithSchedules("schedule-persistence-fleet")

		createdFleet, status := suite.Fleet.CreateFleet(suite.Ctx, suite.OrgID, fleet)
		Expect(status.Code).To(Equal(int32(http.StatusCreated)), status.Message)
		Expect(createdFleet).NotTo(BeNil())

		retrievedFleet, status := suite.Fleet.GetFleet(suite.Ctx, suite.OrgID, "schedule-persistence-fleet", api.GetFleetParams{})
		Expect(status.Code).To(Equal(int32(http.StatusOK)), status.Message)
		expectUpdatePolicyJSON(retrievedFleet.Spec.Template.Spec.UpdatePolicy, fleet.Spec.Template.Spec.UpdatePolicy)
	})

	DescribeTable("rejects invalid schedules without changing the persisted fleet",
		func(mutate func(*api.Fleet), expectedMessage string) {
			fleet := fleetWithSchedules("schedule-validation-fleet")
			createdFleet, status := suite.Fleet.CreateFleet(suite.Ctx, suite.OrgID, fleet)
			Expect(status.Code).To(Equal(int32(http.StatusCreated)), status.Message)

			candidate := fleetWithSchedules("schedule-validation-fleet")
			candidate.Metadata = createdFleet.Metadata
			mutate(&candidate)

			_, status = suite.Fleet.ReplaceFleet(suite.Ctx, suite.OrgID, "schedule-validation-fleet", candidate, true)
			Expect(status.Code).To(Equal(int32(http.StatusBadRequest)), status.Message)
			Expect(status.Message).To(ContainSubstring(expectedMessage))

			retrievedFleet, status := suite.Fleet.GetFleet(suite.Ctx, suite.OrgID, "schedule-validation-fleet", api.GetFleetParams{})
			Expect(status.Code).To(Equal(int32(http.StatusOK)), status.Message)
			expectUpdatePolicyJSON(retrievedFleet.Spec.Template.Spec.UpdatePolicy, fleet.Spec.Template.Spec.UpdatePolicy)
		},
		Entry("invalid date", func(fleet *api.Fleet) {
			windows := fleet.Spec.Template.Spec.UpdatePolicy.DownloadSchedule.OneTimeWindows
			(*windows)[0].Date = "2026-02-30"
		}, "oneTimeWindows[0].date"),
		Entry("invalid local time", func(fleet *api.Fleet) {
			windows := fleet.Spec.Template.Spec.UpdatePolicy.DownloadSchedule.OneTimeWindows
			(*windows)[0].At = "24:00"
		}, "oneTimeWindows[0].at"),
		Entry("invalid duration", func(fleet *api.Fleet) {
			windows := fleet.Spec.Template.Spec.UpdatePolicy.DownloadSchedule.OneTimeWindows
			(*windows)[0].StartGraceDuration = "1ms"
		}, "oneTimeWindows[0].startGraceDuration"),
		Entry("invalid timezone", func(fleet *api.Fleet) {
			timeZone := api.TimeZone("Not/A_Time_Zone")
			fleet.Spec.Template.Spec.UpdatePolicy.DownloadSchedule.TimeZone = &timeZone
		}, "invalid time zone"),
		Entry("incomplete recurring schedule", func(fleet *api.Fleet) {
			schedule := fleet.Spec.Template.Spec.UpdatePolicy.DownloadSchedule
			schedule.StartGraceDuration = nil
		}, "startGraceDuration must be set"),
	)
})

func fleetWithSchedules(name string) api.Fleet {
	downloadAt := api.CronExpression("0 2 * * *")
	downloadDuration := api.Duration("1h")
	downloadTimeZone := api.TimeZone("Asia/Jerusalem")
	updateTimeZone := api.TimeZone("UTC")

	fleet := api.Fleet{
		Metadata: api.ObjectMeta{Name: &name},
	}
	fleet.Spec.Template.Spec.UpdatePolicy = &api.DeviceUpdatePolicySpec{
		DownloadSchedule: &api.UpdateSchedule{
			At:                 &downloadAt,
			StartGraceDuration: &downloadDuration,
			TimeZone:           &downloadTimeZone,
			OneTimeWindows: &[]api.OneTimeWindow{{
				Date:               "2026-11-15",
				At:                 "22:00",
				StartGraceDuration: "90m",
			}},
		},
		UpdateSchedule: &api.UpdateSchedule{
			TimeZone: &updateTimeZone,
			OneTimeWindows: &[]api.OneTimeWindow{{
				Date:               "2000-01-01",
				At:                 "00:00",
				StartGraceDuration: "2h",
			}},
		},
	}

	return fleet
}

func expectUpdatePolicyJSON(actual, expected *api.DeviceUpdatePolicySpec) {
	actualJSON, err := json.Marshal(actual)
	Expect(err).NotTo(HaveOccurred())
	expectedJSON, err := json.Marshal(expected)
	Expect(err).NotTo(HaveOccurred())
	Expect(actualJSON).To(MatchJSON(expectedJSON))
}
