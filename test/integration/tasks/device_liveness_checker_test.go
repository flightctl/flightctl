package tasks_test

import (
	"context"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var _ = Describe("DeviceLivenessChecker", func() {
	var (
		log             *logrus.Logger
		ctx             context.Context
		orgId           uuid.UUID
		storeInst       store.Store
		cfg             *config.Config
		dbName          string
		taskManager     tasks.TaskManager
		now             time.Time
		livenessChecker *tasks.DeviceLivenessChecker
		deviceName      string
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		storeInst, cfg, dbName = store.PrepareDBForUnitTests(log)
		taskManager = tasks.Init(log, storeInst)

		// Expiration warning is set at 30m, and expiration error at 1h
		deviceName = "mydevice"
		testutil.CreateTestDevice(ctx, storeInst.Device(), orgId, deviceName, nil, nil, nil)

		// Override time methods
		now = time.Now()
		timeGetter := store.TimeGetter(func() time.Time {
			return now
		})
		storeInst.Device().OverrideTimeGetterForTesting(timeGetter)

		livenessChecker = tasks.NewDeviceLivenessChecker(taskManager)
		livenessChecker.OverrideTimeGetterForTesting(tasks.TimeGetter(timeGetter))
	})

	AfterEach(func() {
		store.DeleteTestDB(cfg, storeInst, dbName)
	})

	When("a device has expired", func() {
		It("set the appropriate conditions", func() {
			// The device checks in "now"
			device := &api.Device{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr(deviceName),
				},
				Status: &api.DeviceStatus{},
			}
			_, err := storeInst.Device().UpdateStatus(ctx, orgId, device)
			Expect(err).ToNot(HaveOccurred())

			// 20 minutes pass, the device should still not be expired
			now = now.Add(20 * time.Minute)
			livenessChecker.Poll()
			Expect(err).ToNot(HaveOccurred())
			device, err = storeInst.Device().Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(device.Status.Conditions).To(BeNil())

			// Another 20 minutes pass (40 total), the device should have a warning set
			now = now.Add(20 * time.Minute)
			livenessChecker.Poll()
			Expect(err).ToNot(HaveOccurred())
			device, err = storeInst.Device().Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(api.IsStatusConditionTrue(*device.Status.Conditions, api.DeviceHeartbeatWarning)).To(BeTrue())
			Expect(api.IsStatusConditionTrue(*device.Status.Conditions, api.DeviceHeartbeatError)).To(BeFalse())

			// Another 30 minutes pass (70 total), the device should have a warning and error set
			now = now.Add(30 * time.Minute)
			livenessChecker.Poll()
			Expect(err).ToNot(HaveOccurred())
			device, err = storeInst.Device().Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(api.IsStatusConditionTrue(*device.Status.Conditions, api.DeviceHeartbeatWarning)).To(BeTrue())
			Expect(api.IsStatusConditionTrue(*device.Status.Conditions, api.DeviceHeartbeatError)).To(BeTrue())

			// After the device checks in again, the warning and error should be reset
			device = &api.Device{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr(deviceName),
				},
				Status: &api.DeviceStatus{},
			}
			_, err = storeInst.Device().UpdateStatus(ctx, orgId, device)
			Expect(err).ToNot(HaveOccurred())
			device, err = storeInst.Device().Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(api.IsStatusConditionTrue(*device.Status.Conditions, api.DeviceHeartbeatWarning)).To(BeFalse())
			Expect(api.IsStatusConditionTrue(*device.Status.Conditions, api.DeviceHeartbeatError)).To(BeFalse())
		})
	})
})
