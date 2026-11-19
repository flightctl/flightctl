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

var _ = Describe("ReplaceFleetRequireExisting", func() {
	var suite *ServiceTestSuite

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
	})

	AfterEach(func() {
		suite.Teardown()
	})

	It("should return NotFound when the fleet does not exist", func() {
		fleet := api.Fleet{
			Metadata: api.ObjectMeta{
				Name: lo.ToPtr("absent-fleet"),
			},
			Spec: api.FleetSpec{},
		}
		_, status := suite.Fleet.ReplaceFleetRequireExisting(suite.Ctx, suite.OrgID, "absent-fleet", fleet)
		Expect(status.Code).To(Equal(int32(http.StatusNotFound)))
	})

	It("should update an existing fleet and return the updated resource", func() {
		// Create the fleet first via normal ReplaceFleet (create-or-replace).
		original := api.Fleet{
			Metadata: api.ObjectMeta{
				Name: lo.ToPtr("existing-fleet"),
			},
			Spec: api.FleetSpec{},
		}
		created, status := suite.Fleet.ReplaceFleet(suite.Ctx, suite.OrgID, "existing-fleet", original, false)
		Expect(status.Code).To(Equal(int32(http.StatusCreated)))
		Expect(created.Metadata.ResourceVersion).NotTo(BeNil())

		// Update via RequireExisting with the resource version from create.
		updated := *created
		updated.Spec.Template.Spec.Os = &api.DeviceOsSpec{Image: "quay.io/org/os:v2"}
		result, status := suite.Fleet.ReplaceFleetRequireExisting(suite.Ctx, suite.OrgID, "existing-fleet", updated)
		Expect(status.Code).To(Equal(int32(http.StatusOK)))
		Expect(result.Spec.Template.Spec.Os.Image).To(Equal("quay.io/org/os:v2"))
	})

	It("should return Conflict on stale resource version", func() {
		original := api.Fleet{
			Metadata: api.ObjectMeta{
				Name: lo.ToPtr("cas-fleet"),
			},
			Spec: api.FleetSpec{},
		}
		created, status := suite.Fleet.ReplaceFleet(suite.Ctx, suite.OrgID, "cas-fleet", original, false)
		Expect(status.Code).To(Equal(int32(http.StatusCreated)))

		// First update succeeds, advancing the resource version.
		update1 := *created
		_, status = suite.Fleet.ReplaceFleetRequireExisting(suite.Ctx, suite.OrgID, "cas-fleet", update1)
		Expect(status.Code).To(Equal(int32(http.StatusOK)))

		// Second update with the OLD resource version must fail.
		update2 := *created // same stale RV
		_, status = suite.Fleet.ReplaceFleetRequireExisting(suite.Ctx, suite.OrgID, "cas-fleet", update2)
		Expect(status.Code).To(Equal(int32(http.StatusConflict)))
	})

	It("should not re-create a fleet deleted after read", func() {
		original := api.Fleet{
			Metadata: api.ObjectMeta{
				Name: lo.ToPtr("delete-race-fleet"),
			},
			Spec: api.FleetSpec{},
		}
		created, status := suite.Fleet.ReplaceFleet(suite.Ctx, suite.OrgID, "delete-race-fleet", original, false)
		Expect(status.Code).To(Equal(int32(http.StatusCreated)))

		// Delete the fleet.
		status = suite.Fleet.DeleteFleet(suite.Ctx, suite.OrgID, "delete-race-fleet", false)
		Expect(status.Code).To(Equal(int32(http.StatusOK)))

		// Attempt to update with the old resource version — must return NotFound.
		update := *created
		update.Spec.Template.Spec.Os = &api.DeviceOsSpec{Image: "quay.io/org/os:v2"}
		_, status = suite.Fleet.ReplaceFleetRequireExisting(suite.Ctx, suite.OrgID, "delete-race-fleet", update)
		Expect(status.Code).To(Equal(int32(http.StatusNotFound)))

		// Verify the fleet remains absent.
		_, status = suite.Fleet.GetFleet(suite.Ctx, suite.OrgID, "delete-race-fleet", api.GetFleetParams{})
		Expect(status.Code).To(Equal(int32(http.StatusNotFound)))
	})

	It("should preserve normal ReplaceFleet create-when-absent behavior", func() {
		fleet := api.Fleet{
			Metadata: api.ObjectMeta{
				Name: lo.ToPtr("public-create-fleet"),
			},
			Spec: api.FleetSpec{},
		}
		created, status := suite.Fleet.ReplaceFleet(suite.Ctx, suite.OrgID, "public-create-fleet", fleet, false)
		Expect(status.Code).To(Equal(int32(http.StatusCreated)))
		Expect(lo.FromPtr(created.Metadata.Name)).To(Equal("public-create-fleet"))
	})
})
