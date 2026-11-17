package tasks_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/rendered"
	catalogservice "github.com/flightctl/flightctl/internal/service/updatercatalog"
	"github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	"github.com/flightctl/flightctl/internal/store/model"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	catalogstore "github.com/flightctl/flightctl/internal/store/updatercatalog"
	"github.com/flightctl/flightctl/internal/tasks"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/firmwarecatalog"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
	"gorm.io/gorm"
)

type policyRecipient struct {
	Org    uuid.UUID
	Name   string
	Before *domain.Device
}
type policyHint struct {
	Org             uuid.UUID `json:"org_id"`
	Name            string    `json:"name"`
	Revision        int64     `json:"policy_revision"`
	RenderedVersion string    `json:"render_version"`
}

var _ = Describe("Durable updater policy fanout", func() {
	var (
		ctx           context.Context
		cfg           *config.Config
		dbName        string
		db            *gorm.DB
		kv            kvstore.KVStore
		broker        queues.Provider
		catalog       *catalogservice.Service
		st            *catalogstore.Store
		devices       *devicestore.DeviceStore
		task          *tasks.UpdaterCatalogRefresh
		recipients    []policyRecipient
		input         catalogservice.AdministratorConfig
		cancel        context.CancelFunc
		subscriptions []queues.Subscription
		renderedKeys  []string
	)
	BeforeEach(func() {
		ctx, cancel = context.WithCancel(testutil.StartSpecTracerForGinkgo(suiteCtx))
		subscriptions = nil
		renderedKeys = nil
		log := flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		kv, err = kvstore.NewKVStore(ctx, log, redisHost, redisPort, redisPassword)
		Expect(err).NotTo(HaveOccurred())
		broker, err = queues.NewRedisProvider(ctx, log, "policy-test-"+uuid.NewString(), redisHost, redisPort, redisPassword, queues.DefaultRetryConfig())
		Expect(err).NotTo(HaveOccurred())
		st, err = catalogstore.NewStore(db)
		Expect(err).NotTo(HaveOccurred())
		catalog, err = catalogservice.NewService(st)
		Expect(err).NotTo(HaveOccurred())
		notifier, err := rendered.NewFirmwarePolicyNotifier(broker)
		Expect(err).NotTo(HaveOccurred())
		task, err = tasks.NewUpdaterCatalogRefresh(catalog, notifier, log)
		Expect(err).NotTo(HaveOccurred())
		devices = devicestore.NewDeviceStore(db, log)
		orgB := uuid.New()
		Expect(testutil.CreateTestOrganization(ctx, organizationstore.NewOrganizationStore(db), orgB)).To(Succeed())
		recipients = nil
		prefix := uuid.NewString()[:8]
		for _, org := range []uuid.UUID{uuid.Nil, orgB} {
			for _, kind := range []string{"legacy", "invalid", "pinned"} {
				name := prefix + "-" + kind
				device := &domain.Device{ApiVersion: "v1beta1", Kind: domain.DeviceKind, Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)}, Spec: &domain.DeviceSpec{}}
				if kind == "invalid" {
					device.Spec.Firmware = &domain.FirmwareSpec{Profile: "unsupported", Updater: domain.FirmwareUpdaterSpec{Name: "unknown", Version: "1", Config: map[string]any{}}}
				}
				if kind == "pinned" {
					status := domain.NewDeviceStatus()
					status.Firmware = &domain.FirmwareStatus{Attempt: &domain.FirmwareAttemptStatus{Id: "pinned", Phase: domain.FirmwareAttemptPhaseFailed}}
					device.Status = &status
				}
				created, err := devices.Create(ctx, org, device, nil)
				Expect(err).NotTo(HaveOccurred())
				recipients = append(recipients, policyRecipient{Org: org, Name: name, Before: created})
				renderedKey := fmt.Sprintf("v1/%s/device/%s/rendered", org, name)
				Expect(kv.Set(ctx, renderedKey, []byte("7"), 0)).To(Succeed())
				renderedKeys = append(renderedKeys, renderedKey)
			}
		}
		input, err = firmwarecatalog.New()
		Expect(err).NotTo(HaveOccurred())
		_, err = catalog.Activate(ctx, input)
		Expect(err).NotTo(HaveOccurred())
	})
	AfterEach(func() {
		cancel()
		for _, sub := range subscriptions {
			sub.Close()
		}
		broker.Stop()
		broker.Wait()
		cleanupCtx := context.WithoutCancel(ctx)
		for _, key := range renderedKeys {
			Expect(kv.Delete(cleanupCtx, key)).To(Succeed())
		}
		kv.Close()
		Expect(testdb.DeleteTestDB(context.WithoutCancel(ctx), flightlog.InitLogs(), cfg, db, dbName)).To(Succeed())
	})
	drain := func(worker *tasks.UpdaterCatalogRefresh, limit int) {
		for range 100 {
			work, err := worker.DispatchPending(ctx, limit)
			Expect(err).NotTo(HaveOccurred())
			if !work {
				return
			}
		}
		Fail("fanout did not converge")
	}
	unchanged := func() {
		for _, r := range recipients {
			after, err := devices.Get(ctx, r.Org, r.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(after.Spec).To(Equal(r.Before.Spec))
			Expect(after.Status).To(Equal(r.Before.Status))
			Expect(after.Metadata.Generation).To(Equal(r.Before.Metadata.Generation))
			Expect(after.Metadata.ResourceVersion).To(Equal(r.Before.Metadata.ResourceVersion))
			value, err := kv.Get(ctx, fmt.Sprintf("v1/%s/device/%s/rendered", r.Org, r.Name))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(value)).To(Equal("7"))
		}
	}
	ddl := func(statement string) {
		admin, err := store.InitMigrationDB(cfg, flightlog.InitLogs())
		Expect(err).NotTo(HaveOccurred())
		sqlDB, err := admin.DB()
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(sqlDB.Close()).To(Succeed()) }()
		Expect(admin.WithContext(ctx).Exec(statement).Error).To(Succeed())
	}
	It("When fanout spans organizations and invalid or unchanged intent it should notify every identity without touching targets", func() {
		sub, err := broker.NewPubSubSubscriber(ctx, "firmware_policy_notifier")
		Expect(err).NotTo(HaveOccurred())
		received := make(chan policyHint, 100)
		subscription, err := sub.Subscribe(ctx, func(_ context.Context, payload []byte, _ logrus.FieldLogger) error {
			var message policyHint
			if err := json.Unmarshal(payload, &message); err != nil {
				return err
			}
			select {
			case received <- message:
			default:
			}
			return nil
		})
		Expect(err).NotTo(HaveOccurred())
		subscriptions = append(subscriptions, subscription)
		probe, err := broker.NewPubSubPublisher(ctx, "firmware_policy_notifier")
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			if err := probe.Publish(ctx, []byte(`{"name":"__probe__"}`)); err != nil {
				return false
			}
			select {
			case message := <-received:
				return message.Name == "__probe__"
			default:
				return false
			}
		}, 5*time.Second, 10*time.Millisecond).Should(BeTrue())
		drain(task, 2)
		seen := map[string]bool{}
		Eventually(func() int {
			for {
				select {
				case message := <-received:
					if message.Revision == 0 {
						continue
					}
					Expect(message.Revision).To(Equal(int64(1)))
					Expect(message.RenderedVersion).To(BeEmpty())
					seen[message.Org.String()+"/"+message.Name] = true
				default:
					return len(seen)
				}
			}
		}, 5*time.Second, 10*time.Millisecond).Should(Equal(6))
		for _, r := range recipients {
			Expect(seen[r.Org.String()+"/"+r.Name]).To(BeTrue())
		}
		pending, err := catalog.PendingNotifications(ctx, 20)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(BeEmpty())
		unchanged()
	})
	It("When processing restarts between pages it should retain its cursor and pending recipients", func() {
		worked, err := catalog.ExpandPendingRefresh(ctx, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(worked).To(BeTrue())
		fresh, err := catalogstore.NewStore(db.Session(&gorm.Session{NewDB: true}))
		Expect(err).NotTo(HaveOccurred())
		service, err := catalogservice.NewService(fresh)
		Expect(err).NotTo(HaveOccurred())
		notifier, err := rendered.NewFirmwarePolicyNotifier(broker)
		Expect(err).NotTo(HaveOccurred())
		restarted, err := tasks.NewUpdaterCatalogRefresh(service, notifier, flightlog.InitLogs())
		Expect(err).NotTo(HaveOccurred())
		drain(restarted, 1)
		pending, err := catalog.PendingNotifications(ctx, 20)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(BeEmpty())
		var rows int64
		Expect(db.WithContext(ctx).Model(&model.UpdaterPolicyTarget{}).Count(&rows).Error).To(Succeed())
		Expect(rows).To(Equal(int64(6)))
		unchanged()
	})
	It("When replicas expand and publish concurrently it should converge without lost or duplicate target records", func() {
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 100 {
					work, err := task.DispatchPending(ctx, 2)
					if err != nil {
						errs <- err
						return
					}
					if !work {
						return
					}
				}
				errs <- errors.New("did not converge")
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			Expect(err).NotTo(HaveOccurred())
		}
		drain(task, 2)
		var rows int64
		Expect(db.WithContext(ctx).Model(&model.UpdaterPolicyTarget{}).Count(&rows).Error).To(Succeed())
		Expect(rows).To(Equal(int64(6)))
		pending, err := catalog.PendingNotifications(ctx, 20)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(BeEmpty())
		unchanged()
	})
	It("When a higher revision arrives mid-scan it should coalesce old work into current authorization", func() {
		_, err := catalog.ExpandPendingRefresh(ctx, 1)
		Expect(err).NotTo(HaveOccurred())
		input.Revision = 2
		input.Releases = nil
		_, err = catalog.Activate(ctx, input)
		Expect(err).NotTo(HaveOccurred())
		drain(task, 2)
		var targets []model.UpdaterPolicyTarget
		Expect(db.WithContext(ctx).Find(&targets).Error).To(Succeed())
		Expect(targets).To(HaveLen(6))
		for _, target := range targets {
			Expect(target.Revision).To(Equal(int64(2)))
			Expect(target.NotifiedRevision).To(Equal(int64(2)))
		}
		unchanged()
	})
	It("When publication fails it should retain pending work and republish on retry", func() {
		ctrl := gomock.NewController(GinkgoT())
		provider := queues.NewMockProvider(ctrl)
		pub := queues.NewMockPubSubPublisher(ctrl)
		provider.EXPECT().NewPubSubPublisher(gomock.Any(), "firmware_policy_notifier").Return(pub, nil)
		var calls atomic.Int64
		pub.EXPECT().Publish(gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, []byte) error {
			if calls.Add(1) == 1 {
				return errors.New("broker unavailable")
			}
			return nil
		}).AnyTimes()
		notifier, err := rendered.NewFirmwarePolicyNotifier(provider)
		Expect(err).NotTo(HaveOccurred())
		retry, err := tasks.NewUpdaterCatalogRefresh(catalog, notifier, flightlog.InitLogs())
		Expect(err).NotTo(HaveOccurred())
		_, err = retry.DispatchPending(ctx, 20)
		Expect(err).To(HaveOccurred())
		pending, err := catalog.PendingNotifications(ctx, 20)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(HaveLen(6))
		drain(retry, 20)
		Expect(calls.Load()).To(Equal(int64(7)))
		unchanged()
	})
	It("When recording publication fails it should retry an already published revision", func() {
		ddl(`ALTER TABLE updater_policy_targets ADD CONSTRAINT fail_notified CHECK (notified_revision < 1)`)
		_, err := task.DispatchPending(ctx, 20)
		Expect(err).To(HaveOccurred())
		pending, err := catalog.PendingNotifications(ctx, 20)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(HaveLen(6))
		ddl(`ALTER TABLE updater_policy_targets DROP CONSTRAINT fail_notified`)
		drain(task, 20)
		pending, err = catalog.PendingNotifications(ctx, 20)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(BeEmpty())
		unchanged()
	})
	It("When recipient persistence fails it should roll back the scan cursor and retry the same page", func() {
		ddl(`ALTER TABLE updater_policy_targets ADD CONSTRAINT fail_target CHECK (revision < 1)`)
		_, err := task.DispatchPending(ctx, 2)
		Expect(err).To(HaveOccurred())
		var job model.UpdaterCatalogRefresh
		Expect(db.WithContext(ctx).First(&job, 1).Error).To(Succeed())
		Expect(job.CursorName).To(BeEmpty())
		Expect(job.Dispatched).To(BeFalse())
		var rows int64
		Expect(db.WithContext(ctx).Model(&model.UpdaterPolicyTarget{}).Count(&rows).Error).To(Succeed())
		Expect(rows).To(BeZero())
		ddl(`ALTER TABLE updater_policy_targets DROP CONSTRAINT fail_target`)
		drain(task, 2)
		unchanged()
	})
})
