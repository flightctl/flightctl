package enrollmentrequest

import (
	"context"
	"errors"
	"math"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AdmissionSnapshot struct {
	Credential     *domain.CertificateSigningRequest
	Policies       []domain.EnrollmentApprovalPolicy
	PolicyRevision int64
	Request        *domain.EnrollmentRequest
}

type AdmissionDecision struct {
	Source     string
	Actor      string
	PolicyName string
	Events     []domain.Event
}

type AdmissionApply func(context.Context, *AdmissionSnapshot) (*AdmissionDecision, error)

var ErrAdmissionBindingUnavailable = errors.New("enrollment credential binding is unavailable")
var ErrAdmissionOutcomeUnknown = errors.New("enrollment admission outcome is unknown")

type AdmissionStore interface {
	Admit(context.Context, uuid.UUID, string, AdmissionApply) (*domain.EnrollmentRequest, error)
	AdmitManual(context.Context, uuid.UUID, string, AdmissionApply) (*domain.EnrollmentRequest, error)
	PendingNames(context.Context, uuid.UUID, string, string, int) ([]string, error)
	PolicyCertificate(context.Context, uuid.UUID, string) (string, error)
	PublishAdmissions(context.Context, uuid.UUID, func(context.Context, uuid.UUID, *domain.Event) error) error
}

func Pending(request *domain.EnrollmentRequest) bool {
	return request.Status == nil || (request.Status.Certificate == nil &&
		(request.Status.Approval == nil || !request.Status.Approval.Approved) &&
		!domain.IsStatusConditionTrue(request.Status.Conditions, domain.ConditionTypeEnrollmentRequestApproved) &&
		!domain.IsStatusConditionTrue(request.Status.Conditions, domain.ConditionTypeEnrollmentRequestDenied))
}

func (s *EnrollmentRequestStore) Admit(ctx context.Context, orgID uuid.UUID, name string, apply AdmissionApply) (*domain.EnrollmentRequest, error) {
	return s.admit(ctx, orgID, name, false, apply)
}

func (s *EnrollmentRequestStore) AdmitManual(ctx context.Context, orgID uuid.UUID, name string, apply AdmissionApply) (*domain.EnrollmentRequest, error) {
	result, err := s.admit(ctx, orgID, name, true, apply)
	var uncertain *admissionCommitError
	if !errors.As(err, &uncertain) {
		return result, err
	}
	resolved, resolveErr := s.resolveAdmission(ctx, uncertain)
	if resolveErr != nil {
		return nil, errors.Join(ErrAdmissionOutcomeUnknown, err, resolveErr)
	}
	if resolved != nil {
		return resolved, nil
	}
	return nil, uncertain.cause
}

type admissionCommitError struct {
	cause           error
	request         model.EnrollmentRequest
	certificateName string
}

func (err *admissionCommitError) Error() string { return err.cause.Error() }
func (err *admissionCommitError) Unwrap() error { return err.cause }

func (s *EnrollmentRequestStore) resolveAdmission(ctx context.Context, attempt *admissionCommitError) (*domain.EnrollmentRequest, error) {
	if attempt.request.AdmissionID == nil {
		return nil, ErrAdmissionOutcomeUnknown
	}
	var result *domain.EnrollmentRequest
	err := store.WithTransaction(ctx, s.dbHandler, func(txCtx context.Context) error {
		tx := s.getDB(txCtx)
		if attempt.certificateName != "" {
			var credential model.CertificateSigningRequest
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND name = ?", attempt.request.OrgID, attempt.certificateName).Find(&credential).Error; err != nil {
				return err
			}
			var policies []model.EnrollmentApprovalPolicy
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND certificate_name = ?", attempt.request.OrgID, attempt.certificateName).Order("name").Find(&policies).Error; err != nil {
				return err
			}
		}
		var current model.EnrollmentRequest
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND name = ?", attempt.request.OrgID, attempt.request.Name).Find(&current).Error; err != nil {
			return err
		}
		var prior model.EnrollmentAdmission
		err := tx.Where("org_id = ? AND name = ? AND admission_id = ?", attempt.request.OrgID, attempt.request.Name, *attempt.request.AdmissionID).Take(&prior).Error
		if errors.Is(err, gorm.ErrRecordNotFound) && current.CreatedAt.Equal(attempt.request.CreatedAt) && current.AdmissionID == nil {
			return nil
		}
		if err != nil {
			return err
		}
		result, err = admissionResult(&prior, &attempt.request, attempt.certificateName)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *EnrollmentRequestStore) admit(ctx context.Context, orgID uuid.UUID, name string, manual bool, apply AdmissionApply) (*domain.EnrollmentRequest, error) {
	if store.InTransaction(ctx) {
		return nil, errors.New("admission requires ownership of the commit boundary")
	}
	var result *domain.EnrollmentRequest
	attempt := &admissionCommitError{}
	bodySucceeded := false
	err := store.WithTransaction(ctx, s.dbHandler, func(txCtx context.Context) (transactionErr error) {
		defer func() { bodySucceeded = transactionErr == nil }()
		tx := s.getDB(txCtx)
		var initial model.EnrollmentRequest
		if err := tx.Where("org_id = ? AND name = ?", orgID, name).Take(&initial).Error; err != nil {
			return store.ErrorFromGormError(err)
		}
		certificateName := ""
		if initial.Status != nil {
			certificateName = lo.FromPtr(initial.Status.Data.EnrollmentCertificateName)
		}
		if certificateName == "" && !manual {
			return ErrAdmissionBindingUnavailable
		}
		var credential model.CertificateSigningRequest
		var policies []model.EnrollmentApprovalPolicy
		if certificateName != "" {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND name = ?", orgID, certificateName).Take(&credential).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrAdmissionBindingUnavailable
				}
				return store.ErrorFromGormError(err)
			}
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND certificate_name = ?", orgID, certificateName).Order("name").Find(&policies).Error; err != nil {
				return err
			}
		}
		var request model.EnrollmentRequest
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND name = ?", orgID, name).Take(&request).Error; err != nil {
			return store.ErrorFromGormError(err)
		}
		currentBinding := ""
		if request.Status != nil {
			currentBinding = lo.FromPtr(request.Status.Data.EnrollmentCertificateName)
		}
		if currentBinding != certificateName || !request.CreatedAt.Equal(initial.CreatedAt) {
			return flterrors.ErrResourceVersionConflict
		}
		attempt.certificateName = certificateName
		defer func() { attempt.request = request }()
		if request.AdmissionID != nil {
			var err error
			result, err = committedAdmission(tx, &request, certificateName)
			return err
		}
		resource, err := request.ToApiResource()
		if err != nil {
			return err
		}
		if !Pending(resource) && (!manual || !manuallyDenied(resource)) {
			result = resource
			return nil
		}
		var credentialResource *domain.CertificateSigningRequest
		if certificateName != "" {
			credentialResource, err = credential.ToApiResource()
			if err != nil {
				return err
			}
		}
		snapshot := &AdmissionSnapshot{Credential: credentialResource, Request: resource, PolicyRevision: credential.PolicySetRevision}
		for _, policy := range policies {
			candidate, err := policy.ToApiResource()
			if err != nil {
				return err
			}
			snapshot.Policies = append(snapshot.Policies, *candidate)
		}
		decision, err := apply(txCtx, snapshot)
		if err != nil {
			return err
		}
		result, err = persistAdmission(tx, &request, &credential, policies, resource, decision)
		return err
	})
	if err != nil {
		if manual && bodySucceeded {
			attempt.cause = err
			return nil, attempt
		}
		return nil, store.ErrorFromGormError(err)
	}
	return result, nil
}

