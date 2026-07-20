package certificatesigningrequest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"testing"

	cacfg "github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	enrollmentrequeststore "github.com/flightctl/flightctl/internal/store/enrollmentrequest"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

const (
	statusSuccessCode    = int32(200)
	statusCreatedCode    = int32(201)
	statusBadRequestCode = int32(400)
	statusNotFoundCode   = int32(404)
	statusConflictCode   = int32(409)
)

// fakeCertificateSigningRequestStore is a small in-memory implementation of
// internal/store/certificatesigningrequest.Store.
type fakeCertificateSigningRequestStore struct {
	items map[string]*domain.CertificateSigningRequest
}

func newFakeCertificateSigningRequestStore() *fakeCertificateSigningRequestStore {
	return &fakeCertificateSigningRequestStore{items: map[string]*domain.CertificateSigningRequest{}}
}

func (f *fakeCertificateSigningRequestStore) InitialMigration(ctx context.Context) error { return nil }

func (f *fakeCertificateSigningRequestStore) Create(ctx context.Context, orgId uuid.UUID, req *domain.CertificateSigningRequest, eventCallback store.EventCallback) (*domain.CertificateSigningRequest, error) {
	name := lo.FromPtr(req.Metadata.Name)
	if _, exists := f.items[name]; exists {
		return nil, flterrors.ErrDuplicateName
	}
	// Mirror internal/store/model.NewCertificateSigningRequestFromApiResource, which always
	// defaults Status to a non-nil, empty-conditions struct regardless of the caller's input.
	if req.Status == nil {
		req.Status = &domain.CertificateSigningRequestStatus{Conditions: []domain.Condition{}}
	}
	f.items[name] = req
	if eventCallback != nil {
		eventCallback(ctx, domain.CertificateSigningRequestKind, orgId, name, nil, req, true, nil)
	}
	return req, nil
}

func (f *fakeCertificateSigningRequestStore) Update(ctx context.Context, orgId uuid.UUID, req *domain.CertificateSigningRequest, eventCallback store.EventCallback) (*domain.CertificateSigningRequest, error) {
	name := lo.FromPtr(req.Metadata.Name)
	old, exists := f.items[name]
	if !exists {
		return nil, flterrors.ErrResourceNotFound
	}
	// Mirror internal/store/model.NewCertificateSigningRequestFromApiResource, which always
	// defaults Status to a non-nil, empty-conditions struct regardless of the caller's input.
	if req.Status == nil {
		req.Status = &domain.CertificateSigningRequestStatus{Conditions: []domain.Condition{}}
	}
	f.items[name] = req
	if eventCallback != nil {
		eventCallback(ctx, domain.CertificateSigningRequestKind, orgId, name, old, req, false, nil)
	}
	return req, nil
}

func (f *fakeCertificateSigningRequestStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, req *domain.CertificateSigningRequest, eventCallback store.EventCallback) (*domain.CertificateSigningRequest, bool, error) {
	name := lo.FromPtr(req.Metadata.Name)
	if _, exists := f.items[name]; exists {
		result, err := f.Update(ctx, orgId, req, eventCallback)
		return result, false, err
	}
	result, err := f.Create(ctx, orgId, req, eventCallback)
	return result, true, err
}

func (f *fakeCertificateSigningRequestStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.CertificateSigningRequest, error) {
	csr, ok := f.items[name]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	return csr, nil
}

func (f *fakeCertificateSigningRequestStore) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.CertificateSigningRequestList, error) {
	var items []domain.CertificateSigningRequest
	for _, csr := range f.items {
		items = append(items, *csr)
	}
	return &domain.CertificateSigningRequestList{Items: items}, nil
}

func (f *fakeCertificateSigningRequestStore) Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback store.EventCallback) error {
	if _, exists := f.items[name]; !exists {
		return nil
	}
	delete(f.items, name)
	if eventCallback != nil {
		eventCallback(ctx, domain.CertificateSigningRequestKind, orgId, name, nil, nil, false, nil)
	}
	return nil
}

func (f *fakeCertificateSigningRequestStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, req *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, error) {
	name := lo.FromPtr(req.Metadata.Name)
	if _, exists := f.items[name]; !exists {
		return nil, flterrors.ErrResourceNotFound
	}
	f.items[name] = req
	return req, nil
}

