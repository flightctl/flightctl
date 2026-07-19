package certificatesigningrequest

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestCallbackCertificateSigningRequestUpdated(t *testing.T) {
	t.Run("When err is non-nil it should emit a failure event", func(t *testing.T) {
		h, _, _, ev, _ := newTestHandler(t)
		h.callbackCertificateSigningRequestUpdated(context.Background(), domain.CertificateSigningRequestKind, uuid.New(), "csr1", nil, nil, true, errors.New("boom"))
		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonResourceCreationFailed, ev.created[0].Reason)
	})

	t.Run("When created it should emit a resource-created event", func(t *testing.T) {
		h, _, _, ev, _ := newTestHandler(t)
		csr := &domain.CertificateSigningRequest{Metadata: domain.ObjectMeta{Name: lo.ToPtr("csr1")}}
		h.callbackCertificateSigningRequestUpdated(context.Background(), domain.CertificateSigningRequestKind, uuid.New(), "csr1", nil, csr, true, nil)
		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonResourceCreated, ev.created[0].Reason)
	})
}