func manuallyDenied(request *domain.EnrollmentRequest) bool {
	status := request.Status
	if status == nil || status.Certificate != nil || (status.Approval != nil && status.Approval.Approved) {
		return false
	}
	denied := domain.FindStatusCondition(status.Conditions, domain.ConditionTypeEnrollmentRequestDenied)
	return denied != nil && denied.Status == domain.ConditionStatusTrue && denied.Reason == "ManuallyDenied"
}

func committedAdmission(tx *gorm.DB, request *model.EnrollmentRequest, certificateName string) (*domain.EnrollmentRequest, error) {
	var prior model.EnrollmentAdmission
	if err := tx.Where("org_id = ? AND name = ? AND admission_id = ?", request.OrgID, request.Name, *request.AdmissionID).Take(&prior).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("committed admission result is unavailable")
		}
		return nil, err
	}
	return admissionResult(&prior, request, certificateName)
}

func admissionResult(prior *model.EnrollmentAdmission, request *model.EnrollmentRequest, certificateName string) (*domain.EnrollmentRequest, error) {
	if prior.EnrollmentRequest == nil {
		return nil, errors.New("committed admission result is unavailable")
	}
	if prior.CertificateName != certificateName || request.Spec == nil || prior.EnrollmentRequest.Data.Spec.Csr != request.Spec.Data.Csr || prior.EnrollmentRequest.Data.Metadata.CreationTimestamp == nil || !prior.EnrollmentRequest.Data.Metadata.CreationTimestamp.Equal(request.CreatedAt) {
		return nil, flterrors.ErrResourceVersionConflict
	}
	return &prior.EnrollmentRequest.Data, nil
}