func (f *fakeCertificateSigningRequestStore) UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition) error {
	csr, exists := f.items[name]
	if !exists {
		return flterrors.ErrResourceNotFound
	}
	csr.Status.Conditions = conditions
	return nil
}

// fakeEnrollmentRequestStore embeds enrollmentrequeststore.Store (nil) and overrides only Get,
// the sole method verifyTPMCSRRequest calls.
type fakeEnrollmentRequestStore struct {
	enrollmentrequeststore.Store
	items map[string]*domain.EnrollmentRequest
}

func newFakeEnrollmentRequestStore() *fakeEnrollmentRequestStore {
	return &fakeEnrollmentRequestStore{items: map[string]*domain.EnrollmentRequest{}}
}

func (f *fakeEnrollmentRequestStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.EnrollmentRequest, error) {
	er, ok := f.items[name]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	return er, nil
}

// fakeEventsService is a recording fake for events.Service. CertificateSigningRequest's own
// event decision logic (in handler.go's callbackCertificateSigningRequestUpdated) now calls
// CreateEvent directly, so tests assert on the actual emitted events rather than intercepting
// the removed HandleCertificateSigningRequestUpdatedEvents method.
type fakeEventsService struct {
	events.Service
	created []*domain.Event
	deleted []string
}

func (f *fakeEventsService) CreateEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event) {
	if event == nil {
		return
	}
	f.created = append(f.created, event)
}

func (f *fakeEventsService) HandleGenericResourceDeletedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	f.deleted = append(f.deleted, name)
}

func newTestCA(t *testing.T) (*crypto.CAClient, *cacfg.Config) {
	cfg := cacfg.NewDefault(t.TempDir())
	caClient, _, err := crypto.EnsureCA(cfg)
	require.NoError(t, err)
	return caClient, cfg
}

func newTestHandler(t *testing.T) (*ServiceHandler, *fakeCertificateSigningRequestStore, *fakeEnrollmentRequestStore, *fakeEventsService, *cacfg.Config) {
	csrStore := newFakeCertificateSigningRequestStore()
	erStore := newFakeEnrollmentRequestStore()
	ev := &fakeEventsService{}
	caClient, cfg := newTestCA(t)
	logger := logrus.New()
	return NewServiceHandler(csrStore, erStore, caClient, ev, logger, "", ""), csrStore, erStore, ev, cfg
}

// csrPEM generates a throwaway PEM-encoded PKCS#10 CSR for the given common name.
func csrPEM(t *testing.T, cn string) []byte {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	csrTemplate := x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: cn},
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}
	csrBytes, err := x509.CreateCertificateRequest(rand.Reader, &csrTemplate, privateKey)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrBytes})
}

// validUsages returns the minimal spec.usages set required by CertificateSigningRequest.Validate().
func validUsages() *[]string {
	return &[]string{"clientAuth", "CA:false"}
}

// tcgCSRHeaderBytes returns the minimal byte sequence internal/tpm.IsTCGCSRFormat recognizes
// as TCG-CSR-IDEVID version 1.0 (the leading 4-byte big-endian version marker 0x01000100),
// padded to the parser's 12-byte minimum length. It is not a fully parseable TCG CSR, so it is
// only used in tests that exercise verifyTPMCSRRequest's early return paths (owner-kind check,
// enrollment-request lookup, non-TPM-verified enrollment request) that never reach the deeper
// tpm.ParseTCGCSR/tpm.VerifyTCGCSRSigningChain calls.
func tcgCSRHeaderBytes() []byte {
	return []byte{0x01, 0x00, 0x01, 0x00, 0, 0, 0, 0, 0, 0, 0, 0}
}

