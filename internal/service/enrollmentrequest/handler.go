package enrollmentrequest

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/contextutil"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/service/device"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	enrollmentrequeststore "github.com/flightctl/flightctl/internal/store/enrollmentrequest"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/semaphore"
)

type ServiceHandler struct {
	store      enrollmentrequeststore.Store
	deviceSvc  device.Service
	ca         *crypto.CAClient
	kvStore    kvstore.KVStore
	events     events.Service
	log        logrus.FieldLogger
	tpmCAPaths []string
	agentGate  *semaphore.Weighted
}

// NewServiceHandler creates a new enrollmentrequest ServiceHandler instance.
func NewServiceHandler(store enrollmentrequeststore.Store, deviceSvc device.Service, ca *crypto.CAClient, kvStore kvstore.KVStore, events events.Service, log logrus.FieldLogger, tpmCAPaths []string) *ServiceHandler {
	return &ServiceHandler{
		store:      store,
		deviceSvc:  deviceSvc,
		ca:         ca,
		kvStore:    kvStore,
		events:     events,
		log:        log,
		tpmCAPaths: tpmCAPaths,
		agentGate:  semaphore.NewWeighted(common.MaxConcurrentAgents),
	}
}

var _ Service = (*ServiceHandler)(nil)

// SanitizeEnrollmentRequest clears status and managed metadata from an untrusted enrollment
// request document (HTTP body).
func SanitizeEnrollmentRequest(er *domain.EnrollmentRequest) {
	if er == nil {
		return
	}
	er.Status = nil
	common.NilOutManagedObjectMetaProperties(&er.Metadata)
}

// CreateEnrollmentRequestFromUntrusted sanitizes an untrusted enrollment request document, then creates it.
func CreateEnrollmentRequestFromUntrusted(ctx context.Context, svc Service, orgId uuid.UUID, er domain.EnrollmentRequest) (*domain.EnrollmentRequest, domain.Status) {
	SanitizeEnrollmentRequest(&er)
	return svc.CreateEnrollmentRequest(ctx, orgId, er)
}

// ReplaceEnrollmentRequestFromUntrusted sanitizes an untrusted enrollment request document, then replaces it.
func ReplaceEnrollmentRequestFromUntrusted(ctx context.Context, svc Service, orgId uuid.UUID, name string, er domain.EnrollmentRequest) (*domain.EnrollmentRequest, domain.Status) {
	SanitizeEnrollmentRequest(&er)
	return svc.ReplaceEnrollmentRequest(ctx, orgId, name, er)
}

// getTPMCAPool loads the TPM CA certificates from configured paths
func (h *ServiceHandler) getTPMCAPool() *x509.CertPool {
	if len(h.tpmCAPaths) == 0 {
		return nil
	}

	roots, err := tpm.LoadCAsFromPaths(h.tpmCAPaths)
	if err != nil {
		h.log.Warnf("Failed to load TPM CA certificates from configured paths: %v", err)
		return nil
	}

	return roots
}

func (h *ServiceHandler) verifyTPMEnrollmentRequest(er *domain.EnrollmentRequest, name string) error {
	csrBytes, isTPM := tpm.ParseTCGCSRBytes(er.Spec.Csr)
	if !isTPM {
		return fmt.Errorf("failed to parse TCG CSR")
	}

	trustedRoots := h.getTPMCAPool()
	condition := domain.Condition{
		Type:   domain.ConditionTypeEnrollmentRequestTPMVerified,
		Status: domain.ConditionStatusFalse,
	}
	if err := tpm.VerifyTCGCSRChainOfTrustWithRoots(csrBytes, trustedRoots); err != nil {
		condition.Reason = domain.TPMVerificationFailedReason
		condition.Message = err.Error()
		h.log.Warnf("TPM verification failed for enrollment request %s: %v", name, err)
	} else {
		condition.Reason = domain.TPMChallengeRequiredReason
		condition.Message = "TPM chain of trust partially verified, activate credential challenge required"
		h.log.Debugf("TPM chain partially verified for enrollment request %s, challenge required", name)
	}
	domain.SetStatusCondition(&er.Status.Conditions, condition)
	return nil
}

