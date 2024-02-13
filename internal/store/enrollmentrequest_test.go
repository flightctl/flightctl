package store

import (
	"context"
	"fmt"
	"log"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func createEnrollmentRequests(numEnrollmentRequests int, ctx context.Context, store Store, orgId uuid.UUID) {
	for i := 1; i <= numEnrollmentRequests; i++ {
		resource := api.EnrollmentRequest{
			Metadata: api.ObjectMeta{
				Name:   util.StrToPtr(fmt.Sprintf("myenrollmentrequest-%d", i)),
				Labels: &map[string]string{"key": fmt.Sprintf("value-%d", i)},
			},
			Spec: api.EnrollmentRequestSpec{
				Csr: "csr string",
			},
		}

		_, err := store.EnrollmentRequest().Create(ctx, orgId, &resource)
		if err != nil {
			log.Fatalf("creating enrollmentrequest: %v", err)
		}
	}
}

var _ = Describe("enrollmentRequestStore create", func() {
	var (
		log                   *logrus.Logger
		ctx                   context.Context
		orgId                 uuid.UUID
		store                 Store
		cfg                   *config.Config
		dbName                string
		numEnrollmentRequests int
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		numEnrollmentRequests = 3
		store, cfg, dbName = PrepareDBForUnitTests(log)

		createEnrollmentRequests(3, ctx, store, orgId)
	})

	AfterEach(func() {
		DeleteTestDB(cfg, store, dbName)
	})

	Context("EnrollmentRequest store", func() {
		It("Get enrollmentrequest success", func() {
			dev, err := store.EnrollmentRequest().Get(ctx, orgId, "myenrollmentrequest-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*dev.Metadata.Name).To(Equal("myenrollmentrequest-1"))
		})

		It("Get enrollmentrequest - not found error", func() {
			_, err := store.EnrollmentRequest().Get(ctx, orgId, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(gorm.ErrRecordNotFound))
		})

		It("Get enrollmentrequest - wrong org - not found error", func() {
			badOrgId, _ := uuid.NewUUID()
			_, err := store.EnrollmentRequest().Get(ctx, badOrgId, "myenrollmentrequest-1")
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(gorm.ErrRecordNotFound))
		})

		It("Delete enrollmentrequest success", func() {
			err := store.EnrollmentRequest().Delete(ctx, orgId, "myenrollmentrequest-1")
			Expect(err).ToNot(HaveOccurred())
		})

		It("Delete enrollmentrequest success when not found", func() {
			err := store.EnrollmentRequest().Delete(ctx, orgId, "nonexistent")
			Expect(err).ToNot(HaveOccurred())
		})

		It("Delete all enrollmentrequests in org", func() {
			otherOrgId, _ := uuid.NewUUID()
			log.Infof("DELETING DEVICES WITH ORG ID %s", otherOrgId)
			err := store.EnrollmentRequest().DeleteAll(ctx, otherOrgId)
			Expect(err).ToNot(HaveOccurred())

			listParams := ListParams{Limit: 1000}
			enrollmentrequests, err := store.EnrollmentRequest().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(enrollmentrequests.Items)).To(Equal(numEnrollmentRequests))

			log.Infof("DELETING DEVICES WITH ORG ID %s", orgId)
			err = store.EnrollmentRequest().DeleteAll(ctx, orgId)
			Expect(err).ToNot(HaveOccurred())

			enrollmentrequests, err = store.EnrollmentRequest().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(enrollmentrequests.Items)).To(Equal(0))
		})

		It("List with paging", func() {
			listParams := ListParams{Limit: 1000}
			allEnrollmentRequests, err := store.EnrollmentRequest().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(allEnrollmentRequests.Items)).To(Equal(numEnrollmentRequests))
			allDevNames := make([]string, len(allEnrollmentRequests.Items))
			for i, dev := range allEnrollmentRequests.Items {
				allDevNames[i] = *dev.Metadata.Name
			}

			foundDevNames := make([]string, len(allEnrollmentRequests.Items))
			listParams.Limit = 1
			enrollmentrequests, err := store.EnrollmentRequest().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(enrollmentrequests.Items)).To(Equal(1))
			Expect(*enrollmentrequests.Metadata.RemainingItemCount).To(Equal(int64(2)))
			foundDevNames[0] = *enrollmentrequests.Items[0].Metadata.Name

			cont, err := ParseContinueString(enrollmentrequests.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			enrollmentrequests, err = store.EnrollmentRequest().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(enrollmentrequests.Items)).To(Equal(1))
			Expect(*enrollmentrequests.Metadata.RemainingItemCount).To(Equal(int64(1)))
			foundDevNames[1] = *enrollmentrequests.Items[0].Metadata.Name

			cont, err = ParseContinueString(enrollmentrequests.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			enrollmentrequests, err = store.EnrollmentRequest().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(enrollmentrequests.Items)).To(Equal(1))
			Expect(enrollmentrequests.Metadata.RemainingItemCount).To(BeNil())
			Expect(enrollmentrequests.Metadata.Continue).To(BeNil())
			foundDevNames[2] = *enrollmentrequests.Items[0].Metadata.Name

			for i := range allDevNames {
				Expect(allDevNames[i]).To(Equal(foundDevNames[i]))
			}
		})

		It("List with paging", func() {
			listParams := ListParams{
				Limit:  1000,
				Labels: map[string]string{"key": "value-1"}}
			enrollmentrequests, err := store.EnrollmentRequest().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(enrollmentrequests.Items)).To(Equal(1))
			Expect(*enrollmentrequests.Items[0].Metadata.Name).To(Equal("myenrollmentrequest-1"))
		})

		It("CreateOrUpdateEnrollmentRequest create mode", func() {
			condition := api.EnrollmentRequestCondition{
				Type:               "type",
				LastTransitionTime: util.TimeStampStringPtr(),
				Status:             api.False,
				Reason:             util.StrToPtr("reason"),
				Message:            util.StrToPtr("message"),
			}
			enrollmentrequest := api.EnrollmentRequest{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr("newresourcename"),
				},
				Spec: api.EnrollmentRequestSpec{
					Csr: "csr string",
				},
				Status: &api.EnrollmentRequestStatus{
					Conditions: &[]api.EnrollmentRequestCondition{condition},
				},
			}
			dev, created, err := store.EnrollmentRequest().CreateOrUpdate(ctx, orgId, &enrollmentrequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(true))
			Expect(dev.ApiVersion).To(Equal(model.EnrollmentRequestAPI))
			Expect(dev.Kind).To(Equal(model.EnrollmentRequestKind))
			Expect(dev.Spec.Csr).To(Equal("csr string"))
			Expect(dev.Status.Conditions).To(BeNil())
		})

		It("CreateOrUpdateEnrollmentRequest update mode", func() {
			condition := api.EnrollmentRequestCondition{
				Type:               "type",
				LastTransitionTime: util.TimeStampStringPtr(),
				Status:             api.False,
				Reason:             util.StrToPtr("reason"),
				Message:            util.StrToPtr("message"),
			}
			enrollmentrequest := api.EnrollmentRequest{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr("myenrollmentrequest-1"),
				},
				Spec: api.EnrollmentRequestSpec{
					Csr: "csr string",
				},
				Status: &api.EnrollmentRequestStatus{
					Conditions: &[]api.EnrollmentRequestCondition{condition},
				},
			}
			dev, created, err := store.EnrollmentRequest().CreateOrUpdate(ctx, orgId, &enrollmentrequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(false))
			Expect(dev.ApiVersion).To(Equal(model.EnrollmentRequestAPI))
			Expect(dev.Kind).To(Equal(model.EnrollmentRequestKind))
			Expect(dev.Spec.Csr).To(Equal("csr string"))
			Expect(dev.Status.Conditions).To(BeNil())
		})

		It("UpdateEnrollmentRequestStatus", func() {
			condition := api.EnrollmentRequestCondition{
				Type:               "type",
				LastTransitionTime: util.TimeStampStringPtr(),
				Status:             api.False,
				Reason:             util.StrToPtr("reason"),
				Message:            util.StrToPtr("message"),
			}
			enrollmentrequest := api.EnrollmentRequest{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr("myenrollmentrequest-1"),
				},
				Spec: api.EnrollmentRequestSpec{
					Csr: "different csr string",
				},
				Status: &api.EnrollmentRequestStatus{
					Conditions: &[]api.EnrollmentRequestCondition{condition},
				},
			}
			_, err := store.EnrollmentRequest().UpdateStatus(ctx, orgId, &enrollmentrequest)
			Expect(err).ToNot(HaveOccurred())
			dev, err := store.EnrollmentRequest().Get(ctx, orgId, "myenrollmentrequest-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.ApiVersion).To(Equal(model.EnrollmentRequestAPI))
			Expect(dev.Kind).To(Equal(model.EnrollmentRequestKind))
			Expect(dev.Spec.Csr).To(Equal("csr string"))
			Expect(dev.Status.Conditions).ToNot(BeNil())
			Expect((*dev.Status.Conditions)[0].Type).To(Equal("type"))
		})
	})
})
