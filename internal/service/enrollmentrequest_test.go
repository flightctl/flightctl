package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"os"
	"testing"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

// A dummy callback manager that does nothing.
type dummyCallbackManager struct{}

func (c dummyCallbackManager) EmitEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event) {
}

func newTestServiceHandler(t *testing.T, s store.Store, caClient *crypto.CAClient) (*ServiceHandler, context.Context) {
	logger := log.InitLogs()
	callbackManager := dummyCallbackManager{}
	handler := &ServiceHandler{
		store:        s,
		log:          logger,
		ca:           caClient,
		eventHandler: NewEventHandler(s, worker_client.WorkerClient(callbackManager), logger),
	}
	ctx := context.WithValue(context.Background(), consts.OrganizationIDCtxKey, store.NullOrgId)
	baseIdentity := common.NewBaseIdentity("test", "test-uid", []common.ReportedOrganization{})
	ctx = context.WithValue(ctx, consts.IdentityCtxKey, baseIdentity)
	// Add MappedIdentity for ApproveEnrollmentRequest
	mappedIdentity := identity.NewMappedIdentity("test", "test-uid", []*model.Organization{}, map[string][]string{}, false, nil)
	ctx = context.WithValue(ctx, consts.MappedIdentityCtxKey, mappedIdentity)
	return handler, ctx
}

func createTestEnrollmentRequest(t *testing.T, name string, status *domain.EnrollmentRequestStatus) (*ServiceHandler, context.Context, uuid.UUID, domain.EnrollmentRequest) {
	require := require.New(t)
	testStore := &TestStore{}
	serviceHandler, ctx := newTestServiceHandler(t, testStore, nil)

	testOrgId := uuid.New()
	deviceStatus := domain.NewDeviceStatus()
	enrollmentRequest := domain.EnrollmentRequest{
		ApiVersion: "v1beta1",
		Kind:       "EnrollmentRequest",
		Metadata: domain.ObjectMeta{
			Name:   lo.ToPtr(name),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: domain.EnrollmentRequestSpec{
			Csr:          "TestCSR",
			DeviceStatus: &deviceStatus,
		},
		Status: status,
	}

	_, err := serviceHandler.store.EnrollmentRequest().Create(ctx, testOrgId, &enrollmentRequest, nil)
	require.NoError(err)
	return serviceHandler, ctx, testOrgId, enrollmentRequest
}

func testEnrollmentRequestPatch(t *testing.T, patch domain.PatchRequest) (*domain.EnrollmentRequest, domain.EnrollmentRequest, domain.Status) {
	require := require.New(t)
	serviceHandler, ctx, testOrgId, enrollmentRequest := createTestEnrollmentRequest(t, "validname", nil)
	resp, status := serviceHandler.PatchEnrollmentRequest(ctx, testOrgId, "validname", patch)
	require.NotEqual(statusFailedCode, status.Code)
	return resp, enrollmentRequest, status
}

func TestAlreadyApprovedEnrollmentRequestApprove(t *testing.T) {
	require := require.New(t)

	// Create enrollment request with already approved status
	approvedStatus := &domain.EnrollmentRequestStatus{
		Conditions: []domain.Condition{{
			Type:    domain.ConditionTypeEnrollmentRequestApproved,
			Status:  domain.ConditionStatusTrue,
			Reason:  "ManuallyApproved",
			Message: "Approved by "}},
	}

	serviceHandler, ctx, testOrgId, _ := createTestEnrollmentRequest(t, "foo", approvedStatus)
	approval := domain.EnrollmentRequestApproval{
		Approved: true,
		Labels:   &map[string]string{"label": "value"},
	}

	_, stat := serviceHandler.ApproveEnrollmentRequest(ctx, testOrgId, "foo", approval)
	require.Equal(statusBadRequestCode, stat.Code)
	require.Equal("Enrollment request is already approved", stat.Message)

	event, _ := serviceHandler.store.Event().List(ctx, testOrgId, store.ListParams{})
	require.Len(event.Items, 0)
}

func TestNotFoundReplaceEnrollmentRequestStatus(t *testing.T) {
	require := require.New(t)
	serviceHandler, _ := newTestServiceHandler(t, &TestStore{}, nil)
	ctx := context.Background()

	invalidER := domain.EnrollmentRequest{
		ApiVersion: "v1beta1",
		Kind:       "EnrollmentRequest",
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr("NonExistingName"),
		},
		Spec: domain.EnrollmentRequestSpec{
			Csr: "TestCSR",
		},
	}

	testOrgId := uuid.New()
	_, status := serviceHandler.ReplaceEnrollmentRequestStatus(ctx, testOrgId, "InvalidName", invalidER)

	require.Equal(statusNotFoundCode, status.Code)
}

