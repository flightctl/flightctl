package enrollmentrequest

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
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
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	enrollmentrequeststore "github.com/flightctl/flightctl/internal/store/enrollmentrequest"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/semaphore"
)

// maxConcurrentAgents bounds the number of concurrent agent-originated enrollment requests
// this handler will process at once. Duplicated from internal/service.MaxConcurrentAgents
// (internal/service/utils.go) rather than imported, since importing the monolithic
// internal/service package here would reintroduce the exact dependency breadth this epic
// is decomposing away.
const maxConcurrentAgents = 15

// ServiceHandler implements Service. It holds the isolated EnrollmentRequest store, the
// isolated Device store (for the three call sites that check/create devices during
// enrollment and approval), the CA client (approval flow signs certificates), the KV store
// (awaiting-reconnect bookkeeping), events, and its own agent-request semaphore. `agentEndpoint`
// is deliberately omitted: none of the 9 in-scope methods reference it today (it is only used
// by GetEnrollmentConfig/GenerateEnrollmentCredential, which are out of this story's scope).
type ServiceHandler struct {
	store       enrollmentrequeststore.Store
	deviceStore devicestore.Store
	ca          *crypto.CAClient
	kvStore     kvstore.KVStore
	events      events.Service
	log         logrus.FieldLogger
	tpmCAPaths  []string
	agentGate   *semaphore.Weighted
}

// NewServiceHandler creates a new enrollmentrequest ServiceHandler instance.
func NewServiceHandler(store enrollmentrequeststore.Store, deviceStore devicestore.Store, ca *crypto.CAClient, kvStore kvstore.KVStore, events events.Service, log logrus.FieldLogger, tpmCAPaths []string) *ServiceHandler {
	return &ServiceHandler{
		store:       store,
		deviceStore: deviceStore,
		ca:          ca,
		kvStore:     kvStore,
		events:      events,
		log:         log,
		tpmCAPaths:  tpmCAPaths,
		agentGate:   semaphore.NewWeighted(maxConcurrentAgents),
	}
}

var _ Service = (*ServiceHandler)(nil)

// deviceOnlyStore adapts a narrow devicestore.Store into the full monolithic store.Store
// shape required by common.UpdateServiceSideStatus's signature. Only Device() is overridden;
// every other accessor (Fleet(), Repository(), ...) panics if called via the embedded nil
// store.Store. This is safe here because createDeviceFromEnrollmentRequest never sets
// Metadata.Owner on the device it builds, so domain.Device.IsManaged() is always false and
// the only branch of UpdateServiceSideStatus that dereferences st.Fleet() is unreachable from
// this call site (see .artifacts/implement/EDM-4677/01-context.md "Handler Dependency
// Decision" for the proof, and handler_test.go's regression test for the enforced invariant).
type deviceOnlyStore struct {
	store.Store
	deviceStore devicestore.Store
}

func (s *deviceOnlyStore) Device() store.Device {
	return &deviceStoreAdapter{Store: s.deviceStore}
}

// deviceStoreAdapter adapts devicestore.Store (internal/store/device.Store) to the store.Device
// interface. Most methods have identical signatures between the two packages (both are defined
// in terms of store.EventCallback / store.ListParams / store.IntegrationTestCallback), so they
// are promoted directly via the embedded devicestore.Store. A handful of methods reference
// package-local named types on each side (DeviceListParams, DeviceStoreValidationCallback,
// DeviceStatusType, CountByOrgAndStatusResult) that are structurally identical but are distinct
// Go types, so those methods are re-declared here with an explicit type conversion.
type deviceStoreAdapter struct {
	devicestore.Store
}

func (a *deviceStoreAdapter) Update(ctx context.Context, orgId uuid.UUID, device *domain.Device, fieldsToUnset []string, fromAPI bool, validationCallback store.DeviceStoreValidationCallback, eventCallback store.EventCallback) (*domain.Device, error) {
	return a.Store.Update(ctx, orgId, device, fieldsToUnset, fromAPI, devicestore.DeviceStoreValidationCallback(validationCallback), eventCallback)
}

func (a *deviceStoreAdapter) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, device *domain.Device, fieldsToUnset []string, fromAPI bool, validationCallback store.DeviceStoreValidationCallback, eventCallback store.EventCallback) (*domain.Device, bool, error) {
	return a.Store.CreateOrUpdate(ctx, orgId, device, fieldsToUnset, fromAPI, devicestore.DeviceStoreValidationCallback(validationCallback), eventCallback)
}

func (a *deviceStoreAdapter) List(ctx context.Context, orgId uuid.UUID, listParams store.DeviceListParams) (*domain.DeviceList, error) {
	return a.Store.List(ctx, orgId, devicestore.DeviceListParams(listParams))
}

func (a *deviceStoreAdapter) SetServiceConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition, callback store.ServiceConditionsCallback) error {
	return a.Store.SetServiceConditions(ctx, orgId, name, conditions, devicestore.ServiceConditionsCallback(callback))
}

