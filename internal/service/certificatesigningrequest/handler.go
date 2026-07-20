package certificatesigningrequest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/contextutil"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/service/events"
	certificatesigningrequeststore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	enrollmentrequeststore "github.com/flightctl/flightctl/internal/store/enrollmentrequest"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// nowFunc allows overriding for unit tests
var nowFunc = time.Now

type ServiceHandler struct {
	store                  certificatesigningrequeststore.Store
	enrollmentRequestStore enrollmentrequeststore.Store
	ca                     *crypto.CAClient
	events                 events.Service
	log                    logrus.FieldLogger
	agentEndpoint          string
	uiUrl                  string
}

// NewServiceHandler creates a new certificatesigningrequest ServiceHandler instance.
// agentEndpoint/uiUrl are only used by GenerateEnrollmentCredential (they're embedded in the
// returned crypto.EnrollmentCredential); pass "" if a caller never needs enrollment credentials.
func NewServiceHandler(store certificatesigningrequeststore.Store, enrollmentRequestStore enrollmentrequeststore.Store, ca *crypto.CAClient, events events.Service, log logrus.FieldLogger, agentEndpoint string, uiUrl string) *ServiceHandler {
	return &ServiceHandler{store: store, enrollmentRequestStore: enrollmentRequestStore, ca: ca, events: events, log: log, agentEndpoint: agentEndpoint, uiUrl: uiUrl}
}

var _ Service = (*ServiceHandler)(nil)

// SanitizeCertificateSigningRequest clears status and managed metadata from an untrusted CSR
// document (HTTP body). Callers that must set Owner (e.g. the agent creating its own device CSR)
// must not use this.
func SanitizeCertificateSigningRequest(csr *domain.CertificateSigningRequest) {
	if csr == nil {
		return
	}
	csr.Status = nil
	common.NilOutManagedObjectMetaProperties(&csr.Metadata)
}

// CreateCertificateSigningRequestFromUntrusted sanitizes an untrusted CSR document, then creates it.
func CreateCertificateSigningRequestFromUntrusted(ctx context.Context, svc Service, orgId uuid.UUID, csr domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, domain.Status) {
	SanitizeCertificateSigningRequest(&csr)
	return svc.CreateCertificateSigningRequest(ctx, orgId, csr)
}

// ReplaceCertificateSigningRequestFromUntrusted sanitizes an untrusted CSR document, then replaces it.
func ReplaceCertificateSigningRequestFromUntrusted(ctx context.Context, svc Service, orgId uuid.UUID, name string, csr domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, domain.Status) {
	SanitizeCertificateSigningRequest(&csr)
	return svc.ReplaceCertificateSigningRequest(ctx, orgId, name, csr)
}

func (h *ServiceHandler) autoApprove(ctx context.Context, orgId uuid.UUID, csr *domain.CertificateSigningRequest) {
	if domain.IsStatusConditionTrue(csr.Status.Conditions, domain.ConditionTypeCertificateSigningRequestApproved) || domain.IsStatusConditionTrue(csr.Status.Conditions, domain.ConditionTypeCertificateSigningRequestDenied) {
		return
	}

	domain.SetStatusCondition(&csr.Status.Conditions, domain.Condition{
		Type:    domain.ConditionTypeCertificateSigningRequestApproved,
		Status:  domain.ConditionStatusTrue,
		Reason:  "Approved",
		Message: "Auto-approved by enrollment signer",
	})
	domain.RemoveStatusCondition(&csr.Status.Conditions, domain.ConditionTypeCertificateSigningRequestFailed)

	if _, err := h.store.UpdateStatus(ctx, orgId, csr); err != nil {
		h.log.WithError(err).Error("failed to set approval condition")
	}
}

func (h *ServiceHandler) signApprovedCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, csr *domain.CertificateSigningRequest) {
	if csr.Status.Certificate != nil && len(*csr.Status.Certificate) > 0 {
		return
	}

	request, _, err := newSignRequestFromCertificateSigningRequest(csr)
	if err != nil {
		h.setCSRFailedCondition(ctx, orgId, csr, "SigningFailed", fmt.Sprintf("Failed to sign certificate: %v", err))
		return
	}

	certPEM, err := signer.SignAsPEM(ctx, h.ca, request)
	if err != nil {
		h.setCSRFailedCondition(ctx, orgId, csr, "SigningFailed", fmt.Sprintf("Failed to sign certificate: %v", err))
		return
	}

	csr.Status.Certificate = &certPEM
	if _, err := h.store.UpdateStatus(ctx, orgId, csr); err != nil {
		h.log.WithError(err).Error("failed to set signed certificate")
	}
}

