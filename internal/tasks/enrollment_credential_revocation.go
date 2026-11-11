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
	"github.com/samber/lo"
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
	requests, err := listEnrollmentRequestsByCredential(ctx, orgId, credentialName, svc)
	if err != nil {
		return err
	}
	for i := range requests {
		request := requests[i]
		if enrollmentRequestAlreadyDecided(request) || enrollmentRequestAlreadyDenied(request) {
			continue
		}
		name := lo.FromPtr(request.Metadata.Name)
		if name == "" {
			continue
		}
		updated := request
		if updated.Status == nil {
			updated.Status = &domain.EnrollmentRequestStatus{}
		}
		domain.SetStatusCondition(&updated.Status.Conditions, domain.Condition{
			Type:    domain.ConditionTypeEnrollmentRequestDenied,
			Status:  domain.ConditionStatusTrue,
			Reason:  domain.EnrollmentCredentialRevokedReason,
			Message: fmt.Sprintf("Enrollment credential %q was revoked; enrollment request rejected", credentialName),
		})
		if _, status := svc.ReplaceEnrollmentRequestStatus(ctx, orgId, name, updated); status.Code != http.StatusOK {
			return fmt.Errorf("update enrollment request %q: %s", name, status.Message)
		}
		log.Infof("Rejected pending enrollment request %q for revoked enrollment credential %q", name, credentialName)
	}
	return nil
}

func enrollmentRequestAlreadyDecided(request domain.EnrollmentRequest) bool {
	if request.Status != nil && domain.IsStatusConditionTrue(request.Status.Conditions, domain.ConditionTypeEnrollmentRequestApproved) {
		return true
	}
	return request.Status != nil && request.Status.Approval != nil && request.Status.Approval.Approved
}

func enrollmentRequestAlreadyDenied(request domain.EnrollmentRequest) bool {
	if request.Status == nil {
		return false
	}
	condition := domain.FindStatusCondition(request.Status.Conditions, domain.ConditionTypeEnrollmentRequestDenied)
	return condition != nil && condition.Status == domain.ConditionStatusTrue
}

func listEnrollmentRequestsByCredential(ctx context.Context, orgId uuid.UUID, credentialName string, svc enrollmentrequestservice.Service) ([]domain.EnrollmentRequest, error) {
	fieldSelector := fmt.Sprintf("status.enrollmentCertificateName=%s", credentialName)
	params := domain.ListEnrollmentRequestsParams{FieldSelector: &fieldSelector}
	var requests []domain.EnrollmentRequest
	for {
		list, status := svc.ListEnrollmentRequests(ctx, orgId, params)
		if status.Code != http.StatusOK {
			return nil, fmt.Errorf("list enrollment requests: %s", status.Message)
		}
		if list == nil {
			return nil, fmt.Errorf("list enrollment requests: empty response")
		}
		requests = append(requests, list.Items...)
		if list.Metadata.Continue == nil || *list.Metadata.Continue == "" {
			return requests, nil
		}
		params.Continue = list.Metadata.Continue
	}
}

func flagDevicesForRevokedCredential(ctx context.Context, orgId uuid.UUID, credentialName string, svc deviceservice.Service, log logrus.FieldLogger) error {
	names, err := listDeviceNamesByCredential(ctx, orgId, credentialName, svc)
	if err != nil {
		return err
	}
	for _, name := range names {
		if status := svc.RevokeDeviceEnrollmentCredential(ctx, orgId, name); status.Code != http.StatusOK {
			return fmt.Errorf("flag device %q: %s", name, status.Message)
		}
		log.Infof("Flagged device %q for revoked enrollment credential %q", name, credentialName)
	}
	return nil
}

func listDeviceNamesByCredential(ctx context.Context, orgId uuid.UUID, credentialName string, svc deviceservice.Service) ([]string, error) {
	fieldSelector := fmt.Sprintf("status.enrollmentCredential.certificateName=%s", credentialName)
	params := domain.ListDevicesParams{FieldSelector: &fieldSelector}
	var names []string
	for {
		list, status := svc.ListDevices(ctx, orgId, params, nil)
		if status.Code != http.StatusOK {
			return nil, fmt.Errorf("list devices: %s", status.Message)
		}
		if list == nil {
			return nil, fmt.Errorf("list devices: empty response")
		}
		for i := range list.Items {
			if name := lo.FromPtr(list.Items[i].Metadata.Name); name != "" {
				names = append(names, name)
			}
		}
		if list.Metadata.Continue == nil || *list.Metadata.Continue == "" {
			return names, nil
		}
		params.Continue = list.Metadata.Continue
	}
}
