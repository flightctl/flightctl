package service_test

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	icrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	policystore "github.com/flightctl/flightctl/internal/store/enrollmentapprovalpolicy"
	enrollmentstore "github.com/flightctl/flightctl/internal/store/enrollmentrequest"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"go.uber.org/mock/gomock"
	"gorm.io/gorm"
)

type admissionMatchResult struct {
	matched bool
	err     error
}

type admissionMatcher map[string]admissionMatchResult

type countingAdmissionCA struct {
	*icrypto.CAClient
	calls atomic.Int32
}

func (ca *countingAdmissionCA) IssueRequestedCertificateAsX509(ctx context.Context, csr *x509.CertificateRequest, expirySeconds int, usage []x509.ExtKeyUsage, opts ...icrypto.CertOption) (*x509.Certificate, error) {
	ca.calls.Add(1)
	if len(usage) == 1 && usage[0] == x509.ExtKeyUsageClientAuth {
		return ca.IssueRequestedClientCertificate(ctx, csr, expirySeconds, opts...)
	}
	return nil, errors.New("unexpected certificate usage")
}

type admissionMatchFunc func(context.Context, domain.EnrollmentApprovalPolicy, domain.EnrollmentRequest) (bool, error)

func (match admissionMatchFunc) Match(ctx context.Context, policy domain.EnrollmentApprovalPolicy, request domain.EnrollmentRequest) (bool, error) {
	return match(ctx, policy, request)
}

func (matcher admissionMatcher) Match(_ context.Context, policy domain.EnrollmentApprovalPolicy, _ domain.EnrollmentRequest) (bool, error) {
	result := matcher[lo.FromPtr(policy.Metadata.Name)]
	return result.matched, result.err
}

type ambiguousAdmissionPool struct {
	gorm.ConnPool
	fail        *atomic.Bool
	afterCommit func()
}

func (pool ambiguousAdmissionPool) GetDBConn() (*sql.DB, error) {
	return pool.ConnPool.(*sql.DB), nil
}

func (pool ambiguousAdmissionPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := pool.ConnPool.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	}).BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &ambiguousAdmissionTx{Tx: tx, fail: pool.fail, afterCommit: pool.afterCommit}, nil
}

type ambiguousAdmissionTx struct {
	*sql.Tx
	fail        *atomic.Bool
	admitted    bool
	afterCommit func()
}

func (tx *ambiguousAdmissionTx) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	if strings.Contains(query, `INSERT INTO "enrollment_admissions"`) {
		tx.admitted = true
	}
	return tx.Tx.QueryContext(ctx, query, args...)
}

func (tx *ambiguousAdmissionTx) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	if strings.Contains(query, `INSERT INTO "enrollment_admissions"`) {
		tx.admitted = true
	}
	return tx.Tx.ExecContext(ctx, query, args...)
}

func (tx *ambiguousAdmissionTx) Commit() error {
	if err := tx.Tx.Commit(); err != nil {
		return err
	}
	if tx.admitted && tx.fail.Swap(false) {
		if tx.afterCommit != nil {
			tx.afterCommit()
		}
		return errors.New("commit acknowledgement lost")
	}
	return nil
}

