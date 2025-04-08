package store_test

import (
	"context"
	"strconv"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

var _ = Describe("EventStore Integration Tests", func() {
	var (
		log       *logrus.Logger
		ctx       context.Context
		orgId     uuid.UUID
		storeInst store.Store
		cfg       *config.Config
		dbName    string
		events    []api.Event
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(log)

		events = []api.Event{
			{
				Type:          api.EventTypeResourceCreationSucceeded,
				Severity:      api.EventSeverityInfo,
				CorrelationId: lo.ToPtr("123"),
				Message:       "Resource created",
			},
			{
				Type:          api.EventTypeResourceUpdateSucceeded,
				Severity:      api.EventSeverityWarning,
				CorrelationId: lo.ToPtr("123"),
				Message:       "Resource updated",
			},
			{
				Type:          api.EventTypeResourceDeletionSucceeded,
				Severity:      api.EventSeverityCritical,
				CorrelationId: lo.ToPtr("456"),
				Message:       "Resource deleted",
			},
		}

		// Insert test events
		for _, event := range events {
			err := storeInst.Event().Create(ctx, orgId, &event)
			Expect(err).ToNot(HaveOccurred())
		}
	})

	AfterEach(func() {
		store.DeleteTestDB(log, cfg, storeInst, dbName)
	})

	Context("Event Store", func() {
		It("List all events", func() {
			listParams := store.ListEventsParams{Limit: 100}
			eventList, err := storeInst.Event().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventList.Items).To(HaveLen(len(events)))

			// Verify order (should be descending by timestamp)
			Expect(eventList.Items[0].Type).To(Equal(api.EventTypeResourceDeletionSucceeded))
			Expect(eventList.Items[1].Type).To(Equal(api.EventTypeResourceUpdateSucceeded))
			Expect(eventList.Items[2].Type).To(Equal(api.EventTypeResourceCreationSucceeded))
		})

		It("Filters events by severity", func() {
			// List only critical events
			listParams := store.ListEventsParams{
				Severity: lo.ToPtr(string(api.EventSeverityCritical)),
				Limit:    10,
			}

			eventList, err := storeInst.Event().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(eventList.Items)).To(Equal(1))
			Expect(eventList.Items[0].Type).To(Equal(api.EventTypeResourceDeletionSucceeded))
		})

		It("Filters events by correlation ID", func() {
			listParams := store.ListEventsParams{
				CorrelationId: lo.ToPtr("123"),
				Limit:         10,
			}

			eventList, err := storeInst.Event().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(eventList.Items)).To(Equal(2))
			Expect(eventList.Items[0].Type).To(Equal(api.EventTypeResourceUpdateSucceeded))
			Expect(eventList.Items[1].Type).To(Equal(api.EventTypeResourceCreationSucceeded))
		})

		It("Paginates events correctly", func() {
			// List first event with limit 1
			listParams := store.ListEventsParams{Limit: 1}
			eventList, err := storeInst.Event().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())

			Expect(len(eventList.Items)).To(Equal(1))
			Expect(eventList.Metadata.Continue).ToNot(BeNil())
			Expect(eventList.Metadata.RemainingItemCount).ToNot(BeNil())
			Expect(*eventList.Metadata.RemainingItemCount).To(Equal(int64(2)))

			// Fetch next page using continue token
			continueInt, err := strconv.ParseUint(*eventList.Metadata.Continue, 10, 64)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = &continueInt
			eventList2, err := storeInst.Event().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())

			Expect(len(eventList2.Items)).To(Equal(1))
			Expect(eventList2.Metadata.Continue).ToNot(BeNil())
			Expect(eventList2.Metadata.RemainingItemCount).ToNot(BeNil())
			Expect(*eventList2.Metadata.RemainingItemCount).To(Equal(int64(1)))

			// Ensure events are different across pages
			Expect(eventList.Items[0].Type).ToNot(Equal(eventList2.Items[0].Type))

			// Fetch next page using continue token
			continueInt, err = strconv.ParseUint(*eventList2.Metadata.Continue, 10, 64)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = &continueInt
			eventList3, err := storeInst.Event().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())

			Expect(len(eventList3.Items)).To(Equal(1))
			Expect(eventList3.Metadata.Continue).To(BeNil())
			Expect(eventList3.Metadata.RemainingItemCount).To(BeNil())

			// Ensure events are different across pages
			Expect(eventList.Items[0].Type).ToNot(Equal(eventList3.Items[0].Type))
			Expect(eventList2.Items[0].Type).ToNot(Equal(eventList3.Items[0].Type))
		})
	})
})
