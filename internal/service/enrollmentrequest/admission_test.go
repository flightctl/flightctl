package enrollmentrequest

import (
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestPolicyAdmissionGates(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		name   string
		limit  *int64
		count  int64
		window *int32
		issued *time.Time
		tpm    bool
		reason string
	}{
		{name: "When unlimited it should permit counting", count: 12},
		{name: "When the limit is zero it should not pass", limit: lo.ToPtr(int64(0)), reason: "PolicyApprovalLimitReached"},
		{name: "When the count reaches the limit it should not pass", limit: lo.ToPtr(int64(2)), count: 2, reason: "PolicyApprovalLimitReached"},
		{name: "When issuance is unknown it should not pass", window: lo.ToPtr(int32(1)), reason: "CredentialIssuanceTimeUnavailable"},
		{name: "When issuance is future it should not pass", window: lo.ToPtr(int32(1)), issued: lo.ToPtr(now.Add(time.Minute)), reason: "CredentialIssuanceTimeUnavailable"},
		{name: "When the window expires exactly now it should not pass", window: lo.ToPtr(int32(1)), issued: lo.ToPtr(now.AddDate(0, 0, -1)), reason: "PolicyApprovalWindowExpired"},
		{name: "When TPM evidence is absent it should not pass", tpm: true, reason: "TPMVerificationRequired"},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := &domain.EnrollmentApprovalPolicy{Spec: domain.EnrollmentApprovalPolicySpec{Enabled: true, ApprovalLimit: test.limit, AutoApprovalWindowDays: test.window, RequireTPMVerification: test.tpm}, Status: &domain.EnrollmentApprovalPolicyStatus{EnrollmentCount: test.count}}
			credential := &domain.CertificateSigningRequest{Status: &domain.CertificateSigningRequestStatus{IssuedAt: test.issued}}
			request := &domain.EnrollmentRequest{Status: &domain.EnrollmentRequestStatus{}}
			require.Equal(t, test.reason, policyAdmissionGate(policy, credential, request, now))
		})
	}
}