var _ = Describe("Enrollment admission", func() {
	var suite *ServiceTestSuite
	var credentialName string
	var requests enrollmentstore.Store
	var handler *enrollmentrequest.ServiceHandler
	newHandler := func(database *gorm.DB, matcher enrollmentrequest.PolicyMatcher, publishers ...worker_client.WorkerClient) *enrollmentrequest.ServiceHandler {
		publisher := suite.workerClient
		if len(publishers) != 0 {
			publisher = publishers[0]
		}
		return enrollmentrequest.NewServiceHandler(enrollmentstore.NewEnrollmentRequestStore(database, suite.Log), suite.DeviceStore, suite.CertificateSigningRequest, suite.caClient, suite.kvStore, events.NewServiceHandler(suite.EventStore, publisher, suite.Log), suite.Log, nil, "", "", suite.EnrollmentHookPolicy, suite.NotifySecretsStore, enrollmentrequest.WithPolicyMatcher(matcher))
	}
	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
		suite.Ctx = util.WithOrganizationID(suite.Ctx, suite.OrgID)
		credential, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, CreateTestCSR())
		Expect(status.Code).To(Equal(int32(201)))
		Expect(credential.Status.Certificate).NotTo(BeNil())
		credentialName = *credential.Metadata.Name
		requests = enrollmentstore.NewEnrollmentRequestStore(suite.DB, suite.Log)
		handler = newHandler(suite.DB, admissionMatcher{"policy": {matched: true}})
		policy := &domain.EnrollmentApprovalPolicy{Metadata: domain.ObjectMeta{Name: lo.ToPtr("policy")}, Spec: domain.EnrollmentApprovalPolicySpec{CertificateName: credentialName, Enabled: true, MatchExpression: "true", DeviceLabels: map[string]string{"static": "value"}, ApprovalLimit: lo.ToPtr(int64(1))}, Status: &domain.EnrollmentApprovalPolicyStatus{}}
		record, err := model.NewEnrollmentApprovalPolicyFromApiResource(policy)
		Expect(err).NotTo(HaveOccurred())
		record.OrgID = suite.OrgID
		record.ResourceVersion = lo.ToPtr(int64(1))
		record.Generation = lo.ToPtr(int64(1))
		Expect(suite.DB.WithContext(suite.Ctx).Create(record).Error).To(Succeed())
	})
	AfterEach(func() { suite.Teardown() })
	create := func() *domain.EnrollmentRequest {
		request := CreateTestER()
		request.Status = &domain.EnrollmentRequestStatus{EnrollmentCertificateName: &credentialName, Conditions: []domain.Condition{}}
		created, err := requests.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		return created
	}
	event := func(name string) domain.Event {
		return domain.Event{InvolvedObject: domain.ObjectReference{Kind: domain.EnrollmentRequestKind, Name: name}, Reason: domain.EventReasonResourceCreated}
	}
	count := func() int64 {
		var policy model.EnrollmentApprovalPolicy
		Expect(suite.DB.WithContext(suite.Ctx).Unscoped().Where("org_id = ? AND name = ?", suite.OrgID, "policy").Take(&policy).Error).To(Succeed())
		return policy.Status.Data.EnrollmentCount
	}
	load := func(name string) *domain.EnrollmentRequest {
		request, err := requests.Get(suite.Ctx, suite.OrgID, name)
		Expect(err).NotTo(HaveOccurred())
		return request
	}
	assertRollback := func(name string) {
		Expect(load(name).Status.Certificate).To(BeNil())
		Expect(enrollmentstore.Pending(load(name))).To(BeTrue())
		Expect(count()).To(BeZero())
		for _, record := range []interface{}{&model.Device{}, &model.EnrollmentAdmission{}} {
			var total int64
			Expect(suite.DB.WithContext(suite.Ctx).Model(record).Where("org_id = ? AND name = ?", suite.OrgID, name).Count(&total).Error).To(Succeed())
			Expect(total).To(BeZero())
		}
		var events int64
		Expect(suite.DB.WithContext(suite.Ctx).Model(&model.Event{}).Where("org_id = ? AND involved_object_name = ?", suite.OrgID, name).Count(&events).Error).To(Succeed())
		Expect(events).To(BeZero())
	}
	It("When matching is unavailable it should leave the request pending without issuing", func() {
		request := create()
		handler = newHandler(suite.DB, nil)
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(*request.Metadata.Name))).To(Succeed())
		Expect(domain.FindStatusCondition(load(*request.Metadata.Name).Status.Conditions, domain.ConditionTypeEnrollmentRequestApproved).Reason).To(Equal("PolicyMatchingUnavailable"))
		assertRollback(*request.Metadata.Name)
	})
	DescribeTable("When admission commits before a spec mutation it should preserve the committed result", func(operation string) {
		request := create()
		name := *request.Metadata.Name
		if operation == "recreated patch" {
			Expect(newHandler(suite.DB, nil).ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(name))).To(Succeed())
			Expect(lo.FromPtr(load(name).Metadata.ResourceVersion)).To(Equal("2"))
		}
		if operation == "approved patch" {
			Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(name))).To(Succeed())
		} else {
			table := "enrollment_requests"
			if operation == "racing replace" {
				table = "devices"
			}
			var admitted atomic.Bool
			Expect(suite.DB.Callback().Query().After("gorm:query").Register("admission_before_mutation", func(tx *gorm.DB) {
				if tx.Statement.Table == table && admitted.CompareAndSwap(false, true) {
					if operation == "recreated patch" {
						_, err := requests.Delete(suite.Ctx, suite.OrgID, name)
						Expect(err).NotTo(HaveOccurred())
						_, err = requests.Create(suite.Ctx, suite.OrgID, &domain.EnrollmentRequest{
							Metadata: domain.ObjectMeta{Name: &name}, Spec: request.Spec,
							Status: &domain.EnrollmentRequestStatus{EnrollmentCertificateName: &credentialName, Conditions: []domain.Condition{}},
						})
						Expect(err).NotTo(HaveOccurred())
					}
					Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(name))).To(Succeed())
					if operation == "recreated patch" {
						Expect(lo.FromPtr(load(name).Metadata.ResourceVersion)).To(Equal("2"))
					}
				}
			})).To(Succeed())
			DeferCleanup(func() { Expect(suite.DB.Callback().Query().Remove("admission_before_mutation")).To(Succeed()) })
		}
		var status domain.Status
		if operation == "racing replace" {
			request.Spec.Csr += "\n"
			request.Metadata.ResourceVersion = nil
			_, status = handler.ReplaceEnrollmentRequest(suite.Ctx, suite.OrgID, name, *request)
		} else {
			var value interface{} = request.Spec.Csr + "\n"
			_, status = handler.PatchEnrollmentRequest(suite.Ctx, suite.OrgID, name, domain.PatchRequest{{Op: "replace", Path: "/spec/csr", Value: &value}})
		}
		Expect(status.Code).To(Equal(int32(409)))
		var original model.EnrollmentAdmission
		Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND name = ?", suite.OrgID, name).Take(&original).Error).To(Succeed())
		Expect(load(name)).To(Equal(&original.EnrollmentRequest.Data))
		replayed, err := requests.(enrollmentstore.AdmissionStore).Admit(suite.Ctx, suite.OrgID, name, func(context.Context, *enrollmentstore.AdmissionSnapshot) (*enrollmentstore.AdmissionDecision, error) {
			return nil, errors.New("must not sign again")
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(replayed).To(Equal(&original.EnrollmentRequest.Data))
		Expect(count()).To(Equal(int64(1)))
	}, Entry("approved patch", "approved patch"), Entry("racing patch", "racing patch"), Entry("racing replace", "racing replace"), Entry("recreated patch", "recreated patch"))
	It("When a committed admission result is missing it should fail closed without signing", func() {
		request := create()
		name := *request.Metadata.Name
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(name))).To(Succeed())
		original := load(name)
		Expect(suite.DB.WithContext(suite.Ctx).Unscoped().Where("org_id = ? AND name = ?", suite.OrgID, name).Delete(&model.EnrollmentAdmission{}).Error).To(Succeed())
		called := false
		result, err := requests.(enrollmentstore.AdmissionStore).Admit(suite.Ctx, suite.OrgID, name, func(context.Context, *enrollmentstore.AdmissionSnapshot) (*enrollmentstore.AdmissionDecision, error) {
			called = true
			return nil, errors.New("must not sign again")
		})
		Expect(err).To(MatchError("committed admission result is unavailable"))
		Expect(result).To(BeNil())
		Expect(called).To(BeFalse())
		Expect(load(name)).To(Equal(original))
		_, err = suite.DeviceStore.Get(suite.Ctx, suite.OrgID, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(count()).To(Equal(int64(1)))
	})
	DescribeTable("When matching outcomes differ it should decide per candidate", func(first, second admissionMatchResult, approved bool, reason string) {
		request := create()
		var policy model.EnrollmentApprovalPolicy
		Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND name = ?", suite.OrgID, "policy").Take(&policy).Error).To(Succeed())
		policy.Name = "other"
		Expect(suite.DB.WithContext(suite.Ctx).Create(&policy).Error).To(Succeed())
		handler = newHandler(suite.DB, admissionMatcher{"policy": first, "other": second})
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(*request.Metadata.Name))).To(Succeed())
		loaded := load(*request.Metadata.Name)
		Expect(!enrollmentstore.Pending(loaded)).To(Equal(approved))
		if !approved {
			Expect(domain.FindStatusCondition(loaded.Status.Conditions, domain.ConditionTypeEnrollmentRequestApproved).Reason).To(Equal(reason))
			assertRollback(*request.Metadata.Name)
		}
	}, Entry("no matches", admissionMatchResult{}, admissionMatchResult{}, false, "NoMatchingPolicy"), Entry("ambiguity", admissionMatchResult{matched: true}, admissionMatchResult{matched: true}, false, "AmbiguousPolicyMatch"), Entry("isolated failure", admissionMatchResult{matched: true}, admissionMatchResult{err: errors.New("private evaluation data")}, true, ""))
	DescribeTable("When matching policies have mixed quota capacity it should remain ambiguous without falling through", func(exhaustedName string) {
		request := create()
		name := *request.Metadata.Name
		var policy model.EnrollmentApprovalPolicy
		Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND name = ?", suite.OrgID, "policy").Take(&policy).Error).To(Succeed())
		policy.Name = "other"
		Expect(suite.DB.WithContext(suite.Ctx).Create(&policy).Error).To(Succeed())
		Expect(suite.DB.WithContext(suite.Ctx).Exec("UPDATE enrollment_approval_policies SET status = jsonb_set(status, '{enrollmentCount}', '1') WHERE org_id = ? AND name = ?", suite.OrgID, exhaustedName).Error).To(Succeed())
		var before []model.EnrollmentApprovalPolicy
		Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ?", suite.OrgID).Order("name").Find(&before).Error).To(Succeed())
		Expect(before).To(HaveLen(2))
		for _, candidate := range before {
			Expect(candidate.Spec.Data.Enabled).To(BeTrue())
			Expect(candidate.Spec.Data.RequireTPMVerification).To(BeFalse())
			Expect(candidate.Spec.Data.AutoApprovalWindowDays).To(BeNil())
			Expect(candidate.Spec.Data.ApprovalLimit).To(Equal(lo.ToPtr(int64(1))))
			expectedCount := int64(0)
			if candidate.Name == exhaustedName {
				expectedCount = 1
			}
			Expect(candidate.Status.Data.EnrollmentCount).To(Equal(expectedCount))
		}
		ca := &countingAdmissionCA{CAClient: suite.caClient}
		suite.caClient = icrypto.NewCAClient(suite.caClient.Cfg, ca)
		producer := queues.NewMockQueueProducer(suite.ctrl)
		producer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
		var matched []string
		handler = newHandler(suite.DB, admissionMatchFunc(func(_ context.Context, candidate domain.EnrollmentApprovalPolicy, _ domain.EnrollmentRequest) (bool, error) {
			matched = append(matched, *candidate.Metadata.Name)
			return true, nil
		}), worker_client.NewWorkerClient(producer, suite.Log))
		for range 2 {
			matched = nil
			Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(name))).To(Succeed())
			Expect(matched).To(Equal([]string{"other", "policy"}))
			loaded := load(name)
			Expect(enrollmentstore.Pending(loaded)).To(BeTrue())
			Expect(loaded.Status.Certificate).To(BeNil())
			Expect(loaded.Status.Approval).To(BeNil())
			Expect(domain.FindStatusCondition(loaded.Status.Conditions, domain.ConditionTypeEnrollmentRequestApproved).Reason).To(Equal("AmbiguousPolicyMatch"))
			Expect(ca.calls.Load()).To(BeZero())
			var stored model.EnrollmentRequest
			Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND name = ?", suite.OrgID, name).Take(&stored).Error).To(Succeed())
			Expect(stored.AdmissionID).To(BeNil())
			for _, record := range []interface{}{&model.Device{}, &model.EnrollmentAdmission{}} {
				var total int64
				Expect(suite.DB.WithContext(suite.Ctx).Model(record).Where("org_id = ? AND name = ?", suite.OrgID, name).Count(&total).Error).To(Succeed())
				Expect(total).To(BeZero())
			}
			var eventCount int64
			Expect(suite.DB.WithContext(suite.Ctx).Model(&model.Event{}).Where("org_id = ? AND involved_object_name = ?", suite.OrgID, name).Count(&eventCount).Error).To(Succeed())
			Expect(eventCount).To(BeZero())
			var after []model.EnrollmentApprovalPolicy
			Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ?", suite.OrgID).Order("name").Find(&after).Error).To(Succeed())
			Expect(after).To(Equal(before))
		}
	}, Entry("first candidate exhausted", "other"), Entry("second candidate exhausted", "policy"))
	It("When quota is unlimited it should still count every committed admission", func() {
		Expect(suite.DB.WithContext(suite.Ctx).Exec("UPDATE enrollment_approval_policies SET spec = spec - 'approvalLimit' WHERE org_id = ?", suite.OrgID).Error).To(Succeed())
		for index := 0; index < 2; index++ {
			request := create()
			Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(*request.Metadata.Name))).To(Succeed())
		}
		Expect(count()).To(Equal(int64(2)))
	})
	It("When a deleted device reenrolls it should retain distinct immutable admissions", func() {
		Expect(suite.DB.WithContext(suite.Ctx).Exec("UPDATE enrollment_approval_policies SET spec = spec - 'approvalLimit' WHERE org_id = ?", suite.OrgID).Error).To(Succeed())
		request := create()
		name := *request.Metadata.Name
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(name))).To(Succeed())
		var original model.EnrollmentAdmission
		Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND name = ?", suite.OrgID, name).Take(&original).Error).To(Succeed())
		deleted, err := suite.DeviceStore.Delete(suite.Ctx, suite.OrgID, name, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).To(BeTrue())
		_, err = requests.Create(suite.Ctx, suite.OrgID, &domain.EnrollmentRequest{
			Metadata: domain.ObjectMeta{Name: &name}, Spec: request.Spec,
			Status: &domain.EnrollmentRequestStatus{EnrollmentCertificateName: &credentialName, Conditions: []domain.Condition{}},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(name))).To(Succeed())
		current := load(name)
		Expect(current.Status.Certificate).NotTo(BeNil())
		Expect(current.Status.Certificate).NotTo(Equal(original.EnrollmentRequest.Data.Status.Certificate))
		var history []model.EnrollmentAdmission
		Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND name = ?", suite.OrgID, name).Find(&history).Error).To(Succeed())
		Expect(history).To(HaveLen(2))
		Expect(history).To(ContainElement(original))
		replay, err := requests.(enrollmentstore.AdmissionStore).Admit(suite.Ctx, suite.OrgID, name, func(context.Context, *enrollmentstore.AdmissionSnapshot) (*enrollmentstore.AdmissionDecision, error) {
			return nil, errors.New("must not sign")
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(replay).To(Equal(current))
		Expect(count()).To(Equal(int64(2)))
	})
	It("When a device is deleted before outbox recovery it should not block other requests", func() {
		Expect(suite.DB.WithContext(suite.Ctx).Exec("UPDATE enrollment_approval_policies SET spec = spec - 'approvalLimit' WHERE org_id = ?", suite.OrgID).Error).To(Succeed())
		request := create()
		name := *request.Metadata.Name
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(name))).To(Succeed())
		Expect(suite.DB.WithContext(suite.Ctx).Model(&model.EnrollmentAdmission{}).Where("org_id = ? AND name = ?", suite.OrgID, name).Update("published", false).Error).To(Succeed())
		deleted, err := suite.DeviceStore.Delete(suite.Ctx, suite.OrgID, name, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).To(BeTrue())
		Expect(handler.RecoverAdmissions(suite.Ctx, suite.OrgID)).To(Succeed())
		var admission model.EnrollmentAdmission
		Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND name = ?", suite.OrgID, name).Take(&admission).Error).To(Succeed())
		Expect(admission.Published).To(BeTrue())
		other := create()
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(*other.Metadata.Name))).To(Succeed())
		Expect(load(*other.Metadata.Name).Status.Certificate).NotTo(BeNil())
		Expect(count()).To(Equal(int64(2)))
	})
	It("When a credential is revoked it should prevent both manual and automatic approval", func() {
		request := create()
		credential, status := suite.CertificateSigningRequest.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, credentialName)
		Expect(status.Code).To(Equal(int32(200)))
		credential.Status.Revocation = &domain.EnrollmentCertificateRevocation{RevokedAt: time.Now(), Reason: "test", RevokedBy: "test"}
		Expect(suite.DB.WithContext(suite.Ctx).Model(&model.CertificateSigningRequest{}).Where("org_id = ? AND name = ?", suite.OrgID, credentialName).Update("status", model.MakeJSONField(credential.Status)).Error).To(Succeed())
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(*request.Metadata.Name))).To(Succeed())
		_, status = handler.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name, domain.EnrollmentRequestApproval{Approved: true})
		Expect(status.Code).To(Equal(int32(409)))
		assertRollback(*request.Metadata.Name)
	})
	It("When publication is interrupted it should recover the committed outbox without another admission", func() {
		request := create()
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(*request.Metadata.Name))).To(Succeed())
		Expect(suite.DB.WithContext(suite.Ctx).Model(&model.EnrollmentAdmission{}).Where("org_id = ? AND name = ?", suite.OrgID, *request.Metadata.Name).Update("published", false).Error).To(Succeed())
		admissions := requests.(enrollmentstore.AdmissionStore)
		Expect(admissions.PublishAdmissions(suite.Ctx, suite.OrgID, func(context.Context, uuid.UUID, *domain.Event) error { return errors.New("queue unavailable") })).To(MatchError("queue unavailable"))
		Expect(handler.RecoverAdmissions(suite.Ctx, suite.OrgID)).To(Succeed())
		var admission model.EnrollmentAdmission
		Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND name = ?", suite.OrgID, *request.Metadata.Name).Take(&admission).Error).To(Succeed())
		Expect(admission.Published).To(BeTrue())
		Expect(count()).To(Equal(int64(1)))
	})
	It("When a committed response is lost it should replay original certificate and provenance without the signer", func() {
		request := create()
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(*request.Metadata.Name))).To(Succeed())
		certificate := lo.FromPtr(load(*request.Metadata.Name).Status.Certificate)
		Expect(certificate).NotTo(BeEmpty())
		var original model.EnrollmentAdmission
		Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND name = ?", suite.OrgID, *request.Metadata.Name).Take(&original).Error).To(Succeed())
		Expect(original.PolicyName).To(Equal("policy"))
		Expect(original.Device.Data.Metadata.Name).To(Equal(request.Metadata.Name))
		Expect(original.EnrollmentRequest.Data.Metadata.ResourceVersion).To(Equal(load(*request.Metadata.Name).Metadata.ResourceVersion))
		admissions := requests.(enrollmentstore.AdmissionStore)
		replayed, err := admissions.Admit(suite.Ctx, suite.OrgID, *request.Metadata.Name, func(context.Context, *enrollmentstore.AdmissionSnapshot) (*enrollmentstore.AdmissionDecision, error) {
			return nil, errors.New("must not sign again")
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(replayed.Status.Certificate)).To(Equal(certificate))
		Expect(count()).To(Equal(int64(1)))
		Expect(original.Published).To(BeTrue())
	})
	It("When a stale status follows admission it should preserve the committed certificate and provenance", func() {
		request := create()
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(*request.Metadata.Name))).To(Succeed())
		committed := load(*request.Metadata.Name)
		result, status := handler.ReplaceEnrollmentRequestStatus(suite.Ctx, suite.OrgID, *request.Metadata.Name, *request)
		Expect(status.Code).To(Equal(int32(200)))
		Expect(result.Status.Certificate).To(Equal(committed.Status.Certificate))
		Expect(result.Status.Approval).To(Equal(committed.Status.Approval))
		Expect(load(*request.Metadata.Name).Status).To(Equal(committed.Status))
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(*request.Metadata.Name))).To(Succeed())
		Expect(count()).To(Equal(int64(1)))
	})
	It("When workers contend for the final slot it should commit one device and count once", func() {
		first, second := create(), create()
		var wait sync.WaitGroup
		errorsOut := make(chan error, 12)
		for index := 0; index < 12; index++ {
			name := *first.Metadata.Name
			if index%2 == 0 {
				name = *second.Metadata.Name
			}
			wait.Go(func() { errorsOut <- handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(name)) })
		}
		wait.Wait()
		close(errorsOut)
		for err := range errorsOut {
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(count()).To(Equal(int64(1)))
		var total int64
		Expect(suite.DB.WithContext(suite.Ctx).Model(&model.Device{}).Count(&total).Error).To(Succeed())
		Expect(total).To(Equal(int64(1)))
	})
	DescribeTable("When persistence fails it should discard signed bytes and roll back every admission row", func(table, operation string) {
		request := create()
		producer := queues.NewMockQueueProducer(suite.ctrl)
		var publications atomic.Int32
		producer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, []byte, int64) error {
			Expect(load(*request.Metadata.Name).Status.Certificate).NotTo(BeNil())
			Expect(count()).To(Equal(int64(1)))
			var device model.Device
			Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND name = ?", suite.OrgID, *request.Metadata.Name).Take(&device).Error).To(Succeed())
			publications.Add(1)
			return nil
		}).AnyTimes()
		handler = newHandler(suite.DB, admissionMatcher{"policy": {matched: true}}, worker_client.NewWorkerClient(producer, suite.Log))
		injected := errors.New("injected persistence failure: " + table)
		var failures atomic.Int32
		fail := func(database *gorm.DB) {
			if database.Statement.Table == table {
				if table == "enrollment_requests" {
					updates, ok := database.Statement.Dest.(map[string]interface{})
					if !ok || updates["status"] == nil {
						return
					}
				}
				failures.Add(1)
				_ = database.AddError(injected)
			}
		}
		if operation == "create" {
			Expect(suite.DB.Callback().Create().Before("gorm:create").Register("admission_failure", fail)).To(Succeed())
			DeferCleanup(func() { Expect(suite.DB.Callback().Create().Remove("admission_failure")).To(Succeed()) })
		} else {
			Expect(suite.DB.Callback().Update().Before("gorm:update").Register("admission_failure", fail)).To(Succeed())
			DeferCleanup(func() { Expect(suite.DB.Callback().Update().Remove("admission_failure")).To(Succeed()) })
		}
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(*request.Metadata.Name))).To(MatchError(ContainSubstring(injected.Error())))
		Expect(failures.Load()).To(Equal(int32(1)))
		assertRollback(*request.Metadata.Name)
		Expect(publications.Load()).To(BeZero())
		if operation == "create" {
			Expect(suite.DB.Callback().Create().Remove("admission_failure")).To(Succeed())
		} else {
			Expect(suite.DB.Callback().Update().Remove("admission_failure")).To(Succeed())
		}
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(*request.Metadata.Name))).To(Succeed())
		Expect(count()).To(Equal(int64(1)))
		Expect(publications.Load()).To(BeNumerically(">", 0))
	}, Entry("Device", "devices", "create"), Entry("ledger", "enrollment_admissions", "create"), Entry("events", "events", "create"), Entry("count", "enrollment_approval_policies", "update"), Entry("status", "enrollment_requests", "update"))
	DescribeTable("When signing fails it should leave no admission side effects and allow a corrected retry", func(validCSR bool) {
		request := create()
		originalCSR := request.Spec.Csr
		request.Spec.Csr = "invalid"
		if validCSR {
			request.Spec.Csr = CreateTestER().Spec.Csr
		}
		Expect(suite.DB.WithContext(suite.Ctx).Model(&model.EnrollmentRequest{}).Where("org_id = ? AND name = ?", suite.OrgID, *request.Metadata.Name).Update("spec", model.MakeJSONField(request.Spec)).Error).To(Succeed())
		err := handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(*request.Metadata.Name))
		Expect(err).To(HaveOccurred())
		if validCSR {
			Expect(err).To(MatchError(ContainSubstring("attempt to supply a fake CN")))
		}
		assertRollback(*request.Metadata.Name)
		request.Spec.Csr = originalCSR
		Expect(suite.DB.WithContext(suite.Ctx).Model(&model.EnrollmentRequest{}).Where("org_id = ? AND name = ?", suite.OrgID, *request.Metadata.Name).Update("spec", model.MakeJSONField(request.Spec)).Error).To(Succeed())
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(*request.Metadata.Name))).To(Succeed())
		Expect(load(*request.Metadata.Name).Status.Certificate).NotTo(BeNil())
		Expect(count()).To(Equal(int64(1)))
	}, Entry("invalid CSR", false), Entry("signer rejects a valid CSR with mismatched identity", true))
	It("When commit acknowledgement is lost it should resolve the committed admission before retry", func() {
		request := create()
		fail := &atomic.Bool{}
		pool := ambiguousAdmissionPool{ConnPool: suite.DB.ConnPool, fail: fail}
		database := suite.DB.Session(&gorm.Session{NewDB: true}).WithContext(suite.Ctx)
		database.Statement.ConnPool = pool
		database.Config.ConnPool = pool
		handler = newHandler(database, admissionMatcher{"policy": {matched: true}})
		fail.Store(true)
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(*request.Metadata.Name))).To(MatchError("commit acknowledgement lost"))
		certificate := load(*request.Metadata.Name).Status.Certificate
		Expect(certificate).NotTo(BeNil())
		admissions := requests.(enrollmentstore.AdmissionStore)
		result, err := admissions.Admit(suite.Ctx, suite.OrgID, *request.Metadata.Name, func(context.Context, *enrollmentstore.AdmissionSnapshot) (*enrollmentstore.AdmissionDecision, error) {
			return nil, errors.New("must not sign")
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Status.Certificate).To(Equal(certificate))
		Expect(count()).To(Equal(int64(1)))
	})
	It("When a manual commit acknowledgement is lost it should return the committed result without a failure event", func() {
		request := create()
		fail := &atomic.Bool{}
		pool := ambiguousAdmissionPool{ConnPool: suite.DB.ConnPool, fail: fail}
		database := suite.DB.Session(&gorm.Session{NewDB: true}).WithContext(suite.Ctx)
		database.Statement.ConnPool = pool
		database.Config.ConnPool = pool
		handler = newHandler(database, nil)
		fail.Store(true)
		result, status := handler.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name, domain.EnrollmentRequestApproval{Approved: true})
		Expect(status.Code).To(Equal(int32(200)))
		Expect(result.Approved).To(BeTrue())
		certificate := load(*request.Metadata.Name).Status.Certificate
		Expect(certificate).NotTo(BeNil())
		replay, status := handler.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name, domain.EnrollmentRequestApproval{Approved: true})
		Expect(status.Code).To(Equal(int32(200)))
		Expect(replay).To(Equal(result))
		Expect(load(*request.Metadata.Name).Status.Certificate).To(Equal(certificate))
		var failures int64
		Expect(suite.DB.WithContext(suite.Ctx).Model(&model.Event{}).Where("org_id = ? AND involved_object_name = ? AND reason = ?", suite.OrgID, *request.Metadata.Name, domain.EventReasonEnrollmentRequestApprovalFailed).Count(&failures).Error).To(Succeed())
		Expect(failures).To(BeZero())
		Expect(count()).To(BeZero())
	})
	It("When manual commit resolution fails it should retain the committed result for a safe retry", func() {
		request := create()
		name := *request.Metadata.Name
		ca := &countingAdmissionCA{CAClient: suite.caClient}
		suite.caClient = icrypto.NewCAClient(suite.caClient.Cfg, ca)
		var publications atomic.Int32
		producer := queues.NewMockQueueProducer(suite.ctrl)
		producer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, []byte, int64) error {
			publications.Add(1)
			return nil
		}).AnyTimes()
		failCommit, failResolution := &atomic.Bool{}, &atomic.Bool{}
		var resolutionFailures atomic.Int32
		Expect(suite.DB.Callback().Query().Before("gorm:query").Register("admission_resolution_failure", func(database *gorm.DB) {
			if database.Statement.Table == "enrollment_admissions" && failResolution.Swap(false) {
				resolutionFailures.Add(1)
				_ = database.AddError(errors.New("admission resolution unavailable"))
			}
		})).To(Succeed())
		DeferCleanup(func() { Expect(suite.DB.Callback().Query().Remove("admission_resolution_failure")).To(Succeed()) })
		pool := ambiguousAdmissionPool{ConnPool: suite.DB.ConnPool, fail: failCommit, afterCommit: func() { failResolution.Store(true) }}
		database := suite.DB.Session(&gorm.Session{NewDB: true}).WithContext(suite.Ctx)
		database.Statement.ConnPool, database.Config.ConnPool = pool, pool
		handler = newHandler(database, nil, worker_client.NewWorkerClient(producer, suite.Log))
		failCommit.Store(true)
		result, status := handler.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, name, domain.EnrollmentRequestApproval{Approved: true})
		Expect(status.Code).To(Equal(int32(500)))
		Expect(result).To(BeNil())
		Expect(suite.Ctx.Err()).NotTo(HaveOccurred())
		Expect(resolutionFailures.Load()).To(Equal(int32(1)))
		Expect(publications.Load()).To(BeZero())
		Expect(ca.calls.Load()).To(Equal(int32(1)))
		committed := load(name)
		Expect(committed.Status.Certificate).NotTo(BeNil())
		Expect(committed.Status.Approval.Approved).To(BeTrue())
		var original model.EnrollmentAdmission
		Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND name = ?", suite.OrgID, name).Take(&original).Error).To(Succeed())
		Expect(original.Source).To(Equal("manual"))
		Expect(original.Published).To(BeFalse())
		Expect(original.EnrollmentRequest.Data.Status.Certificate).To(Equal(committed.Status.Certificate))
		var device model.Device
		Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND name = ?", suite.OrgID, name).Take(&device).Error).To(Succeed())
		Expect(suite.DB.Callback().Query().Remove("admission_resolution_failure")).To(Succeed())
		replay, status := handler.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, name, domain.EnrollmentRequestApproval{Approved: true})
		Expect(status.Code).To(Equal(int32(200)))
		Expect(replay).To(Equal(committed.Status.Approval))
		Expect(load(name).Status.Certificate).To(Equal(committed.Status.Certificate))
		Expect(ca.calls.Load()).To(Equal(int32(1)))
		Expect(count()).To(BeZero())
		Expect(publications.Load()).To(BeNumerically(">", 0))
		var admissions []model.EnrollmentAdmission
		Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND name = ?", suite.OrgID, name).Find(&admissions).Error).To(Succeed())
		Expect(admissions).To(HaveLen(1))
		Expect(admissions[0].AdmissionID).To(Equal(original.AdmissionID))
		Expect(admissions[0].EnrollmentRequest).To(Equal(original.EnrollmentRequest))
		Expect(admissions[0].Device).To(Equal(original.Device))
		Expect(admissions[0].Published).To(BeTrue())
		var devices []model.Device
		Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND name = ?", suite.OrgID, name).Find(&devices).Error).To(Succeed())
		Expect(devices).To(Equal([]model.Device{device}))
		var failures int64
		Expect(suite.DB.WithContext(suite.Ctx).Model(&model.Event{}).Where("org_id = ? AND involved_object_name = ? AND reason = ?", suite.OrgID, name, domain.EventReasonEnrollmentRequestApprovalFailed).Count(&failures).Error).To(Succeed())
		Expect(failures).To(BeZero())
	})
	DescribeTable("When reenrollment races lost commit acknowledgement it should resolve the original admission", func(approveReplacement bool) {
		request := create()
		name := *request.Metadata.Name
		fail := &atomic.Bool{}
		var committed *domain.EnrollmentRequest
		pool := ambiguousAdmissionPool{ConnPool: suite.DB.ConnPool, fail: fail, afterCommit: func() {
			committed = load(name)
			Expect(suite.DB.WithContext(suite.Ctx).Unscoped().Where("org_id = ? AND name = ?", suite.OrgID, name).Delete(&model.Device{}).Error).To(Succeed())
			_, err := requests.Delete(suite.Ctx, suite.OrgID, name)
			Expect(err).NotTo(HaveOccurred())
			replacement := &domain.EnrollmentRequest{Metadata: domain.ObjectMeta{Name: &name}, Spec: request.Spec, Status: &domain.EnrollmentRequestStatus{EnrollmentCertificateName: &credentialName, Conditions: []domain.Condition{}}}
			_, err = requests.Create(suite.Ctx, suite.OrgID, replacement)
			Expect(err).NotTo(HaveOccurred())
			if approveReplacement {
				_, status := newHandler(suite.DB, nil).ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, name, domain.EnrollmentRequestApproval{Approved: true})
				Expect(status.Code).To(Equal(int32(200)))
			}
		}}
		database := suite.DB.Session(&gorm.Session{NewDB: true}).WithContext(suite.Ctx)
		database.Statement.ConnPool, database.Config.ConnPool = pool, pool
		handler = newHandler(database, nil)
		fail.Store(true)
		result, status := handler.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, name, domain.EnrollmentRequestApproval{Approved: true})
		Expect(status.Code).To(Equal(int32(200)))
		Expect(result).To(Equal(committed.Status.Approval))
		current := load(name)
		Expect(current.Metadata.CreationTimestamp).NotTo(Equal(committed.Metadata.CreationTimestamp))
		if approveReplacement {
			Expect(result).NotTo(Equal(current.Status.Approval))
		} else {
			Expect(current.Status.Certificate).To(BeNil())
		}
		var failures int64
		Expect(suite.DB.WithContext(suite.Ctx).Model(&model.Event{}).Where("org_id = ? AND involved_object_name = ? AND reason = ?", suite.OrgID, name, domain.EventReasonEnrollmentRequestApprovalFailed).Count(&failures).Error).To(Succeed())
		Expect(failures).To(BeZero())
		Expect(count()).To(BeZero())
	}, Entry("pending replacement", false), Entry("approved replacement", true))
	DescribeTable("When manual persistence fails it should report failure only after rollback and allow retry", func(table string) {
		request := create()
		name := *request.Metadata.Name
		var failures atomic.Int32
		injected := errors.New("manual persistence failure")
		fail := func(database *gorm.DB) {
			if database.Statement.Table == table {
				failures.Add(1)
				_ = database.AddError(injected)
			}
		}
		Expect(suite.DB.Callback().Create().Before("gorm:create").Register("manual_failure", fail)).To(Succeed())
		Expect(suite.DB.Callback().Update().Before("gorm:update").Register("manual_failure", fail)).To(Succeed())
		DeferCleanup(func() {
			Expect(suite.DB.Callback().Create().Remove("manual_failure")).To(Succeed())
			Expect(suite.DB.Callback().Update().Remove("manual_failure")).To(Succeed())
		})
		_, status := handler.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, name, domain.EnrollmentRequestApproval{Approved: true})
		Expect(status.Code).To(Equal(int32(500)))
		Expect(failures.Load()).To(Equal(int32(1)))
		Expect(load(name).Status.Certificate).To(BeNil())
		Expect(enrollmentstore.Pending(load(name))).To(BeTrue())
		Expect(count()).To(BeZero())
		for _, record := range []interface{}{&model.Device{}, &model.EnrollmentAdmission{}} {
			var total int64
			Expect(suite.DB.WithContext(suite.Ctx).Model(record).Where("org_id = ? AND name = ?", suite.OrgID, name).Count(&total).Error).To(Succeed())
			Expect(total).To(BeZero())
		}
		var recorded []model.Event
		Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND involved_object_name = ?", suite.OrgID, name).Find(&recorded).Error).To(Succeed())
		Expect(recorded).To(HaveLen(1))
		Expect(recorded[0].Reason).To(Equal(string(domain.EventReasonEnrollmentRequestApprovalFailed)))
		Expect(suite.DB.Callback().Create().Remove("manual_failure")).To(Succeed())
		Expect(suite.DB.Callback().Update().Remove("manual_failure")).To(Succeed())
		_, status = handler.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, name, domain.EnrollmentRequestApproval{Approved: true})
		Expect(status.Code).To(Equal(int32(200)))
		Expect(count()).To(BeZero())
	}, Entry("Device", "devices"), Entry("status", "enrollment_requests"), Entry("ledger", "enrollment_admissions"))
	It("When manual and automatic approvals race they should share one immutable result", func() {
		request := create()
		var wait sync.WaitGroup
		var automaticError error
		var manualStatus domain.Status
		wait.Go(func() {
			automaticError = handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(*request.Metadata.Name))
		})
		wait.Go(func() {
			_, manualStatus = handler.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name, domain.EnrollmentRequestApproval{Approved: true})
		})
		wait.Wait()
		Expect(automaticError).NotTo(HaveOccurred())
		Expect(manualStatus.Code).To(Equal(int32(200)))
		var admission model.EnrollmentAdmission
		Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND name = ?", suite.OrgID, *request.Metadata.Name).Take(&admission).Error).To(Succeed())
		if admission.Source == "manual" {
			Expect(count()).To(BeZero())
		} else {
			Expect(admission.Source).To(Equal("policy"))
			Expect(count()).To(Equal(int64(1)))
		}
		Expect(admission.EnrollmentRequest.Data.Status.Certificate).To(Equal(load(*request.Metadata.Name).Status.Certificate))
	})
	It("When a false manual action precedes approval it should allow later explicit approval", func() {
		request := create()
		_, status := handler.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name, domain.EnrollmentRequestApproval{Approved: false})
		Expect(status.Code).To(Equal(int32(200)))
		Expect(load(*request.Metadata.Name).Status.Certificate).To(BeNil())
		Expect(enrollmentstore.Pending(load(*request.Metadata.Name))).To(BeTrue())
		_, status = handler.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name, domain.EnrollmentRequestApproval{Approved: true})
		Expect(status.Code).To(Equal(int32(200)))
		Expect(count()).To(BeZero())
	})
	DescribeTable("When manual approval has no credential binding it should commit and replay without quota", func(withStatus bool) {
		request := CreateTestER()
		request.Status = nil
		if withStatus {
			request.Status = &domain.EnrollmentRequestStatus{Conditions: []domain.Condition{}}
		}
		created, err := requests.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		name := *created.Metadata.Name
		if withStatus {
			Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(name))).To(Succeed())
			Expect(load(name).Status.Certificate).To(BeNil())
		}
		result, status := handler.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, name, domain.EnrollmentRequestApproval{Approved: true})
		Expect(status.Code).To(Equal(int32(200)))
		Expect(result.Approved).To(BeTrue())
		certificate := *load(name).Status.Certificate
		Expect(certificate).NotTo(BeEmpty())
		var admission model.EnrollmentAdmission
		Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND name = ?", suite.OrgID, name).Take(&admission).Error).To(Succeed())
		Expect(admission.Source).To(Equal("manual"))
		Expect(admission.CertificateName).To(BeEmpty())
		replayed, status := handler.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, name, domain.EnrollmentRequestApproval{Approved: true})
		Expect(status.Code).To(Equal(int32(200)))
		Expect(replayed).To(Equal(result))
		Expect(*load(name).Status.Certificate).To(Equal(certificate))
		Expect(count()).To(BeZero())
	}, Entry("status present", true), Entry("status absent", false))
	DescribeTable("When a bound manual credential fails validation it should refuse admission", func(failure string) {
		request := create()
		var credential model.CertificateSigningRequest
		Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND name = ?", suite.OrgID, credentialName).Take(&credential).Error).To(Succeed())
		switch failure {
		case "revoked":
			credential.Status.Data.Revocation = &domain.EnrollmentCertificateRevocation{}
		case "unissued":
			credential.Status.Data.Certificate = nil
		case "wrong signer":
			credential.Spec.Data.SignerName = "different-signer"
		case "invalid certificate":
			credential.Status.Data.Certificate = lo.ToPtr([]byte("invalid"))
		}
		Expect(suite.DB.WithContext(suite.Ctx).Model(&credential).Where("org_id = ? AND name = ?", suite.OrgID, credentialName).Updates(map[string]interface{}{"spec": credential.Spec, "status": credential.Status}).Error).To(Succeed())
		_, status := handler.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name, domain.EnrollmentRequestApproval{Approved: true})
		Expect(status.Code).To(Equal(int32(409)))
		assertRollback(*request.Metadata.Name)
	}, Entry("revoked", "revoked"), Entry("unissued", "unissued"), Entry("wrong signer", "wrong signer"), Entry("invalid certificate", "invalid certificate"))
	It("When an unbound request becomes bound before locking it should conflict rather than bypass credential checks", func() {
		request := CreateTestER()
		request.Status = &domain.EnrollmentRequestStatus{Conditions: []domain.Condition{}}
		created, err := requests.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		name := *created.Metadata.Name
		var changed atomic.Bool
		Expect(suite.DB.Callback().Query().After("gorm:query").Register("bind_before_admission_lock", func(database *gorm.DB) {
			if database.Statement.Table == "enrollment_requests" && changed.CompareAndSwap(false, true) {
				created.Status.EnrollmentCertificateName = &credentialName
				Expect(suite.DB.WithContext(suite.Ctx).Model(&model.EnrollmentRequest{}).Where("org_id = ? AND name = ?", suite.OrgID, name).Update("status", model.MakeJSONField(created.Status)).Error).To(Succeed())
			}
		})).To(Succeed())
		DeferCleanup(func() { Expect(suite.DB.Callback().Query().Remove("bind_before_admission_lock")).To(Succeed()) })
		_, status := handler.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, name, domain.EnrollmentRequestApproval{Approved: true})
		Expect(status.Code).To(Equal(int32(409)))
		Expect(changed.Load()).To(BeTrue())
		Expect(load(name).Status.Certificate).To(BeNil())
		Expect(load(name).Status.EnrollmentCertificateName).To(Equal(&credentialName))
		_, status = handler.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, name, domain.EnrollmentRequestApproval{Approved: true})
		Expect(status.Code).To(Equal(int32(200)))
		Expect(count()).To(BeZero())
	})
	It("When a previously manually denied request is explicitly approved it should remain ineligible to automatic workers", func() {
		request := create()
		request.Status.Approval = &domain.EnrollmentRequestApprovalStatus{Approved: false}
		domain.SetStatusCondition(&request.Status.Conditions, domain.Condition{Type: domain.ConditionTypeEnrollmentRequestDenied, Status: domain.ConditionStatusTrue, Reason: "ManuallyDenied"})
		Expect(suite.DB.WithContext(suite.Ctx).Model(&model.EnrollmentRequest{}).Where("org_id = ? AND name = ?", suite.OrgID, *request.Metadata.Name).Update("status", model.MakeJSONField(request.Status)).Error).To(Succeed())
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(*request.Metadata.Name))).To(Succeed())
		Expect(load(*request.Metadata.Name).Status.Certificate).To(BeNil())
		_, status := handler.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name, domain.EnrollmentRequestApproval{Approved: true})
		Expect(status.Code).To(Equal(int32(200)))
		Expect(domain.IsStatusConditionTrue(load(*request.Metadata.Name).Status.Conditions, domain.ConditionTypeEnrollmentRequestDenied)).To(BeFalse())
		Expect(count()).To(BeZero())
	})
	It("When unbound manual approvals race it should commit one tenant-local admission", func() {
		request := CreateTestER()
		request.Status = &domain.EnrollmentRequestStatus{Conditions: []domain.Condition{}}
		created, err := requests.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		var wait sync.WaitGroup
		results := make(chan domain.Status, 2)
		for range 2 {
			wait.Go(func() {
				_, status := handler.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, *created.Metadata.Name, domain.EnrollmentRequestApproval{Approved: true})
				results <- status
			})
		}
		wait.Wait()
		close(results)
		for status := range results {
			Expect(status.Code).To(Equal(int32(200)))
		}
		var total int64
		Expect(suite.DB.WithContext(suite.Ctx).Model(&model.EnrollmentAdmission{}).Where("org_id = ? AND name = ?", suite.OrgID, *created.Metadata.Name).Count(&total).Error).To(Succeed())
		Expect(total).To(Equal(int64(1)))
		_, status := handler.ApproveEnrollmentRequest(suite.Ctx, uuid.New(), *created.Metadata.Name, domain.EnrollmentRequestApproval{Approved: true})
		Expect(status.Code).To(Equal(int32(404)))
		Expect(count()).To(BeZero())
	})
	It("When a policy is deleted it should reevaluate only its pending credential scope", func() {
		request := create()
		_, _, err := policystore.NewStore(suite.DB, suite.Log).Mutate(suite.Ctx, suite.OrgID, "policy", credentialName, policystore.Delete, func(*domain.EnrollmentApprovalPolicy, *domain.CertificateSigningRequest, []domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
			return nil, nil
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, domain.Event{InvolvedObject: domain.ObjectReference{Kind: domain.EnrollmentApprovalPolicyKind, Name: "policy"}, Reason: domain.EventReasonResourceDeleted})).To(Succeed())
		Expect(domain.FindStatusCondition(load(*request.Metadata.Name).Status.Conditions, domain.ConditionTypeEnrollmentRequestApproved).Reason).To(Equal("NoMatchingPolicy"))
		Expect(handler.ReevaluateEnrollment(suite.Ctx, uuid.New(), event(*request.Metadata.Name))).To(Succeed())
		Expect(count()).To(BeZero())
	})
	DescribeTable("When a policy event is replayed it should scan only pending requests in its organization and credential", func(reason domain.EventReason) {
		pending := create()
		denied := create()
		_, _, _, err := requests.Mutate(suite.Ctx, suite.OrgID, *denied.Metadata.Name, denied, func(mutation *enrollmentstore.EnrollmentRequestMutation) error {
			domain.SetStatusCondition(&mutation.EnrollmentRequest.Status.Conditions, domain.Condition{Type: domain.ConditionTypeEnrollmentRequestDenied, Status: domain.ConditionStatusTrue, Reason: "ManuallyDenied"})
			return nil
		})
		Expect(err).NotTo(HaveOccurred())
		otherCredential, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, CreateTestCSR())
		Expect(status.Code).To(Equal(int32(201)))
		otherRequest := CreateTestER()
		otherRequest.Status = &domain.EnrollmentRequestStatus{EnrollmentCertificateName: otherCredential.Metadata.Name}
		other, err := requests.Create(suite.Ctx, suite.OrgID, &otherRequest)
		Expect(err).NotTo(HaveOccurred())
		otherOrg := uuid.New()
		Expect(suite.DB.WithContext(suite.Ctx).Create(&model.Organization{ID: otherOrg, ExternalID: otherOrg.String(), DisplayName: "Other organization"}).Error).To(Succeed())
		otherOrgCtx := util.WithOrganizationID(suite.Ctx, otherOrg)
		foreignRequest := *pending
		foreignRequest.Metadata.ResourceVersion = nil
		foreignRequest.Metadata.CreationTimestamp = nil
		foreign, err := requests.Create(otherOrgCtx, otherOrg, &foreignRequest)
		Expect(err).NotTo(HaveOccurred())
		var foreignPolicy model.EnrollmentApprovalPolicy
		Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND name = ?", suite.OrgID, "policy").Take(&foreignPolicy).Error).To(Succeed())
		foreignPolicy.OrgID = otherOrg
		Expect(suite.DB.WithContext(otherOrgCtx).Create(&foreignPolicy).Error).To(Succeed())
		var foreignCredential model.CertificateSigningRequest
		Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND name = ?", suite.OrgID, credentialName).Take(&foreignCredential).Error).To(Succeed())
		foreignCredential.OrgID = otherOrg
		Expect(suite.DB.WithContext(otherOrgCtx).Create(&foreignCredential).Error).To(Succeed())
		admissions := requests.(enrollmentstore.AdmissionStore)
		names, err := admissions.PendingNames(suite.Ctx, suite.OrgID, credentialName, "", 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(names).To(Equal([]string{*pending.Metadata.Name}))
		names, err = admissions.PendingNames(suite.Ctx, suite.OrgID, credentialName, names[0], 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(names).To(BeEmpty())
		if reason == domain.EventReasonResourceDeleted {
			_, _, err := policystore.NewStore(suite.DB, suite.Log).Mutate(suite.Ctx, suite.OrgID, "policy", credentialName, policystore.Delete, func(*domain.EnrollmentApprovalPolicy, *domain.CertificateSigningRequest, []domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
				return nil, nil
			})
			Expect(err).NotTo(HaveOccurred())
		}
		if reason == domain.EventReasonResourceUpdated {
			_, _, err := policystore.NewStore(suite.DB, suite.Log).Mutate(suite.Ctx, suite.OrgID, "policy", credentialName, policystore.Update, func(policy *domain.EnrollmentApprovalPolicy, _ *domain.CertificateSigningRequest, _ []domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
				policy.Spec.DeviceLabels = map[string]string{"current": "value"}
				return policy, nil
			})
			Expect(err).NotTo(HaveOccurred())
		}
		policyEvent := domain.Event{InvolvedObject: domain.ObjectReference{Kind: domain.EnrollmentApprovalPolicyKind, Name: "policy"}, Reason: reason}
		for range 2 {
			Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, policyEvent)).To(Succeed())
		}
		if reason == domain.EventReasonResourceDeleted {
			Expect(domain.FindStatusCondition(load(*pending.Metadata.Name).Status.Conditions, domain.ConditionTypeEnrollmentRequestApproved).Reason).To(Equal("NoMatchingPolicy"))
			Expect(count()).To(BeZero())
		} else {
			Expect(load(*pending.Metadata.Name).Status.Certificate).NotTo(BeNil())
			Expect(count()).To(Equal(int64(1)))
		}
		Expect(load(*denied.Metadata.Name).Metadata.ResourceVersion).To(Equal(lo.ToPtr("2")))
		Expect(load(*other.Metadata.Name)).To(Equal(other))
		currentForeign, err := requests.Get(otherOrgCtx, otherOrg, *foreign.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(currentForeign).To(Equal(foreign))
		var currentForeignPolicy model.EnrollmentApprovalPolicy
		Expect(suite.DB.WithContext(otherOrgCtx).Where("org_id = ? AND name = ?", otherOrg, "policy").Take(&currentForeignPolicy).Error).To(Succeed())
		Expect(currentForeignPolicy.Status.Data.EnrollmentCount).To(BeZero())
		Expect(currentForeignPolicy.ResourceVersion).To(Equal(foreignPolicy.ResourceVersion))
		Expect(currentForeignPolicy.Spec).To(Equal(foreignPolicy.Spec))
		var currentForeignCredential model.CertificateSigningRequest
		Expect(suite.DB.WithContext(otherOrgCtx).Where("org_id = ? AND name = ?", otherOrg, credentialName).Take(&currentForeignCredential).Error).To(Succeed())
		Expect(currentForeignCredential.PolicySetRevision).To(Equal(foreignCredential.PolicySetRevision))
	}, Entry("create", domain.EventReasonResourceCreated), Entry("update", domain.EventReasonResourceUpdated), Entry("delete", domain.EventReasonResourceDeleted))
	DescribeTable("When admission and credential-scoped mutations contend they should serialize authoritative decisions", func(operation string, admissionFirst bool) {
		request := create()
		name := *request.Metadata.Name
		ctx, cancel := context.WithTimeout(suite.Ctx, 15*time.Second)
		defer cancel()
		locked := make(chan struct{})
		release := make(chan struct{})
		var releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		hold := func() error {
			close(locked)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		mutate := func() error {
			return store.WithTransaction(ctx, suite.DB, func(txCtx context.Context) error {
				if operation == "revoke" {
					credentials := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.cfg.CA.DeviceEnrollmentSignerName)
					_, _, err := credentials.Revoke(txCtx, suite.OrgID, credentialName, domain.EnrollmentCertificateRevocation{RevokedAt: time.Now(), Reason: "test", RevokedBy: "test"}, nil)
					if err != nil {
						return err
					}
				} else {
					mutation := policystore.Update
					if operation == "delete" {
						mutation = policystore.Delete
					}
					_, _, err := policystore.NewStore(suite.DB, suite.Log).Mutate(txCtx, suite.OrgID, "policy", credentialName, mutation, func(policy *domain.EnrollmentApprovalPolicy, _ *domain.CertificateSigningRequest, _ []domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
						policy.Spec.Enabled = operation == "report"
						policy.Spec.DeviceLabels = map[string]string{"revision": "current"}
						return policy, nil
					})
					if err != nil {
						return err
					}
				}
				if !admissionFirst {
					return hold()
				}
				return nil
			})
		}
		if admissionFirst {
			handler = newHandler(suite.DB, admissionMatchFunc(func(context.Context, domain.EnrollmentApprovalPolicy, domain.EnrollmentRequest) (bool, error) {
				return true, hold()
			}))
		} else if operation == "report" {
			handler = newHandler(suite.DB, admissionMatchFunc(func(_ context.Context, policy domain.EnrollmentApprovalPolicy, current domain.EnrollmentRequest) (bool, error) {
				return policy.Spec.DeviceLabels["revision"] == "current" && current.Spec.DeviceStatus != nil && current.Spec.DeviceStatus.SystemInfo.AdditionalProperties["productSerial"] == "current", nil
			}))
		}
		admit := func() error { return handler.ReevaluateEnrollment(ctx, suite.OrgID, event(name)) }
		first, second := mutate, admit
		if admissionFirst {
			first, second = admit, mutate
		}
		firstDone, secondDone := make(chan error, 1), make(chan error, 1)
		var workers sync.WaitGroup
		defer func() {
			unblock()
			cancel()
			joined := make(chan struct{})
			go func() { workers.Wait(); close(joined) }()
			Eventually(joined, 10*time.Second).Should(BeClosed())
		}()
		workers.Go(func() { firstDone <- first() })
		Eventually(locked, 10*time.Second).Should(BeClosed())
		workers.Go(func() { secondDone <- second() })
		Eventually(func() (int64, error) {
			var waiting int64
			err := suite.DB.WithContext(ctx).Raw("SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid() AND wait_event_type = 'Lock' AND query LIKE '%certificate_signing_requests%' AND query LIKE '%FOR UPDATE%'").Scan(&waiting).Error
			return waiting, err
		}, 10*time.Second).Should(BeNumerically(">", 0))
		if operation == "report" {
			_, _, _, err := requests.Mutate(suite.Ctx, suite.OrgID, name, nil, func(mutation *enrollmentstore.EnrollmentRequestMutation) error {
				mutation.EnrollmentRequest.Spec.DeviceStatus = &domain.DeviceStatus{SystemInfo: domain.DeviceSystemInfo{AdditionalProperties: map[string]string{"productSerial": "current"}}}
				return nil
			})
			Expect(err).NotTo(HaveOccurred())
		}
		unblock()
		Eventually(firstDone, 10*time.Second).Should(Receive(Succeed()))
		Eventually(secondDone, 10*time.Second).Should(Receive(Succeed()))
		if admissionFirst || operation == "report" {
			committed := load(name)
			Expect(committed.Status.Certificate).NotTo(BeNil())
			Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(name))).To(Succeed())
			Expect(load(name)).To(Equal(committed))
			Expect(count()).To(Equal(int64(1)))
			if operation == "report" {
				var admission model.EnrollmentAdmission
				Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND name = ?", suite.OrgID, name).Take(&admission).Error).To(Succeed())
				Expect(admission.PolicyRevision).To(Equal(int64(1)))
				Expect(admission.EnrollmentRequest.Data.Spec.DeviceStatus.SystemInfo.AdditionalProperties).To(HaveKeyWithValue("productSerial", "current"))
			}
		} else {
			assertRollback(name)
		}
		if operation == "revoke" {
			credential, status := suite.CertificateSigningRequest.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, credentialName)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(credential.Status.Revocation).NotTo(BeNil())
			_, status = handler.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, *create().Metadata.Name, domain.EnrollmentRequestApproval{Approved: true})
			Expect(status.Code).To(Equal(int32(409)))
		}
	}, Entry("disable first", "disable", false), Entry("admit before disable", "disable", true), Entry("delete first", "delete", false), Entry("admit before delete", "delete", true), Entry("revoke first", "revoke", false), Entry("admit before revoke", "revoke", true), Entry("current report and policy", "report", false))
	It("When revocation consequences follow admission it should flag the device without changing the committed result", func() {
		request := create()
		name := *request.Metadata.Name
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(name))).To(Succeed())
		committed := load(name)
		pending := create()
		_, status := suite.CertificateSigningRequest.RevokeCertificateSigningRequest(suite.Ctx, suite.OrgID, credentialName, domain.EnrollmentCertificateRevocationRequest{Reason: "test"})
		Expect(status.Code).To(Equal(int32(200)))
		changed, status := handler.DenyEnrollmentRequestsForRevokedCredential(suite.Ctx, suite.OrgID, credentialName, domain.EnrollmentCredentialRevocationBatchSize)
		Expect(status.Code).To(Equal(int32(200)))
		Expect(changed).To(Equal(int64(1)))
		changed, status = suite.Device.FlagDevicesForRevokedEnrollmentCredential(suite.Ctx, suite.OrgID, credentialName, domain.EnrollmentCredentialRevocationBatchSize)
		Expect(status.Code).To(Equal(int32(200)))
		Expect(changed).To(Equal(int64(1)))
		device, err := suite.DeviceStore.Get(suite.Ctx, suite.OrgID, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(domain.IsStatusConditionTrue(device.Status.Conditions, domain.ConditionTypeDeviceEnrollmentCredentialRevoked)).To(BeTrue())
		Expect(domain.IsStatusConditionTrue(load(*pending.Metadata.Name).Status.Conditions, domain.ConditionTypeEnrollmentRequestDenied)).To(BeTrue())
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(name))).To(Succeed())
		Expect(load(name)).To(Equal(committed))
		Expect(count()).To(Equal(int64(1)))
	})
	It("When the policy changes before admission it should use the current revision and gates", func() {
		request := create()
		policies := policystore.NewStore(suite.DB, suite.Log)
		change := func(enabled bool) {
			_, _, err := policies.Mutate(suite.Ctx, suite.OrgID, "policy", credentialName, policystore.Update, func(policy *domain.EnrollmentApprovalPolicy, _ *domain.CertificateSigningRequest, _ []domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
				policy.Spec.Enabled = enabled
				policy.Spec.DeviceLabels = map[string]string{"revision": "current"}
				return policy, nil
			})
			Expect(err).NotTo(HaveOccurred())
		}
		policyEvent := domain.Event{InvolvedObject: domain.ObjectReference{Kind: domain.EnrollmentApprovalPolicyKind, Name: "policy"}, Reason: domain.EventReasonResourceUpdated}
		change(false)
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, policyEvent)).To(Succeed())
		Expect(domain.FindStatusCondition(load(*request.Metadata.Name).Status.Conditions, domain.ConditionTypeEnrollmentRequestApproved).Reason).To(Equal("NoMatchingPolicy"))
		assertRollback(*request.Metadata.Name)
		change(true)
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, policyEvent)).To(Succeed())
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(*request.Metadata.Name))).To(Succeed())
		var admission model.EnrollmentAdmission
		Expect(suite.DB.WithContext(suite.Ctx).Where("org_id = ? AND name = ?", suite.OrgID, *request.Metadata.Name).Take(&admission).Error).To(Succeed())
		Expect(admission.PolicyRevision).To(Equal(int64(2)))
		Expect(lo.FromPtr(admission.Device.Data.Metadata.Labels)).To(HaveKeyWithValue("revision", "current"))
		Expect(count()).To(Equal(int64(1)))
	})
	It("When issuance time is in the future it should fail closed", func() {
		request := create()
		Expect(suite.DB.WithContext(suite.Ctx).Exec("UPDATE enrollment_approval_policies SET spec = jsonb_set(spec, '{autoApprovalWindowDays}', '1') WHERE org_id = ?", suite.OrgID).Error).To(Succeed())
		credential, status := suite.CertificateSigningRequest.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, credentialName)
		Expect(status.Code).To(Equal(int32(200)))
		credential.Status.IssuedAt = lo.ToPtr(time.Now().Add(time.Hour))
		Expect(suite.DB.WithContext(suite.Ctx).Model(&model.CertificateSigningRequest{}).Where("org_id = ? AND name = ?", suite.OrgID, credentialName).Update("status", model.MakeJSONField(credential.Status)).Error).To(Succeed())
		Expect(handler.ReevaluateEnrollment(suite.Ctx, suite.OrgID, event(*request.Metadata.Name))).To(Succeed())
		assertRollback(*request.Metadata.Name)
	})
	It("When nested work rolls back it should not commit a device independently", func() {
		request := create()
		err := store.WithTransaction(suite.Ctx, suite.DB, func(ctx context.Context) error {
			return suite.DeviceStore.WithTransaction(ctx, func(ctx context.Context) error {
				_, err := suite.DeviceStore.Create(ctx, suite.OrgID, &domain.Device{Metadata: domain.ObjectMeta{Name: request.Metadata.Name}}, nil)
				if err != nil {
					return err
				}
				return errors.New("rollback")
			})
		})
		Expect(err).To(MatchError("rollback"))
		assertRollback(*request.Metadata.Name)
	})
})
