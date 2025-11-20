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

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

// A dummy callback manager that does nothing.
type dummyCallbackManager struct{}

func (c dummyCallbackManager) C(ctx context.Context, resourceKind v1alpha1.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
}

func newTestServiceHandler(t *testing.T, store store.Store, caClient *crypto.CAClient) (*ServiceHandler, context.Context) {
	logger := log.InitLogs()
	callbackManager := dummyCallbackManager{}
	handler := &ServiceHandler{
		store:        store,
		log:          logger,
		ca:           caClient,
		eventHandler: NewEventHandler(store, callbackManager, logger),
	}
	ctx := context.WithValue(context.Background(), consts.OrganizationIDCtxKey, store.NullOrgId)
	identity := &common.Identity{Username: "test"}
	ctx = context.WithValue(ctx, common.IdentityCtxKey, identity)
	return handler, ctx
}

func createTestEnrollmentRequest(t *testing.T, name string, status *v1alpha1.EnrollmentRequestStatus) (*ServiceHandler, context.Context, v1alpha1.EnrollmentRequest) {
	require := require.New(t)
	testStore := &TestStore{}
	serviceHandler, ctx := newTestServiceHandler(t, testStore, nil)

	deviceStatus := v1alpha1.NewDeviceStatus()
	enrollmentRequest := v1alpha1.EnrollmentRequest{
		ApiVersion: "v1",
		Kind:       "EnrollmentRequest",
		Metadata: v1alpha1.ObjectMeta{
			Name:   lo.ToPtr(name),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: v1alpha1.EnrollmentRequestSpec{
			Csr:          "TestCSR",
			DeviceStatus: &deviceStatus,
		},
		Status: status,
	}

	_, err := serviceHandler.store.EnrollmentRequest().Create(ctx, store.NullOrgId, &enrollmentRequest, nil)
	require.NoError(err)
	return serviceHandler, ctx, enrollmentRequest
}

func testEnrollmentRequestPatch(t *testing.T, patch v1alpha1.PatchRequest) (*v1alpha1.EnrollmentRequest, v1alpha1.EnrollmentRequest, v1alpha1.Status) {
	require := require.New(t)
	serviceHandler, ctx, enrollmentRequest := createTestEnrollmentRequest(t, "validname", nil)
	resp, status := serviceHandler.PatchEnrollmentRequest(ctx, "validname", patch)
	require.NotEqual(statusFailedCode, status.Code)
	return resp, enrollmentRequest, status
}

func TestAlreadyApprovedEnrollmentRequestApprove(t *testing.T) {
	require := require.New(t)

	// Create enrollment request with already approved status
	approvedStatus := &v1alpha1.EnrollmentRequestStatus{
		Conditions: []v1alpha1.Condition{{
			Type:    v1alpha1.ConditionTypeEnrollmentRequestApproved,
			Status:  v1alpha1.ConditionStatusTrue,
			Reason:  "ManuallyApproved",
			Message: "Approved by "}},
	}

	serviceHandler, ctx, _ := createTestEnrollmentRequest(t, "foo", approvedStatus)

	approval := v1alpha1.EnrollmentRequestApproval{
		Approved: true,
		Labels:   &map[string]string{"label": "value"},
	}

	_, stat := serviceHandler.ApproveEnrollmentRequest(ctx, "foo", approval)
	require.Equal(statusBadRequestCode, stat.Code)
	require.Equal("Enrollment request is already approved", stat.Message)

	event, _ := serviceHandler.store.Event().List(ctx, store.NullOrgId, store.ListParams{})
	require.Len(event.Items, 0)
}

func TestNotFoundReplaceEnrollmentRequestStatus(t *testing.T) {
	require := require.New(t)
	serviceHandler, _ := newTestServiceHandler(t, &TestStore{}, nil)
	ctx := context.Background()

	invalidER := v1alpha1.EnrollmentRequest{
		ApiVersion: "v1",
		Kind:       "EnrollmentRequest",
		Metadata: v1alpha1.ObjectMeta{
			Name: lo.ToPtr("NonExistingName"),
		},
		Spec: v1alpha1.EnrollmentRequestSpec{
			Csr: "TestCSR",
		},
	}

	_, status := serviceHandler.ReplaceEnrollmentRequestStatus(ctx, "InvalidName", invalidER)

	require.Equal(statusNotFoundCode, status.Code)
}

func TestEnrollmentRequestPatchInvalidRequests(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name         string
		patchRequest v1alpha1.PatchRequest
	}{
		{
			name: "replace name with invalid value",
			patchRequest: v1alpha1.PatchRequest{
				{Op: "replace", Path: "/metadata/name", Value: func() *interface{} { var v interface{} = "InvalidName"; return &v }()},
			},
		},
		{
			name: "remove name field",
			patchRequest: v1alpha1.PatchRequest{
				{Op: "remove", Path: "/metadata/name"},
			},
		},
		{
			name: "replace kind field",
			patchRequest: v1alpha1.PatchRequest{
				{Op: "replace", Path: "/kind", Value: func() *interface{} { var v interface{} = "SomeOtherKind"; return &v }()},
			},
		},
		{
			name: "remove kind field",
			patchRequest: v1alpha1.PatchRequest{
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

func verifyERPatchFailed(require *require.Assertions, status v1alpha1.Status) {
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
		DeviceEnrollmentSignerName: "device-enrollment",
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
	enrollmentRequest := v1alpha1.EnrollmentRequest{
		ApiVersion: "v1",
		Kind:       "EnrollmentRequest",
		Metadata: v1alpha1.ObjectMeta{
			Name: lo.ToPtr("test-device-fingerprint-long"),
		},
		Spec: v1alpha1.EnrollmentRequestSpec{
			Csr: string(csrPem),
		},
	}
	_, status := serviceHandler.CreateEnrollmentRequest(ctx, enrollmentRequest)
	require.Equal(v1alpha1.StatusCreated(), status)

	// Approve the enrollment request
	approval := v1alpha1.EnrollmentRequestApproval{
		Approved: true,
	}
	_, status = serviceHandler.ApproveEnrollmentRequest(ctx, "test-device-fingerprint-long", approval)
	require.Equal(v1alpha1.StatusOK(), status)

	// Get the device and check its integrity status
	device, err := serviceHandler.store.Device().Get(ctx, orgId, "test-device-fingerprint-long")
	require.NoError(err)
	require.NotNil(device)
	require.NotNil(device.Status)
	require.NotNil(device.Status.Integrity)
	require.Equal(v1alpha1.DeviceIntegrityStatusUnsupported, device.Status.Integrity.Status)
	require.NotNil(device.Status.Integrity.DeviceIdentity)
	require.Equal(v1alpha1.DeviceIntegrityCheckStatusUnsupported, device.Status.Integrity.DeviceIdentity.Status)
	require.NotNil(device.Status.Integrity.Tpm)
	require.Equal(v1alpha1.DeviceIntegrityCheckStatusUnsupported, device.Status.Integrity.Tpm.Status)
}