func (h *ServiceHandler) ListCertificateSigningRequests(ctx context.Context, orgId uuid.UUID, params domain.ListCertificateSigningRequestsParams) (*domain.CertificateSigningRequestList, domain.Status) {
	listParams, status := common.PrepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != domain.StatusOK() {
		return nil, status
	}

	result, err := h.store.List(ctx, orgId, *listParams)
	if err == nil {
		return result, domain.StatusOK()
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return nil, domain.StatusBadRequest(se.Error())
	default:
		return nil, domain.StatusInternalServerError(err.Error())
	}
}

func (h *ServiceHandler) verifyTPMCSRRequest(ctx context.Context, orgId uuid.UUID, csr *domain.CertificateSigningRequest) error {
	if csr.Status == nil {
		csr.Status = &domain.CertificateSigningRequestStatus{}
	}
	csrBytes, isTPM := tpm.ParseTCGCSRBytes(string(csr.Spec.Request))
	if !isTPM {
		return fmt.Errorf("parsing TCG CSR")
	}

	// setTPMVerifiedFalse takes an already-formatted message rather than a format string + args
	// so that `go vet`'s printf check does not flag call sites passing a non-constant message
	// (e.g. notTPMBasedMessage below) as a "non-constant format string" error.
	setTPMVerifiedFalse := func(message string) {
		domain.SetStatusCondition(&csr.Status.Conditions, domain.Condition{
			Message: message,
			Reason:  domain.TPMVerificationFailedReason,
			Status:  domain.ConditionStatusFalse,
			Type:    domain.ConditionTypeCertificateSigningRequestTPMVerified,
		})
	}

	kind, owner, err := util.GetResourceOwner(csr.Metadata.Owner)
	if err != nil {
		setTPMVerifiedFalse("Failed to determine resource owner")
		return nil
	}
	if kind != domain.DeviceKind {
		setTPMVerifiedFalse(fmt.Sprintf("The CSR's owner is not a %s", domain.DeviceKind))
		return nil
	}
	// TODO this should be retrieved from the device rather than from the ER
	er, err := h.enrollmentRequestStore.Get(ctx, orgId, owner)
	if err != nil {
		setTPMVerifiedFalse(fmt.Sprintf("Unable to find CSR's owner: %s/%s", orgId, owner))
		return nil
	}

	notTPMBasedMessage := fmt.Sprintf("The CSR's owner %s is not TPM based.", lo.FromPtr(csr.Metadata.Owner))
	if er.Status == nil || !domain.IsStatusConditionTrue(er.Status.Conditions, domain.ConditionTypeEnrollmentRequestTPMVerified) {
		setTPMVerifiedFalse(notTPMBasedMessage)
		return nil
	}

	erBytes, isTPM := tpm.ParseTCGCSRBytes(er.Spec.Csr)
	if !isTPM {
		setTPMVerifiedFalse(notTPMBasedMessage)
		return nil
	}

	parsed, err := tpm.ParseTCGCSR(erBytes)
	if err != nil {
		setTPMVerifiedFalse(notTPMBasedMessage)
		return nil
	}

	if err = tpm.VerifyTCGCSRSigningChain(csrBytes, parsed.CSRContents.Payload.AttestPub); err != nil {
		setTPMVerifiedFalse(err.Error())
		return nil
	}
	domain.SetStatusCondition(&csr.Status.Conditions, domain.Condition{
		Message: "TPM chain of trust verified",
		Reason:  "TPMVerificationSucceeded",
		Status:  domain.ConditionStatusTrue,
		Type:    domain.ConditionTypeCertificateSigningRequestTPMVerified,
	})

	return nil
}

