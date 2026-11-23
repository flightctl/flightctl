package enrollmentapprovalpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

type ServiceHandler struct {
	store      policystore.Store
	validator  ExpressionValidator
	signerName string
	events     events.Service
	log        logrus.FieldLogger
}

func NewServiceHandler(policies policystore.Store, validator ExpressionValidator, signerName string, eventService events.Service, log logrus.FieldLogger) (*ServiceHandler, error) {
	switch {
	case lo.IsNil(policies):
		return nil, fmt.Errorf("enrollment approval policy store is required")
	case lo.IsNil(validator):
		return nil, fmt.Errorf("enrollment approval policy expression validator is required")
	case signerName == "":
		return nil, fmt.Errorf("device enrollment signer name is required")
	case lo.IsNil(log):
		return nil, fmt.Errorf("enrollment approval policy logger is required")
	}
	if lo.IsNil(eventService) {
		eventService = nil
	}
	return &ServiceHandler{store: policies, validator: validator, signerName: signerName, events: eventService, log: log}, nil
}

func policyErrorToAPIStatus(err error, name string, created bool) domain.Status {
	var invalidPolicy InvalidPolicyError
	var invalidPolicyPointer *InvalidPolicyError
	var selectorFailure *selector.SelectorError
	var input InvalidPolicyInputError
	var inputPointer *InvalidPolicyInputError
	var quotaFailure approvalLimitAuthorizationError
	switch {
	case errors.As(err, &invalidPolicy):
		return domain.NewFailureStatus(400, "InvalidPolicy", invalidPolicy.Error())
	case errors.As(err, &invalidPolicyPointer):
		return domain.NewFailureStatus(400, "InvalidPolicy", invalidPolicyPointer.Error())
	case errors.As(err, &quotaFailure):
		return quotaFailure.status()
	case errors.Is(err, ErrValidationUnavailable):
		return domain.NewFailureStatus(503, "PolicyValidationUnavailable", ErrValidationUnavailable.Error())
	case errors.As(err, &input):
		return domain.StatusBadRequest(input.Error())
	case errors.As(err, &inputPointer):
		return domain.StatusBadRequest(inputPointer.Error())
	case errors.As(err, &selectorFailure):
		return domain.StatusBadRequest(selectorFailure.Error())
	case errors.Is(err, flterrors.ErrImmutableCertificateBinding):
		return domain.NewFailureStatus(409, "ImmutableCertificateBinding", flterrors.ErrImmutableCertificateBinding.Error())
	case errors.Is(err, flterrors.ErrResourceVersionConflict):
		return domain.NewFailureStatus(409, "ResourceVersionConflict", flterrors.ErrResourceVersionConflict.Error())
	case !created && errors.Is(err, policystore.ErrNameReserved):
		return domain.StatusResourceNotFound(domain.EnrollmentApprovalPolicyKind, name)
	case errors.Is(err, flterrors.ErrNoRowsUpdated):
		return domain.StatusResourceVersionConflict(flterrors.ErrNoRowsUpdated.Error())
	case errors.Is(err, flterrors.ErrFieldSelectorUnknownSelector):
		return domain.StatusBadRequest(flterrors.ErrFieldSelectorUnknownSelector.Error())
	case errors.Is(err, flterrors.ErrIllegalResourceVersionFormat):
		return domain.StatusBadRequest(flterrors.ErrIllegalResourceVersionFormat.Error())
	case errors.Is(err, flterrors.ErrUnsupportedUnicode):
		return domain.StatusBadRequest(flterrors.ErrUnsupportedUnicode.Error())
	case errors.Is(err, flterrors.ErrDuplicateName):
		return domain.NewFailureStatus(409, "AlreadyExists", flterrors.ErrDuplicateName.Error())
	default:
		status := common.StoreErrorToApiStatus(err, created, domain.EnrollmentApprovalPolicyKind, &name)
		if status.Code == 500 {
			return domain.StatusInternalServerError("An internal error occurred")
		}
		return status
	}
}

func unknownCredential(name string, cause error) InvalidPolicyError {
	return InvalidPolicyError{
		Message: fmt.Sprintf("spec.certificateName %q does not identify an enrollment credential", name),
		Cause:   cause,
	}
}

func (handler *ServiceHandler) validateCredentialBinding(credential *domain.CertificateSigningRequest, name string) error {
	if credential == nil {
		return unknownCredential(name, flterrors.ErrResourceNotFound)
	}
	if credential.Spec.SignerName != handler.signerName {
		return InvalidPolicyError{Message: "certificateName must select an enrollment credential"}
	}
	return nil
}

