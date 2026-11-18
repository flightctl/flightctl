package store_test

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"gorm.io/gorm"
)

var _ = Describe("CSR transaction ownership", func() {
	var suite *certificatePersistenceSuite
	var ctx context.Context
	var provider csrstore.Store
	var pool *labelSyncTransactionCounter
	var recorder *labelSyncQueryRecorder

	BeforeEach(func() {
		suite = newCertificatePersistenceSuite()
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(suite.Ctx, 30*time.Second)
		DeferCleanup(cancel)
		sqlDB, err := suite.DB.DB()
		Expect(err).NotTo(HaveOccurred())
		pool = &labelSyncTransactionCounter{DB: sqlDB}
		recorder = &labelSyncQueryRecorder{Interface: suite.DB.Logger}
		observedDB := suite.DB.Session(&gorm.Session{Context: ctx, Logger: recorder})
		observedDB.ConnPool, observedDB.Statement.ConnPool = pool, pool
		provider = csrstore.NewCertificateSigningRequestStore(observedDB, suite.Log, suite.cfg.CA.DeviceEnrollmentSignerName)
	})

	write := func(txCtx context.Context, method string, request *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, *domain.CertificateSigningRequest, bool, error) {
		if method == "Update" {
			updated, before, err := provider.Update(txCtx, suite.OrgID, request)
			return updated, before, false, err
		}
		return provider.CreateOrUpdate(txCtx, suite.OrgID, request)
	}

	for _, method := range []string{"Update", "CreateOrUpdate"} {
		for _, create := range []bool{false, true} {
			if method == "Update" && create {
				continue
			}
			path := "an existing CSR"
			if create {
				path = "a new CSR"
			}
			for _, ownership := range []string{"caller commit", "caller rollback", "standalone"} {
				It("When "+method+" writes "+path+" with "+ownership+" it should preserve transaction ownership", func() {
					request := newStoredCSR()
					var original *domain.CertificateSigningRequest
					if !create {
						var err error
						original, err = provider.Create(ctx, suite.OrgID, &request)
						Expect(err).NotTo(HaveOccurred())
						request = *original
					}
					request.Spec.Usages = lo.ToPtr([]string{"client auth"})
					request.Metadata.Labels = lo.ToPtr(map[string]string{"transaction": "changed"})
					pool.begins.Store(0)
					recorder.drain()
					rollback := errors.New("caller rollback")
					mutate := func(txCtx context.Context) error {
						updated, before, created, err := write(txCtx, method, &request)
						Expect(err).NotTo(HaveOccurred())
						Expect(created).To(Equal(create))
						Expect(updated.Spec).To(Equal(request.Spec))
						Expect(updated.Metadata.Labels).To(Equal(request.Metadata.Labels))
						if create {
							Expect(before).To(BeNil())
						} else {
							Expect(before.Spec).To(Equal(original.Spec))
							Expect(updated.Metadata.ResourceVersion).NotTo(Equal(original.Metadata.ResourceVersion))
						}
						inside, err := provider.Get(txCtx, suite.OrgID, *request.Metadata.Name)
						Expect(err).NotTo(HaveOccurred())
						Expect(inside.Spec).To(Equal(request.Spec))
						if ownership == "caller rollback" {
							return rollback
						}
						Expect(pool.begins.Load()).To(Equal(int64(1)), "the write must not start a second transaction")
						if ownership == "caller commit" && !create {
							var locked model.CertificateSigningRequest
							err := suite.DB.WithContext(ctx).Raw("SELECT * FROM certificate_signing_requests WHERE org_id = ? AND name = ? FOR UPDATE NOWAIT", suite.OrgID, *request.Metadata.Name).Scan(&locked).Error
							var pgErr *pgconn.PgError
							Expect(errors.As(err, &pgErr)).To(BeTrue(), "caller must still hold the CSR row lock")
							Expect(pgErr.Code).To(Equal("55P03"))
						}
						return nil
					}
					var err error
					if ownership == "standalone" {
						err = mutate(ctx)
					} else {
						err = provider.WithTransaction(ctx, mutate)
					}
					if ownership == "caller rollback" {
						Expect(err).To(MatchError(rollback))
					} else {
						Expect(err).NotTo(HaveOccurred())
					}
					outside, err := provider.Get(ctx, suite.OrgID, *request.Metadata.Name)
					if ownership == "caller rollback" && create {
						Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
					} else {
						Expect(err).NotTo(HaveOccurred())
						if ownership == "caller rollback" {
							Expect(outside).To(Equal(original))
						} else {
							Expect(outside.Spec).To(Equal(request.Spec))
							Expect(outside.Metadata.Labels).To(Equal(request.Metadata.Labels))
						}
					}
					Expect(pool.begins.Load()).To(Equal(int64(1)))
					queries := strings.Join(recorder.drain(), "\n")
					Expect(queries).To(ContainSubstring("FOR UPDATE"))
					Expect(queries).NotTo(ContainSubstring("SAVEPOINT"))
				})
			}
		}

		for _, retained := range []bool{false, true} {
			binding := "live"
			if retained {
				binding = "retained"
			}
			It("When "+method+" sees an uncommitted "+binding+" policy binding it should reject replacement in the caller transaction", func() {
				request := newStoredCSR()
				original, err := provider.Create(ctx, suite.OrgID, &request)
				Expect(err).NotTo(HaveOccurred())
				request = *original
				request.Metadata.Labels = lo.ToPtr(map[string]string{"transaction": "blocked"})
				pool.begins.Store(0)
				recorder.drain()
				rollback := errors.New("caller rollback")
				err = provider.WithTransaction(ctx, func(txCtx context.Context) error {
					policy := &model.EnrollmentApprovalPolicy{
						Resource:        model.Resource{OrgID: suite.OrgID, Name: uuid.NewString()},
						CertificateName: *request.Metadata.Name,
					}
					if !retained {
						policy.Spec = model.MakeJSONField(domain.EnrollmentApprovalPolicySpec{CertificateName: *request.Metadata.Name, MatchExpression: "true"})
					}
					Expect(store.DB(txCtx, suite.DB).Create(policy).Error).To(Succeed())
					updated, before, created, err := write(txCtx, method, &request)
					Expect(err).To(MatchError(flterrors.ErrEnrollmentCredentialRetained))
					Expect(updated).To(BeNil())
					Expect(before).To(BeNil())
					Expect(created).To(BeFalse())
					Expect(pool.begins.Load()).To(Equal(int64(1)))
					return rollback
				})
				Expect(err).To(MatchError(rollback))
				outside, err := provider.Get(ctx, suite.OrgID, *request.Metadata.Name)
				Expect(err).NotTo(HaveOccurred())
				Expect(outside).To(Equal(original))
				var count int64
				Expect(suite.DB.WithContext(ctx).Model(&model.EnrollmentApprovalPolicy{}).Count(&count).Error).To(Succeed())
				Expect(count).To(BeZero())
				queries := strings.Join(recorder.drain(), "\n")
				Expect(queries).To(ContainSubstring("FOR UPDATE"))
				Expect(queries).NotTo(ContainSubstring("SAVEPOINT"))
			})
		}
	}
})