func (h *ServiceHandler) CreateCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, csr domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, domain.Status) {
	// Support legacy shorthand "enrollment" by replacing it with the configured signer name
	if csr.Spec.SignerName == "enrollment" {
		csr.Spec.SignerName = h.ca.Cfg.DeviceEnrollmentSignerName
	}

	if errs := csr.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	if err := h.validateAllowedSignersForCSRService(&csr); err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}

	request, isTPM, err := newSignRequestFromCertificateSigningRequest(&csr)
	if err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}

	if err := signer.Verify(ctx, h.ca, request); err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}
	if isTPM {
		if err = h.verifyTPMCSRRequest(ctx, orgId, &csr); err != nil {
			return nil, domain.StatusBadRequest(err.Error())
		}
	}

	result, err := h.store.Create(ctx, orgId, &csr, h.callbackCertificateSigningRequestUpdated)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, true, domain.CertificateSigningRequestKind, csr.Metadata.Name)
	}

	if result.Status == nil {
		result.Status = &domain.CertificateSigningRequestStatus{}
	}

	if result.Spec.SignerName == h.ca.Cfg.DeviceEnrollmentSignerName {
		h.autoApprove(ctx, orgId, result)
	}

	if domain.IsStatusConditionTrue(result.Status.Conditions, domain.ConditionTypeCertificateSigningRequestApproved) {
		h.signApprovedCertificateSigningRequest(ctx, orgId, result)
	}

	return result, domain.StatusCreated()
}

func (h *ServiceHandler) DeleteCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string) domain.Status {
	err := h.store.Delete(ctx, orgId, name, h.callbackCertificateSigningRequestDeleted)
	return common.StoreErrorToApiStatus(err, false, domain.CertificateSigningRequestKind, &name)
}

func (h *ServiceHandler) GetCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string) (*domain.CertificateSigningRequest, domain.Status) {
	result, err := h.store.Get(ctx, orgId, name)
	return result, common.StoreErrorToApiStatus(err, false, domain.CertificateSigningRequestKind, &name)
}

func (h *ServiceHandler) PatchCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.CertificateSigningRequest, domain.Status) {
	currentObj, err := h.store.Get(ctx, orgId, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.CertificateSigningRequestKind, &name)
	}

	newObj := &domain.CertificateSigningRequest{}
	err = common.ApplyJSONPatch(ctx, currentObj, newObj, patch, "/certificatesigningrequests/"+name)
	if err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}

	if errs := currentObj.ValidateUpdate(newObj); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	common.NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	// Support legacy shorthand "enrollment" by replacing it with the configured signer name
	if newObj.Spec.SignerName == "enrollment" {
		newObj.Spec.SignerName = h.ca.Cfg.DeviceEnrollmentSignerName
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	if err := h.validateAllowedSignersForCSRService(newObj); err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}

	request, isTPM, err := newSignRequestFromCertificateSigningRequest(newObj)
	if err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}

	if err := signer.Verify(ctx, h.ca, request); err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}
	if isTPM {
		if err = h.verifyTPMCSRRequest(ctx, orgId, newObj); err != nil {
			return nil, domain.StatusBadRequest(err.Error())
		}
	}

	result, err := h.store.Update(ctx, orgId, newObj, h.callbackCertificateSigningRequestUpdated)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.CertificateSigningRequestKind, &name)
	}

	if result.Spec.SignerName == h.ca.Cfg.DeviceEnrollmentSignerName {
		h.autoApprove(ctx, orgId, result)
	}
	if domain.IsStatusConditionTrue(result.Status.Conditions, domain.ConditionTypeCertificateSigningRequestApproved) {
		h.signApprovedCertificateSigningRequest(ctx, orgId, result)
	}

	return result, domain.StatusOK()
}

func (h *ServiceHandler) ReplaceCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string, csr domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, domain.Status) {
	if name != *csr.Metadata.Name {
		return nil, domain.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	// Support legacy shorthand "enrollment" by replacing it with the configured signer name
	if csr.Spec.SignerName == "enrollment" {
		csr.Spec.SignerName = h.ca.Cfg.DeviceEnrollmentSignerName
	}

	if errs := csr.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	if err := h.validateAllowedSignersForCSRService(&csr); err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}

	request, isTPM, err := newSignRequestFromCertificateSigningRequest(&csr)
	if err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}

	if err := signer.Verify(ctx, h.ca, request); err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}

	if isTPM {
		if err = h.verifyTPMCSRRequest(ctx, orgId, &csr); err != nil {
			return nil, domain.StatusBadRequest(err.Error())
		}
	}

	result, created, err := h.store.CreateOrUpdate(ctx, orgId, &csr, h.callbackCertificateSigningRequestUpdated)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, created, domain.CertificateSigningRequestKind, &name)
	}

	if result.Spec.SignerName == h.ca.Cfg.DeviceEnrollmentSignerName {
		h.autoApprove(ctx, orgId, result)
	}
	if domain.IsStatusConditionTrue(result.Status.Conditions, domain.ConditionTypeCertificateSigningRequestApproved) {
		h.signApprovedCertificateSigningRequest(ctx, orgId, result)
	}

	return result, common.StoreErrorToApiStatus(nil, created, domain.CertificateSigningRequestKind, &name)
}

