package service_test

import (
	"context"
	"net/http"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks_client"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
)

func TestService(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Service Suite")
}

var (
	suiteCtx context.Context
)

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Store Suite")
})

var _ = Describe("Fleet create", func() {
	var (
		serviceInst     service.Service
		ctrl            *gomock.Controller
		mockPublisher   *queues.MockPublisher
		callbackManager tasks_client.CallbackManager
		log             *logrus.Logger
		ctx             context.Context
		storeInst       store.Store
		cfg             *config.Config
		dbName          string
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(ctx, log)
		ctrl = gomock.NewController(GinkgoT())
		mockPublisher = queues.NewMockPublisher(ctrl)
		callbackManager = tasks_client.NewCallbackManager(mockPublisher, log)
		mockPublisher.EXPECT().Publish(gomock.Any(), gomock.Any()).AnyTimes()
		kvStore, err := kvstore.NewKVStore(ctx, log, "localhost", 6379, "adminpass")
		Expect(err).ToNot(HaveOccurred())
		serviceInst = service.NewServiceHandler(storeInst, callbackManager, kvStore, nil, log, "", "")
	})

	AfterEach(func() {
		store.DeleteTestDB(ctx, log, cfg, storeInst, dbName)
	})
	DescribeTable("ReplaceFleet nilled owner",
		func(internal bool, expectedOwner string) {
			ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, internal)
			fleet := &api.Fleet{
				Metadata: api.ObjectMeta{
					Name:  lo.ToPtr("test-fleet"),
					Owner: lo.ToPtr("test-owner"),
				},
				Spec: api.FleetSpec{},
			}
			createdFleet, status := serviceInst.ReplaceFleet(ctx, "test-fleet", *fleet)
			Expect(status.Code).To(Equal(int32(http.StatusCreated)))
			Expect(lo.FromPtr(createdFleet.Metadata.Name)).To(Equal("test-fleet"))
			Expect(lo.FromPtr(createdFleet.Metadata.Owner)).To(Equal(expectedOwner))

			retrievedFleet, status := serviceInst.GetFleet(ctx, "test-fleet", api.GetFleetParams{})
			Expect(status.Code).To(Equal(int32(http.StatusOK)))
			Expect(lo.FromPtr(retrievedFleet.Metadata.Name)).To(Equal("test-fleet"))
			Expect(lo.FromPtr(retrievedFleet.Metadata.Owner)).To(Equal(expectedOwner))
		},
		Entry("Internal request should keep owner", true, "test-owner"),
		Entry("Non-internal request should nil owner", false, ""),
	)
})
