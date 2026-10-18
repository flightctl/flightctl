package service_test

import (
	"context"
	"database/sql"
	"encoding/pem"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type delayedConditionConnection struct {
	*sql.DB
	ready   chan struct{}
	release <-chan struct{}
}

func (connection *delayedConditionConnection) ExecContext(ctx context.Context, query string, arguments ...any) (sql.Result, error) {
	select {
	case connection.ready <- struct{}{}:
	default:
	}
	select {
	case <-connection.release:
		return connection.DB.ExecContext(ctx, query, arguments...)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

var _ = Describe("Condition updates across credential name reuse", func() {
	It("When an old condition write races recreation it should retain the new incarnation's issuance and lookup key", func() {
		suite := NewServiceTestSuite()
		suite.Setup()
		DeferCleanup(suite.Teardown)
		ctx, cancel := context.WithTimeout(suite.Ctx, 30*time.Second)
		defer cancel()
		createdAt := time.Date(2025, 3, 4, 5, 6, 7, 123456000, time.UTC)
		recreatedAt := time.Date(2025, 3, 4, 5, 6, 7, 123457000, time.UTC)
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB.Session(&gorm.Session{NowFunc: func() time.Time { return createdAt }}), suite.Log, "example.org/enrollment")
		request := CreateTestCSR()
		request.Spec.SignerName = "example.org/enrollment"
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		oldLeaf, err := suite.caClient.IssueRequestedClientCertificate(ctx, parsed, 3600)
		Expect(err).NotTo(HaveOccurred())
		newLeaf, err := suite.caClient.IssueRequestedClientCertificate(ctx, parsed, 3600)
		Expect(err).NotTo(HaveOccurred())
		Expect(newLeaf.Raw).NotTo(Equal(oldLeaf.Raw))
		original, err := provider.Create(ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		original.Status.Certificate = lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: oldLeaf.Raw}))
		original.Status.IssuedAt = &createdAt
		original, err = provider.UpdateStatus(ctx, suite.OrgID, original)
		Expect(err).NotTo(HaveOccurred())

		database, err := suite.DB.DB()
		Expect(err).NotTo(HaveOccurred())
		release := make(chan struct{})
		released := false
		defer func() {
			if !released {
				close(release)
			}
		}()
		connection := &delayedConditionConnection{DB: database, ready: make(chan struct{}, 1), release: release}
		conditionDB, err := gorm.Open(postgres.New(postgres.Config{Conn: connection}), &gorm.Config{SkipDefaultTransaction: true})
		Expect(err).NotTo(HaveOccurred())
		conditionProvider := csrstore.NewCertificateSigningRequestStore(conditionDB, suite.Log, "example.org/enrollment")
		result := make(chan error, 1)
		go func() {
			result <- conditionProvider.UpdateConditions(ctx, suite.OrgID, *original.Metadata.Name, []domain.Condition{{
				Type: domain.ConditionTypeCertificateSigningRequestDenied, Status: domain.ConditionStatusTrue, Reason: "Denied",
			}})
		}()
		select {
		case <-connection.ready:
		case <-ctx.Done():
			Fail("condition write did not reach the database before its deadline")
		}
		_, err = provider.Delete(ctx, suite.OrgID, *original.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		replacementProvider := csrstore.NewCertificateSigningRequestStore(suite.DB.Session(&gorm.Session{NowFunc: func() time.Time { return recreatedAt }}), suite.Log, "example.org/enrollment")
		replacement, err := replacementProvider.Create(ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		replacement.Status.Certificate = lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: newLeaf.Raw}))
		replacement.Status.IssuedAt = &recreatedAt
		replacement, err = replacementProvider.UpdateStatus(ctx, suite.OrgID, replacement)
		Expect(err).NotTo(HaveOccurred())
		Expect(replacement.Metadata.ResourceVersion).To(Equal(original.Metadata.ResourceVersion))
		close(release)
		released = true
		select {
		case err := <-result:
			Expect(err).NotTo(HaveOccurred())
		case <-ctx.Done():
			Fail("condition update did not finish before its deadline")
		}
		current, err := provider.Get(ctx, suite.OrgID, *replacement.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(current.Status.Certificate).To(Equal(replacement.Status.Certificate))
		Expect(current.Status.IssuedAt).To(Equal(&recreatedAt))
		Expect(current.Status.Conditions).To(ContainElement(HaveField("Reason", "Denied")))
		resolved, err := provider.FindNameByCertificate(ctx, suite.OrgID, newLeaf.Raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved).To(Equal(replacement.Metadata.Name))
		resolved, err = provider.FindNameByCertificate(ctx, suite.OrgID, oldLeaf.Raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved).To(BeNil())
	})
})
