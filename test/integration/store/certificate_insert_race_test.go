package store_test

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	policystore "github.com/flightctl/flightctl/internal/store/enrollmentapprovalpolicy"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/jackc/pgx/v5/pgconn"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"gorm.io/gorm"
)

type csrInsertRaceContextKey struct{}

var _ = Describe("CSR missing-row concurrency", func() {
	for _, version := range []string{"1", "2", "invalid"} {
		It("When an insert race supplies version "+version+" it should preserve the version precondition", func() {
			suite := newCertificatePersistenceSuite()
			ctx, cancel := context.WithTimeout(suite.Ctx, 30*time.Second)
			defer cancel()
			provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.cfg.CA.DeviceEnrollmentSignerName)
			request := newStoredCSR()
			var fired atomic.Bool
			callbackName := "test:csr_version_race"
			Expect(suite.DB.Callback().Query().After("gorm:after_query").Register(callbackName, func(query *gorm.DB) {
				if query.Statement.Context.Value(csrInsertRaceContextKey{}) == nil || query.Statement.Table != "certificate_signing_requests" || !errors.Is(query.Error, gorm.ErrRecordNotFound) {
					return
				}
				if _, locked := query.Statement.Clauses["FOR"]; !locked || !fired.CompareAndSwap(false, true) {
					return
				}
				_, err := provider.Create(ctx, suite.OrgID, &request)
				Expect(err).NotTo(HaveOccurred())
			})).To(Succeed())
			DeferCleanup(func() { Expect(suite.DB.Callback().Query().Remove(callbackName)).To(Succeed()) })
			candidate := request
			candidate.Metadata.ResourceVersion = lo.ToPtr(version)
			candidate.Spec.SignerName = "replacement-signer"
			marked := context.WithValue(ctx, csrInsertRaceContextKey{}, true)
			Expect(provider.WithTransaction(marked, func(txCtx context.Context) error {
				updated, before, created, err := provider.CreateOrUpdate(txCtx, suite.OrgID, &candidate)
				Expect(fired.Load()).To(BeTrue())
				Expect(created).To(BeFalse())
				switch version {
				case "1":
					Expect(err).NotTo(HaveOccurred())
					Expect(before.Spec).To(Equal(request.Spec))
					Expect(updated.Metadata.ResourceVersion).To(Equal(lo.ToPtr("2")))
				case "2":
					Expect(err).To(MatchError(flterrors.ErrResourceVersionConflict))
					Expect(updated).To(BeNil())
				default:
					Expect(err).To(MatchError(flterrors.ErrIllegalResourceVersionFormat))
					Expect(updated).To(BeNil())
				}
				var probe int
				return store.DB(txCtx, suite.DB).Raw("SELECT 1").Scan(&probe).Error
			})).To(Succeed())
		})
	}
	for _, method := range []string{"Update", "Delete", "CreateOrUpdate", "insert conflict"} {
		for _, bound := range []bool{false, true} {
			binding := "unbound"
			if bound {
				binding = "bound"
			}
			for _, ownership := range []string{"standalone", "caller commit", "caller rollback"} {
				It("When "+method+" races with a "+binding+" insert using "+ownership+" it should preserve guards and transaction ownership", func() {
					suite := newCertificatePersistenceSuite()
					ctx, cancel := context.WithTimeout(suite.Ctx, 30*time.Second)
					defer cancel()
					sqlDB, err := suite.DB.DB()
					Expect(err).NotTo(HaveOccurred())
					pool := &labelSyncTransactionCounter{DB: sqlDB}
					observedDB := suite.DB.Session(&gorm.Session{Context: ctx})
					observedDB.ConnPool, observedDB.Statement.ConnPool = pool, pool
					provider := csrstore.NewCertificateSigningRequestStore(observedDB, suite.Log, suite.cfg.CA.DeviceEnrollmentSignerName)
					other := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.cfg.CA.DeviceEnrollmentSignerName)
					request := newStoredCSR()
					name := *request.Metadata.Name
					var original *domain.CertificateSigningRequest
					var fired atomic.Bool
					insert := func(query *gorm.DB) {
						if query.Statement.Context.Value(csrInsertRaceContextKey{}) == nil || query.Statement.Table != "certificate_signing_requests" {
							return
						}
						if method != "insert conflict" {
							if _, locked := query.Statement.Clauses["FOR"]; !locked || !errors.Is(query.Error, gorm.ErrRecordNotFound) {
								return
							}
						}
						if !fired.CompareAndSwap(false, true) {
							return
						}
						Expect(other.WithTransaction(ctx, func(otherCtx context.Context) error {
							var createErr error
							original, createErr = other.Create(otherCtx, suite.OrgID, &request)
							if createErr != nil || !bound {
								return createErr
							}
							_, _, createErr = mutatePolicy(otherCtx, policystore.NewStore(suite.DB, suite.Log), suite.OrgID, "bound", name, func(_ *domain.EnrollmentApprovalPolicy, _ *domain.CertificateSigningRequest, _ []domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
								return &domain.EnrollmentApprovalPolicy{Metadata: domain.ObjectMeta{Name: lo.ToPtr("bound")}, Spec: domain.EnrollmentApprovalPolicySpec{CertificateName: name, MatchExpression: "true"}}, nil
							})
							return createErr
						})).To(Succeed())
					}
					callbackName := "test:csr_insert_race"
					if method == "insert conflict" {
						Expect(suite.DB.Callback().Create().Before("gorm:create").Register(callbackName, insert)).To(Succeed())
						DeferCleanup(func() { Expect(suite.DB.Callback().Create().Remove(callbackName)).To(Succeed()) })
					} else {
						Expect(suite.DB.Callback().Query().After("gorm:after_query").Register(callbackName, insert)).To(Succeed())
						DeferCleanup(func() { Expect(suite.DB.Callback().Query().Remove(callbackName)).To(Succeed()) })
					}
					candidate := request
					candidate.Spec.SignerName = "replacement-signer"
					rollback := errors.New("caller rollback")
					mutate := func(txCtx context.Context) error {
						if method == "Delete" {
							deleted, deleteErr := provider.Delete(txCtx, suite.OrgID, name)
							Expect(deleteErr).NotTo(HaveOccurred())
							Expect(deleted).To(BeFalse())
						} else if method == "Update" {
							updated, before, updateErr := provider.Update(txCtx, suite.OrgID, &candidate)
							Expect(updateErr).To(MatchError(flterrors.ErrResourceNotFound))
							Expect(updated).To(BeNil())
							Expect(before).To(BeNil())
						} else {
							updated, before, created, updateErr := provider.CreateOrUpdate(txCtx, suite.OrgID, &candidate)
							Expect(created).To(BeFalse())
							if bound {
								Expect(updateErr).To(MatchError(flterrors.ErrEnrollmentCredentialRetained))
								Expect(updated).To(BeNil())
								Expect(before).To(BeNil())
							} else {
								Expect(updateErr).NotTo(HaveOccurred())
								Expect(before.Spec).To(Equal(original.Spec))
								Expect(updated.Spec).To(Equal(candidate.Spec))
								Expect(updated.Metadata.ResourceVersion).To(Equal(lo.ToPtr("2")))
							}
							if ownership != "standalone" {
								var locked model.CertificateSigningRequest
								lockErr := suite.DB.WithContext(ctx).Raw("SELECT * FROM certificate_signing_requests WHERE org_id = ? AND name = ? FOR UPDATE NOWAIT", suite.OrgID, name).Scan(&locked).Error
								var pgErr *pgconn.PgError
								Expect(errors.As(lockErr, &pgErr)).To(BeTrue())
								Expect(pgErr.Code).To(Equal("55P03"))
							}
						}
						Expect(fired.Load()).To(BeTrue())
						Expect(pool.begins.Load()).To(Equal(int64(1)))
						var probe int
						Expect(store.DB(txCtx, suite.DB).Raw("SELECT 1").Scan(&probe).Error).To(Succeed())
						if ownership == "caller rollback" {
							return rollback
						}
						return nil
					}
					marked := context.WithValue(ctx, csrInsertRaceContextKey{}, true)
					if ownership == "standalone" {
						err = mutate(marked)
					} else {
						err = provider.WithTransaction(marked, mutate)
					}
					if ownership == "caller rollback" {
						Expect(err).To(MatchError(rollback))
					} else {
						Expect(err).NotTo(HaveOccurred())
					}
					outside, err := provider.Get(ctx, suite.OrgID, name)
					Expect(err).NotTo(HaveOccurred())
					if !bound && (method == "CreateOrUpdate" || method == "insert conflict") && ownership != "caller rollback" {
						Expect(outside.Spec).To(Equal(candidate.Spec))
					} else {
						Expect(outside.Spec).To(Equal(original.Spec))
					}
					var count int64
					Expect(suite.DB.Unscoped().Model(&model.EnrollmentApprovalPolicy{}).Where("certificate_name = ?", name).Count(&count).Error).To(Succeed())
					Expect(count == 1).To(Equal(bound))
				})
			}
		}
	}
})
