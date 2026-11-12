package store_test

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
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

var _ = Describe("Device completion counts with firmware state", func() {
	const templateVersion = "tv-1"
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
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	// completedDevice creates the only device of its own fleet, selected for
	// rollout and reporting that it runs the rendered version of the template
	// version, and stores firmwareSpec and firmwareStatus as raw JSON.
	completedDevice := func(name, firmwareSpec, firmwareStatus string) string {
		owner := "Fleet/" + name
		status := api.NewDeviceStatus()
		status.Config.RenderedVersion = "5"
		_, err := devStore.Create(ctx, orgId, &api.Device{
			Metadata: api.ObjectMeta{
				Name:  lo.ToPtr(name),
				Owner: lo.ToPtr(owner),
				Annotations: &map[string]string{
					api.DeviceAnnotationSelectedForRollout:      "",
					api.DeviceAnnotationTemplateVersion:         templateVersion,
					api.DeviceAnnotationRenderedTemplateVersion: templateVersion,
					api.DeviceAnnotationRenderedVersion:         "5",
				},
			},
			Spec:   &api.DeviceSpec{Os: &api.DeviceOsSpec{Image: "quay.io/example/os:1"}},
			Status: &status,
		}, nil)
		Expect(err).ToNot(HaveOccurred())
		if firmwareSpec != "" {
			Expect(db.WithContext(ctx).Exec(`UPDATE devices SET spec = jsonb_set(spec, '{firmware}', ?::jsonb) WHERE org_id = ? AND name = ?`,
				firmwareSpec, orgId, name).Error).To(Succeed())
		}
		if firmwareStatus != "" {
			Expect(db.WithContext(ctx).Exec(`UPDATE devices SET status = jsonb_set(status, '{firmware}', ?::jsonb) WHERE org_id = ? AND name = ?`,
				firmwareStatus, orgId, name).Error).To(Succeed())
		}
		return owner
	}

	const desiredFirmware = `{"profile":"board-a","updater":{"name":"vendor-updater","version":"1.0.0","config":{}},` +
		`"components":[{"name":"bios","version":{"exact":"2.0"}}],"overlays":[]}`
	attempt := func(phase string) string {
		return fmt.Sprintf(`{"bootID":"boot-1","observedAt":"2026-01-02T03:04:05Z","support":{"apiVersion":1,"supported":true,"updaters":[]},`+
			`"components":[],"attempt":{"id":"attempt-1","phase":%s,"recovery":[],"changed":false,"renderedVersion":"5","retryGeneration":0,`+
			`"specHash":"hash","startedAt":"2026-01-02T03:04:05Z"}}`, phase)
	}

	DescribeTable("When a device has reached the rendered template version it should count as complete only without a firmware hold",
		func(firmwareSpec, firmwareStatus string, held bool) {
			name := "dev-" + uuid.NewString()[:8]
			owner := completedDevice(name, firmwareSpec, firmwareStatus)
			device, err := devStore.Get(ctx, orgId, name)
			Expect(err).ToNot(HaveOccurred())
			Expect(domain.DeviceRequiresFirmwareHold(device)).To(Equal(held), "the stored state must match the hold predicate")

			counts, err := devStore.CompletionCounts(ctx, orgId, owner, templateVersion, nil)

			Expect(err).ToNot(HaveOccurred())
			Expect(counts).To(HaveLen(1))
			Expect(counts[0].Count).To(Equal(int64(1)))
			Expect(counts[0].SameTemplateVersion).To(BeTrue())
			Expect(counts[0].SameRenderedVersion).To(Equal(!held))
		},
		Entry("desired firmware", desiredFirmware, "", true),
		Entry("desired firmware stored as JSON null", "null", "", false),
		Entry("an outstanding attempt", "", attempt(`"Staging"`), true),
		Entry("an attempt in an unknown phase", "", attempt(`"Mystery"`), true),
		Entry("an attempt with an empty phase", "", attempt(`""`), true),
		Entry("an attempt whose phase is JSON null", "", attempt(`null`), true),
		Entry("an attempt stored as JSON null", "", `{"bootID":"boot-1","observedAt":"2026-01-02T03:04:05Z","support":{"apiVersion":1,"supported":true,"updaters":[]},"components":[],"attempt":null}`, false),
		Entry("reported firmware stored as JSON null", "", "null", false),
		Entry("a succeeded attempt", "", attempt(`"Succeeded"`), false),
		Entry("a failed attempt", "", attempt(`"Failed"`), false),
		Entry("a canceled attempt", "", attempt(`"Canceled"`), false),
		Entry("desired firmware and a succeeded attempt", desiredFirmware, attempt(`"Succeeded"`), true),
		Entry("no firmware state", "", "", false),
	)

	It("When a held device has not reached the rendered version it should still count as incomplete", func() {
		owner := completedDevice("dev-stale", desiredFirmware, "")
		Expect(db.WithContext(ctx).Exec(`UPDATE devices SET status = jsonb_set(status, '{config,renderedVersion}', '"4"') WHERE org_id = ? AND name = ?`,
			orgId, "dev-stale").Error).To(Succeed())

		counts, err := devStore.CompletionCounts(ctx, orgId, owner, templateVersion, nil)

		Expect(err).ToNot(HaveOccurred())
		Expect(counts).To(HaveLen(1))
		Expect(counts[0].SameRenderedVersion).To(BeFalse())
	})
})