func TestValidateAllowedSignersForCSRService(t *testing.T) {
	cfg := cacfg.NewDefault(t.TempDir())
	ca, _, err := crypto.EnsureCA(cfg)
	require.NoError(t, err)

	h := &ServiceHandler{ca: ca}

	cases := []struct {
		name       string
		signerName string
		wantErr    bool
	}{
		{
			name:       "When device management signer is used it should be rejected",
			signerName: cfg.DeviceManagementSignerName,
			wantErr:    true,
		},
		{
			name:       "When server svc signer is used it should be rejected",
			signerName: cfg.ServerSvcSignerName,
			wantErr:    true,
		},
		{
			name:       "When enrollment signer is used it should be accepted",
			signerName: cfg.DeviceEnrollmentSignerName,
			wantErr:    false,
		},
		{
			name:       "When device management renewal signer is used it should be accepted",
			signerName: cfg.DeviceManagementRenewalSignerName,
			wantErr:    false,
		},
		{
			name:       "When device svc client signer is used it should be accepted",
			signerName: cfg.DeviceSvcClientSignerName,
			wantErr:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			csr := &domain.CertificateSigningRequest{
				Spec: domain.CertificateSigningRequestSpec{
					SignerName: tc.signerName,
				},
			}
			err := h.validateAllowedSignersForCSRService(csr)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestUpdateCertificateSigningRequestApprovalServerSvc(t *testing.T) {
	cfg := cacfg.NewDefault(t.TempDir())

	superAdminCtx := context.WithValue(context.Background(), consts.MappedIdentityCtxKey,
		identity.NewMappedIdentity("admin", "uid-admin", nil, nil, true, nil))
	nonAdminCtx := context.WithValue(context.Background(), consts.MappedIdentityCtxKey,
		identity.NewMappedIdentity("user", "uid-user", nil, nil, false, nil))

	cases := []struct {
		name       string
		ctx        context.Context
		signerName string
		wantErr    bool
	}{
		{
			name:       "When server svc signer approval is by super-admin it should be accepted",
			ctx:        superAdminCtx,
			signerName: cfg.ServerSvcSignerName,
			wantErr:    false,
		},
		{
			name:       "When server svc signer approval is by non-super-admin it should be rejected",
			ctx:        nonAdminCtx,
			signerName: cfg.ServerSvcSignerName,
			wantErr:    true,
		},
		{
			name:       "When server svc signer approval is without identity it should be rejected",
			ctx:        context.Background(),
			signerName: cfg.ServerSvcSignerName,
			wantErr:    true,
		},
		{
			name:       "When non-server-svc signer approval is by non-super-admin it should be accepted",
			ctx:        nonAdminCtx,
			signerName: cfg.DeviceEnrollmentSignerName,
			wantErr:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkServerSvcApprovalPrivilege(tc.ctx, tc.signerName, cfg.ServerSvcSignerName)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestCreateCertificateSigningRequest(t *testing.T) {
	t.Run("When the enrollment signer is used it should auto-approve and sign", func(t *testing.T) {
		h, fakeStore, _, fakeEvents, _ := newTestHandler(t)
		cn := "test-csr-autoapprove"
		csr := domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(cn)},
			Spec:     domain.CertificateSigningRequestSpec{SignerName: "enrollment", Request: csrPEM(t, cn), Usages: validUsages()},
		}

		result, status := h.CreateCertificateSigningRequest(context.Background(), uuid.New(), csr)
		require.Equal(t, statusCreatedCode, status.Code)
		require.NotNil(t, result)
		require.True(t, domain.IsStatusConditionTrue(result.Status.Conditions, domain.ConditionTypeCertificateSigningRequestApproved))
		require.NotNil(t, result.Status.Certificate)
		require.Contains(t, fakeStore.items, cn)
		require.Len(t, fakeEvents.created, 1)
		require.Equal(t, domain.EventReasonResourceCreated, fakeEvents.created[0].Reason)
	})

	t.Run("When managed metadata fields are set by the caller CreateCertificateSigningRequestFromUntrusted should clear them before creation", func(t *testing.T) {
		h, fakeStore, _, _, _ := newTestHandler(t)
		cn := "untrusted-csr"
		csr := domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{
				Name:       lo.ToPtr(cn),
				Owner:      lo.ToPtr("someone"),
				Generation: lo.ToPtr(int64(5)),
			},
			Spec: domain.CertificateSigningRequestSpec{SignerName: "enrollment", Request: csrPEM(t, cn), Usages: validUsages()},
		}

		_, status := CreateCertificateSigningRequestFromUntrusted(context.Background(), h, uuid.New(), csr)
		require.Equal(t, statusCreatedCode, status.Code)
		require.Nil(t, fakeStore.items[cn].Metadata.Owner)
		require.Nil(t, fakeStore.items[cn].Metadata.Generation)
	})

	t.Run("When managed metadata fields are set by the caller CreateCertificateSigningRequest (trusted) should preserve them", func(t *testing.T) {
		h, fakeStore, _, _, _ := newTestHandler(t)
		cn := "trusted-csr"
		csr := domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{
				Name:       lo.ToPtr(cn),
				Owner:      lo.ToPtr("someone"),
				Generation: lo.ToPtr(int64(5)),
			},
			Spec: domain.CertificateSigningRequestSpec{SignerName: "enrollment", Request: csrPEM(t, cn), Usages: validUsages()},
		}

		_, status := h.CreateCertificateSigningRequest(context.Background(), uuid.New(), csr)
		require.Equal(t, statusCreatedCode, status.Code)
		require.Equal(t, "someone", lo.FromPtr(fakeStore.items[cn].Metadata.Owner))
		require.Equal(t, int64(5), lo.FromPtr(fakeStore.items[cn].Metadata.Generation))
	})

	t.Run("When the device-management signer is used it should be rejected", func(t *testing.T) {
		h, _, _, _, cfg := newTestHandler(t)
		cn := "test-csr-rejected"
		csr := domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(cn)},
			Spec:     domain.CertificateSigningRequestSpec{SignerName: cfg.DeviceManagementSignerName, Request: csrPEM(t, cn)},
		}

		_, status := h.CreateCertificateSigningRequest(context.Background(), uuid.New(), csr)
		require.Equal(t, statusBadRequestCode, status.Code)
	})

	t.Run("When the CSR is malformed it should return bad request", func(t *testing.T) {
		h, _, _, _, _ := newTestHandler(t)
		csr := domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("test-csr-malformed")},
			Spec:     domain.CertificateSigningRequestSpec{SignerName: "enrollment", Request: []byte("not a csr")},
		}

		_, status := h.CreateCertificateSigningRequest(context.Background(), uuid.New(), csr)
		require.Equal(t, statusBadRequestCode, status.Code)
	})
}

