package store_test

import (
	"context"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	enrollmentrequeststore "github.com/flightctl/flightctl/internal/store/enrollmentrequest"
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

var _ = Describe("enrollmentRequestStore create", func() {
	var (
		log                    *logrus.Logger
		ctx                    context.Context
		orgId                  uuid.UUID
		enrollmentRequestStore enrollmentrequeststore.Store
		cfg                    *config.Config
		dbName                 string
		db                     *gorm.DB
		numEnrollmentRequests  int
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		numEnrollmentRequests = 3
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		enrollmentRequestStore = enrollmentrequeststore.NewEnrollmentRequestStore(db, log.WithField("pkg", "enrollmentrequest-store"))
		organizationStore := organizationstore.NewOrganizationStore(db)

		orgId = uuid.New()
		err = testutil.CreateTestOrganization(ctx, organizationStore, orgId)
		Expect(err).ToNot(HaveOccurred())

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

			_, err := enrollmentRequestStore.Create(ctx, orgId, &resource)
			if err != nil {
				Expect(err).NotTo(HaveOccurred())
			}
			_, _, _, err = enrollmentRequestStore.Mutate(ctx, orgId, *resource.Metadata.Name, nil, func(mutation *enrollmentrequeststore.EnrollmentRequestMutation) error {
				if err := mutation.RequireExisting(); err != nil {
					return err
				}
				mutation.Resource().Status = resource.Status
				return nil
			})
			if err != nil {
				Expect(err).NotTo(HaveOccurred())
			}
		}
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	Context("EnrollmentRequest store", func() {
		It("Mutate creates an enrollment request when it does not exist", func() {
			resource := api.EnrollmentRequest{
				Metadata: api.ObjectMeta{
					Name:   lo.ToPtr("mutate-create"),
					Labels: &map[string]string{"phase": "created"},
				},
				Spec: api.EnrollmentRequestSpec{
					Csr: "mutate create csr",
				},
				Status: &api.EnrollmentRequestStatus{
					Certificate: lo.ToPtr("mutate create certificate"),
				},
			}

			updated, before, created, err := enrollmentRequestStore.Mutate(ctx, orgId, *resource.Metadata.Name, nil, func(mutation *enrollmentrequeststore.EnrollmentRequestMutation) error {
				Expect(mutation.EnrollmentRequest).To(BeNil())
				mutation.EnrollmentRequest = &resource
				return nil
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())
			Expect(before).To(BeNil())
			Expect(updated).ToNot(BeNil())
			Expect(*updated.Metadata.Name).To(Equal("mutate-create"))
			Expect(updated.Spec.Csr).To(Equal("mutate create csr"))
			Expect(updated.Metadata.Labels).ToNot(BeNil())
			Expect(*updated.Metadata.Labels).To(HaveKeyWithValue("phase", "created"))

			loaded, err := enrollmentRequestStore.Get(ctx, orgId, "mutate-create")
			Expect(err).ToNot(HaveOccurred())
			Expect(loaded.Spec.Csr).To(Equal("mutate create csr"))
			Expect(loaded.Metadata.Labels).ToNot(BeNil())
			Expect(*loaded.Metadata.Labels).To(HaveKeyWithValue("phase", "created"))
			Expect(loaded.Metadata.ResourceVersion).To(Equal(updated.Metadata.ResourceVersion))
		})

		It("Get enrollmentrequest success", func() {
			dev, err := enrollmentRequestStore.Get(ctx, orgId, "myenrollmentrequest-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*dev.Metadata.Name).To(Equal("myenrollmentrequest-1"))
		})

		It("Get enrollmentrequest - not found error", func() {
			_, err := enrollmentRequestStore.Get(ctx, orgId, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("Get enrollmentrequest - wrong org - not found error", func() {
			badOrgId, _ := uuid.NewUUID()
			_, err := enrollmentRequestStore.Get(ctx, badOrgId, "myenrollmentrequest-1")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("Delete enrollmentrequest success", func() {
			_, err := enrollmentRequestStore.Delete(ctx, orgId, "myenrollmentrequest-1")
			Expect(err).ToNot(HaveOccurred())
		})

		It("Delete enrollmentrequest success when not found", func() {
			_, err := enrollmentRequestStore.Delete(ctx, orgId, "nonexistent")
			Expect(err).ToNot(HaveOccurred())
		})

		It("List with paging", func() {
			listParams := store.ListParams{Limit: 1000}
			allEnrollmentRequests, err := enrollmentRequestStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(allEnrollmentRequests.Items)).To(Equal(numEnrollmentRequests))
			allDevNames := make([]string, len(allEnrollmentRequests.Items))
			for i, dev := range allEnrollmentRequests.Items {
				allDevNames[i] = *dev.Metadata.Name
			}

			foundDevNames := make([]string, len(allEnrollmentRequests.Items))
			listParams.Limit = 1
			enrollmentrequests, err := enrollmentRequestStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(enrollmentrequests.Items)).To(Equal(1))
			Expect(*enrollmentrequests.Metadata.RemainingItemCount).To(Equal(int64(2)))
			foundDevNames[0] = *enrollmentrequests.Items[0].Metadata.Name

			cont, err := store.ParseContinueString(enrollmentrequests.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			enrollmentrequests, err = enrollmentRequestStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(enrollmentrequests.Items)).To(Equal(1))
			Expect(*enrollmentrequests.Metadata.RemainingItemCount).To(Equal(int64(1)))
			foundDevNames[1] = *enrollmentrequests.Items[0].Metadata.Name

			cont, err = store.ParseContinueString(enrollmentrequests.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			enrollmentrequests, err = enrollmentRequestStore.List(ctx, orgId, listParams)
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
			enrollmentrequests, err := enrollmentRequestStore.List(ctx, orgId, listParams)
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
			er, _, created, err := enrollmentRequestStore.CreateOrUpdate(ctx, orgId, &enrollmentrequest)
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
			er, _, created, err := enrollmentRequestStore.CreateOrUpdate(ctx, orgId, &enrollmentrequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(false))
			Expect(er.ApiVersion).To(Equal(model.EnrollmentRequestAPIVersion()))
			Expect(er.Kind).To(Equal(api.EnrollmentRequestKind))
			Expect(er.Spec.Csr).To(Equal("new csr string"))

			er, err = enrollmentRequestStore.Get(ctx, orgId, "myenrollmentrequest-1")
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
			_, _, _, err := enrollmentRequestStore.Mutate(ctx, orgId, *enrollmentrequest.Metadata.Name, nil, func(mutation *enrollmentrequeststore.EnrollmentRequestMutation) error {
				if err := mutation.RequireExisting(); err != nil {
					return err
				}
				mutation.Resource().Status = enrollmentrequest.Status
				return nil
			})
			Expect(err).ToNot(HaveOccurred())
			dev, err := enrollmentRequestStore.Get(ctx, orgId, "myenrollmentrequest-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.ApiVersion).To(Equal(model.EnrollmentRequestAPIVersion()))
			Expect(dev.Kind).To(Equal(api.EnrollmentRequestKind))
			Expect(dev.Spec.Csr).To(Equal("csr string"))
			Expect(dev.Status.Conditions).ToNot(BeNil())
			Expect(dev.Status.Conditions).ToNot(BeEmpty())
			Expect(dev.Status.Conditions[0].Type).To(Equal(api.ConditionTypeEnrollmentRequestApproved))
		})

		It("Denies pending enrollment requests for a revoked credential in one update", func() {
			createRequest := func(name, credentialName string, conditions []api.Condition, approval *api.EnrollmentRequestApprovalStatus) {
				request := api.EnrollmentRequest{
					Metadata: api.ObjectMeta{Name: lo.ToPtr(name)},
					Spec:     api.EnrollmentRequestSpec{Csr: "csr"},
					Status: &api.EnrollmentRequestStatus{
						EnrollmentCertificateName: lo.ToPtr(credentialName),
						Conditions:                conditions,
						Approval:                  approval,
					},
				}
				_, err := enrollmentRequestStore.Create(ctx, orgId, &request)
				Expect(err).ToNot(HaveOccurred())
			}
			existingCondition := api.Condition{
				Type:               api.ConditionTypeEnrollmentRequestTPMVerified,
				Status:             api.ConditionStatusTrue,
				Reason:             "Verified",
				Message:            "verified",
				LastTransitionTime: time.Now().UTC(),
			}
			createRequest("pending-a", "credential-a", []api.Condition{existingCondition}, nil)
			createRequest("approved-a", "credential-a", []api.Condition{{
				Type:   api.ConditionTypeEnrollmentRequestApproved,
				Status: api.ConditionStatusTrue,
			}}, &api.EnrollmentRequestApprovalStatus{Approved: true, ApprovedAt: time.Now().UTC(), ApprovedBy: "admin"})
			createRequest("already-denied-a", "credential-a", []api.Condition{{
				Type:    api.ConditionTypeCertificateSigningRequestDenied,
				Status:  api.ConditionStatusTrue,
				Reason:  "ManualRejection",
				Message: "kept",
			}}, nil)
			createRequest("pending-b", "credential-b", nil, nil)

			count, err := enrollmentRequestStore.DenyEnrollmentRequestsForRevokedCredential(ctx, orgId, "credential-a")
			Expect(err).ToNot(HaveOccurred())
			Expect(count).To(Equal(int64(1)))

			pending, err := enrollmentRequestStore.Get(ctx, orgId, "pending-a")
			Expect(err).ToNot(HaveOccurred())
			condition := api.FindStatusCondition(pending.Status.Conditions, api.ConditionTypeCertificateSigningRequestDenied)
			Expect(condition).ToNot(BeNil())
			Expect(condition.Status).To(Equal(api.ConditionStatusTrue))
			Expect(condition.Reason).To(Equal(api.EnrollmentCredentialRevokedReason))
			Expect(api.FindStatusCondition(pending.Status.Conditions, api.ConditionTypeEnrollmentRequestTPMVerified)).ToNot(BeNil())
			Expect(pending.Status.EnrollmentCertificateName).To(Equal(lo.ToPtr("credential-a")))

			approved, err := enrollmentRequestStore.Get(ctx, orgId, "approved-a")
			Expect(err).ToNot(HaveOccurred())
			Expect(api.FindStatusCondition(approved.Status.Conditions, api.ConditionTypeCertificateSigningRequestDenied)).To(BeNil())

			alreadyDenied, err := enrollmentRequestStore.Get(ctx, orgId, "already-denied-a")
			Expect(err).ToNot(HaveOccurred())
			alreadyDeniedCondition := api.FindStatusCondition(alreadyDenied.Status.Conditions, api.ConditionTypeCertificateSigningRequestDenied)
			Expect(alreadyDeniedCondition).ToNot(BeNil())
			Expect(alreadyDeniedCondition.Message).To(Equal("kept"))

			other, err := enrollmentRequestStore.Get(ctx, orgId, "pending-b")
			Expect(err).ToNot(HaveOccurred())
			Expect(api.FindStatusCondition(other.Status.Conditions, api.ConditionTypeCertificateSigningRequestDenied)).To(BeNil())

			count, err = enrollmentRequestStore.DenyEnrollmentRequestsForRevokedCredential(ctx, orgId, "credential-a")
			Expect(err).ToNot(HaveOccurred())
			Expect(count).To(BeZero())
		})
	})
})