func persistAdmission(tx *gorm.DB, request *model.EnrollmentRequest, credential *model.CertificateSigningRequest, policies []model.EnrollmentApprovalPolicy, resource *domain.EnrollmentRequest, decision *AdmissionDecision) (*domain.EnrollmentRequest, error) {
	updates := map[string]interface{}{"status": model.MakeJSONField(resource.Status), "labels": lo.FromPtr(resource.Metadata.Labels), "resource_version": gorm.Expr("COALESCE(resource_version, 0) + 1")}
	var admission *model.EnrollmentAdmission
	if decision != nil {
		var err error
		admission, err = prepareAdmission(tx, request, credential, policies, resource, decision)
		if err != nil {
			return nil, err
		}
		updates["admission_id"] = admission.AdmissionID
	}
	if err := tx.Model(request).Where("org_id = ? AND name = ?", request.OrgID, request.Name).Clauses(clause.Returning{}).Updates(updates).Error; err != nil {
		return nil, err
	}
	result, err := request.ToApiResource()
	if err != nil {
		return nil, err
	}
	if admission != nil {
		admission.EnrollmentRequest = model.MakeJSONField(*result)
		if err := tx.Create(admission).Error; err != nil {
			return nil, err
		}
	}
	return result, nil
}

func prepareAdmission(tx *gorm.DB, request *model.EnrollmentRequest, credential *model.CertificateSigningRequest, policies []model.EnrollmentApprovalPolicy, resource *domain.EnrollmentRequest, decision *AdmissionDecision) (*model.EnrollmentAdmission, error) {
	if Pending(resource) || resource.Status == nil || resource.Status.Certificate == nil {
		return nil, errors.New("admission did not produce an approved certificate")
	}
	if decision.Source != "manual" && decision.Source != "policy" {
		return nil, errors.New("admission source is invalid")
	}
	policyVersion := ""
	if decision.Source == "policy" {
		var err error
		policyVersion, err = countAdmission(tx, policies, decision.PolicyName)
		if err != nil {
			return nil, err
		}
	}
	var device model.Device
	if err := tx.Where("org_id = ? AND name = ?", request.OrgID, request.Name).Take(&device).Error; err != nil {
		return nil, err
	}
	deviceResource, err := device.ToApiResource()
	if err != nil {
		return nil, err
	}
	return &model.EnrollmentAdmission{
		Resource: model.Resource{OrgID: request.OrgID, Name: request.Name}, AdmissionID: uuid.New(),
		Source: decision.Source, Actor: decision.Actor, CertificateName: credential.Name,
		PolicyName: decision.PolicyName, PolicyRevision: credential.PolicySetRevision, PolicyVersion: policyVersion,
		Device: model.MakeJSONField(*deviceResource), Events: model.MakeJSONField(decision.Events),
	}, nil
}

