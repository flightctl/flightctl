package enrollmentapprovalpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	policystore "github.com/flightctl/flightctl/internal/store/enrollmentapprovalpolicy"
	"github.com/flightctl/flightctl/internal/store/selector"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

type ServiceHandler struct {
	store      policystore.Store
	validator  PolicySetValidator
	signerName string
	events     events.Service
	log        logrus.FieldLogger
}

func NewServiceHandler(policies policystore.Store, validator PolicySetValidator, signerName string, eventService events.Service, log logrus.FieldLogger) *ServiceHandler {
	if policies == nil || validator == nil || signerName == "" || log == nil {
		panic("policy store, validator, enrollment signer and logger are required")
	}
	return &ServiceHandler{store: policies, validator: validator, signerName: signerName, events: eventService, log: log}
}

func policyErrorToAPIStatus(err error, name string, created bool) domain.Status {
	var overlap PolicyOverlapError
	var invalidPolicy InvalidPolicyError
	var selectorFailure *selector.SelectorError
	var input InvalidPolicyInputError
	switch {
	case errors.As(err, &invalidPolicy):
		return domain.NewFailureStatus(400, "InvalidPolicy", invalidPolicy.Error())
	case errors.As(err, &input):
		return domain.StatusBadRequest(input.Error())
	case errors.Is(err, ErrValidationUnavailable):
		return domain.NewFailureStatus(503, "PolicyValidationUnavailable", err.Error())
	case errors.As(err, &overlap):
		return domain.NewFailureStatus(409, "PolicyOverlap", overlap.Error())
	case errors.Is(err, flterrors.ErrImmutableCertificateBinding):
		return domain.NewFailureStatus(409, "ImmutableCertificateBinding", err.Error())
	case errors.Is(err, flterrors.ErrResourceVersionConflict):
		return domain.NewFailureStatus(409, "ResourceVersionConflict", err.Error())
	case errors.Is(err, flterrors.ErrDuplicateName):
		return domain.NewFailureStatus(409, "AlreadyExists", err.Error())
	case errors.As(err, &selectorFailure), errors.Is(err, flterrors.ErrFieldSelectorUnknownSelector):
		return domain.StatusBadRequest(err.Error())
	default:
		status := common.StoreErrorToApiStatus(err, created, domain.EnrollmentApprovalPolicyKind, &name)
		if status.Code == 500 {
			return domain.StatusInternalServerError("An internal error occurred")
		}
		return status
	}
}

func (handler *ServiceHandler) credentialValid(credential *domain.CertificateSigningRequest, candidate *domain.EnrollmentApprovalPolicy, certificateName string, cleanup bool) error {
	if credential == nil {
		return InvalidPolicyError{Message: fmt.Sprintf("spec.certificateName %q does not identify an enrollment credential", certificateName), Cause: flterrors.ErrResourceNotFound}
	}
	if credential.Spec.SignerName != handler.signerName {
		return InvalidPolicyError{Message: "certificateName must select an enrollment credential"}
	}
	if credential.Status == nil || credential.Status.Certificate == nil || (!cleanup && credential.Status.Revocation != nil) {
		return InvalidPolicyError{Message: "enrollment credential must be issued and unrevoked"}
	}
	certificate, err := fcrypto.ParseCertificatePEM(*credential.Status.Certificate)
	if err != nil {
		return InvalidPolicyError{Message: "enrollment credential certificate is invalid", Cause: err}
	}
	now := time.Now()
	if now.Before(certificate.NotBefore) || (!cleanup && !now.Before(certificate.NotAfter)) {
		return InvalidPolicyError{Message: "enrollment credential must be currently valid"}
	}
	if candidate != nil && candidate.Spec.AutoApprovalWindowDays != nil && credential.Status.IssuedAt == nil {
		return InvalidPolicyError{Message: "window requires authoritative certificate issuance time"}
	}
	return nil
}

func (handler *ServiceHandler) validatePolicySet(ctx context.Context, candidate *domain.EnrollmentApprovalPolicy, credential *domain.CertificateSigningRequest, enabled []domain.EnrollmentApprovalPolicy, certificateName string, cleanup bool) error {
	if err := handler.credentialValid(credential, candidate, certificateName, cleanup); err != nil {
		return err
	}
	if candidate != nil {
		if errs := candidate.Validate(); len(errs) > 0 {
			cause := errors.Join(errs...)
			return InvalidPolicyError{Message: cause.Error(), Cause: cause}
		}
		if candidate.Spec.Enabled {
			enabled = append(enabled, *candidate)
		}
	}
	return handler.validator.ValidatePolicySet(ctx, candidate, enabled)
}

func (handler *ServiceHandler) updatedEvent(ctx context.Context, orgID uuid.UUID, result, previous *domain.EnrollmentApprovalPolicy, created bool) {
	if handler.events == nil {
		return
	}
	common.SafeEventCallback(handler.log, func() {
		var metadata domain.ObjectMeta
		if previous != nil {
			metadata = previous.Metadata
		}
		details := common.ComputeResourceUpdatedDetails(metadata, result.Metadata)
		handler.events.CreateEvent(ctx, orgID, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, domain.EnrollmentApprovalPolicyKind, *result.Metadata.Name, details, handler.log, nil))
	})
}

