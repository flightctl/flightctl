package enrollmentapprovalpolicy

import (
	"context"
	"errors"
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
	if policies == nil || validator == nil || signerName == "" {
		panic("policy store, validator and enrollment signer are required")
	}
	return &ServiceHandler{store: policies, validator: validator, signerName: signerName, events: eventService, log: log}
}

func policyErrorToAPIStatus(err error, name string, created bool) domain.Status {
	var overlap PolicyOverlapError
	var invalidPolicy InvalidPolicyError
	var selectorFailure *selector.SelectorError
	switch {
	case errors.Is(err, ErrValidationUnavailable):
		return domain.NewFailureStatus(503, "PolicyValidationUnavailable", err.Error())
	case errors.As(err, &overlap):
		return domain.NewFailureStatus(409, "PolicyOverlap", overlap.Error())
	case errors.As(err, &invalidPolicy):
		return domain.NewFailureStatus(400, "InvalidPolicy", invalidPolicy.Error())
	case errors.Is(err, flterrors.ErrImmutableCertificateBinding):
		return domain.NewFailureStatus(409, "ImmutableCertificateBinding", err.Error())
	case errors.Is(err, flterrors.ErrResourceVersionConflict):
		return domain.NewFailureStatus(409, "ResourceVersionConflict", err.Error())
	case errors.Is(err, flterrors.ErrDuplicateName):
		return domain.NewFailureStatus(409, "AlreadyExists", err.Error())
	case errors.As(err, &selectorFailure), errors.Is(err, flterrors.ErrFieldSelectorUnknownSelector):
		return domain.StatusBadRequest(err.Error())
	default:
		return common.StoreErrorToApiStatus(err, created, domain.EnrollmentApprovalPolicyKind, &name)
	}
}