func approveAndSignEnrollmentRequest(ctx context.Context, ca *crypto.CAClient, enrollmentRequest *domain.EnrollmentRequest, approval *domain.EnrollmentRequestApprovalStatus) error {
	if enrollmentRequest == nil {
		return errors.New("approveAndSignEnrollmentRequest: enrollmentRequest is nil")
	}

	request, _, err := newSignRequestFromEnrollment(ca.Cfg, enrollmentRequest)
	if err != nil {
		return fmt.Errorf("approveAndSignEnrollmentRequest: %w", err)
	}

	certData, err := signer.SignAsPEM(ctx, ca, request)
	if err != nil {
		return fmt.Errorf("approveAndSignEnrollmentRequest: %w", err)
	}

	// preserve existing conditions when approving
	existingConditions := []domain.Condition{}
	if enrollmentRequest.Status != nil && enrollmentRequest.Status.Conditions != nil {
		existingConditions = enrollmentRequest.Status.Conditions
	}

	enrollmentRequest.Status = &domain.EnrollmentRequestStatus{
		Certificate: lo.ToPtr(string(certData)),
		Conditions:  existingConditions,
		Approval:    approval,
	}

	// Merge user-provided labels with agent-provided labels (only if not replacing)
	// If replaceLabels is true, use approval.Labels as-is (the complete final set)
	// If replaceLabels is false/nil (default), merge with agent-provided labels
	replaceLabels := approval.ReplaceLabels != nil && *approval.ReplaceLabels
	if !replaceLabels && enrollmentRequest.Spec.Labels != nil {
		for k, v := range *enrollmentRequest.Spec.Labels {
			// don't override user-provided labels
			if _, ok := (*enrollmentRequest.Status.Approval.Labels)[k]; !ok {
				(*enrollmentRequest.Status.Approval.Labels)[k] = v
			}
		}
	}

	condition := domain.Condition{
		Type:    domain.ConditionTypeEnrollmentRequestApproved,
		Status:  domain.ConditionStatusTrue,
		Reason:  "ManuallyApproved",
		Message: "Approved by " + approval.ApprovedBy,
	}
	domain.SetStatusCondition(&enrollmentRequest.Status.Conditions, condition)
	return nil
}

func addStatusIfNeeded(enrollmentRequest *domain.EnrollmentRequest) {
	if enrollmentRequest.Status == nil {
		enrollmentRequest.Status = &domain.EnrollmentRequestStatus{
			Certificate: nil,
			Conditions:  []domain.Condition{},
		}
	}
}

