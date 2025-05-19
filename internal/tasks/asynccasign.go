package tasks

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

func setEreqFailed(enrollmentRequest *api.EnrollmentRequest, err error) {
	condition := api.Condition{
		Type:    api.CertificateSigningRequestFailed,
		Status:  api.ConditionStatusFalse,
		Reason:  "SigningFailed",
		Message: fmt.Sprintf("Signing failed %s", err.Error()),
	}
	api.SetStatusCondition(&enrollmentRequest.Status.Conditions, condition)
}

func setCSRFailed(enrollmentRequest *api.CertificateSigningRequest, err error) {
	condition := api.Condition{
		Type:    api.CertificateSigningRequestFailed,
		Status:  api.ConditionStatusFalse,
		Reason:  "SigningFailed",
		Message: fmt.Sprintf("Signing failed %s", err.Error()),
	}
	api.SetStatusCondition(&enrollmentRequest.Status.Conditions, condition)
}

var caMutex sync.Mutex
var rerun atomic.Bool

func asyncCASign(ctx context.Context, resourceRef *tasks_client.ResourceReference, serviceHandler *service.ServiceHandler, ca *crypto.CAClient, log logrus.FieldLogger) error {
	rerun.Store(true)
	if !caMutex.TryLock() {
		return nil
	}
	defer caMutex.Unlock()

	if ca == nil {
		return errors.New("CA not initialized for async processing")
	}

	for rerun.Swap(false) {

		elist_params := api.ListEnrollmentRequestsParams{
			FieldSelector: lo.ToPtr("!status.certificate"),
		}
		ereqs, status := serviceHandler.ListEnrollmentRequests(ctx, elist_params)
		if status.Code != http.StatusOK {
			return errors.New("Failed to list enrollment requests")
		}
		for _, ereq := range ereqs.Items {
			if ereq.Status != nil && ereq.Status.Certificate == nil && ereq.Status.Conditions != nil && api.IsStatusConditionTrue(ereq.Status.Conditions, api.EnrollmentRequestApproved) {
				raw := []byte(ereq.Spec.Csr)
				csr, err := crypto.ParseCSR(raw)
				if err != nil {
					setEreqFailed(&ereq, err)
					serviceHandler.ReplaceEnrollmentRequestStatus(ctx, *ereq.Metadata.Name, ereq)
					continue
				}
				cert, err := ca.IssueRequestedClientCertificate(csr, ca.Cfg.EnrollmentValidityDays*24*3600)
				if err != nil {
					setEreqFailed(&ereq, err)
					serviceHandler.ReplaceEnrollmentRequestStatus(ctx, *ereq.Metadata.Name, ereq)
					continue

				}
				signed := string(cert)
				ereq.Status.Certificate = &signed
				serviceHandler.ReplaceEnrollmentRequestStatus(ctx, *ereq.Metadata.Name, ereq)
			}
		}
		clist_params := api.ListCertificateSigningRequestsParams{
			FieldSelector: lo.ToPtr("!status.certificate"),
		}
		csreqs, status := serviceHandler.ListCertificateSigningRequests(ctx, clist_params)
		if status.Code != http.StatusOK {
			return errors.New("Failed to list certificate signing requests")
		}
		for _, csreq := range csreqs.Items {
			if csreq.Status != nil && csreq.Status.Certificate == nil && csreq.Status.Conditions != nil && api.IsStatusConditionTrue(csreq.Status.Conditions, api.CertificateSigningRequestApproved) {
				csr, err := crypto.ParseCSR(csreq.Spec.Request)
				if err != nil {
					setCSRFailed(&csreq, err)
					serviceHandler.ReplaceCertificateSigningRequest(ctx, *csreq.Metadata.Name, csreq)
					continue
				}
				cert, err := ca.IssueRequestedClientCertificate(csr, ca.Cfg.ClientBootstrapValidityDays*24*3600)
				if err != nil {
					setCSRFailed(&csreq, err)
					serviceHandler.ReplaceCertificateSigningRequest(ctx, *csreq.Metadata.Name, csreq)
					continue

				}
				csreq.Status.Certificate = &cert
				serviceHandler.ReplaceCertificateSigningRequest(ctx, *csreq.Metadata.Name, csreq)
			}
		}
	}
	return nil
}