func (handler *ServiceHandler) credentialValid(credential *domain.CertificateSigningRequest, candidate *domain.EnrollmentApprovalPolicy, cleanup bool) error {
	if credential.Spec.SignerName != handler.signerName {
		return InvalidPolicyError{Message: "certificateName must select an enrollment credential"}
	}
	if credential.Status == nil || credential.Status.Certificate == nil || (!cleanup && credential.Status.Revocation != nil) {
		return InvalidPolicyError{Message: "enrollment credential must be issued and unrevoked"}
	}
	certificate, err := fcrypto.ParseCertificatePEM(*credential.Status.Certificate)
	if err != nil {
		return InvalidPolicyError{Message: "enrollment credential certificate is invalid"}
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

func (handler *ServiceHandler) mutate(ctx context.Context, orgID uuid.UUID, name, certificate string, operation policystore.Operation, prepare func(*domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error)) (*domain.EnrollmentApprovalPolicy, domain.Status) {
	result, previous, err := handler.store.Mutate(ctx, orgID, name, certificate, operation, func(current *domain.EnrollmentApprovalPolicy, credential *domain.CertificateSigningRequest, enabled []domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
		wasEnabled := current != nil && current.Spec.Enabled
		candidate, err := prepare(current)
		if err != nil {
			return nil, err
		}
		cleanup := current != nil && ((operation == policystore.Delete && candidate == nil) ||
			(operation == policystore.Update && wasEnabled && candidate != nil && !candidate.Spec.Enabled))
		if err := handler.credentialValid(credential, candidate, cleanup); err != nil {
			return nil, err
		}
		if candidate != nil {
			if errs := candidate.Validate(); len(errs) > 0 {
				return nil, InvalidPolicyError{Message: errors.Join(errs...).Error()}
			}
			if candidate.Spec.Enabled {
				enabled = append(enabled, *candidate)
			}
		}
		if err := handler.validator.ValidatePolicySet(ctx, candidate, enabled); err != nil {
			return nil, err
		}
		return candidate, nil
	})
	if err == nil && handler.events != nil {
		common.SafeEventCallback(handler.log, func() {
			if operation == policystore.Delete {
				handler.events.HandleGenericResourceDeletedEvents(ctx, domain.EnrollmentApprovalPolicyKind, orgID, name, nil, nil, false, nil)
				return
			}
			var previousMetadata domain.ObjectMeta
			if previous != nil {
				previousMetadata = previous.Metadata
			}
			details := common.ComputeResourceUpdatedDetails(previousMetadata, result.Metadata)
			handler.events.CreateEvent(ctx, orgID, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, operation == policystore.Create, domain.EnrollmentApprovalPolicyKind, name, details, handler.log, nil))
		})
	}
	return result, policyErrorToAPIStatus(err, name, operation == policystore.Create)
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
	return handler.mutate(ctx, orgID, *policy.Metadata.Name, policy.Spec.CertificateName, policystore.Create, func(*domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) { return &policy, nil })
}

func (handler *ServiceHandler) GetEnrollmentApprovalPolicy(ctx context.Context, orgID uuid.UUID, name string) (*domain.EnrollmentApprovalPolicy, domain.Status) {
	result, err := handler.store.Get(ctx, orgID, name)
	return result, common.StoreErrorToApiStatus(err, false, domain.EnrollmentApprovalPolicyKind, &name)
}

func (handler *ServiceHandler) ListEnrollmentApprovalPolicies(ctx context.Context, orgID uuid.UUID, params domain.ListEnrollmentApprovalPoliciesParams) (*domain.EnrollmentApprovalPolicyList, domain.Status) {
	listParams, status := common.PrepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status.Code != 200 {
		return nil, status
	}
	result, err := handler.store.List(ctx, orgID, *listParams)
	return result, policyErrorToAPIStatus(err, "", false)
}

func (handler *ServiceHandler) GetEnrollmentApprovalPolicyStatus(ctx context.Context, orgID uuid.UUID, name string) (*domain.EnrollmentApprovalPolicyStatus, domain.Status) {
	policy, status := handler.GetEnrollmentApprovalPolicy(ctx, orgID, name)
	if policy == nil {
		return nil, status
	}
	return policy.Status, status
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
	return handler.mutate(ctx, orgID, name, current.Spec.CertificateName, policystore.Update, func(stored *domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
		if *policy.Metadata.ResourceVersion != lo.FromPtr(stored.Metadata.ResourceVersion) {
			return nil, flterrors.ErrResourceVersionConflict
		}
		if policy.Spec.CertificateName != stored.Spec.CertificateName {
			return nil, flterrors.ErrImmutableCertificateBinding
		}
		common.NilOutManagedObjectMetaProperties(&policy.Metadata)
		store.ApplyObjectMetaUpdate(&stored.Metadata, &policy.Metadata, nil)
		policy.Metadata = stored.Metadata
		return &policy, nil
	})
}

func (handler *ServiceHandler) PatchEnrollmentApprovalPolicy(ctx context.Context, orgID uuid.UUID, name string, patch domain.PatchRequest) (*domain.EnrollmentApprovalPolicy, domain.Status) {
	version := ""
	for _, operation := range patch {
		if operation.Path == "/metadata/resourceVersion" && string(operation.Op) == "test" {
			version, _ = operation.Value.(string)
			continue
		}
		if !strings.HasPrefix(operation.Path, "/spec/") && operation.Path != "/spec" && !strings.HasPrefix(operation.Path, "/metadata/labels/") && operation.Path != "/metadata/labels" {
			return nil, domain.NewFailureStatus(400, "ProtectedField", "patch may only change spec or labels")
		}
	}
	if version == "" {
		return nil, domain.NewFailureStatus(409, "ResourceVersionConflict", "patch requires a resourceVersion test")
	}
	current, status := handler.GetEnrollmentApprovalPolicy(ctx, orgID, name)
	if current == nil {
		return nil, status
	}
	return handler.mutate(ctx, orgID, name, current.Spec.CertificateName, policystore.Update, func(stored *domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
		if version != lo.FromPtr(stored.Metadata.ResourceVersion) {
			return nil, flterrors.ErrResourceVersionConflict
		}
		stored.Status = nil
		var candidate domain.EnrollmentApprovalPolicy
		if err := common.ApplyJSONPatch(ctx, stored, &candidate, patch, "/enrollmentapprovalpolicies/"+name); err != nil {
			return nil, InvalidPolicyError{Message: err.Error()}
		}
		if candidate.Spec.CertificateName != stored.Spec.CertificateName {
			return nil, flterrors.ErrImmutableCertificateBinding
		}
		return &candidate, nil
	})
}

func (handler *ServiceHandler) DeleteEnrollmentApprovalPolicy(ctx context.Context, orgID uuid.UUID, name string) domain.Status {
	current, status := handler.GetEnrollmentApprovalPolicy(ctx, orgID, name)
	if current == nil {
		return status
	}
	_, status = handler.mutate(ctx, orgID, name, current.Spec.CertificateName, policystore.Delete, func(*domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) { return nil, nil })
	return status
}

var _ Service = (*ServiceHandler)(nil)