// NOTE: Approval currently also issues a certificate - this will change in the future based on policy
func (h *ServiceHandler) UpdateCertificateSigningRequestApproval(ctx context.Context, orgId uuid.UUID, name string, csr domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, domain.Status) {
	newCSR := &csr
	common.NilOutManagedObjectMetaProperties(&newCSR.Metadata)
	if errs := newCSR.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *newCSR.Metadata.Name {
		return nil, domain.StatusBadRequest("resource name specified in metadata does not match name in path")
	}
	if newCSR.Status == nil {
		return nil, domain.StatusBadRequest("status is required")
	}
	allowedConditionTypes := []domain.ConditionType{
		domain.ConditionTypeCertificateSigningRequestApproved,
		domain.ConditionTypeCertificateSigningRequestDenied,
		domain.ConditionTypeCertificateSigningRequestFailed,
		domain.ConditionTypeCertificateSigningRequestTPMVerified,
	}
	// manual approving of TPMVerified false is allowed
	trueConditions := []domain.ConditionType{
		domain.ConditionTypeCertificateSigningRequestApproved,
		domain.ConditionTypeCertificateSigningRequestDenied,
		domain.ConditionTypeCertificateSigningRequestFailed,
	}
	exclusiveConditions := []domain.ConditionType{domain.ConditionTypeCertificateSigningRequestApproved, domain.ConditionTypeCertificateSigningRequestDenied}
	errs := domain.ValidateConditions(newCSR.Status.Conditions, allowedConditionTypes, trueConditions, exclusiveConditions)
	if len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	oldCSR, err := h.store.Get(ctx, orgId, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.CertificateSigningRequestKind, &name)
	}

	if err := checkServerSvcApprovalPrivilege(ctx, oldCSR.Spec.SignerName, h.ca.Cfg.ServerSvcSignerName); err != nil {
		return nil, domain.StatusForbidden(err.Error())
	}

	// do not approve a denied request, or recreate a cert for an already-approved request
	if domain.IsStatusConditionTrue(oldCSR.Status.Conditions, domain.ConditionTypeCertificateSigningRequestDenied) {
		return nil, domain.StatusConflict("The request has already been denied")
	}
	if domain.IsStatusConditionTrue(oldCSR.Status.Conditions, domain.ConditionTypeCertificateSigningRequestApproved) && oldCSR.Status.Certificate != nil && len(*oldCSR.Status.Certificate) > 0 {
		return nil, domain.StatusConflict("The request has already been approved and the certificate issued")
	}

	populateConditionTimestamps(newCSR, oldCSR)
	newConditions := newCSR.Status.Conditions

	// Updating the approval should only update the conditions.
	newCSR.Spec = oldCSR.Spec
	newCSR.Status = oldCSR.Status
	newCSR.Status.Conditions = newConditions

	result, err := h.store.UpdateStatus(ctx, orgId, newCSR)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.CertificateSigningRequestKind, &name)
	}

	if domain.IsStatusConditionTrue(result.Status.Conditions, domain.ConditionTypeCertificateSigningRequestApproved) {
		h.signApprovedCertificateSigningRequest(ctx, orgId, result)
	}

	return result, domain.StatusOK()
}

func newSignRequestFromCertificateSigningRequest(csr *domain.CertificateSigningRequest) (signer.SignRequest, bool, error) {
	var opts []signer.SignRequestOption
	csrData, isTPM, err := tpm.NormalizeEnrollmentCSR(string(csr.Spec.Request))
	if err != nil {
		return nil, isTPM, fmt.Errorf("normalizing CSR: %w", err)
	}

	if csr.Status != nil && csr.Status.Certificate != nil {
		opts = append(opts, signer.WithIssuedCertificateBytes(*csr.Status.Certificate))
	}

	if csr.Spec.ExpirationSeconds != nil {
		opts = append(opts, signer.WithExpirationSeconds(*csr.Spec.ExpirationSeconds))
	}

	if csr.Metadata.Name != nil {
		opts = append(opts, signer.WithResourceName(*csr.Metadata.Name))
	}

	signReq, err := signer.NewSignRequestFromBytes(csr.Spec.SignerName, csrData, opts...)
	return signReq, isTPM, err
}