func checkCurrentPolicy(current *domain.EnrollmentApprovalPolicy, certificateName, version string) error {
	if current == nil {
		return flterrors.ErrResourceNotFound
	}
	if current.Spec.CertificateName != certificateName {
		return flterrors.ErrImmutableCertificateBinding
	}
	if version != lo.FromPtr(current.Metadata.ResourceVersion) {
		return flterrors.ErrResourceVersionConflict
	}
	return nil
}

func (handler *ServiceHandler) CreateEnrollmentApprovalPolicy(ctx context.Context, orgID uuid.UUID, policy domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, domain.Status) {
	if policy.Status != nil {
		return nil, domain.NewFailureStatus(400, "ProtectedField", "status is server-owned")
	}
	if errs := policy.Validate(); len(errs) > 0 {
		return nil, domain.NewFailureStatus(400, "InvalidPolicy", errors.Join(errs...).Error())
	}
	if policy.Metadata.ResourceVersion != nil {
		return nil, domain.NewFailureStatus(400, "InvalidPolicy", "resourceVersion must be absent on create")
	}
	common.NilOutManagedObjectMetaProperties(&policy.Metadata)
	var result, previous *domain.EnrollmentApprovalPolicy
	name := *policy.Metadata.Name
	err := handler.store.WithPolicySetTransaction(ctx, orgID, policy.Spec.CertificateName, name, func(txCtx context.Context, credential *domain.CertificateSigningRequest, enabled []domain.EnrollmentApprovalPolicy) error {
		if credential == nil {
			return handler.credentialValid(nil, &policy, policy.Spec.CertificateName, false)
		}
		var err error
		result, previous, _, err = handler.store.Mutate(txCtx, orgID, name, func(m store.ResourceMutation[domain.EnrollmentApprovalPolicy]) error {
			if m.Resource() != nil {
				return flterrors.ErrDuplicateName
			}
			if err := handler.validatePolicySet(txCtx, &policy, credential, enabled, policy.Spec.CertificateName, false); err != nil {
				return err
			}
			m.SetResource(&policy)
			return nil
		})
		return err
	})
	if err != nil {
		return nil, policyErrorToAPIStatus(err, name, true)
	}
	handler.updatedEvent(ctx, orgID, result, previous, true)
	return result, domain.StatusCreated()
}

func (handler *ServiceHandler) GetEnrollmentApprovalPolicy(ctx context.Context, orgID uuid.UUID, name string) (*domain.EnrollmentApprovalPolicy, domain.Status) {
	result, err := handler.store.Get(ctx, orgID, name)
	return result, policyErrorToAPIStatus(err, name, false)
}

func (handler *ServiceHandler) ListEnrollmentApprovalPolicies(ctx context.Context, orgID uuid.UUID, params domain.ListEnrollmentApprovalPoliciesParams) (*domain.EnrollmentApprovalPolicyList, domain.Status) {
	listParams, status := common.PrepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status.Code != 200 {
		return nil, status
	}
	result, err := handler.store.List(ctx, orgID, *listParams)
	return result, policyErrorToAPIStatus(err, "", false)
}

func (handler *ServiceHandler) ReplaceEnrollmentApprovalPolicy(ctx context.Context, orgID uuid.UUID, name string, policy domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, domain.Status) {
	if policy.Status != nil {
		return nil, domain.NewFailureStatus(400, "ProtectedField", "status is server-owned")
	}
	if policy.Metadata.Name == nil || *policy.Metadata.Name != name {
		return nil, domain.NewFailureStatus(400, "InvalidPolicy", "metadata.name must match the path")
	}
	if policy.Metadata.ResourceVersion == nil {
		return nil, domain.NewFailureStatus(409, "ResourceVersionConflict", "current resourceVersion is required")
	}
	current, status := handler.GetEnrollmentApprovalPolicy(ctx, orgID, name)
	if current == nil {
		return nil, status
	}
	var result, previous *domain.EnrollmentApprovalPolicy
	err := handler.store.WithPolicySetTransaction(ctx, orgID, current.Spec.CertificateName, name, func(txCtx context.Context, credential *domain.CertificateSigningRequest, enabled []domain.EnrollmentApprovalPolicy) error {
		if credential == nil {
			return handler.credentialValid(nil, current, current.Spec.CertificateName, false)
		}
		var err error
		result, previous, _, err = handler.store.Mutate(txCtx, orgID, name, func(m store.ResourceMutation[domain.EnrollmentApprovalPolicy]) error {
			stored := m.Resource()
			if err := checkCurrentPolicy(stored, current.Spec.CertificateName, *policy.Metadata.ResourceVersion); err != nil {
				return err
			}
			if policy.Spec.CertificateName != stored.Spec.CertificateName {
				return flterrors.ErrImmutableCertificateBinding
			}
			cleanup := stored.Spec.Enabled && !policy.Spec.Enabled
			common.NilOutManagedObjectMetaProperties(&policy.Metadata)
			store.ApplyObjectMetaUpdate(&stored.Metadata, &policy.Metadata, nil)
			policy.Metadata = stored.Metadata
			if err := handler.validatePolicySet(txCtx, &policy, credential, enabled, current.Spec.CertificateName, cleanup); err != nil {
				return err
			}
			m.SetResource(&policy)
			return nil
		})
		if errors.Is(err, policystore.ErrNameReserved) {
			return flterrors.ErrResourceNotFound
		}
		return err
	})
	if err != nil {
		return nil, policyErrorToAPIStatus(err, name, false)
	}
	handler.updatedEvent(ctx, orgID, result, previous, false)
	return result, domain.StatusOK()
}

