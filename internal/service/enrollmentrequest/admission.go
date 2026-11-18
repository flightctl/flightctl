package enrollmentrequest

import (
	"bytes"
	"context"
	"errors"
	"math"
	"time"

	"github.com/flightctl/flightctl/internal/contextutil"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service/common"
	enrollmentstore "github.com/flightctl/flightctl/internal/store/enrollmentrequest"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

func pendingAdmission(request *domain.EnrollmentRequest, reason string) {
	addStatusIfNeeded(request)
	domain.SetStatusCondition(&request.Status.Conditions, domain.Condition{
		Type: domain.ConditionTypeEnrollmentRequestApproved, Status: domain.ConditionStatusFalse,
		Reason: reason, Message: "Automatic approval is pending: " + reason,
	})
}

func credentialAdmissionGate(credential *domain.CertificateSigningRequest, request *domain.EnrollmentRequest, signerName string, now time.Time) string {
	if credential == nil || request.Status == nil || lo.FromPtr(request.Status.EnrollmentCertificateName) != lo.FromPtr(credential.Metadata.Name) || signerName == "" || credential.Spec.SignerName != signerName {
		return "EnrollmentCredentialUnavailable"
	}
	if credential.Status == nil || credential.Status.Certificate == nil || credential.Status.Revocation != nil {
		return "EnrollmentCredentialInvalid"
	}
	certificate, err := crypto.ParseCertificatePEM(*credential.Status.Certificate)
	if err != nil || now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
		return "EnrollmentCredentialInvalid"
	}
	csr, err := crypto.ParseCSR(credential.Spec.Request)
	if err != nil || csr.CheckSignature() != nil || !bytes.Equal(csr.RawSubjectPublicKeyInfo, certificate.RawSubjectPublicKeyInfo) {
		return "EnrollmentCredentialInvalid"
	}
	return ""
}

func policyAdmissionGate(policy *domain.EnrollmentApprovalPolicy, credential *domain.CertificateSigningRequest, request *domain.EnrollmentRequest, now time.Time) string {
	if !policy.Spec.Enabled {
		return "PolicyDisabled"
	}
	if policy.Status == nil || policy.Status.EnrollmentCount < 0 || policy.Status.EnrollmentCount == math.MaxInt64 {
		return "PolicyAccountingUnavailable"
	}
	if policy.Spec.ApprovalLimit != nil && policy.Status.EnrollmentCount >= *policy.Spec.ApprovalLimit {
		return "PolicyApprovalLimitReached"
	}
	return policyEligibilityGate(policy, credential, request, now)
}

func policyEligibilityGate(policy *domain.EnrollmentApprovalPolicy, credential *domain.CertificateSigningRequest, request *domain.EnrollmentRequest, now time.Time) string {
	if policy.Spec.AutoApprovalWindowDays != nil {
		if credential.Status == nil || credential.Status.IssuedAt == nil || credential.Status.IssuedAt.After(now) {
			return "CredentialIssuanceTimeUnavailable"
		}
		days := *policy.Spec.AutoApprovalWindowDays
		if days <= 0 || !now.Before(credential.Status.IssuedAt.AddDate(0, 0, int(days))) {
			return "PolicyApprovalWindowExpired"
		}
	}
	if policy.Spec.RequireTPMVerification {
		_, isTPM := tpm.ParseTCGCSRBytes(request.Spec.Csr)
		if !isTPM || request.Status == nil || !domain.IsStatusConditionTrue(request.Status.Conditions, domain.ConditionTypeEnrollmentRequestTPMVerified) {
			return "TPMVerificationRequired"
		}
	}
	return ""
}

