package store_test

import (
	"context"
	"errors"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var _ = Describe("Device status updates with a merge", func() {
	var (
		log      *logrus.Logger
		ctx      context.Context
		orgId    uuid.UUID
		devStore *devicestore.DeviceStore
		cfg      *config.Config
		db       *gorm.DB
		dbName   string
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		devStore = devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		orgId = uuid.New()
		Expect(testutil.CreateTestOrganization(ctx, organizationstore.NewOrganizationStore(db), orgId)).To(Succeed())
		testutil.CreateTestDevices(ctx, 1, devStore, orgId, nil, false)
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	const name = "mydevice-1"
	stored := func() *api.Device {
		device, err := devStore.Get(ctx, orgId, name)
		Expect(err).ToNot(HaveOccurred())
		return device
	}
	withSummary := func(device *api.Device, info string) *api.Device {
		updated := *device
		status := *device.Status
		status.Summary.Info = lo.ToPtr(info)
		updated.Status = &status
		return &updated
	}
	firmwareAttempt := func(id string) *api.FirmwareStatus {
		return &api.FirmwareStatus{
			Support: api.FirmwareSupportStatus{ApiVersion: 1, Updaters: []api.FirmwareSupportedUpdater{}},
			Attempt: &api.FirmwareAttemptStatus{Id: id, Phase: api.FirmwareAttemptPhaseStaging, Recovery: []api.FirmwareRecoveryAction{}},
		}
	}

	It("When a merge is given it should derive the written status from the device loaded for every attempt", func() {
		original := stored()
		incoming := withSummary(original, "incoming")
		concurrentStore := devicestore.NewDeviceStore(db, log)
		interleaved := false
		devStore.SetIntegrationTestCreateOrUpdateCallback(func() {
			if interleaved {
				return
			}
			interleaved = true
			concurrent := stored()
			concurrent.Status.Firmware = firmwareAttempt("attempt-1")
			_, _, err := concurrentStore.UpdateStatus(ctx, orgId, concurrent, nil)
			Expect(err).ToNot(HaveOccurred())
		})
		var seen []*api.FirmwareStatus
		merge := func(current *api.Device) (*api.DeviceStatus, error) {
			seen = append(seen, current.Status.Firmware)
			status := *incoming.Status
			status.Firmware = current.Status.Firmware
			return &status, nil
		}

		updated, _, err := devStore.UpdateStatus(ctx, orgId, incoming, original, devicestore.WithStatusMerge(merge))

		Expect(err).ToNot(HaveOccurred())
		Expect(seen).To(HaveLen(2))
		Expect(seen[0]).To(BeNil())
		Expect(seen[1]).ToNot(BeNil())
		after := stored()
		Expect(after.Status.Firmware).ToNot(BeNil())
		Expect(after.Status.Firmware.Attempt.Id).To(Equal("attempt-1"))
		Expect(lo.FromPtr(after.Status.Summary.Info)).To(Equal("incoming"))
		Expect(updated.Status.Firmware.Attempt.Id).To(Equal("attempt-1"))
		Expect(incoming.Status.Firmware).To(BeNil(), "the given device must not be modified")
	})

	It("When the merge rejects the write it should leave the status and resource version unchanged", func() {
		original := stored()
		rejection := errors.New("rejected by merge")

		_, _, err := devStore.UpdateStatus(ctx, orgId, withSummary(original, "rejected"), original,
			devicestore.WithStatusMerge(func(*api.Device) (*api.DeviceStatus, error) { return nil, rejection }))

		Expect(err).To(MatchError(rejection))
		after := stored()
		Expect(after.Status).To(Equal(original.Status))
		Expect(after.Metadata.ResourceVersion).To(Equal(original.Metadata.ResourceVersion))
	})

	It("When no merge is given it should write the given status as before without running the merge hook", func() {
		original := stored()
		hookCalls := 0
		devStore.SetIntegrationTestCreateOrUpdateCallback(func() { hookCalls++ })

		_, _, err := devStore.UpdateStatus(ctx, orgId, withSummary(original, "plain"), original)

		Expect(err).ToNot(HaveOccurred())
		Expect(lo.FromPtr(stored().Status.Summary.Info)).To(Equal("plain"))
		Expect(hookCalls).To(BeZero())
	})
})
