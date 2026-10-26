package v1beta1

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestCertificateSigningRequestIssuedAtRoundTrip(t *testing.T) {
	issuedAt := time.Date(2025, 3, 4, 5, 6, 7, 123456000, time.UTC)
	status := CertificateSigningRequestStatus{
		Certificate: lo.ToPtr([]byte("certificate")),
		Conditions:  []Condition{},
		IssuedAt:    &issuedAt,
	}

	encoded, err := json.Marshal(status)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"issuedAt":"2025-03-04T05:06:07.123456Z"`)

	var decoded CertificateSigningRequestStatus
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.NotNil(t, decoded.IssuedAt)
	require.True(t, decoded.IssuedAt.Equal(issuedAt))
	require.Equal(t, time.UTC, decoded.IssuedAt.Location())
}

func TestCertificateSigningRequestIssuedAtIsOptional(t *testing.T) {
	encoded, err := json.Marshal(CertificateSigningRequestStatus{Conditions: []Condition{}})
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "issuedAt")
}