func (h *ServiceHandler) applyAdmission(ctx context.Context, orgID uuid.UUID, snapshot *enrollmentstore.AdmissionSnapshot, approval *domain.EnrollmentRequestApprovalStatus) (*enrollmentstore.AdmissionDecision, error) {
	request := snapshot.Request
	automatic := approval == nil
	var selected *domain.EnrollmentApprovalPolicy
	if automatic {
		var reason string
		selected, reason = selectEnrollmentPolicy(ctx, h.matcher, snapshot.Policies, *request, func(policy domain.EnrollmentApprovalPolicy) string {
			if reason := policyEligibilityGate(&policy, snapshot.Credential, request, time.Now()); reason != "" {
				return reason
			}
			if policy.Spec.RequireTPMVerification {
				csrBytes, _ := tpm.ParseTCGCSRBytes(request.Spec.Csr)
				roots := h.getTPMCAPool()
				if roots == nil || tpm.VerifyTCGCSRChainOfTrustWithRoots(csrBytes, roots) != nil {
					return "TPMVerificationRequired"
				}
			}
			return ""
		})
		if selected == nil {
			pendingAdmission(request, reason)
			return nil, nil
		}
	}
	if h.ca == nil || h.ca.Cfg == nil {
		pendingAdmission(request, "EnrollmentSignerUnavailable")
		return nil, nil
	}
	if h.events == nil || h.csrService == nil || h.deviceStore == nil {
		pendingAdmission(request, "AdmissionPersistenceUnavailable")
		return nil, nil
	}
	if reason := credentialAdmissionGate(snapshot.Credential, request, h.ca.Cfg.DeviceEnrollmentSignerName, time.Now()); reason != "" {
		pendingAdmission(request, reason)
		return nil, nil
	}
	decision := &enrollmentstore.AdmissionDecision{Source: "manual"}
	if automatic {
		if reason := policyAdmissionGate(selected, snapshot.Credential, request, time.Now()); reason != "" {
			pendingAdmission(request, reason)
			return nil, nil
		}
		if selected.Spec.RequireTPMVerification {
			csrBytes, _ := tpm.ParseTCGCSRBytes(request.Spec.Csr)
			roots := h.getTPMCAPool()
			if roots == nil || tpm.VerifyTCGCSRChainOfTrustWithRoots(csrBytes, roots) != nil {
				pendingAdmission(request, "TPMVerificationRequired")
				return nil, nil
			}
		}
		decision.Source, decision.PolicyName = "policy", lo.FromPtr(selected.Metadata.Name)
		approval = approvalForPolicy(selected, request)
	}
	decision.Actor = approval.ApprovedBy
	if approval.Labels == nil {
		approval.Labels = &map[string]string{}
	}
	if err := approveAndSignEnrollmentRequest(ctx, h.ca, request, approval); err != nil {
		return nil, err
	}
	if automatic {
		domain.SetStatusCondition(&request.Status.Conditions, domain.Condition{Type: domain.ConditionTypeEnrollmentRequestApproved, Status: domain.ConditionStatusTrue, Reason: "PolicyApproved"})
	}
	if err := h.createDeviceFromEnrollmentRequest(ctx, orgID, request); err != nil {
		return nil, err
	}
	if reason := credentialAdmissionGate(snapshot.Credential, request, h.ca.Cfg.DeviceEnrollmentSignerName, time.Now()); reason != "" {
		return nil, errors.New(reason)
	}
	if automatic {
		if reason := policyAdmissionGate(selected, snapshot.Credential, request, time.Now()); reason != "" {
			return nil, errors.New(reason)
		}
	}
	name := lo.FromPtr(request.Metadata.Name)
	decision.Events = []domain.Event{
		*common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, true, domain.DeviceKind, name, nil, h.log, nil),
		*common.GetEnrollmentRequestApprovedEvent(ctx, name, h.log),
	}
	for index := range decision.Events {
		if err := h.events.PersistEvent(ctx, orgID, &decision.Events[index]); err != nil {
			return nil, err
		}
	}
	return decision, nil
}

func approvalForPolicy(policy *domain.EnrollmentApprovalPolicy, request *domain.EnrollmentRequest) *domain.EnrollmentRequestApprovalStatus {
	labels := make(map[string]string)
	for key, value := range policy.Spec.DeviceLabels {
		labels[key] = value
	}
	for _, key := range lo.FromPtr(policy.Spec.AllowedIncomingLabels) {
		if _, exists := labels[key]; !exists && request.Spec.Labels != nil {
			if value, exists := (*request.Spec.Labels)[key]; exists {
				labels[key] = value
			}
		}
	}
	return &domain.EnrollmentRequestApprovalStatus{Approved: true, ApprovedAt: time.Now(), ApprovedBy: "policy:" + lo.FromPtr(policy.Metadata.Name), Labels: &labels, ReplaceLabels: lo.ToPtr(true)}
}