func TestEnrollmentRequestPatchInvalidRequests(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name         string
		patchRequest domain.PatchRequest
	}{
		{
			name: "replace name with invalid value",
			patchRequest: domain.PatchRequest{
				{Op: "replace", Path: "/metadata/name", Value: func() *interface{} { var v interface{} = "InvalidName"; return &v }()},
			},
		},
		{
			name: "remove name field",
			patchRequest: domain.PatchRequest{
				{Op: "remove", Path: "/metadata/name"},
			},
		},
		{
			name: "replace kind field",
			patchRequest: domain.PatchRequest{
				{Op: "replace", Path: "/kind", Value: func() *interface{} { var v interface{} = "SomeOtherKind"; return &v }()},
			},
		},
		{
			name: "remove kind field",
			patchRequest: domain.PatchRequest{
				{Op: "remove", Path: "/kind"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, status := testEnrollmentRequestPatch(t, tc.patchRequest)
			verifyERPatchFailed(require, status)
		})
	}
}

func verifyERPatchFailed(require *require.Assertions, status domain.Status) {
	require.Equal(statusBadRequestCode, status.Code)
}

func TestApproveEnrollmentRequestUnsupportedIntegrity(t *testing.T) {
	require := require.New(t)

	// Create a temporary directory for certs
	tmpDir, err := os.MkdirTemp("", "flightctl-test-certs")
	require.NoError(err)
	defer os.RemoveAll(tmpDir)

	// Create a CAClient
	caConfig := &ca.Config{
		InternalConfig: &ca.InternalCfg{
			CertStore:        tmpDir,
			CertFile:         "ca.crt",
			KeyFile:          "ca.key",
			SerialFile:       "ca.serial",
			SignerCertName:   "flightctl-test-ca",
			CertValidityDays: 365,
		},
		DeviceManagementSignerName: "device-enrollment",
	}
	caClient, _, err := crypto.EnsureCA(caConfig)
	require.NoError(err)

	// Create a private key for the CSR
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(err)

	// Create a CSR
	csrTemplate := x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName: "test-device-fingerprint-long",
		},
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}
	csrBytes, err := x509.CreateCertificateRequest(rand.Reader, &csrTemplate, privateKey)
	require.NoError(err)
	csrPem := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrBytes})

	// Create a ServiceHandler
	testStore := &TestStore{}
	serviceHandler, ctx := newTestServiceHandler(t, testStore, caClient)
	orgId := store.NullOrgId

	// Create an enrollment request
	enrollmentRequest := domain.EnrollmentRequest{
		ApiVersion: "v1beta1",
		Kind:       "EnrollmentRequest",
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr("test-device-fingerprint-long"),
		},
		Spec: domain.EnrollmentRequestSpec{
			Csr:          string(csrPem),
			DeviceStatus: lo.ToPtr(domain.NewDeviceStatus()),
		},
	}
	_, status := serviceHandler.CreateEnrollmentRequest(ctx, orgId, enrollmentRequest)
	require.Equal(domain.StatusCreated(), status)

	// Approve the enrollment request
	approval := domain.EnrollmentRequestApproval{
		Approved: true,
	}
	_, status = serviceHandler.ApproveEnrollmentRequest(ctx, orgId, "test-device-fingerprint-long", approval)
	require.Equal(domain.StatusOK(), status)

	// Get the device and check its integrity status
	device, err := serviceHandler.store.Device().Get(ctx, orgId, "test-device-fingerprint-long")
	require.NoError(err)
	require.NotNil(device)
	require.NotNil(device.Status)
	require.NotNil(device.Status.Integrity)
	require.Equal(domain.DeviceIntegrityStatusUnsupported, device.Status.Integrity.Status)
	require.NotNil(device.Status.Integrity.DeviceIdentity)
	require.Equal(domain.DeviceIntegrityCheckStatusUnsupported, device.Status.Integrity.DeviceIdentity.Status)
	require.NotNil(device.Status.Integrity.Tpm)
	require.Equal(domain.DeviceIntegrityCheckStatusUnsupported, device.Status.Integrity.Tpm.Status)
}

