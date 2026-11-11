package tasks

import (
	"context"
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/internal/domain"
	certificatesigningrequestservice "github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	enrollmentrequestservice "github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

func shouldProcessEnrollmentCredentialRevocation(event domain.Event) bool {
	return event.InvolvedObject.Kind == domain.CertificateSigningRequestKind &&
		event.Reason == domain.EventReasonEnrollmentCredentialRevoked
}

func processEnrollmentCredentialRevocation(ctx context.Context, orgId uuid.UUID, event domain.Event, deviceSvc deviceservice.Service, enrollmentRequestSvc enrollmentrequestservice.Service, csrSvc certificatesigningrequestservice.Service, log logrus.FieldLogger) error {
	credentialName := event.InvolvedObject.Name
	if credentialName == "" {
		return fmt.Errorf("enrollment credential revocation event is missing the credential name")
	}
	if enrollmentRequestSvc == nil {
		return fmt.Errorf("enrollment request service is not configured")
	}
	if deviceSvc == nil {
		return fmt.Errorf("device service is not configured")
	}
	if csrSvc == nil {
		return fmt.Errorf("certificate signing request service is not configured")
	}

	if err := rejectPendingEnrollmentRequests(ctx, orgId, credentialName, enrollmentRequestSvc, log); err != nil {
		return fmt.Errorf("reject pending enrollment requests for credential %q: %w", credentialName, err)
	}
	if err := flagDevicesForRevokedCredential(ctx, orgId, credentialName, deviceSvc, log); err != nil {
		return fmt.Errorf("flag devices for revoked credential %q: %w", credentialName, err)
	}
	if _, status := csrSvc.MarkEnrollmentCredentialFlaggingCompleted(ctx, orgId, credentialName); status.Code != http.StatusOK {
		return fmt.Errorf("mark flagging completed for credential %q: %s", credentialName, status.Message)
	}
	return nil
}

func rejectPendingEnrollmentRequests(ctx context.Context, orgId uuid.UUID, credentialName string, svc enrollmentrequestservice.Service, log logrus.FieldLogger) error {
	count, status := svc.DenyEnrollmentRequestsForRevokedCredential(ctx, orgId, credentialName)
	if status.Code != http.StatusOK {
		return fmt.Errorf("deny enrollment requests: %s", status.Message)
	}
	log.Infof("Denied %d pending enrollment request(s) for revoked enrollment credential %q", count, credentialName)
	return nil
}

func flagDevicesForRevokedCredential(ctx context.Context, orgId uuid.UUID, credentialName string, svc deviceservice.Service, log logrus.FieldLogger) error {
	count, status := svc.RevokeDevicesForEnrollmentCredential(ctx, orgId, credentialName)
	if status.Code != http.StatusOK {
		return fmt.Errorf("flag devices: %s", status.Message)
	}
	log.Infof("Flagged %d device(s) for revoked enrollment credential %q", count, credentialName)
	return nil
}
