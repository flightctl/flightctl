package service_test

import (
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