func (h *ServiceHandler) approveAdmission(ctx context.Context, orgID uuid.UUID, name string, approval domain.EnrollmentRequestApproval) (*domain.EnrollmentRequestApprovalStatus, domain.Status) {
	if errs := approval.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	identity, ok := contextutil.GetMappedIdentityFromContext(ctx)
	if !ok {
		return nil, domain.StatusInternalServerError("approval identity is unavailable")
	}
	actor := "unknown"
	if identity != nil && identity.GetUsername() != "" {
		actor = identity.GetUsername()
	}
	admissions := h.store.(enrollmentstore.AdmissionStore)
	result, err := admissions.Admit(ctx, orgID, name, func(txCtx context.Context, snapshot *enrollmentstore.AdmissionSnapshot) (*enrollmentstore.AdmissionDecision, error) {
		if !approval.Approved {
			addStatusIfNeeded(snapshot.Request)
			snapshot.Request.Status.Approval = &domain.EnrollmentRequestApprovalStatus{Approved: false, ApprovedAt: time.Now(), ApprovedBy: actor}
			domain.SetStatusCondition(&snapshot.Request.Status.Conditions, domain.Condition{Type: domain.ConditionTypeEnrollmentRequestDenied, Status: domain.ConditionStatusTrue, Reason: "ManuallyDenied", Message: "Enrollment was denied by an administrator"})
			return nil, nil
		}
		return h.applyAdmission(txCtx, orgID, snapshot, &domain.EnrollmentRequestApprovalStatus{Approved: true, ApprovedAt: time.Now(), ApprovedBy: actor, Labels: approval.Labels, ReplaceLabels: approval.ReplaceLabels})
	})
	if err != nil {
		if errors.Is(err, enrollmentstore.ErrAdmissionBindingUnavailable) {
			return nil, domain.StatusConflict("Enrollment credential binding is unavailable")
		}
		return nil, common.StoreErrorToApiStatus(err, false, domain.EnrollmentRequestKind, &name)
	}
	if result == nil || result.Status == nil || result.Status.Approval == nil || result.Status.Approval.Approved != approval.Approved {
		if !approval.Approved && result != nil && enrollmentstore.Pending(result) {
			return nil, domain.StatusOK()
		}
		return nil, domain.StatusConflict("enrollment admission is unavailable or conflicts with a committed decision")
	}
	if err := h.publishAdmissions(ctx, orgID); err != nil {
		h.log.WithError(err).Warn("Enrollment admission publication will be retried")
	}
	return result.Status.Approval, domain.StatusOK()
}

func (h *ServiceHandler) ReevaluateEnrollment(ctx context.Context, orgID uuid.UUID, event domain.Event) error {
	admissions, ok := h.store.(enrollmentstore.AdmissionStore)
	if !ok {
		return errors.New("enrollment admission store is unavailable")
	}
	if err := h.publishAdmissions(ctx, orgID); err != nil {
		return err
	}
	if event.InvolvedObject.Kind == domain.EnrollmentRequestKind {
		_, err := admissions.Admit(ctx, orgID, event.InvolvedObject.Name, func(txCtx context.Context, snapshot *enrollmentstore.AdmissionSnapshot) (*enrollmentstore.AdmissionDecision, error) {
			return h.applyAdmission(txCtx, orgID, snapshot, nil)
		})
		if errors.Is(err, enrollmentstore.ErrAdmissionBindingUnavailable) {
			_, _, _, err = h.store.Mutate(ctx, orgID, event.InvolvedObject.Name, nil, func(mutation *enrollmentstore.EnrollmentRequestMutation) error {
				if err := mutation.RequireExisting(); err != nil {
					return err
				}
				if enrollmentstore.Pending(mutation.EnrollmentRequest) {
					pendingAdmission(mutation.EnrollmentRequest, "EnrollmentCredentialUnavailable")
				}
				return nil
			})
		}
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return h.publishAdmissions(ctx, orgID)
	}
	certificate, err := admissions.PolicyCertificate(ctx, orgID, event.InvolvedObject.Name)
	if errors.Is(err, flterrors.ErrResourceNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	for after := ""; ; {
		names, err := admissions.PendingNames(ctx, orgID, certificate, after, 100)
		if err != nil {
			return err
		}
		if len(names) == 0 {
			return h.publishAdmissions(ctx, orgID)
		}
		for _, name := range names {
			_, err := admissions.Admit(ctx, orgID, name, func(txCtx context.Context, snapshot *enrollmentstore.AdmissionSnapshot) (*enrollmentstore.AdmissionDecision, error) {
				return h.applyAdmission(txCtx, orgID, snapshot, nil)
			})
			if err != nil && !errors.Is(err, flterrors.ErrResourceNotFound) {
				return err
			}
		}
		after = names[len(names)-1]
	}
}

func (h *ServiceHandler) publishAdmissions(ctx context.Context, orgID uuid.UUID) error {
	return h.store.(enrollmentstore.AdmissionStore).PublishAdmissions(ctx, orgID, func(ctx context.Context, orgID uuid.UUID, event *domain.Event) error {
		if event.InvolvedObject.Kind == domain.DeviceKind && h.kvStore != nil {
			device, err := h.deviceStore.Get(ctx, orgID, event.InvolvedObject.Name)
			if err != nil && !errors.Is(err, flterrors.ErrResourceNotFound) {
				return err
			}
			if device != nil && device.Metadata.Annotations != nil && (*device.Metadata.Annotations)[domain.DeviceAnnotationAwaitingReconnect] == "true" {
				key := kvstore.AwaitingReconnectionKey{OrgID: orgID, DeviceName: event.InvolvedObject.Name}
				if _, err := h.kvStore.SetNX(ctx, key.ComposeKey(), []byte("true")); err != nil {
					return err
				}
			}
		}
		return h.events.PublishEvent(ctx, orgID, event)
	})
}

func (h *ServiceHandler) RecoverAdmissions(ctx context.Context, orgID uuid.UUID) error {
	return h.publishAdmissions(ctx, orgID)
}