func TestDeleteEnrollmentRequest(t *testing.T) {
	require := require.New(t)
	serviceHandler, ctx, testOrgId, _ := createTestEnrollmentRequest(t, "test-delete", nil)

	// Verify the enrollment request exists before deletion
	existingER, err := serviceHandler.store.EnrollmentRequest().Get(ctx, testOrgId, "test-delete")
	require.NoError(err)
	require.NotNil(existingER)

	// Delete the enrollment request (which should add a "Denied" condition instead of actually deleting)
	status := serviceHandler.DeleteEnrollmentRequest(ctx, testOrgId, "test-delete")
	require.Equal(domain.StatusOK(), status)

	// Verify the enrollment request still exists in the database
	afterDeleteER, err := serviceHandler.store.EnrollmentRequest().Get(ctx, testOrgId, "test-delete")
	require.NoError(err)
	require.NotNil(afterDeleteER)

	// Verify that a "Denied" condition was added
	require.NotNil(afterDeleteER.Status)
	require.NotNil(afterDeleteER.Status.Conditions)
	deniedCondition := domain.FindStatusCondition(afterDeleteER.Status.Conditions, domain.ConditionTypeEnrollmentRequestDenied)
	require.NotNil(deniedCondition)
	require.Equal(domain.ConditionTypeEnrollmentRequestDenied, deniedCondition.Type)
	require.Equal(domain.ConditionStatusTrue, deniedCondition.Status)
	require.Equal("AdminDeleted", deniedCondition.Reason)
	require.Equal("Enrollment request denied via deletion", deniedCondition.Message)

	// Verify that the deleted enrollment request does not appear in the list
	listResult, listStatus := serviceHandler.ListEnrollmentRequests(ctx, testOrgId, domain.ListEnrollmentRequestsParams{})
	require.Equal(domain.StatusOK(), listStatus)
	require.NotNil(listResult)

	// The "test-delete" enrollment request should not appear in the list because it's been denied/filtered out
	for _, er := range listResult.Items {
		require.NotEqual("test-delete", *er.Metadata.Name, "denied enrollment request should not appear in list")
	}
}

func TestDeleteEnrollmentRequestAlreadyDenied(t *testing.T) {
	require := require.New(t)

	// Create enrollment request with already denied status
	deniedStatus := &domain.EnrollmentRequestStatus{
		Conditions: []domain.Condition{{
			Type:    domain.ConditionTypeEnrollmentRequestDenied,
			Status:  domain.ConditionStatusTrue,
			Reason:  "ManuallyDenied",
			Message: "Denied by admin"}},
	}

	serviceHandler, ctx, testOrgId, _ := createTestEnrollmentRequest(t, "test-already-denied", deniedStatus)

	// Attempt to delete the already denied enrollment request
	status := serviceHandler.DeleteEnrollmentRequest(ctx, testOrgId, "test-already-denied")
	require.Equal(statusConflictCode, status.Code)
	require.Contains(status.Message, "enrollment request \"test-already-denied\" is already denied")
}

func TestDeleteEnrollmentRequestWithDevice(t *testing.T) {
	require := require.New(t)
	serviceHandler, ctx, testOrgId, _ := createTestEnrollmentRequest(t, "test-device-conflict", nil)

	// Create a device with the same name to simulate the conflict case
	device := domain.Device{
		ApiVersion: "v1beta1",
		Kind:       "Device",
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr("test-device-conflict"),
		},
		Spec: &domain.DeviceSpec{},
	}
	_, err := serviceHandler.store.Device().Create(ctx, testOrgId, &device, nil)
	require.NoError(err)

	// Attempt to delete enrollment request when device exists
	status := serviceHandler.DeleteEnrollmentRequest(ctx, testOrgId, "test-device-conflict")
	require.Equal(statusConflictCode, status.Code)
	require.Contains(status.Message, "cannot delete ER \"test-device-conflict\": device exists")
}
