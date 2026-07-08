package store_test

import (
	"context"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	"github.com/flightctl/flightctl/internal/store/model"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	"github.com/flightctl/flightctl/internal/store/selector"
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

var _ = Describe("EventStore Integration Tests", func() {
	var (
		log        *logrus.Logger
		ctx        context.Context
		orgId      uuid.UUID
		eventStore eventstore.Store
		cfg        *config.Config
		dbName     string
		db         *gorm.DB
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		eventStore = eventstore.NewEventStore(db, log.WithField("pkg", "event-store"))
		organizationStore := organizationstore.NewOrganizationStore(db)

		orgId = uuid.New()
		err = testutil.CreateTestOrganization(ctx, organizationStore, orgId)
		Expect(err).ToNot(HaveOccurred())

		createEvents(ctx, eventStore, orgId)

		now := time.Now()
		oneMinuteAgo := now.Add(-1 * time.Minute)

		// Set event1, event2, event3 to 1 minute ago
		if err := db.WithContext(ctx).Model(&model.Event{}).
			Where("name IN ?", []string{"event1", "event2", "event3"}).
			UpdateColumn("created_at", oneMinuteAgo).Error; err != nil {
			Expect(err).ToNot(HaveOccurred())
		}

		// Set event4, event5, event6 to now
		if err := db.WithContext(ctx).Model(&model.Event{}).
			Where("name IN ?", []string{"event4", "event5", "event6"}).
			UpdateColumn("created_at", now).Error; err != nil {
			Expect(err).ToNot(HaveOccurred())
		}
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	Context("Event Store", func() {
		It("List all events", func() {
			listParams := store.ListParams{Limit: 100, SortColumns: []store.SortColumn{store.SortByCreatedAt, store.SortByName}, SortOrder: lo.ToPtr(store.SortDesc)}
			eventList, err := eventStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventList.Items).To(HaveLen(6))

			// Verify order (should be descending by timestamp - newest first)
			Expect(*(eventList.Items[0].Metadata.Name)).To(Equal("event6"))
			Expect(*(eventList.Items[1].Metadata.Name)).To(Equal("event5"))
			Expect(*(eventList.Items[2].Metadata.Name)).To(Equal("event4"))
			Expect(*(eventList.Items[3].Metadata.Name)).To(Equal("event3"))
			Expect(*(eventList.Items[4].Metadata.Name)).To(Equal("event2"))
			Expect(*(eventList.Items[5].Metadata.Name)).To(Equal("event1"))
		})

		It("List all events with paging", func() {
			listParams := store.ListParams{Limit: 2, SortColumns: []store.SortColumn{store.SortByCreatedAt, store.SortByName}, SortOrder: lo.ToPtr(store.SortDesc)}
			eventList, err := eventStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventList.Items).To(HaveLen(2))

			// Verify order (should be descending by timestamp - newest first)
			Expect(*(eventList.Items[0].Metadata.Name)).To(Equal("event6"))
			Expect(*(eventList.Items[1].Metadata.Name)).To(Equal("event5"))
			Expect(eventList.Metadata.Continue).ToNot(BeNil())
			Expect(eventList.Metadata.RemainingItemCount).ToNot(BeNil())
			Expect(*eventList.Metadata.RemainingItemCount).To(Equal(int64(4)))

			cont, err := store.ParseContinueString(eventList.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			eventList, err = eventStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventList.Items).To(HaveLen(2))
			Expect(*(eventList.Items[0].Metadata.Name)).To(Equal("event4"))
			Expect(*(eventList.Items[1].Metadata.Name)).To(Equal("event3"))
			Expect(eventList.Metadata.Continue).ToNot(BeNil())
			Expect(eventList.Metadata.RemainingItemCount).ToNot(BeNil())
			Expect(*eventList.Metadata.RemainingItemCount).To(Equal(int64(2)))

			cont, err = store.ParseContinueString(eventList.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			eventList, err = eventStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventList.Items).To(HaveLen(2))
			Expect(*(eventList.Items[0].Metadata.Name)).To(Equal("event2"))
			Expect(*(eventList.Items[1].Metadata.Name)).To(Equal("event1"))
			Expect(eventList.Metadata.Continue).To(BeNil())
			Expect(eventList.Metadata.RemainingItemCount).To(BeNil())
		})

		It("Filters events by reason", func() {
			listParams := store.ListParams{
				Limit:       100,
				SortColumns: []store.SortColumn{store.SortByCreatedAt, store.SortByName},
				SortOrder:   lo.ToPtr(store.SortDesc),
				FieldSelector: selector.NewFieldSelectorFromMapOrDie(
					map[string]string{"reason": string(api.EventReasonResourceDeleted)}, selector.WithPrivateSelectors()),
			}

			eventList, err := eventStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(eventList.Items)).To(Equal(2))
			Expect(*(eventList.Items[0].Metadata.Name)).To(Equal("event5"))
			Expect(*(eventList.Items[1].Metadata.Name)).To(Equal("event2"))
		})

		It("Filters events by actor", func() {
			listParams := store.ListParams{
				Limit:       100,
				SortColumns: []store.SortColumn{store.SortByCreatedAt, store.SortByName},
				SortOrder:   lo.ToPtr(store.SortDesc),
				FieldSelector: selector.NewFieldSelectorFromMapOrDie(
					map[string]string{"actor": "user:admin"}, selector.WithPrivateSelectors()),
			}

			eventList, err := eventStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(eventList.Items)).To(Equal(2))
			Expect(*(eventList.Items[0].Metadata.Name)).To(Equal("event5"))
			Expect(*(eventList.Items[1].Metadata.Name)).To(Equal("event2"))
		})

		It("Filters events by involved object", func() {
			listParams := store.ListParams{
				Limit:       100,
				SortColumns: []store.SortColumn{store.SortByCreatedAt, store.SortByName},
				SortOrder:   lo.ToPtr(store.SortDesc),
				FieldSelector: selector.NewFieldSelectorFromMapOrDie(
					map[string]string{"involvedObject.kind": string(api.DeviceKind), "involvedObject.name": "event2"},
					selector.WithPrivateSelectors()),
			}

			eventList, err := eventStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(eventList.Items)).To(Equal(1))
			Expect(*(eventList.Items[0].Metadata.Name)).To(Equal("event2"))
		})
	})
})

func createEvents(ctx context.Context, store eventstore.Store, orgId uuid.UUID) {
	for i := 1; i <= 6; i++ {
		name := fmt.Sprintf("event%d", i)
		ev := &api.Event{
			Reason:         api.EventReasonResourceCreated,
			InvolvedObject: api.ObjectReference{Kind: api.DeviceKind, Name: name},
			Metadata:       api.ObjectMeta{Name: &name},
		}
		if i == 2 || i == 5 {
			ev.Reason = api.EventReasonResourceDeleted
			ev.Actor = "user:admin"
		}
		err := store.Create(ctx, orgId, ev)
		Expect(err).ToNot(HaveOccurred())
	}
}