func countAdmission(tx *gorm.DB, policies []model.EnrollmentApprovalPolicy, name string) (string, error) {
	var selected *model.EnrollmentApprovalPolicy
	for index := range policies {
		if policies[index].Name == name {
			selected = &policies[index]
			break
		}
	}
	if selected == nil || selected.Spec == nil || !selected.Spec.Data.Enabled || selected.Status == nil {
		return "", flterrors.ErrResourceVersionConflict
	}
	count := selected.Status.Data.EnrollmentCount
	limit := selected.Spec.Data.ApprovalLimit
	if count < 0 || count == math.MaxInt64 || (limit != nil && count >= *limit) {
		return "", errors.New("policy approval limit reached")
	}
	policy, err := selected.ToApiResource()
	if err != nil {
		return "", err
	}
	selected.Status.Data.EnrollmentCount++
	err = tx.Model(selected).Where("org_id = ? AND name = ?", selected.OrgID, selected.Name).Updates(map[string]interface{}{"status": selected.Status, "resource_version": gorm.Expr("COALESCE(resource_version, 0) + 1")}).Error
	return lo.FromPtr(policy.Metadata.ResourceVersion), err
}

func (s *EnrollmentRequestStore) PendingNames(ctx context.Context, orgID uuid.UUID, certificate, after string, limit int) ([]string, error) {
	var records []model.EnrollmentRequest
	query := s.getDB(ctx).Where("org_id = ? AND name > ?", orgID, after).Order("name").Limit(limit)
	if certificate != "" {
		query = query.Where("status->>'enrollmentCertificateName' = ?", certificate)
	}
	query = query.Where("COALESCE(status->'approval'->>'approved', 'false') <> 'true' AND status->>'certificate' IS NULL").Where(`NOT jsonb_path_exists(COALESCE(status, '{}'::jsonb), '$.conditions[*] ? ((@.type == "Approved" || @.type == "Denied") && @.status == "True")')`)
	if err := query.Find(&records).Error; err != nil {
		return nil, err
	}
	names := make([]string, 0, len(records))
	for _, record := range records {
		names = append(names, record.Name)
	}
	return names, nil
}

func (s *EnrollmentRequestStore) PolicyCertificate(ctx context.Context, orgID uuid.UUID, name string) (string, error) {
	var policy model.EnrollmentApprovalPolicy
	err := s.getDB(ctx).Unscoped().Where("org_id = ? AND name = ?", orgID, name).Take(&policy).Error
	return policy.CertificateName, store.ErrorFromGormError(err)
}

func (s *EnrollmentRequestStore) PublishAdmissions(ctx context.Context, orgID uuid.UUID, publish func(context.Context, uuid.UUID, *domain.Event) error) error {
	if store.InTransaction(ctx) {
		return errors.New("admission publication requires committed state")
	}
	return store.WithTransaction(ctx, s.dbHandler, func(txCtx context.Context) error {
		var admissions []model.EnrollmentAdmission
		tx := s.getDB(txCtx)
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("org_id = ? AND published = false", orgID).Order("name").Limit(100).Find(&admissions).Error; err != nil {
			return err
		}
		for _, admission := range admissions {
			if admission.Events != nil {
				for _, event := range admission.Events.Data {
					if err := publish(ctx, orgID, &event); err != nil {
						return err
					}
				}
			}
			if err := tx.Model(&admission).Update("published", true).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