func (handler *ServiceHandler) PatchEnrollmentApprovalPolicy(ctx context.Context, orgID uuid.UUID, name string, patch domain.PatchRequest) (*domain.EnrollmentApprovalPolicy, domain.Status) {
	version := ""
	patch = append(domain.PatchRequest(nil), patch...)
	for i, operation := range patch {
		if operation.Path == "/metadata/resourceVersion" && string(operation.Op) == "test" {
			version, _ = operation.Value.(string)
			continue
		}
		if !strings.HasPrefix(operation.Path, "/spec/") && operation.Path != "/spec" && !strings.HasPrefix(operation.Path, "/metadata/labels/") && operation.Path != "/metadata/labels" {
			return nil, domain.NewFailureStatus(400, "ProtectedField", "patch may only change spec or labels")
		}
		if operation.Value == nil && operation.Op != "remove" {
			if operation.Path != "/spec/approvalLimit" && operation.Path != "/spec/autoApprovalWindowDays" {
				return nil, domain.StatusBadRequest("patch value must not be null for " + operation.Path)
			}
			patch[i].Value = json.RawMessage("null")
		}
	}
	if version == "" {
		return nil, domain.NewFailureStatus(409, "ResourceVersionConflict", "patch requires a resourceVersion test")
	}
	current, status := handler.GetEnrollmentApprovalPolicy(ctx, orgID, name)
	if current == nil {
		return nil, status
	}
	var result, previous *domain.EnrollmentApprovalPolicy
	err := handler.store.WithPolicySetTransaction(ctx, orgID, current.Spec.CertificateName, name, func(txCtx context.Context, credential *domain.CertificateSigningRequest, enabled []domain.EnrollmentApprovalPolicy) error {
		if credential == nil {
			return handler.credentialValid(nil, current, current.Spec.CertificateName, false)
		}
		var err error
		result, previous, _, err = handler.store.Mutate(txCtx, orgID, name, func(m store.ResourceMutation[domain.EnrollmentApprovalPolicy]) error {
			stored := m.Resource()
			if err := checkCurrentPolicy(stored, current.Spec.CertificateName, version); err != nil {
				return err
			}
			stored.Status = nil
			var candidate domain.EnrollmentApprovalPolicy
			if err := applyPolicyPatch(txCtx, stored, &candidate, patch, name); err != nil {
				return InvalidPolicyInputError{Cause: fmt.Errorf("apply patch: %w", err)}
			}
			if candidate.Spec.CertificateName != stored.Spec.CertificateName {
				return flterrors.ErrImmutableCertificateBinding
			}
			cleanup := stored.Spec.Enabled && !candidate.Spec.Enabled
			if err := handler.validatePolicySet(txCtx, &candidate, credential, enabled, current.Spec.CertificateName, cleanup); err != nil {
				return err
			}
			m.SetResource(&candidate)
			return nil
		})
		if errors.Is(err, policystore.ErrNameReserved) {
			return flterrors.ErrResourceNotFound
		}
		return err
	})
	if err != nil {
		return nil, policyErrorToAPIStatus(err, name, false)
	}
	handler.updatedEvent(ctx, orgID, result, previous, false)
	return result, domain.StatusOK()
}

func (handler *ServiceHandler) DeleteEnrollmentApprovalPolicy(ctx context.Context, orgID uuid.UUID, name string) domain.Status {
	current, status := handler.GetEnrollmentApprovalPolicy(ctx, orgID, name)
	if current == nil {
		return status
	}
	err := handler.store.WithPolicySetTransaction(ctx, orgID, current.Spec.CertificateName, name, func(txCtx context.Context, credential *domain.CertificateSigningRequest, enabled []domain.EnrollmentApprovalPolicy) error {
		stored, err := handler.store.Get(txCtx, orgID, name)
		if err != nil {
			return err
		}
		if stored.Spec.CertificateName != current.Spec.CertificateName {
			return flterrors.ErrImmutableCertificateBinding
		}
		if err := handler.validatePolicySet(txCtx, nil, credential, enabled, current.Spec.CertificateName, true); err != nil {
			return err
		}
		return handler.store.Delete(txCtx, orgID, name)
	})
	if err == nil && handler.events != nil {
		common.SafeEventCallback(handler.log, func() {
			handler.events.HandleGenericResourceDeletedEvents(ctx, domain.EnrollmentApprovalPolicyKind, orgID, name, nil, nil, false, nil)
		})
	}
	return policyErrorToAPIStatus(err, name, false)
}

var _ Service = (*ServiceHandler)(nil)