func (a *deviceStoreAdapter) CountByOrgAndStatus(ctx context.Context, orgId *uuid.UUID, statusType store.DeviceStatusType, groupByFleet bool) ([]store.CountByOrgAndStatusResult, error) {
	results, err := a.Store.CountByOrgAndStatus(ctx, orgId, devicestore.DeviceStatusType(statusType), groupByFleet)
	if err != nil {
		return nil, err
	}
	converted := make([]store.CountByOrgAndStatusResult, len(results))
	for i, r := range results {
		converted[i] = store.CountByOrgAndStatusResult(r)
	}
	return converted, nil
}

var _ store.Device = (*deviceStoreAdapter)(nil)

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
	_ = common.UpdateServiceSideStatus(ctx, orgId, apiResource, &deviceOnlyStore{deviceStore: h.deviceStore}, h.log)

	_, _, err := h.deviceStore.CreateOrUpdate(ctx, orgId, apiResource, nil, false, func(ctx context.Context, before *domain.Device, after *domain.Device) error {
		// Prevent overwriting existing devices during enrollment request approval
		if before != nil {
			return fmt.Errorf("device %s already exists and cannot be overwritten during enrollment request approval", *after.Metadata.Name)
		}
		return nil
	}, func(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
		// Only invoke callback on success
		if err == nil {
			h.callbackDeviceUpdated(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
		}
	})
	return err
}

func (h *ServiceHandler) CreateEnrollmentRequest(ctx context.Context, orgId uuid.UUID, er domain.EnrollmentRequest) (*domain.EnrollmentRequest, domain.Status) {
	er.Status = nil
	addStatusIfNeeded(&er)

	// don't set fields that are managed by the service for external requests
	if !common.IsInternalRequest(ctx) {
		common.NilOutManagedObjectMetaProperties(&er.Metadata)
	}

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

	// Use fromAPI=false for internal requests to preserve annotations
	result, err := h.store.CreateWithFromAPI(ctx, orgId, &er, false, h.callbackEnrollmentRequestUpdated)
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
	// don't set fields that are managed by the service
	er.Status = nil
	addStatusIfNeeded(&er)
	common.NilOutManagedObjectMetaProperties(&er.Metadata)

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

	result, created, err := h.store.CreateOrUpdate(ctx, orgId, &er, h.callbackEnrollmentRequestUpdated)
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

	result, err := h.store.Update(ctx, orgId, newObj, h.callbackEnrollmentRequestUpdated)
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

	err = h.store.Delete(ctx, orgId, name, h.callbackEnrollmentRequestDeleted)
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
	_, err = h.store.UpdateStatus(ctx, orgId, enrollmentReq, h.callbackEnrollmentRequestApproved)
	return approvalStatusToReturn, common.StoreErrorToApiStatus(err, false, domain.EnrollmentRequestKind, &name)
}

func (h *ServiceHandler) ReplaceEnrollmentRequestStatus(ctx context.Context, orgId uuid.UUID, name string, er domain.EnrollmentRequest) (*domain.EnrollmentRequest, domain.Status) {
	addStatusIfNeeded(&er)

	result, err := h.store.UpdateStatus(ctx, orgId, &er, h.callbackEnrollmentRequestUpdated)
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
	device, err := h.deviceStore.Get(ctx, orgId, name)
	if errors.Is(err, flterrors.ErrResourceNotFound) {
		return nil // Device not found: allow to create or update
	}
	if device != nil {
		return flterrors.ErrDuplicateName // Duplicate name: creation blocked
	}
	return err
}

// deviceExists checks if a device with the given name exists in the store.
// Error is returned if there is an error other than ErrResourceNotFound.
func (h *ServiceHandler) deviceExists(ctx context.Context, orgId uuid.UUID, name string) (bool, error) {
	dev, err := h.deviceStore.Get(ctx, orgId, name)
	if errors.Is(err, flterrors.ErrResourceNotFound) {
		return false, nil
	}
	return dev != nil, err
}

// callbackDeviceUpdated mirrors internal/service/device.go's callbackDeviceUpdated (out of
// this story's scope) so createDeviceFromEnrollmentRequest's device-creation callback keeps
// emitting the same device-updated event it does today.
func (h *ServiceHandler) callbackDeviceUpdated(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.events.HandleDeviceUpdatedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// callbackEnrollmentRequestUpdated is the enrollment request-specific callback that handles enrollment request events
func (h *ServiceHandler) callbackEnrollmentRequestUpdated(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.events.HandleEnrollmentRequestUpdatedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// callbackEnrollmentRequestDeleted is the enrollment request-specific callback that handles enrollment request deletion events
func (h *ServiceHandler) callbackEnrollmentRequestDeleted(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.events.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// callbackEnrollmentRequestApproved is the enrollment request-specific callback that handles enrollment request approval events
func (h *ServiceHandler) callbackEnrollmentRequestApproved(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.events.HandleEnrollmentRequestApprovedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}