func (h *ServiceHandler) createDeviceFromEnrollmentRequest(ctx context.Context, orgId uuid.UUID, enrollmentRequest *domain.EnrollmentRequest) error {
	deviceStatus := domain.NewDeviceStatus()
	deviceStatus.Lifecycle = domain.DeviceLifecycleStatus{Status: "Enrolled"}

	// Check if TPM was verified during enrollment request creation
	isTPMVerified := false
	var tpmVerificationError string
	if enrollmentRequest.Status != nil {
		if condition := domain.FindStatusCondition(enrollmentRequest.Status.Conditions, domain.ConditionTypeEnrollmentRequestTPMVerified); condition != nil {
			isTPMVerified = condition.Status == domain.ConditionStatusTrue
			if !isTPMVerified {
				tpmVerificationError = condition.Message
			}
		}
	}

	// always set device integrity status based on TPM verification result
	now := time.Now()
	if isTPMVerified {
		deviceStatus.Integrity = domain.DeviceIntegrityStatus{
			Status:       domain.DeviceIntegrityStatusVerified,
			Info:         lo.ToPtr("All integrity checks completed successfully"),
			LastVerified: &now,
			DeviceIdentity: &domain.DeviceIntegrityCheckStatus{
				Status: domain.DeviceIntegrityCheckStatusVerified,
				Info:   lo.ToPtr("Device identity verified through certificate chain"),
			},
			Tpm: &domain.DeviceIntegrityCheckStatus{
				Status: domain.DeviceIntegrityCheckStatusVerified,
				Info:   lo.ToPtr("TPM chain of trust verified"),
			},
		}
	} else {
		// Check if this was a TCG CSR enrollment attempt that failed verification
		csrBytes := []byte(enrollmentRequest.Spec.Csr)

		if !tpm.IsTCGCSRFormat(csrBytes) {
			decodedBytes, err := base64.StdEncoding.DecodeString(enrollmentRequest.Spec.Csr)
			if err == nil && tpm.IsTCGCSRFormat(decodedBytes) {
				csrBytes = decodedBytes
			}
		}

		if tpm.IsTCGCSRFormat(csrBytes) {
			tpmErrorMsg := "TPM attestation verification failed"
			deviceIdentityMsg := "Device identity verification failed"

			if tpmVerificationError != "" {
				if strings.Contains(tpmVerificationError, "TPM CA certificates not configured") {
					tpmErrorMsg = "TPM CA certificates not configured - cannot verify chain"
					deviceIdentityMsg = "Cannot verify identity - TPM CA certificates not configured"
				} else {
					tpmErrorMsg = fmt.Sprintf("TPM verification failed: %s", tpmVerificationError)
					deviceIdentityMsg = fmt.Sprintf("Identity verification failed: %s", tpmVerificationError)
				}
			}

			deviceStatus.Integrity = domain.DeviceIntegrityStatus{
				Status:       domain.DeviceIntegrityStatusFailed,
				Info:         lo.ToPtr("Integrity verification failed"),
				LastVerified: &now,
				DeviceIdentity: &domain.DeviceIntegrityCheckStatus{
					Status: domain.DeviceIntegrityCheckStatusFailed,
					Info:   lo.ToPtr(deviceIdentityMsg),
				},
				Tpm: &domain.DeviceIntegrityCheckStatus{
					Status: domain.DeviceIntegrityCheckStatusFailed,
					Info:   lo.ToPtr(tpmErrorMsg),
				},
			}
		} else {
			// Device enrolled without TPM - integrity verification not supported
			deviceStatus.Integrity = domain.DeviceIntegrityStatus{
				Status: domain.DeviceIntegrityStatusUnsupported,
				Info:   lo.ToPtr("TPM not present or not enabled on this device"),
				DeviceIdentity: &domain.DeviceIntegrityCheckStatus{
					Status: domain.DeviceIntegrityCheckStatusUnsupported,
				},
				Tpm: &domain.DeviceIntegrityCheckStatus{
					Status: domain.DeviceIntegrityCheckStatusUnsupported,
				},
			}
		}
	}

	name := lo.FromPtr(enrollmentRequest.Metadata.Name)
	apiResource := &domain.Device{
		Metadata: domain.ObjectMeta{
			Name: &name,
		},
		Status: &deviceStatus,
	}
	if errs := apiResource.Validate(); len(errs) > 0 {
		return fmt.Errorf("failed validating new device: %w", errors.Join(errs...))
	}
	if enrollmentRequest.Status != nil && enrollmentRequest.Status.Approval != nil {
		apiResource.Metadata.Labels = enrollmentRequest.Status.Approval.Labels
	}

	// Transfer awaitingReconnect annotation from enrollment request to device if present
	if enrollmentRequest.Metadata.Annotations != nil {
		if awaitingReconnect, exists := (*enrollmentRequest.Metadata.Annotations)[domain.DeviceAnnotationAwaitingReconnect]; exists && awaitingReconnect == "true" {
			if apiResource.Metadata.Annotations == nil {
				apiResource.Metadata.Annotations = &map[string]string{}
			}
			(*apiResource.Metadata.Annotations)[domain.DeviceAnnotationAwaitingReconnect] = "true"

			// Set device status to awaiting reconnection
			deviceStatus.Summary = domain.DeviceSummaryStatus{
				Status: domain.DeviceSummaryStatusAwaitingReconnect,
				Info:   lo.ToPtr(common.DeviceStatusInfoAwaitingReconnect),
			}
			// Add awaiting reconnection key to KV store
			key := kvstore.AwaitingReconnectionKey{
				OrgID:      orgId,
				DeviceName: name,
			}
			keyStr := key.ComposeKey()
			_, err := h.kvStore.SetNX(ctx, keyStr, []byte("true"))
			if err != nil {
				h.log.WithError(err).Errorf("Failed to add awaiting reconnection key for device %s in org %s", name, orgId)
			}
		}
	}
	// CreateDevice owns validation, UpdateServiceSideStatus, and device-created events.
	// apiResource never has Metadata.Owner set above (unenrolled), so the managed-fleet
	// branch remains unreachable (see TestCreateDeviceFromEnrollmentRequestNeverManaged).
	_, status := h.deviceSvc.CreateDevice(ctx, orgId, *apiResource)
	if status.Code == http.StatusConflict {
		return fmt.Errorf("device %s already exists and cannot be overwritten during enrollment request approval", name)
	}
	return common.ApiStatusToErr(status)
}

