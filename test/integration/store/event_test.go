package store_test

import (
	"context"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var _ = Describe("EventStore Integration Tests", func() {
	var (
		log       *logrus.Logger
		ctx       context.Context
		orgId     uuid.UUID
		storeInst store.Store
		cfg       *config.Config
		dbName    string
		db        *gorm.DB
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		storeInst, cfg, dbName, db = store.PrepareDBForUnitTests(ctx, log)

		createEvents(ctx, storeInst, orgId)

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
		store.DeleteTestDB(ctx, log, cfg, storeInst, dbName)
	})

	Context("Event Store", func() {
		It("List all events", func() {
			listParams := store.ListParams{Limit: 100, SortColumns: []store.SortColumn{store.SortByCreatedAt, store.SortByName}, SortOrder: lo.ToPtr(store.SortDesc)}
			eventList, err := storeInst.Event().List(ctx, orgId, listParams)
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
			eventList, err := storeInst.Event().List(ctx, orgId, listParams)
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
			eventList, err = storeInst.Event().List(ctx, orgId, listParams)
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
			eventList, err = storeInst.Event().List(ctx, orgId, listParams)
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
					map[string]string{"reason": string(api.ResourceDeleted)}, selector.WithPrivateSelectors()),
			}

			eventList, err := storeInst.Event().List(ctx, orgId, listParams)
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

			eventList, err := storeInst.Event().List(ctx, orgId, listParams)
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

			eventList, err := storeInst.Event().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(eventList.Items)).To(Equal(1))
			Expect(*(eventList.Items[0].Metadata.Name)).To(Equal("event2"))
		})
	})
})

func createEvents(ctx context.Context, store store.Store, orgId uuid.UUID) {
	for i := 1; i <= 6; i++ {
		name := fmt.Sprintf("event%d", i)
		ev := &api.Event{
			Reason:         api.ResourceCreated,
			InvolvedObject: api.ObjectReference{Kind: api.DeviceKind, Name: name},
			Metadata:       api.ObjectMeta{Name: &name},
		}
		if i == 2 || i == 5 {
			ev.Reason = api.ResourceDeleted
			ev.Actor = "user:admin"
		}
		err := store.Event().Create(ctx, orgId, ev)
		Expect(err).ToNot(HaveOccurred())
	}
}
