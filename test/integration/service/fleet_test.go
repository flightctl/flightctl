package service_test

import (
	"context"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/consts"
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
	DescribeTable("ReplaceFleet nilled owner",
		func(internal bool, expectedOwner string) {
			ctx := context.WithValue(suite.Ctx, consts.InternalRequestCtxKey, internal)
			fleet := &api.Fleet{
				Metadata: api.ObjectMeta{
					Name:  lo.ToPtr("test-fleet"),
					Owner: lo.ToPtr("test-owner"),
				},
				Spec: api.FleetSpec{},
			}
			createdFleet, status := suite.Handler.ReplaceFleet(ctx, suite.OrgID, "test-fleet", *fleet)
			Expect(status.Code).To(Equal(int32(http.StatusCreated)))
			Expect(lo.FromPtr(createdFleet.Metadata.Name)).To(Equal("test-fleet"))
			Expect(lo.FromPtr(createdFleet.Metadata.Owner)).To(Equal(expectedOwner))

			retrievedFleet, status := suite.Handler.GetFleet(ctx, suite.OrgID, "test-fleet", api.GetFleetParams{})
			Expect(status.Code).To(Equal(int32(http.StatusOK)))
			Expect(lo.FromPtr(retrievedFleet.Metadata.Name)).To(Equal("test-fleet"))
			Expect(lo.FromPtr(retrievedFleet.Metadata.Owner)).To(Equal(expectedOwner))
		},
		Entry("Internal request should keep owner", true, "test-owner"),
		Entry("Non-internal request should nil owner", false, ""),
	)
})