func (h *ServiceHandler) CreateEnrollmentRequest(ctx context.Context, orgId uuid.UUID, er domain.EnrollmentRequest) (*domain.EnrollmentRequest, domain.Status) {
	addStatusIfNeeded(&er)

	// Check if knownRenderedVersion is provided and not "0", add awaitingReconnect annotation
	if er.Spec.KnownRenderedVersion != nil && *er.Spec.KnownRenderedVersion != "" && *er.Spec.KnownRenderedVersion != "0" {
		annotations := util.EnsureMap(lo.FromPtr(er.Metadata.Annotations))
		annotations[domain.DeviceAnnotationAwaitingReconnect] = "true"
		er.Metadata.Annotations = &annotations
		h.log.Infof("Adding awaitingReconnect annotation for knownRenderedVersion: %s", *er.Spec.KnownRenderedVersion)
	}

	if errs := er.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	request, isTPM, err := newSignRequestFromEnrollment(h.ca.Cfg, &er)
	if err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}
	if err := signer.Verify(ctx, h.ca, request); err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}
	if isTPM {
		if err := h.verifyTPMEnrollmentRequest(&er, *er.Metadata.Name); err != nil {
			return nil, domain.StatusBadRequest(err.Error())
		}
	}
	if _, isAgent := ctx.Value(consts.AgentCtxKey).(string); isAgent {
		if h.agentGate.Acquire(ctx, 1) == nil {
			defer h.agentGate.Release(1)
		}
	}

	setGenerationOnCreate(&er.Metadata)
	result, err := h.store.Create(ctx, orgId, &er)
	h.callbackEnrollmentRequestUpdated(ctx, domain.EnrollmentRequestKind, orgId, lo.FromPtr(er.Metadata.Name), nil, result, true, err)
	return result, common.StoreErrorToApiStatus(err, true, domain.EnrollmentRequestKind, er.Metadata.Name)
}

func (h *ServiceHandler) ListEnrollmentRequests(ctx context.Context, orgId uuid.UUID, params domain.ListEnrollmentRequestsParams) (*domain.EnrollmentRequestList, domain.Status) {
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

func (h *ServiceHandler) GetEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string) (*domain.EnrollmentRequest, domain.Status) {
	if _, isAgent := ctx.Value(consts.AgentCtxKey).(string); isAgent {
		if h.agentGate.Acquire(ctx, 1) == nil {
			defer h.agentGate.Release(1)
		}
	}
	result, err := h.store.Get(ctx, orgId, name)
	return result, common.StoreErrorToApiStatus(err, false, domain.EnrollmentRequestKind, &name)
}