func TestListCertificateSigningRequests(t *testing.T) {
	h, fakeStore, _, _, _ := newTestHandler(t)
	csr := domain.CertificateSigningRequest{Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")}}
	fakeStore.items["foo"] = &csr

	result, status := h.ListCertificateSigningRequests(context.Background(), uuid.New(), domain.ListCertificateSigningRequestsParams{})
	require.Equal(t, statusSuccessCode, status.Code)
	require.Len(t, result.Items, 1)
}

func TestGetCertificateSigningRequest(t *testing.T) {
	h, fakeStore, _, _, _ := newTestHandler(t)
	csr := domain.CertificateSigningRequest{Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")}}
	fakeStore.items["foo"] = &csr

	result, status := h.GetCertificateSigningRequest(context.Background(), uuid.New(), "foo")
	require.Equal(t, statusSuccessCode, status.Code)
	require.Equal(t, "foo", lo.FromPtr(result.Metadata.Name))

	_, status = h.GetCertificateSigningRequest(context.Background(), uuid.New(), "missing")
	require.Equal(t, statusNotFoundCode, status.Code)
}

func TestDeleteCertificateSigningRequest(t *testing.T) {
	h, fakeStore, _, fakeEvents, _ := newTestHandler(t)
	csr := domain.CertificateSigningRequest{Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")}}
	fakeStore.items["foo"] = &csr

	status := h.DeleteCertificateSigningRequest(context.Background(), uuid.New(), "foo")
	require.Equal(t, statusSuccessCode, status.Code)
	require.NotContains(t, fakeStore.items, "foo")
	require.Len(t, fakeEvents.deleted, 1)
}

func TestPatchCertificateSigningRequest(t *testing.T) {
	h, fakeStore, _, _, _ := newTestHandler(t)
	cn := "test-csr-patch"
	csr := domain.CertificateSigningRequest{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr(cn), Labels: &map[string]string{"a": "b"}},
		Spec:     domain.CertificateSigningRequestSpec{SignerName: "enrollment", Request: csrPEM(t, cn), Usages: validUsages()},
		Status:   &domain.CertificateSigningRequestStatus{},
	}
	fakeStore.items[cn] = &csr

	t.Run("When the patch attempts to change metadata.name it should fail", func(t *testing.T) {
		var value interface{} = "bar"
		patch := domain.PatchRequest{{Op: "replace", Path: "/metadata/name", Value: &value}}
		_, status := h.PatchCertificateSigningRequest(context.Background(), uuid.New(), cn, patch)
		require.Equal(t, statusBadRequestCode, status.Code)
	})

	t.Run("When the resource does not exist it should return not found", func(t *testing.T) {
		var value interface{} = "bar"
		patch := domain.PatchRequest{{Op: "replace", Path: "/metadata/labels/a", Value: &value}}
		_, status := h.PatchCertificateSigningRequest(context.Background(), uuid.New(), "missing", patch)
		require.Equal(t, statusNotFoundCode, status.Code)
	})
}

func TestReplaceCertificateSigningRequest(t *testing.T) {
	h, fakeStore, _, _, _ := newTestHandler(t)
	cn := "test-csr-replace"
	csr := domain.CertificateSigningRequest{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr(cn)},
		Spec:     domain.CertificateSigningRequestSpec{SignerName: "enrollment", Request: csrPEM(t, cn), Usages: validUsages()},
	}
	fakeStore.items[cn] = &csr

	result, status := h.ReplaceCertificateSigningRequest(context.Background(), uuid.New(), cn, csr)
	require.Equal(t, statusSuccessCode, status.Code)
	require.NotNil(t, result)

	t.Run("When managed metadata fields are set by the caller ReplaceCertificateSigningRequestFromUntrusted should clear them before replacing", func(t *testing.T) {
		h, fakeStore, _, _, _ := newTestHandler(t)
		orgId := uuid.New()
		cn := "replace-untrusted"
		csr := domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{
				Name:       lo.ToPtr(cn),
				Owner:      lo.ToPtr("someone"),
				Generation: lo.ToPtr(int64(5)),
			},
			Spec: domain.CertificateSigningRequestSpec{SignerName: "enrollment", Request: csrPEM(t, cn), Usages: validUsages()},
		}
		fakeStore.items[cn] = &csr

		_, status := ReplaceCertificateSigningRequestFromUntrusted(context.Background(), h, orgId, cn, csr)
		require.Equal(t, statusSuccessCode, status.Code)
		require.Nil(t, fakeStore.items[cn].Metadata.Owner)
		require.Nil(t, fakeStore.items[cn].Metadata.Generation)
	})

	t.Run("When managed metadata fields are set by the caller ReplaceCertificateSigningRequest (trusted) should preserve them", func(t *testing.T) {
		h, fakeStore, _, _, _ := newTestHandler(t)
		orgId := uuid.New()
		cn := "replace-trusted"
		csr := domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{
				Name:       lo.ToPtr(cn),
				Owner:      lo.ToPtr("someone"),
				Generation: lo.ToPtr(int64(5)),
			},
			Spec: domain.CertificateSigningRequestSpec{SignerName: "enrollment", Request: csrPEM(t, cn), Usages: validUsages()},
		}
		fakeStore.items[cn] = &csr

		_, status := h.ReplaceCertificateSigningRequest(context.Background(), orgId, cn, csr)
		require.Equal(t, statusSuccessCode, status.Code)
		require.Equal(t, "someone", lo.FromPtr(fakeStore.items[cn].Metadata.Owner))
		require.Equal(t, int64(5), lo.FromPtr(fakeStore.items[cn].Metadata.Generation))
	})
}