func validateCredentialEligibility(credential *domain.CertificateSigningRequest, windowSet bool) error {
	if credential.Status == nil || credential.Status.Certificate == nil || credential.Status.Revocation != nil {
		return InvalidPolicyError{Message: "enrollment credential must be issued and unrevoked"}
	}
	certificate, err := fcrypto.ParseCertificatePEM(*credential.Status.Certificate)
	if err != nil {
		return InvalidPolicyError{Message: "enrollment credential certificate is invalid", Cause: err}
	}
	now := time.Now()
	if now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
		return InvalidPolicyError{Message: "enrollment credential must be currently valid"}
	}
	if windowSet && credential.Status.IssuedAt == nil {
		return InvalidPolicyError{Message: "window requires authoritative certificate issuance time"}
	}
	return nil
}

func (handler *ServiceHandler) validatePolicyChange(ctx context.Context, credential *domain.CertificateSigningRequest, previous, candidate *domain.EnrollmentApprovalPolicy) error {
	if errs := candidate.Validate(); len(errs) > 0 {
		cause := errors.Join(errs...)
		return InvalidPolicyError{Message: cause.Error(), Cause: cause}
	}
	if !candidate.Spec.Enabled {
		return nil
	}
	if err := validateCredentialEligibility(credential, candidate.Spec.AutoApprovalWindowDays != nil); err != nil {
		return err
	}
	if previous == nil || !previous.Spec.Enabled || candidate.Spec.MatchExpression != previous.Spec.MatchExpression {
		if err := handler.validator.ValidateExpression(ctx, candidate.Spec.MatchExpression); err != nil {
			return fmt.Errorf("validate policy expression: %w", err)
		}
	}
	return nil
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

func checkCurrentPolicy(current *domain.EnrollmentApprovalPolicy, version string) error {
	if current == nil {
		return flterrors.ErrResourceNotFound
	}
	if version != lo.FromPtr(current.Metadata.ResourceVersion) {
		return flterrors.ErrResourceVersionConflict
	}
	return nil
}

// Retry only PostgreSQL errors that establish a transaction abort. Each owned
// attempt reads current state in a new transaction. Callers own any outer transaction.
func (handler *ServiceHandler) withPolicyWrite(ctx context.Context, write func(context.Context) error) error {
	if store.InTransaction(ctx) {
		return handler.store.WithTransaction(ctx, write)
	}
	var err error
	for range 3 {
		err = handler.store.WithTransaction(ctx, write)
		abort, ok := err.(*pgconn.PgError)
		if !ok || (abort.Code != "40001" && abort.Code != "40P01") || ctx.Err() != nil {
			return err
		}
	}
	return err
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
	err := handler.withPolicyWrite(ctx, func(txCtx context.Context) error {
		if _, err := handler.store.Get(txCtx, orgID, name); err == nil {
			return flterrors.ErrDuplicateName
		} else if !errors.Is(err, flterrors.ErrResourceNotFound) {
			return err
		}
		credential, err := handler.store.GetCredential(txCtx, orgID, policy.Spec.CertificateName)
		if err != nil {
			return err
		}
		if err := handler.validateCredentialBinding(credential, policy.Spec.CertificateName); err != nil {
			return err
		}
		if err := handler.validatePolicyChange(txCtx, credential, nil, &policy); err != nil {
			return err
		}
		result, previous, _, err = handler.store.Mutate(txCtx, orgID, name, func(m store.ResourceMutation[domain.EnrollmentApprovalPolicy]) error {
			if m.Resource() != nil {
				return flterrors.ErrDuplicateName
			}
			m.SetResource(&policy)
			return nil
		})
		return err
	})
	if err != nil {
		if store.IsEnrollmentPolicyCredentialFKViolation(err) {
			err = unknownCredential(policy.Spec.CertificateName, err)
		}
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

func (handler *ServiceHandler) ReplaceEnrollmentApprovalPolicy(ctx context.Context, orgID uuid.UUID, name string, policy domain.EnrollmentApprovalPolicy, authorize ...ApprovalLimitAuthorizer) (*domain.EnrollmentApprovalPolicy, domain.Status) {
	if policy.Status != nil {
		return nil, domain.NewFailureStatus(400, "ProtectedField", "status is server-owned")
	}
	if policy.Metadata.Name == nil || *policy.Metadata.Name != name {
		return nil, domain.NewFailureStatus(400, "InvalidPolicy", "metadata.name must match the path")
	}
	if policy.Metadata.ResourceVersion == nil {
		return nil, domain.NewFailureStatus(409, "ResourceVersionConflict", "current resourceVersion is required")
	}
	return handler.updatePolicy(ctx, orgID, name, *policy.Metadata.ResourceVersion, authorize, func(_ context.Context, stored *domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
		candidate := policy
		common.NilOutManagedObjectMetaProperties(&candidate.Metadata)
		metadata := stored.Metadata
		store.ApplyObjectMetaUpdate(&metadata, &candidate.Metadata, nil)
		candidate.Metadata = metadata
		return &candidate, nil
	})
}

func (handler *ServiceHandler) PatchEnrollmentApprovalPolicy(ctx context.Context, orgID uuid.UUID, name string, patch domain.PatchRequest, authorize ...ApprovalLimitAuthorizer) (*domain.EnrollmentApprovalPolicy, domain.Status) {
	version := ""
	patch = append(domain.PatchRequest(nil), patch...)
	for i, operation := range patch {
		if operation.Path == "/metadata/resourceVersion" && string(operation.Op) == "test" {
			if version != "" {
				return nil, domain.StatusBadRequest("patch requires exactly one resourceVersion test")
			}
			value, ok := operation.Value.(string)
			if !ok {
				return nil, domain.StatusBadRequest("patch resourceVersion test value must be a string")
			}
			if _, err := strconv.ParseInt(value, 10, 64); err != nil {
				return nil, domain.StatusBadRequest("patch resourceVersion test: " + flterrors.ErrIllegalResourceVersionFormat.Error())
			}
			version = value
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
	return handler.updatePolicy(ctx, orgID, name, version, authorize, func(txCtx context.Context, stored *domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error) {
		input := *stored
		input.Status = nil
		var candidate domain.EnrollmentApprovalPolicy
		if err := applyPolicyPatch(txCtx, &input, &candidate, patch, name); err != nil {
			return nil, InvalidPolicyInputError{Cause: fmt.Errorf("apply patch: %w", err)}
		}
		return &candidate, nil
	})
}

func (handler *ServiceHandler) updatePolicy(ctx context.Context, orgID uuid.UUID, name, version string, authorize []ApprovalLimitAuthorizer, prepare func(context.Context, *domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, error)) (*domain.EnrollmentApprovalPolicy, domain.Status) {
	current, status := handler.GetEnrollmentApprovalPolicy(ctx, orgID, name)
	if current == nil {
		return nil, status
	}
	if err := checkCurrentPolicy(current, version); err != nil {
		return nil, policyErrorToAPIStatus(err, name, false)
	}
	var result, previous *domain.EnrollmentApprovalPolicy
	err := handler.withPolicyWrite(ctx, func(txCtx context.Context) error {
		var err error
		result, previous, _, err = handler.store.Mutate(txCtx, orgID, name, func(m store.ResourceMutation[domain.EnrollmentApprovalPolicy]) error {
			stored := m.Resource()
			if err := checkCurrentPolicy(stored, version); err != nil {
				return err
			}
			candidate, err := prepare(txCtx, stored)
			if err != nil {
				return err
			}
			if candidate.Spec.CertificateName != stored.Spec.CertificateName {
				return flterrors.ErrImmutableCertificateBinding
			}
			var credential *domain.CertificateSigningRequest
			if candidate.Spec.Enabled {
				credential, err = handler.store.GetCredential(txCtx, orgID, stored.Spec.CertificateName)
				if err != nil {
					return err
				}
				if err := handler.validateCredentialBinding(credential, stored.Spec.CertificateName); err != nil {
					return err
				}
			}
			if len(authorize) > 0 && !reflect.DeepEqual(stored.Spec.ApprovalLimit, candidate.Spec.ApprovalLimit) {
				allowed, err := authorize[0](ctx)
				if err != nil || !allowed {
					return approvalLimitAuthorizationError{cause: err}
				}
			}
			if err := handler.validatePolicyChange(txCtx, credential, stored, candidate); err != nil {
				return err
			}
			m.SetResource(candidate)
			return nil
		})
		return err
	})
	if err != nil {
		return nil, policyErrorToAPIStatus(err, name, false)
	}
	handler.updatedEvent(ctx, orgID, result, previous, false)
	return result, domain.StatusOK()
}

func (handler *ServiceHandler) DeleteEnrollmentApprovalPolicy(ctx context.Context, orgID uuid.UUID, name string) domain.Status {
	err := handler.withPolicyWrite(ctx, func(txCtx context.Context) error {
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