func (h *ServiceHandler) ReplaceEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string, er domain.EnrollmentRequest) (*domain.EnrollmentRequest, domain.Status) {
	addStatusIfNeeded(&er)

	if errs := er.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	err := h.allowCreationOrUpdate(ctx, orgId, name)
	if err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}
	if name != *er.Metadata.Name {
		return nil, domain.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	request, isTPM, err := newSignRequestFromEnrollment(h.ca.Cfg, &er)
	if err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}
	if err := signer.Verify(ctx, h.ca, request); err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}
	if isTPM {
		if err := h.verifyTPMEnrollmentRequest(&er, name); err != nil {
			return nil, domain.StatusBadRequest(err.Error())
		}
	}

	var result, oldEnrollmentRequest *domain.EnrollmentRequest
	var created bool
	err = common.RetryOnNoRowsUpdated(func() error {
		existing, getErr := h.store.Get(ctx, orgId, name)
		if getErr != nil {
			if !errors.Is(getErr, flterrors.ErrResourceNotFound) {
				return getErr
			}
			existing = nil
		}

		toWrite := er
		if existing == nil {
			setGenerationOnCreate(&toWrite.Metadata)
		} else {
			setGenerationOnUpdate(existing, &toWrite)
		}

		var writeErr error
		result, oldEnrollmentRequest, created, writeErr = h.store.CreateOrUpdate(ctx, orgId, &toWrite)
		h.callbackEnrollmentRequestUpdated(ctx, domain.EnrollmentRequestKind, orgId, name, oldEnrollmentRequest, result, created, writeErr)
		return writeErr
	})
	return result, common.StoreErrorToApiStatus(err, created, domain.EnrollmentRequestKind, &name)
}

// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.EnrollmentRequest, domain.Status) {
	currentObj, err := h.store.Get(ctx, orgId, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.EnrollmentRequestKind, &name)
	}

	newObj := &domain.EnrollmentRequest{}
	err = common.ApplyJSONPatch(ctx, currentObj, newObj, patch, "/enrollmentrequests/"+name)
	if err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if errs := currentObj.ValidateUpdate(newObj); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	common.NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	request, isTPM, err := newSignRequestFromEnrollment(h.ca.Cfg, newObj)
	if err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}
	if err := signer.Verify(ctx, h.ca, request); err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}
	if isTPM {
		if err := h.verifyTPMEnrollmentRequest(newObj, name); err != nil {
			return nil, domain.StatusBadRequest(err.Error())
		}
	}

	var result, oldEnrollmentRequest *domain.EnrollmentRequest
	err = common.RetryOnNoRowsUpdated(func() error {
		existing, getErr := h.store.Get(ctx, orgId, name)
		if getErr != nil {
			return getErr
		}

		toWrite := *newObj
		setGenerationOnUpdate(existing, &toWrite)

		var writeErr error
		result, oldEnrollmentRequest, writeErr = h.store.Update(ctx, orgId, &toWrite)
		h.callbackEnrollmentRequestUpdated(ctx, domain.EnrollmentRequestKind, orgId, name, oldEnrollmentRequest, result, false, writeErr)
		return writeErr
	})
	return result, common.StoreErrorToApiStatus(err, false, domain.EnrollmentRequestKind, &name)
}

func (h *ServiceHandler) DeleteEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string) domain.Status {
	exists, err := h.deviceExists(ctx, orgId, name)
	if err != nil {
		return common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
	}

	if exists {
		return domain.StatusConflict(fmt.Sprintf("cannot delete ER %q: device exists", name))
	}

	deleted, err := h.store.Delete(ctx, orgId, name)
	if err == nil && deleted {
		h.callbackEnrollmentRequestDeleted(ctx, domain.EnrollmentRequestKind, orgId, name, nil, nil, false, nil)
	}
	return common.StoreErrorToApiStatus(err, false, domain.EnrollmentRequestKind, &name)
}