func TestUpdateCertificateSigningRequestApproval(t *testing.T) {
	t.Run("When approving a pending CSR it should sign it", func(t *testing.T) {
		h, fakeStore, _, _, cfg := newTestHandler(t)
		cn := "test-csr-approve"
		csr := domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(cn)},
			Spec:     domain.CertificateSigningRequestSpec{SignerName: cfg.DeviceSvcClientSignerName, Request: csrPEM(t, cn), Usages: validUsages()},
			Status:   &domain.CertificateSigningRequestStatus{Conditions: []domain.Condition{}},
		}
		fakeStore.items[cn] = &csr

		approval := csr
		approval.Status = &domain.CertificateSigningRequestStatus{
			Conditions: []domain.Condition{{
				Type:   domain.ConditionTypeCertificateSigningRequestApproved,
				Status: domain.ConditionStatusTrue,
			}},
		}

		result, status := h.UpdateCertificateSigningRequestApproval(context.Background(), uuid.New(), cn, approval)
		require.Equal(t, statusSuccessCode, status.Code)
		require.NotNil(t, result.Status.Certificate)
	})

	t.Run("When the request has already been denied it should return conflict", func(t *testing.T) {
		h, fakeStore, _, _, cfg := newTestHandler(t)
		cn := "test-csr-denied"
		csr := domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(cn)},
			Spec:     domain.CertificateSigningRequestSpec{SignerName: cfg.DeviceSvcClientSignerName, Request: csrPEM(t, cn), Usages: validUsages()},
			Status: &domain.CertificateSigningRequestStatus{
				Conditions: []domain.Condition{{
					Type:   domain.ConditionTypeCertificateSigningRequestDenied,
					Status: domain.ConditionStatusTrue,
				}},
			},
		}
		fakeStore.items[cn] = &csr

		approval := csr
		approval.Status = &domain.CertificateSigningRequestStatus{
			Conditions: []domain.Condition{{
				Type:   domain.ConditionTypeCertificateSigningRequestApproved,
				Status: domain.ConditionStatusTrue,
			}},
		}

		_, status := h.UpdateCertificateSigningRequestApproval(context.Background(), uuid.New(), cn, approval)
		require.Equal(t, statusConflictCode, status.Code)
	})

	t.Run("When approving a server-svc signer request without super-admin it should be forbidden", func(t *testing.T) {
		h, fakeStore, _, _, cfg := newTestHandler(t)
		cn := "test-csr-serversvc"
		csr := domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(cn)},
			Spec:     domain.CertificateSigningRequestSpec{SignerName: cfg.ServerSvcSignerName, Request: csrPEM(t, cn), Usages: validUsages()},
			Status:   &domain.CertificateSigningRequestStatus{Conditions: []domain.Condition{}},
		}
		fakeStore.items[cn] = &csr

		approval := csr
		approval.Status = &domain.CertificateSigningRequestStatus{
			Conditions: []domain.Condition{{
				Type:   domain.ConditionTypeCertificateSigningRequestApproved,
				Status: domain.ConditionStatusTrue,
			}},
		}

		_, status := h.UpdateCertificateSigningRequestApproval(context.Background(), uuid.New(), cn, approval)
		require.Equal(t, int32(403), status.Code)
	})
}

