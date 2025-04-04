package store_test

import (
	"context"
	"fmt"
	"log"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

func createEnrollmentRequests(numEnrollmentRequests int, ctx context.Context, store store.Store, orgId uuid.UUID) {
	for i := 1; i <= numEnrollmentRequests; i++ {
		resource := api.EnrollmentRequest{
			Metadata: api.ObjectMeta{
				Name:   lo.ToPtr(fmt.Sprintf("myenrollmentrequest-%d", i)),
				Labels: &map[string]string{"key": fmt.Sprintf("value-%d", i)},
			},
			Spec: api.EnrollmentRequestSpec{
				Csr: "csr string",
			},
			Status: &api.EnrollmentRequestStatus{
				Certificate: lo.ToPtr("cert"),
			},
		}

		_, err := store.EnrollmentRequest().Create(ctx, orgId, &resource)
		if err != nil {
			log.Fatalf("creating enrollmentrequest: %v", err)
		}
		_, err = store.EnrollmentRequest().UpdateStatus(ctx, orgId, &resource)
		if err != nil {
			log.Fatalf("updating enrollmentrequest status: %v", err)
		}
	}
}

var _ = Describe("enrollmentRequestStore create", func() {
	var (
		log                   *logrus.Logger
		ctx                   context.Context
		orgId                 uuid.UUID
		storeInst             store.Store
		cfg                   *config.Config
		dbName                string
		numEnrollmentRequests int
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		numEnrollmentRequests = 3
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(log)

		createEnrollmentRequests(numEnrollmentRequests, ctx, storeInst, orgId)
	})

	AfterEach(func() {
		store.DeleteTestDB(log, cfg, storeInst, dbName)
	})

	Context("EnrollmentRequest store", func() {
		It("Get enrollmentrequest success", func() {
			dev, err := storeInst.EnrollmentRequest().Get(ctx, orgId, "myenrollmentrequest-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*dev.Metadata.Name).To(Equal("myenrollmentrequest-1"))
		})

		It("Get enrollmentrequest - not found error", func() {
			_, err := storeInst.EnrollmentRequest().Get(ctx, orgId, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("Get enrollmentrequest - wrong org - not found error", func() {
			badOrgId, _ := uuid.NewUUID()
			_, err := storeInst.EnrollmentRequest().Get(ctx, badOrgId, "myenrollmentrequest-1")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("Delete enrollmentrequest success", func() {
			err := storeInst.EnrollmentRequest().Delete(ctx, orgId, "myenrollmentrequest-1")
			Expect(err).ToNot(HaveOccurred())
		})

		It("Delete enrollmentrequest success when not found", func() {
			err := storeInst.EnrollmentRequest().Delete(ctx, orgId, "nonexistent")
			Expect(err).ToNot(HaveOccurred())
		})

		It("Delete all enrollmentrequests in org", func() {
			otherOrgId, _ := uuid.NewUUID()
			err := storeInst.EnrollmentRequest().DeleteAll(ctx, otherOrgId)
			Expect(err).ToNot(HaveOccurred())

			listParams := store.ListParams{Limit: 1000}
			enrollmentrequests, err := storeInst.EnrollmentRequest().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(enrollmentrequests.Items)).To(Equal(numEnrollmentRequests))

			err = storeInst.EnrollmentRequest().DeleteAll(ctx, orgId)
			Expect(err).ToNot(HaveOccurred())

			enrollmentrequests, err = storeInst.EnrollmentRequest().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(enrollmentrequests.Items)).To(Equal(0))
		})

		It("List with paging", func() {
			listParams := store.ListParams{Limit: 1000}
			allEnrollmentRequests, err := storeInst.EnrollmentRequest().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(allEnrollmentRequests.Items)).To(Equal(numEnrollmentRequests))
			allDevNames := make([]string, len(allEnrollmentRequests.Items))
			for i, dev := range allEnrollmentRequests.Items {
				allDevNames[i] = *dev.Metadata.Name
			}

			foundDevNames := make([]string, len(allEnrollmentRequests.Items))
			listParams.Limit = 1
			enrollmentrequests, err := storeInst.EnrollmentRequest().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(enrollmentrequests.Items)).To(Equal(1))
			Expect(*enrollmentrequests.Metadata.RemainingItemCount).To(Equal(int64(2)))
			foundDevNames[0] = *enrollmentrequests.Items[0].Metadata.Name

			cont, err := store.ParseContinueString(enrollmentrequests.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			enrollmentrequests, err = storeInst.EnrollmentRequest().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(enrollmentrequests.Items)).To(Equal(1))
			Expect(*enrollmentrequests.Metadata.RemainingItemCount).To(Equal(int64(1)))
			foundDevNames[1] = *enrollmentrequests.Items[0].Metadata.Name

			cont, err = store.ParseContinueString(enrollmentrequests.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			enrollmentrequests, err = storeInst.EnrollmentRequest().List(ctx, orgId, listParams)
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
			listParams := store.ListParams{
				Limit:         1000,
				LabelSelector: selector.NewLabelSelectorFromMapOrDie(map[string]string{"key": "value-1"})}
			enrollmentrequests, err := storeInst.EnrollmentRequest().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(enrollmentrequests.Items)).To(Equal(1))
			Expect(*enrollmentrequests.Items[0].Metadata.Name).To(Equal("myenrollmentrequest-1"))
		})

		It("CreateOrUpdateEnrollmentRequest create mode", func() {
			enrollmentrequest := api.EnrollmentRequest{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("newresourcename"),
				},
				Spec: api.EnrollmentRequestSpec{
					Csr: "csr string",
				},
				Status: nil,
			}
			er, created, err := storeInst.EnrollmentRequest().CreateOrUpdate(ctx, orgId, &enrollmentrequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(true))
			Expect(er.ApiVersion).To(Equal(model.EnrollmentRequestAPIVersion()))
			Expect(er.Kind).To(Equal(api.EnrollmentRequestKind))
			Expect(er.Spec.Csr).To(Equal("csr string"))
			Expect(er.Status.Conditions).ToNot(BeNil())
			Expect(er.Status.Conditions).To(BeEmpty())
		})

		It("CreateOrUpdateEnrollmentRequest update mode", func() {
			enrollmentrequest := api.EnrollmentRequest{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("myenrollmentrequest-1"),
				},
				Spec: api.EnrollmentRequestSpec{
					Csr: "new csr string",
				},
				Status: &api.EnrollmentRequestStatus{
					Certificate: lo.ToPtr("bogus-cert"),
				},
			}
			er, created, err := storeInst.EnrollmentRequest().CreateOrUpdate(ctx, orgId, &enrollmentrequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(false))
			Expect(er.ApiVersion).To(Equal(model.EnrollmentRequestAPIVersion()))
			Expect(er.Kind).To(Equal(api.EnrollmentRequestKind))
			Expect(er.Spec.Csr).To(Equal("new csr string"))

			er, err = storeInst.EnrollmentRequest().Get(ctx, orgId, "myenrollmentrequest-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(er.ApiVersion).To(Equal(model.EnrollmentRequestAPIVersion()))
			Expect(er.Kind).To(Equal(api.EnrollmentRequestKind))
			Expect(er.Spec.Csr).To(Equal("new csr string"))
			Expect(er.Status.Certificate).ToNot(BeNil())
			Expect(*er.Status.Certificate).To(Equal("cert"))
		})

		It("UpdateEnrollmentRequestStatus", func() {
			condition := api.Condition{
				Type:               api.ConditionTypeEnrollmentRequestApproved,
				LastTransitionTime: time.Now(),
				Status:             api.ConditionStatusFalse,
				Reason:             "reason",
				Message:            "message",
			}
			enrollmentrequest := api.EnrollmentRequest{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("myenrollmentrequest-1"),
				},
				Spec: api.EnrollmentRequestSpec{
					Csr: "different csr string",
				},
				Status: &api.EnrollmentRequestStatus{
					Conditions: []api.Condition{condition},
				},
			}
			_, err := storeInst.EnrollmentRequest().UpdateStatus(ctx, orgId, &enrollmentrequest)
			Expect(err).ToNot(HaveOccurred())
			dev, err := storeInst.EnrollmentRequest().Get(ctx, orgId, "myenrollmentrequest-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.ApiVersion).To(Equal(model.EnrollmentRequestAPIVersion()))
			Expect(dev.Kind).To(Equal(api.EnrollmentRequestKind))
			Expect(dev.Spec.Csr).To(Equal("csr string"))
			Expect(dev.Status.Conditions).ToNot(BeNil())
			Expect(dev.Status.Conditions).ToNot(BeEmpty())
			Expect(dev.Status.Conditions[0].Type).To(Equal(api.ConditionTypeEnrollmentRequestApproved))
		})
	})
})