func (h *ServiceHandler) GetEnrollmentRequestStatus(ctx context.Context, orgId uuid.UUID, name string) (*domain.EnrollmentRequest, domain.Status) {
	result, err := h.store.Get(ctx, orgId, name)
	return result, common.StoreErrorToApiStatus(err, false, domain.EnrollmentRequestKind, &name)
}

func (h *ServiceHandler) ApproveEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string, approval domain.EnrollmentRequestApproval) (*domain.EnrollmentRequestApprovalStatus, domain.Status) {
	if errs := approval.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	enrollmentReq, err := h.store.Get(ctx, orgId, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.EnrollmentRequestKind, &name)
	}

	approvalStatusToReturn := enrollmentReq.Status.Approval

	// if the enrollment request was already approved we should not try to approve it one more time
	if approval.Approved {
		if domain.IsStatusConditionTrue(enrollmentReq.Status.Conditions, domain.ConditionTypeEnrollmentRequestApproved) {
			return nil, domain.StatusBadRequest("Enrollment request is already approved")
		}

		identity, ok := contextutil.GetMappedIdentityFromContext(ctx)
		if !ok {
			status := domain.StatusInternalServerError("failed to retrieve user identity while approving enrollment request")
			h.events.CreateEvent(ctx, orgId, common.GetEnrollmentRequestApprovalFailedEvent(ctx, name, status, h.log))
			return nil, status
		}

		approvedBy := "unknown"
		if identity != nil && len(identity.GetUsername()) > 0 {
			approvedBy = identity.GetUsername()
		}

		approvalStatus := domain.EnrollmentRequestApprovalStatus{
			Approved:      approval.Approved,
			Labels:        approval.Labels,
			ReplaceLabels: approval.ReplaceLabels,
			ApprovedAt:    time.Now(),
			ApprovedBy:    approvedBy,
		}
		approvalStatusToReturn = &approvalStatus

		err = approveAndSignEnrollmentRequest(ctx, h.ca, enrollmentReq, &approvalStatus)
		if err != nil {
			status := domain.StatusBadRequest(fmt.Sprintf("Error approving and signing enrollment request: %v", err.Error()))
			h.events.CreateEvent(ctx, orgId, common.GetEnrollmentRequestApprovalFailedEvent(ctx, name, status, h.log))
			return nil, status
		}

		// in case of error we return 500 as it will be caused by creating device in db and not by problem with enrollment request
		if err := h.createDeviceFromEnrollmentRequest(ctx, orgId, enrollmentReq); err != nil {
			status := domain.StatusInternalServerError(fmt.Sprintf("error creating device from enrollment request: %v", err))
			h.events.CreateEvent(ctx, orgId, common.GetEnrollmentRequestApprovalFailedEvent(ctx, name, status, h.log))
			return nil, status
		}
	}

	// Update the enrollment request status using the specific approval callback
	_, oldEnrollmentRequest, err := h.store.UpdateStatus(ctx, orgId, enrollmentReq)
	h.callbackEnrollmentRequestApproved(ctx, domain.EnrollmentRequestKind, orgId, name, oldEnrollmentRequest, enrollmentReq, false, err)
	return approvalStatusToReturn, common.StoreErrorToApiStatus(err, false, domain.EnrollmentRequestKind, &name)
}

func (h *ServiceHandler) ReplaceEnrollmentRequestStatus(ctx context.Context, orgId uuid.UUID, name string, er domain.EnrollmentRequest) (*domain.EnrollmentRequest, domain.Status) {
	addStatusIfNeeded(&er)

	result, oldEnrollmentRequest, err := h.store.UpdateStatus(ctx, orgId, &er)
	h.callbackEnrollmentRequestUpdated(ctx, domain.EnrollmentRequestKind, orgId, name, oldEnrollmentRequest, result, false, err)
	return result, common.StoreErrorToApiStatus(err, false, domain.EnrollmentRequestKind, &name)
}