func TestVerifyTPMCSRRequest(t *testing.T) {
	t.Run("When the owner is not a device it should mark TPM verification false", func(t *testing.T) {
		h, _, _, _, _ := newTestHandler(t)
		csr := &domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Owner: lo.ToPtr("Fleet/myfleet")},
			Spec:     domain.CertificateSigningRequestSpec{Request: tcgCSRHeaderBytes()},
			Status:   &domain.CertificateSigningRequestStatus{},
		}
		err := h.verifyTPMCSRRequest(context.Background(), uuid.New(), csr)
		require.NoError(t, err)
		cond := domain.FindStatusCondition(csr.Status.Conditions, domain.ConditionTypeCertificateSigningRequestTPMVerified)
		require.NotNil(t, cond)
		require.Equal(t, domain.ConditionStatusFalse, cond.Status)
	})

	t.Run("When the owning enrollment request cannot be found it should mark TPM verification false", func(t *testing.T) {
		h, _, _, _, _ := newTestHandler(t)
		csr := &domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Owner: lo.ToPtr("Device/missing-device")},
			Spec:     domain.CertificateSigningRequestSpec{Request: tcgCSRHeaderBytes()},
			Status:   &domain.CertificateSigningRequestStatus{},
		}
		err := h.verifyTPMCSRRequest(context.Background(), uuid.New(), csr)
		require.NoError(t, err)
		cond := domain.FindStatusCondition(csr.Status.Conditions, domain.ConditionTypeCertificateSigningRequestTPMVerified)
		require.NotNil(t, cond)
		require.Equal(t, domain.ConditionStatusFalse, cond.Status)
	})

	t.Run("When the owning enrollment request was not TPM verified it should mark TPM verification false", func(t *testing.T) {
		h, _, fakeER, _, _ := newTestHandler(t)
		fakeER.items["my-device"] = &domain.EnrollmentRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("my-device")},
			Status:   &domain.EnrollmentRequestStatus{Conditions: []domain.Condition{}},
		}
		csr := &domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Owner: lo.ToPtr("Device/my-device")},
			Spec:     domain.CertificateSigningRequestSpec{Request: tcgCSRHeaderBytes()},
			Status:   &domain.CertificateSigningRequestStatus{},
		}
		err := h.verifyTPMCSRRequest(context.Background(), uuid.New(), csr)
		require.NoError(t, err)
		cond := domain.FindStatusCondition(csr.Status.Conditions, domain.ConditionTypeCertificateSigningRequestTPMVerified)
		require.NotNil(t, cond)
		require.Equal(t, domain.ConditionStatusFalse, cond.Status)
	})
}