// borrowed from https://github.com/kubernetes/kubernetes/blob/master/pkg/registry/certificates/certificates/strategy.go
func populateConditionTimestamps(newCSR, oldCSR *domain.CertificateSigningRequest) {
	now := nowFunc()
	for i := range newCSR.Status.Conditions {
		// preserve existing lastTransitionTime if the condition with this type/status already exists,
		// otherwise set to now.
		if newCSR.Status.Conditions[i].LastTransitionTime.IsZero() {
			lastTransition := now
			for _, oldCondition := range oldCSR.Status.Conditions {
				if oldCondition.Type == newCSR.Status.Conditions[i].Type &&
					oldCondition.Status == newCSR.Status.Conditions[i].Status &&
					!oldCondition.LastTransitionTime.IsZero() {
					lastTransition = oldCondition.LastTransitionTime
					break
				}
			}
			newCSR.Status.Conditions[i].LastTransitionTime = lastTransition
		}
	}
}

func checkServerSvcApprovalPrivilege(ctx context.Context, signerName, serverSvcSignerName string) error {
	if signerName == serverSvcSignerName {
		mappedIdentity, ok := contextutil.GetMappedIdentityFromContext(ctx)
		if !ok || !mappedIdentity.IsSuperAdmin() {
			return fmt.Errorf("approving or denying CSRs with signer name %q requires super-admin privileges", signerName)
		}
	}
	return nil
}

func (h *ServiceHandler) validateAllowedSignersForCSRService(csr *domain.CertificateSigningRequest) error {
	if csr.Spec.SignerName == h.ca.Cfg.DeviceManagementSignerName {
		return fmt.Errorf("signer name %q is not allowed in CertificateSigningRequest service; use the EnrollmentRequest API instead", csr.Spec.SignerName)
	}
	if csr.Spec.SignerName == h.ca.Cfg.ServerSvcSignerName {
		return fmt.Errorf("signer name %q requires super-admin privileges", csr.Spec.SignerName)
	}
	return nil
}

// callbackCertificateSigningRequestUpdated is the certificate signing request-specific callback that handles CSR events
func (h *ServiceHandler) callbackCertificateSigningRequestUpdated(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := common.StoreErrorToApiStatus(err, created, string(resourceKind), &name)
		h.events.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, resourceKind, name, status, nil))
	} else {
		// Compute ResourceUpdatedDetails for updates
		var updateDetails *domain.ResourceUpdatedDetails
		if !created {
			var (
				oldCSR, newCSR *domain.CertificateSigningRequest
				ok             bool
			)
			if oldCSR, newCSR, ok = common.CastResources[domain.CertificateSigningRequest](oldResource, newResource); ok && oldCSR != nil && newCSR != nil {
				updateDetails = common.ComputeResourceUpdatedDetails(oldCSR.Metadata, newCSR.Metadata)
			}
		}
		h.events.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, resourceKind, name, updateDetails, h.log, nil))
	}
}

// callbackCertificateSigningRequestDeleted is the certificate signing request-specific callback that handles CSR deletion events
func (h *ServiceHandler) callbackCertificateSigningRequestDeleted(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.events.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// setCSRFailedCondition sets the Failed condition on the provided CSR, persists the change, and logs any error during persistence.
func (h *ServiceHandler) setCSRFailedCondition(ctx context.Context, orgId uuid.UUID, csr *domain.CertificateSigningRequest, reason, message string) {
	domain.SetStatusCondition(&csr.Status.Conditions, domain.Condition{
		Type:    domain.ConditionTypeCertificateSigningRequestFailed,
		Status:  domain.ConditionStatusTrue,
		Reason:  reason,
		Message: message,
	})

	if _, err := h.store.UpdateStatus(ctx, orgId, csr); err != nil {
		h.log.WithError(err).Error("failed to set failure condition")
	}
}
