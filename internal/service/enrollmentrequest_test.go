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

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto"
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

func (c dummyCallbackManager) EmitEvent(ctx context.Context, orgId uuid.UUID, event *v1beta1.Event) {
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

func createTestEnrollmentRequest(t *testing.T, name string, status *v1beta1.EnrollmentRequestStatus) (*ServiceHandler, context.Context, uuid.UUID, v1beta1.EnrollmentRequest) {
	require := require.New(t)
	testStore := &TestStore{}
	serviceHandler, ctx := newTestServiceHandler(t, testStore, nil)

	testOrgId := uuid.New()
	deviceStatus := v1beta1.NewDeviceStatus()
	enrollmentRequest := v1beta1.EnrollmentRequest{
		ApiVersion: "v1",
		Kind:       "EnrollmentRequest",
		Metadata: v1beta1.ObjectMeta{
			Name:   lo.ToPtr(name),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: v1beta1.EnrollmentRequestSpec{
			Csr:          "TestCSR",
			DeviceStatus: &deviceStatus,
		},
		Status: status,
	}

	_, err := serviceHandler.store.EnrollmentRequest().Create(ctx, testOrgId, &enrollmentRequest, nil)
	require.NoError(err)
	return serviceHandler, ctx, testOrgId, enrollmentRequest
}

func testEnrollmentRequestPatch(t *testing.T, patch v1beta1.PatchRequest) (*v1beta1.EnrollmentRequest, v1beta1.EnrollmentRequest, v1beta1.Status) {
	require := require.New(t)
	serviceHandler, ctx, testOrgId, enrollmentRequest := createTestEnrollmentRequest(t, "validname", nil)
	resp, status := serviceHandler.PatchEnrollmentRequest(ctx, testOrgId, "validname", patch)
	require.NotEqual(statusFailedCode, status.Code)
	return resp, enrollmentRequest, status
}

func TestAlreadyApprovedEnrollmentRequestApprove(t *testing.T) {
	require := require.New(t)

	// Create enrollment request with already approved status
	approvedStatus := &v1beta1.EnrollmentRequestStatus{
		Conditions: []v1beta1.Condition{{
			Type:    v1beta1.ConditionTypeEnrollmentRequestApproved,
			Status:  v1beta1.ConditionStatusTrue,
			Reason:  "ManuallyApproved",
			Message: "Approved by "}},
	}

	serviceHandler, ctx, testOrgId, _ := createTestEnrollmentRequest(t, "foo", approvedStatus)
	approval := v1beta1.EnrollmentRequestApproval{
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

	invalidER := v1beta1.EnrollmentRequest{
		ApiVersion: "v1",
		Kind:       "EnrollmentRequest",
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr("NonExistingName"),
		},
		Spec: v1beta1.EnrollmentRequestSpec{
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
		patchRequest v1beta1.PatchRequest
	}{
		{
			name: "replace name with invalid value",
			patchRequest: v1beta1.PatchRequest{
				{Op: "replace", Path: "/metadata/name", Value: func() *interface{} { var v interface{} = "InvalidName"; return &v }()},
			},
		},
		{
			name: "remove name field",
			patchRequest: v1beta1.PatchRequest{
				{Op: "remove", Path: "/metadata/name"},
			},
		},
		{
			name: "replace kind field",
			patchRequest: v1beta1.PatchRequest{
				{Op: "replace", Path: "/kind", Value: func() *interface{} { var v interface{} = "SomeOtherKind"; return &v }()},
			},
		},
		{
			name: "remove kind field",
			patchRequest: v1beta1.PatchRequest{
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

func verifyERPatchFailed(require *require.Assertions, status v1beta1.Status) {
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
	enrollmentRequest := v1beta1.EnrollmentRequest{
		ApiVersion: "v1",
		Kind:       "EnrollmentRequest",
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr("test-device-fingerprint-long"),
		},
		Spec: v1beta1.EnrollmentRequestSpec{
			Csr:          string(csrPem),
			DeviceStatus: lo.ToPtr(v1beta1.NewDeviceStatus()),
		},
	}
	_, status := serviceHandler.CreateEnrollmentRequest(ctx, orgId, enrollmentRequest)
	require.Equal(v1beta1.StatusCreated(), status)

	// Approve the enrollment request
	approval := v1beta1.EnrollmentRequestApproval{
		Approved: true,
	}
	_, status = serviceHandler.ApproveEnrollmentRequest(ctx, orgId, "test-device-fingerprint-long", approval)
	require.Equal(v1beta1.StatusOK(), status)

	// Get the device and check its integrity status
	device, err := serviceHandler.store.Device().Get(ctx, orgId, "test-device-fingerprint-long")
	require.NoError(err)
	require.NotNil(device)
	require.NotNil(device.Status)
	require.NotNil(device.Status.Integrity)
	require.Equal(v1beta1.DeviceIntegrityStatusUnsupported, device.Status.Integrity.Status)
	require.NotNil(device.Status.Integrity.DeviceIdentity)
	require.Equal(v1beta1.DeviceIntegrityCheckStatusUnsupported, device.Status.Integrity.DeviceIdentity.Status)
	require.NotNil(device.Status.Integrity.Tpm)
	require.Equal(v1beta1.DeviceIntegrityCheckStatusUnsupported, device.Status.Integrity.Tpm.Status)
}