func newSignRequestFromEnrollment(cfg *ca.Config, er *domain.EnrollmentRequest) (signer.SignRequest, bool, error) {
	csrData, isTPM, err := tpm.NormalizeEnrollmentCSR(er.Spec.Csr)
	if err != nil {
		return nil, false, fmt.Errorf("failed to normalize CSR: %w", err)
	}

	var opts []signer.SignRequestOption
	if er.Status != nil && er.Status.Certificate != nil {
		certBytes := []byte(*er.Status.Certificate)
		opts = append(opts, signer.WithIssuedCertificateBytes(certBytes))
	}

	if er.Metadata.Name != nil {
		opts = append(opts, signer.WithResourceName(*er.Metadata.Name))
	}

	request, err := signer.NewSignRequestFromBytes(cfg.DeviceManagementSignerName, csrData, opts...)

	if err != nil {
		return nil, isTPM, err
	}

	return request, isTPM, nil
}

func (h *ServiceHandler) allowCreationOrUpdate(ctx context.Context, orgId uuid.UUID, name string) error {
	dev, status := h.deviceSvc.GetDevice(ctx, orgId, name)
	if status.Code == http.StatusNotFound {
		return nil // Device not found: allow to create or update
	}
	if err := common.ApiStatusToErr(status); err != nil {
		return err
	}
	if dev != nil {
		return flterrors.ErrDuplicateName // Duplicate name: creation blocked
	}
	return nil
}

// deviceExists checks if a device with the given name exists.
// Error is returned if there is an error other than not-found.
func (h *ServiceHandler) deviceExists(ctx context.Context, orgId uuid.UUID, name string) (bool, error) {
	dev, status := h.deviceSvc.GetDevice(ctx, orgId, name)
	if status.Code == http.StatusNotFound {
		return false, nil
	}
	if err := common.ApiStatusToErr(status); err != nil {
		return false, err
	}
	return dev != nil, nil
}

// callbackEnrollmentRequestUpdated is the enrollment request-specific callback that handles enrollment request events
func (h *ServiceHandler) callbackEnrollmentRequestUpdated(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	store.SafeEventCallback(h.log, func() {
		if err != nil {
			status := common.StoreErrorToApiStatus(err, created, string(resourceKind), &name)
			h.events.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, resourceKind, name, status, nil))
		} else {
			// Compute ResourceUpdatedDetails for updates
			var updateDetails *domain.ResourceUpdatedDetails
			if !created {
				var (
					oldEnrollmentRequest, newEnrollmentRequest *domain.EnrollmentRequest
					ok                                         bool
				)
				if oldEnrollmentRequest, newEnrollmentRequest, ok = common.CastResources[domain.EnrollmentRequest](oldResource, newResource); ok && oldEnrollmentRequest != nil && newEnrollmentRequest != nil {
					updateDetails = common.ComputeResourceUpdatedDetails(oldEnrollmentRequest.Metadata, newEnrollmentRequest.Metadata)
				}
			}
			h.events.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, resourceKind, name, updateDetails, h.log, nil))
		}
	})
}

// callbackEnrollmentRequestDeleted is the enrollment request-specific callback that handles enrollment request deletion events
func (h *ServiceHandler) callbackEnrollmentRequestDeleted(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	store.SafeEventCallback(h.log, func() {
		h.events.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
	})
}

// callbackEnrollmentRequestApproved is the enrollment request-specific callback that handles enrollment request approval events
func (h *ServiceHandler) callbackEnrollmentRequestApproved(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	store.SafeEventCallback(h.log, func() {
		if err != nil {
			status := common.StoreErrorToApiStatus(err, created, string(resourceKind), &name)
			h.events.CreateEvent(ctx, orgId, common.GetEnrollmentRequestApprovalFailedEvent(ctx, name, status, h.log))
		} else {
			// For enrollment request approval, we always emit the approved event on successful update
			// since this callback is only called when the approval process succeeds
			h.events.CreateEvent(ctx, orgId, common.GetEnrollmentRequestApprovedEvent(ctx, name, h.log))
		}
	})
}
