package certificatesigningrequest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cacfg "github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/service/tpmcsr"
	"github.com/flightctl/flightctl/internal/store"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/store/selector"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

const (
	statusSuccessCode     = int32(200)
	statusCreatedCode     = int32(201)
	statusBadRequestCode  = int32(400)
	statusNotFoundCode    = int32(404)
	statusConflictCode    = int32(409)
	statusServerErrorCode = int32(500)
)

// fakeCertificateSigningRequestStore is a small in-memory implementation of
// internal/store/certificatesigningrequest.Store.
type fakeCertificateSigningRequestStore struct {
	items map[string]*domain.CertificateSigningRequest
}

var _ csrstore.Store = (*fakeCertificateSigningRequestStore)(nil)

func newFakeCertificateSigningRequestStore() *fakeCertificateSigningRequestStore {
	return &fakeCertificateSigningRequestStore{items: map[string]*domain.CertificateSigningRequest{}}
}

func (f *fakeCertificateSigningRequestStore) InitialMigration(ctx context.Context) error { return nil }

func (f *fakeCertificateSigningRequestStore) Create(ctx context.Context, orgId uuid.UUID, req *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, error) {
	name := lo.FromPtr(req.Metadata.Name)
	if req.Metadata.CreationTimestamp == nil {
		req.Metadata.CreationTimestamp = lo.ToPtr(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	}
	if _, exists := f.items[name]; exists {
		return nil, flterrors.ErrDuplicateName
	}
	// Mirror internal/store/model.NewCertificateSigningRequestFromApiResource, which always
	// defaults Status to a non-nil, empty-conditions struct regardless of the caller's input.
	if req.Status == nil {
		req.Status = &domain.CertificateSigningRequestStatus{Conditions: []domain.Condition{}}
	}
	f.items[name] = req
	return req, nil
}

func (f *fakeCertificateSigningRequestStore) Update(ctx context.Context, orgId uuid.UUID, req *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, *domain.CertificateSigningRequest, error) {
	name := lo.FromPtr(req.Metadata.Name)
	old, exists := f.items[name]
	if !exists {
		return nil, nil, flterrors.ErrResourceNotFound
	}
	req.Metadata.CreationTimestamp = old.Metadata.CreationTimestamp
	// Mirror internal/store/model.NewCertificateSigningRequestFromApiResource, which always
	// defaults Status to a non-nil, empty-conditions struct regardless of the caller's input.
	if req.Status == nil {
		req.Status = &domain.CertificateSigningRequestStatus{Conditions: []domain.Condition{}}
	}
	f.items[name] = req
	return req, old, nil
}

func (f *fakeCertificateSigningRequestStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, req *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, *domain.CertificateSigningRequest, bool, error) {
	name := lo.FromPtr(req.Metadata.Name)
	if _, exists := f.items[name]; exists {
		result, old, err := f.Update(ctx, orgId, req)
		return result, old, false, err
	}
	result, err := f.Create(ctx, orgId, req)
	return result, nil, true, err
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

func (f *fakeCertificateSigningRequestStore) Delete(ctx context.Context, orgId uuid.UUID, name string) (bool, error) {
	if _, exists := f.items[name]; !exists {
		return false, nil
	}
	delete(f.items, name)
	return true, nil
}

func (f *fakeCertificateSigningRequestStore) Mutate(ctx context.Context, orgId uuid.UUID, name string, previous *domain.CertificateSigningRequest, apply csrstore.CertificateSigningRequestApplyFunc) (*domain.CertificateSigningRequest, *domain.CertificateSigningRequest, bool, error) {
	before, err := store.CloneJSON(f.items[name])
	if err != nil {
		return nil, nil, false, err
	}
	current, err := store.CloneJSON(before)
	if err != nil {
		return nil, nil, false, err
	}
	mutation := &csrstore.CertificateSigningRequestMutation{CertificateSigningRequest: current}
	if err := apply(mutation); err != nil {
		if errors.Is(err, store.ErrMutateSkipWrite) {
			return mutation.Resource(), before, false, nil
		}
		return nil, before, false, err
	}
	if mutation.Resource() == nil {
		return nil, before, false, flterrors.ErrResourceIsNil
	}
	persisted, err := store.CloneJSON(mutation.Resource())
	if err != nil {
		return nil, before, false, err
	}
	var result *domain.CertificateSigningRequest
	if before == nil {
		result, err = f.Create(ctx, orgId, persisted)
	} else {
		result, _, err = f.Update(ctx, orgId, persisted)
	}
	if err != nil {
		return nil, before, before == nil, err
	}
	result, err = store.CloneJSON(result)
	return result, before, before == nil, err
}

func (f *fakeCertificateSigningRequestStore) UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition) error {
	csr, exists := f.items[name]
	if !exists {
		return flterrors.ErrResourceNotFound
	}
	csr.Status.Conditions = conditions
	return nil
}

func (f *fakeCertificateSigningRequestStore) WithTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	snapshot := make(map[string]*domain.CertificateSigningRequest, len(f.items))
	for name, csr := range f.items {
		clone, err := store.CloneJSON(csr)
		if err != nil {
			return err
		}
		snapshot[name] = clone
	}
	if err := fn(ctx); err != nil {
		f.items = snapshot
		return err
	}
	return nil
}

func (f *fakeCertificateSigningRequestStore) Revoke(ctx context.Context, orgId uuid.UUID, name string, previous *domain.CertificateSigningRequest, revocation domain.EnrollmentCertificateRevocation) (*domain.CertificateSigningRequest, *domain.CertificateSigningRequest, bool, error) {
	updated, before, _, err := f.Mutate(ctx, orgId, name, previous, func(mutation *csrstore.CertificateSigningRequestMutation) error {
		if err := mutation.RequireExisting(); err != nil {
			return err
		}
		current := mutation.Resource()
		if current.Status == nil {
			current.Status = &domain.CertificateSigningRequestStatus{Conditions: []domain.Condition{}}
		}
		if current.Status.Revocation != nil {
			return store.ErrMutateSkipWrite
		}
		current.Status.Revocation = &revocation
		return nil
	})
	changed := err == nil && updated != nil && updated.Status != nil && updated.Status.Revocation != nil &&
		(before == nil || before.Status == nil || before.Status.Revocation == nil)
	return updated, before, changed, err
}

// fakeEnrollmentRequestService is a minimal enrollmentrequest.Service stand-in for TPM
// verification tests (GetEnrollmentRequest only).
type fakeEnrollmentRequestService struct {
	items map[string]*domain.EnrollmentRequest
}

func newFakeEnrollmentRequestService() *fakeEnrollmentRequestService {
	return &fakeEnrollmentRequestService{items: map[string]*domain.EnrollmentRequest{}}
}

func (f *fakeEnrollmentRequestService) GetEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string) (*domain.EnrollmentRequest, domain.Status) {
	er, ok := f.items[name]
	if !ok {
		return nil, domain.Status{Code: statusNotFoundCode, Message: "not found"}
	}
	return er, domain.StatusOK()
}

// fakeEventsService is a recording fake for events.Service. CertificateSigningRequest's own
// event decision logic (in handler.go's callbackCertificateSigningRequestUpdated) now calls
// CreateEvent directly, so tests assert on the actual emitted events rather than intercepting
// the removed HandleCertificateSigningRequestUpdatedEvents method.
type fakeEventsService struct {
	events.Service
	created        []*domain.Event
	deleted        []string
	createEventErr error
}

func (f *fakeEventsService) CreateEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event) {
	if event == nil {
		return
	}
	f.created = append(f.created, event)
}

func (f *fakeEventsService) CreateEventWithRetry(ctx context.Context, orgId uuid.UUID, event *domain.Event) error {
	f.CreateEvent(ctx, orgId, event)
	return f.createEventErr
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

func newTestHandler(t *testing.T) (*ServiceHandler, *fakeCertificateSigningRequestStore, *fakeEnrollmentRequestService, *fakeEventsService, *cacfg.Config) {
	csrStore := newFakeCertificateSigningRequestStore()
	erSvc := newFakeEnrollmentRequestService()
	ev := &fakeEventsService{}
	caClient, cfg := newTestCA(t)
	logger := logrus.New()
	verifier := tpmcsr.NewVerifier(erSvc)
	return NewServiceHandler(csrStore, verifier, caClient, ev, logger, "", ""), csrStore, erSvc, ev, cfg
}

type fakeCertificateService struct {
	Service
	items  []domain.CertificateSigningRequest
	status domain.Status
}

func (f *fakeCertificateService) ListCertificateSigningRequests(ctx context.Context, orgId uuid.UUID, params domain.ListCertificateSigningRequestsParams, opts ...selector.FieldSelectorOption) (*domain.CertificateSigningRequestList, domain.Status) {
	return &domain.CertificateSigningRequestList{Items: f.items}, f.status
}

func TestCSRForLeaf(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	caClient, _, err := crypto.EnsureCA(cacfg.NewDefault(tempDir))
	require.NoError(t, err)

	csrName := "enrollment-credential"
	clientCert, err := caClient.MakeClientCertificate(ctx, filepath.Join(tempDir, "client.crt"), filepath.Join(tempDir, "client.key"), csrName, 1)
	require.NoError(t, err)
	leaf := clientCert.Certs[0]
	certificatePEM, err := fcrypto.EncodeCertificatePEM(leaf)
	require.NoError(t, err)
	matchingCSR := domain.CertificateSigningRequest{
		Metadata: domain.ObjectMeta{Name: &csrName},
		Status: &domain.CertificateSigningRequestStatus{
			Certificate: &certificatePEM,
			Conditions: []domain.Condition{{
				Type:   domain.ConditionTypeCertificateSigningRequestApproved,
				Status: domain.ConditionStatusTrue,
			}},
		},
	}

	tests := []struct {
		name       string
		items      []domain.CertificateSigningRequest
		status     domain.Status
		wantCSR    *domain.CertificateSigningRequest
		wantErrStr string
	}{
		{
			name:    "When the certificate matches it should return the CSR",
			items:   []domain.CertificateSigningRequest{matchingCSR},
			status:  domain.StatusOK(),
			wantCSR: &matchingCSR,
		},
		{
			name:       "When no certificate matches it should return a not-found error",
			status:     domain.StatusOK(),
			wantErrStr: ErrEnrollmentCSRNotFound.Error(),
		},
		{
			name:       "When multiple certificates match it should return an ambiguity error",
			items:      []domain.CertificateSigningRequest{matchingCSR, matchingCSR},
			status:     domain.StatusOK(),
			wantErrStr: ErrAmbiguousCertificate.Error(),
		},
		{
			name:       "When listing fails it should return the lookup error",
			status:     domain.StatusInternalServerError("lookup failed"),
			wantErrStr: "lookup failed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeCertificateService{items: test.items, status: test.status}

			csr, err := CSRForLeaf(ctx, service, uuid.New(), leaf)

			if test.wantErrStr != "" {
				require.ErrorContains(t, err, test.wantErrStr)
				require.Nil(t, csr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantCSR, csr)
		})
	}
}

// conflictOnceStore simulates another writer committing issuance evidence
// between the service's read and its first status write.
type conflictOnceStore struct {
	*fakeCertificateSigningRequestStore
	committedCertificate []byte
	committedIssuedAt    time.Time
	pendingConflicts     int
}

type failingMutationStore struct {
	*fakeCertificateSigningRequestStore
	err error
}

func (s *failingMutationStore) Mutate(context.Context, uuid.UUID, string, *domain.CertificateSigningRequest, csrstore.CertificateSigningRequestApplyFunc) (*domain.CertificateSigningRequest, *domain.CertificateSigningRequest, bool, error) {
	return nil, nil, false, s.err
}

func (s *conflictOnceStore) Mutate(ctx context.Context, orgId uuid.UUID, name string, previous *domain.CertificateSigningRequest, apply csrstore.CertificateSigningRequestApplyFunc) (*domain.CertificateSigningRequest, *domain.CertificateSigningRequest, bool, error) {
	if s.pendingConflicts == 0 {
		return s.fakeCertificateSigningRequestStore.Mutate(ctx, orgId, name, previous, apply)
	}
	s.pendingConflicts--

	stale, err := store.CloneJSON(s.items[name])
	if err != nil {
		return nil, nil, false, err
	}
	if err := apply(&csrstore.CertificateSigningRequestMutation{CertificateSigningRequest: stale}); err != nil {
		return nil, stale, false, err
	}

	committed, err := store.CloneJSON(s.items[name])
	if err != nil {
		return nil, nil, false, err
	}
	committed.Status.Certificate = &s.committedCertificate
	committed.Status.IssuedAt = &s.committedIssuedAt
	persisted, err := store.CloneJSON(committed)
	if err != nil {
		return nil, nil, false, err
	}
	if _, _, err := s.fakeCertificateSigningRequestStore.Update(ctx, orgId, persisted); err != nil {
		return nil, nil, false, err
	}

	return nil, committed, false, flterrors.ErrResourceVersionConflict
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

func TestEnrollmentPredicates(t *testing.T) {
	const signerName = "flightctl.io/enrollment"
	tests := []struct {
		name             string
		configuredSigner string
		resource         *domain.CertificateSigningRequest
		wantEligible     bool
		wantProtected    bool
	}{
		{
			name:     "When the configured signer is empty it should reject both predicates",
			resource: &domain.CertificateSigningRequest{},
		},
		{
			name:             "When the configured signer matches it should treat the CSR as enrollment",
			configuredSigner: signerName,
			resource: &domain.CertificateSigningRequest{
				Spec: domain.CertificateSigningRequestSpec{SignerName: signerName},
			},
			wantEligible:  true,
			wantProtected: true,
		},
		{
			name:             "When the resource is nil it should reject both predicates",
			configuredSigner: signerName,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := &ServiceHandler{ca: &crypto.CAClient{Cfg: &cacfg.Config{DeviceEnrollmentSignerName: test.configuredSigner}}}

			require.Equal(t, test.wantEligible, handler.isEnrollmentIssuanceEligible(test.resource))
			require.Equal(t, test.wantProtected, handler.isProtectedEnrollmentCredential(test.resource))
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

func TestCertificateSigningRequestSnapshots(testingT *testing.T) {
	handler, _, _, eventRecorder, _ := newTestHandler(testingT)
	ctx := context.Background()
	orgID := uuid.New()
	name := "csr-snapshots"
	request := domain.CertificateSigningRequest{
		Metadata: domain.ObjectMeta{Name: &name, Labels: lo.ToPtr(map[string]string{"phase": "initial"})},
		Spec:     domain.CertificateSigningRequestSpec{SignerName: "enrollment", Request: csrPEM(testingT, name), Usages: validUsages()},
	}
	created, status := handler.CreateCertificateSigningRequest(ctx, orgID, request)
	require.Equal(testingT, statusCreatedCode, status.Code)
	require.NotEmpty(testingT, created.Status.Conditions)
	created.Metadata.Labels = lo.ToPtr(map[string]string{"phase": "replacement"})
	created.Status.Conditions[0].Reason = "ClientEdited"
	current, status := handler.GetCertificateSigningRequest(ctx, orgID, name)
	require.Equal(testingT, statusSuccessCode, status.Code)
	require.Equal(testingT, map[string]string{"phase": "initial"}, *current.Metadata.Labels)
	require.Equal(testingT, "Approved", current.Status.Conditions[0].Reason)

	created.Status.Conditions[0].Reason = "Approved"
	updated, status := handler.ReplaceCertificateSigningRequest(ctx, orgID, name, *created)
	require.Equal(testingT, statusSuccessCode, status.Code)
	require.Equal(testingT, map[string]string{"phase": "replacement"}, *updated.Metadata.Labels)
	current, status = handler.GetCertificateSigningRequest(ctx, orgID, name)
	require.Equal(testingT, statusSuccessCode, status.Code)
	require.Equal(testingT, map[string]string{"phase": "replacement"}, *current.Metadata.Labels)
	require.Equal(testingT, "Approved", current.Status.Conditions[0].Reason)
	require.Len(testingT, eventRecorder.created, 2)
	updateEvent := eventRecorder.created[1]
	require.Equal(testingT, domain.EventReasonResourceUpdated, updateEvent.Reason)
	require.NotNil(testingT, updateEvent.Details)
	details, err := updateEvent.Details.AsResourceUpdatedDetails()
	require.NoError(testingT, err)
	require.Equal(testingT, []domain.ResourceUpdatedDetailsUpdatedFields{domain.Labels}, details.UpdatedFields)
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

func TestEnrollmentIssuanceClock(t *testing.T) {
	tests := []struct {
		name string
		now  time.Time
	}{
		{
			name: "When server time is UTC it should publish exact issuance and preserve it on replacement",
			now:  time.Date(2025, 3, 4, 5, 6, 7, 123456000, time.UTC),
		},
		{
			name: "When server time has an offset it should publish UTC issuance and preserve it on replacement",
			now:  time.Date(2025, 3, 4, 7, 6, 7, 123456000, time.FixedZone("server", 2*60*60)),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nowFunc = func() time.Time { return test.now }
			t.Cleanup(func() { nowFunc = time.Now })
			handler, _, _, _, cfg := newTestHandler(t)
			ctx := context.Background()
			orgID := uuid.New()
			name := "enrollment-clock"
			request := domain.CertificateSigningRequest{
				Metadata: domain.ObjectMeta{Name: &name},
				Spec: domain.CertificateSigningRequestSpec{
					SignerName: cfg.DeviceEnrollmentSignerName,
					Request:    csrPEM(t, name),
					Usages:     validUsages(),
				},
			}
			created, status := handler.CreateCertificateSigningRequest(ctx, orgID, request)
			require.Equal(t, statusCreatedCode, status.Code)
			require.NotNil(t, created)
			require.NotNil(t, created.Status)
			require.NotNil(t, created.Status.IssuedAt)
			require.Equal(t, test.now.UTC(), *created.Status.IssuedAt)
			require.Same(t, time.UTC, created.Status.IssuedAt.Location())
			require.NotNil(t, created.Status.Certificate)
			require.NotEmpty(t, *created.Status.Certificate)
			publishedCertificate := append([]byte(nil), (*created.Status.Certificate)...)
			publishedAt := *created.Status.IssuedAt

			nowFunc = func() time.Time { return test.now.Add(24 * time.Hour) }
			replacement := *created
			replacementStatus := *created.Status
			replacementStatus.IssuedAt = lo.ToPtr(test.now.Add(2 * time.Hour))
			replacement.Status = &replacementStatus
			replacement.Metadata.Labels = &map[string]string{"clock": "advanced"}
			updated, status := ReplaceCertificateSigningRequestFromUntrusted(ctx, handler, orgID, name, replacement)
			require.Equal(t, statusSuccessCode, status.Code)
			require.Equal(t, &publishedCertificate, updated.Status.Certificate)
			require.NotNil(t, updated.Status.IssuedAt)
			require.Equal(t, &publishedAt, updated.Status.IssuedAt)
			retrieved, status := handler.GetCertificateSigningRequest(ctx, orgID, name)
			require.Equal(t, statusSuccessCode, status.Code)
			require.Equal(t, "advanced", (*retrieved.Metadata.Labels)["clock"])
			require.Equal(t, &publishedCertificate, retrieved.Status.Certificate)
			require.NotNil(t, retrieved.Status.IssuedAt)
			require.Equal(t, &publishedAt, retrieved.Status.IssuedAt)
			require.Same(t, time.UTC, retrieved.Status.IssuedAt.Location())
		})
	}
}

type recreatedCertificatePublishStore struct {
	*fakeCertificateSigningRequestStore
	replacement *domain.CertificateSigningRequest
	recreated   bool
}

func (s *recreatedCertificatePublishStore) Mutate(ctx context.Context, orgId uuid.UUID, name string, previous *domain.CertificateSigningRequest, apply csrstore.CertificateSigningRequestApplyFunc) (*domain.CertificateSigningRequest, *domain.CertificateSigningRequest, bool, error) {
	if s.recreated {
		return s.fakeCertificateSigningRequestStore.Mutate(ctx, orgId, name, previous, apply)
	}
	s.recreated = true
	delete(s.items, name)
	s.items[name] = s.replacement
	return nil, previous, false, flterrors.ErrResourceVersionConflict
}

func TestEnrollmentIssuanceWhenCSRIsRecreatedWithCertificate(t *testing.T) {
	baseStore := newFakeCertificateSigningRequestStore()
	caClient, cfg := newTestCA(t)
	handler := NewServiceHandler(baseStore, tpmcsr.NewVerifier(newFakeEnrollmentRequestService()), caClient, &fakeEventsService{}, logrus.New(), "", "")
	ctx := context.Background()
	orgID := uuid.New()
	name := "recreated-publish"
	createdAt := time.Date(2025, 4, 5, 6, 7, 8, 0, time.UTC)
	recreatedAt := createdAt.Add(time.Second)
	replacementCertificate := []byte("replacement-certificate")
	approved := []domain.Condition{{Type: domain.ConditionTypeCertificateSigningRequestApproved, Status: domain.ConditionStatusTrue}}

	original, err := baseStore.Create(ctx, orgID, &domain.CertificateSigningRequest{
		Metadata: domain.ObjectMeta{Name: &name, CreationTimestamp: &createdAt},
		Spec: domain.CertificateSigningRequestSpec{
			SignerName: cfg.DeviceEnrollmentSignerName,
			Request:    csrPEM(t, name),
			Usages:     validUsages(),
		},
	})
	require.NoError(t, err)
	original.Status = &domain.CertificateSigningRequestStatus{Conditions: approved}
	candidate, err := store.CloneJSON(original)
	require.NoError(t, err)

	replacement := *original
	replacement.Metadata.CreationTimestamp = &recreatedAt
	replacement.Status = &domain.CertificateSigningRequestStatus{
		Certificate: &replacementCertificate,
		Conditions:  approved,
	}
	provider := &recreatedCertificatePublishStore{fakeCertificateSigningRequestStore: baseStore, replacement: &replacement}

	handler.store = provider
	handler.signApprovedCertificateSigningRequest(ctx, orgID, candidate)

	require.Nil(t, candidate.Status.Certificate)
	require.Equal(t, &replacementCertificate, baseStore.items[name].Status.Certificate)
}

func TestEnrollmentIssuanceSafeguards(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "When issuance evidence is committed concurrently it should preserve the committed certificate and issuance time",
			run: func(t *testing.T) {
				baseStore := newFakeCertificateSigningRequestStore()
				committedAt := time.Date(2025, 4, 5, 6, 7, 8, 0, time.UTC)
				conflictingStore := &conflictOnceStore{
					fakeCertificateSigningRequestStore: baseStore,
					committedCertificate:               []byte("first-committed-certificate"),
					committedIssuedAt:                  committedAt,
					pendingConflicts:                   1,
				}
				caClient, cfg := newTestCA(t)
				handler := NewServiceHandler(conflictingStore, tpmcsr.NewVerifier(newFakeEnrollmentRequestService()), caClient, &fakeEventsService{}, logrus.New(), "", "")
				ctx := context.Background()
				orgID := uuid.New()
				name := "issuance-conflict"
				request := domain.CertificateSigningRequest{
					Metadata: domain.ObjectMeta{Name: &name},
					Spec: domain.CertificateSigningRequestSpec{
						SignerName: cfg.DeviceEnrollmentSignerName,
						Request:    csrPEM(t, name),
						Usages:     validUsages(),
					},
					Status: &domain.CertificateSigningRequestStatus{Conditions: []domain.Condition{{
						Type:   domain.ConditionTypeCertificateSigningRequestApproved,
						Status: domain.ConditionStatusTrue,
					}}},
				}
				stored, err := baseStore.Create(ctx, orgID, &request)
				require.NoError(t, err)
				candidate, err := store.CloneJSON(stored)
				require.NoError(t, err)

				nowFunc = func() time.Time { return committedAt.Add(time.Hour) }
				t.Cleanup(func() { nowFunc = time.Now })
				handler.signApprovedCertificateSigningRequest(ctx, orgID, candidate)

				require.Equal(t, &conflictingStore.committedCertificate, candidate.Status.Certificate)
				require.NotNil(t, candidate.Status.IssuedAt)
				require.Equal(t, committedAt, *candidate.Status.IssuedAt)
				require.Equal(t, &conflictingStore.committedCertificate, baseStore.items[name].Status.Certificate)
				require.NotNil(t, baseStore.items[name].Status.IssuedAt)
				require.Equal(t, committedAt, *baseStore.items[name].Status.IssuedAt)
			},
		},
		{
			name: "When signing fails it should leave no certificate or issuance time",
			run: func(t *testing.T) {
				handler, fakeStore, _, _, cfg := newTestHandler(t)
				ctx := context.Background()
				orgID := uuid.New()
				name := "signing-failure"
				request := domain.CertificateSigningRequest{
					Metadata: domain.ObjectMeta{Name: &name},
					Spec: domain.CertificateSigningRequestSpec{
						SignerName: cfg.DeviceEnrollmentSignerName,
						Request:    csrPEM(t, "mismatched-common-name"),
						Usages:     validUsages(),
					},
				}

				created, status := handler.CreateCertificateSigningRequest(ctx, orgID, request)
				require.Equal(t, statusCreatedCode, status.Code)
				require.Nil(t, created.Status.Certificate)
				require.Nil(t, created.Status.IssuedAt)
				require.True(t, domain.IsStatusConditionTrue(created.Status.Conditions, domain.ConditionTypeCertificateSigningRequestFailed))

				stored := fakeStore.items[name]
				require.Nil(t, stored.Status.Certificate)
				require.Nil(t, stored.Status.IssuedAt)
				require.True(t, domain.IsStatusConditionTrue(stored.Status.Conditions, domain.ConditionTypeCertificateSigningRequestFailed))
			},
		},
		{
			name: "When status publication fails it should leave no certificate or issuance time",
			run: func(t *testing.T) {
				baseStore := newFakeCertificateSigningRequestStore()
				publishErr := errors.New("publication failed")
				failingStore := &failingMutationStore{fakeCertificateSigningRequestStore: baseStore, err: publishErr}
				caClient, cfg := newTestCA(t)
				handler := NewServiceHandler(failingStore, tpmcsr.NewVerifier(newFakeEnrollmentRequestService()), caClient, &fakeEventsService{}, logrus.New(), "", "")
				ctx := context.Background()
				orgID := uuid.New()
				name := "publication-failure"
				request := domain.CertificateSigningRequest{
					Metadata: domain.ObjectMeta{Name: &name},
					Spec: domain.CertificateSigningRequestSpec{
						SignerName: cfg.DeviceEnrollmentSignerName,
						Request:    csrPEM(t, name),
						Usages:     validUsages(),
					},
					Status: &domain.CertificateSigningRequestStatus{Conditions: []domain.Condition{{
						Type:   domain.ConditionTypeCertificateSigningRequestApproved,
						Status: domain.ConditionStatusTrue,
					}}},
				}
				stored, err := baseStore.Create(ctx, orgID, &request)
				require.NoError(t, err)
				candidate, err := store.CloneJSON(stored)
				require.NoError(t, err)

				handler.signApprovedCertificateSigningRequest(ctx, orgID, candidate)

				require.Nil(t, candidate.Status.Certificate)
				require.Nil(t, candidate.Status.IssuedAt)
				require.Nil(t, baseStore.items[name].Status.Certificate)
				require.Nil(t, baseStore.items[name].Status.IssuedAt)
			},
		},
		{
			name: "When a non-enrollment signer issues a certificate it should not set issuance time",
			run: func(t *testing.T) {
				handler, fakeStore, _, _, cfg := newTestHandler(t)
				ctx := context.Background()
				orgID := uuid.New()
				name := "server-svc-certificate"
				request := domain.CertificateSigningRequest{
					Metadata: domain.ObjectMeta{Name: &name},
					Spec: domain.CertificateSigningRequestSpec{
						SignerName: cfg.ServerSvcSignerName,
						Request:    csrPEM(t, "svc-test"),
						Usages:     validUsages(),
					},
					Status: &domain.CertificateSigningRequestStatus{Conditions: []domain.Condition{{
						Type:   domain.ConditionTypeCertificateSigningRequestApproved,
						Status: domain.ConditionStatusTrue,
					}}},
				}
				stored, err := fakeStore.Create(ctx, orgID, &request)
				require.NoError(t, err)
				candidate, err := store.CloneJSON(stored)
				require.NoError(t, err)

				handler.signApprovedCertificateSigningRequest(ctx, orgID, candidate)

				require.NotNil(t, candidate.Status.Certificate)
				require.NotEmpty(t, *candidate.Status.Certificate)
				require.Nil(t, candidate.Status.IssuedAt)
				require.NotNil(t, fakeStore.items[name].Status.Certificate)
				require.Nil(t, fakeStore.items[name].Status.IssuedAt)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, test.run)
	}
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
	_, err := fakeStore.Create(context.Background(), uuid.New(), &csr)
	require.NoError(t, err)
	createdAt := *csr.Metadata.CreationTimestamp

	result, status := h.ReplaceCertificateSigningRequest(context.Background(), uuid.New(), cn, csr)
	require.Equal(t, statusSuccessCode, status.Code)
	require.NotNil(t, result)
	require.NotNil(t, result.Metadata.CreationTimestamp)
	require.Equal(t, createdAt, *result.Metadata.CreationTimestamp)

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
		_, err := fakeStore.Create(context.Background(), orgId, &csr)
		require.NoError(t, err)
		createdAt := *csr.Metadata.CreationTimestamp
		incoming := csr
		incoming.Metadata.CreationTimestamp = lo.ToPtr(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))

		_, status := ReplaceCertificateSigningRequestFromUntrusted(context.Background(), h, orgId, cn, incoming)
		require.Equal(t, statusSuccessCode, status.Code)
		require.Nil(t, fakeStore.items[cn].Metadata.Owner)
		require.Nil(t, fakeStore.items[cn].Metadata.Generation)
		require.NotNil(t, fakeStore.items[cn].Metadata.CreationTimestamp)
		require.Equal(t, createdAt, *fakeStore.items[cn].Metadata.CreationTimestamp)
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
		_, err := fakeStore.Create(context.Background(), orgId, &csr)
		require.NoError(t, err)
		createdAt := *csr.Metadata.CreationTimestamp
		incoming := csr
		incoming.Metadata.CreationTimestamp = lo.ToPtr(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))

		_, status := h.ReplaceCertificateSigningRequest(context.Background(), orgId, cn, incoming)
		require.Equal(t, statusSuccessCode, status.Code)
		require.Equal(t, "someone", lo.FromPtr(fakeStore.items[cn].Metadata.Owner))
		require.Equal(t, int64(5), lo.FromPtr(fakeStore.items[cn].Metadata.Generation))
		require.NotNil(t, fakeStore.items[cn].Metadata.CreationTimestamp)
		require.Equal(t, createdAt, *fakeStore.items[cn].Metadata.CreationTimestamp)
	})
}

func TestReplaceCertificateSigningRequestOwnership(t *testing.T) {
	owner := "Device/my-device"

	t.Run("When replacing an owned CSR with a changed spec it should return conflict", func(t *testing.T) {
		h, fakeStore, _, _, _ := newTestHandler(t)
		cn := "owned-csr"
		existing := domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(cn), Owner: lo.ToPtr(owner)},
			Spec:     domain.CertificateSigningRequestSpec{SignerName: "enrollment", Request: csrPEM(t, cn), Usages: validUsages()},
		}
		fakeStore.items[cn] = &existing

		updated := existing
		updated.Spec.Request = csrPEM(t, "different-cn")

		_, status := h.ReplaceCertificateSigningRequest(context.Background(), uuid.New(), cn, updated)
		require.Equal(t, statusConflictCode, status.Code)
		require.Equal(t, flterrors.ErrUpdatingResourceWithOwnerNotAllowed.Error(), status.Message)
	})

	t.Run("When replacing an owned CSR with an unchanged spec it should succeed", func(t *testing.T) {
		h, fakeStore, _, _, _ := newTestHandler(t)
		cn := "owned-csr-samespec"
		existing := domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(cn), Owner: lo.ToPtr(owner)},
			Spec:     domain.CertificateSigningRequestSpec{SignerName: "enrollment", Request: csrPEM(t, cn), Usages: validUsages()},
		}
		_, err := fakeStore.Create(context.Background(), uuid.New(), &existing)
		require.NoError(t, err)
		createdAt := *existing.Metadata.CreationTimestamp

		_, status := h.ReplaceCertificateSigningRequest(context.Background(), uuid.New(), cn, existing)
		require.Equal(t, statusSuccessCode, status.Code)
		require.Equal(t, owner, lo.FromPtr(fakeStore.items[cn].Metadata.Owner))
		require.NotNil(t, fakeStore.items[cn].Metadata.CreationTimestamp)
		require.Equal(t, createdAt, *fakeStore.items[cn].Metadata.CreationTimestamp)
	})
}

func TestPatchCertificateSigningRequestOwnership(t *testing.T) {
	owner := "Device/my-device"

	t.Run("When patching an owned CSR's spec it should return conflict", func(t *testing.T) {
		h, fakeStore, _, _, _ := newTestHandler(t)
		cn := "owned-csr-patch"
		existing := domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(cn), Owner: lo.ToPtr(owner)},
			Spec:     domain.CertificateSigningRequestSpec{SignerName: "enrollment", Request: csrPEM(t, cn), Usages: validUsages()},
			Status:   &domain.CertificateSigningRequestStatus{Conditions: []domain.Condition{}},
		}
		fakeStore.items[cn] = &existing

		var value interface{} = float64(3600)
		patch := domain.PatchRequest{{Op: "add", Path: "/spec/expirationSeconds", Value: &value}}
		_, status := h.PatchCertificateSigningRequest(context.Background(), uuid.New(), cn, patch)
		require.Equal(t, statusConflictCode, status.Code)
		require.Equal(t, flterrors.ErrUpdatingResourceWithOwnerNotAllowed.Error(), status.Message)
	})
}

func TestUpdateCertificateSigningRequestApproval(t *testing.T) {
	t.Run("When approving a pending CSR it should sign it", func(t *testing.T) {
		h, fakeStore, _, _, cfg := newTestHandler(t)
		cn := "test-csr-approve"
		csr := domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(cn), CreationTimestamp: lo.ToPtr(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))},
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

func TestRevokeCertificateSigningRequest(t *testing.T) {
	t.Run("When revoking an issued enrollment CSR it should return immutable server-owned metadata", func(t *testing.T) {
		h, _, _, fakeEvents, cfg := newTestHandler(t)
		ctx := context.WithValue(context.Background(), consts.MappedIdentityCtxKey,
			identity.NewMappedIdentity("security-officer", "uid-security-officer", nil, nil, true, nil))
		orgId := uuid.New()
		name := "issued-enrollment-csr"
		created, status := h.CreateCertificateSigningRequest(ctx, orgId, domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Spec: domain.CertificateSigningRequestSpec{
				SignerName: cfg.DeviceEnrollmentSignerName,
				Request:    csrPEM(t, name),
				Usages:     validUsages(),
			},
		})
		require.Equal(t, statusCreatedCode, status.Code)
		require.NotNil(t, created.Status.Certificate)

		revoked, status := h.RevokeCertificateSigningRequest(ctx, orgId, name, domain.EnrollmentCertificateRevocationRequest{
			Reason: "Commissioning credential compromised",
		})

		require.Equal(t, statusSuccessCode, status.Code)
		require.NotNil(t, revoked.Status.Revocation)
		require.Equal(t, "Commissioning credential compromised", revoked.Status.Revocation.Reason)
		require.Equal(t, "security-officer", revoked.Status.Revocation.RevokedBy)
		require.False(t, revoked.Status.Revocation.RevokedAt.IsZero())
		require.Nil(t, revoked.Status.Revocation.FlaggingCompletedAt)
		require.Equal(t, created.Status.Certificate, revoked.Status.Certificate)
		require.Equal(t, created.Status.IssuedAt, revoked.Status.IssuedAt)
		var revocationEvents []domain.Event
		for _, event := range fakeEvents.created {
			if event.Reason == domain.EventReasonEnrollmentCredentialRevoked {
				revocationEvents = append(revocationEvents, *event)
			}
		}
		require.Len(t, revocationEvents, 1)
		revocationEvent := revocationEvents[0]
		require.Equal(t, domain.EventReasonEnrollmentCredentialRevoked, revocationEvent.Reason)
		require.Equal(t, "security-officer", revocationEvent.Actor)
		require.Equal(t, name, revocationEvent.InvolvedObject.Name)
	})

	t.Run("When revoking an already revoked CSR it should return the original revocation", func(t *testing.T) {
		h, _, _, fakeEvents, cfg := newTestHandler(t)
		ctx := context.WithValue(context.Background(), consts.MappedIdentityCtxKey,
			identity.NewMappedIdentity("security-officer", "uid-security-officer", nil, nil, true, nil))
		orgId := uuid.New()
		name := "already-revoked-enrollment-csr"
		_, status := h.CreateCertificateSigningRequest(ctx, orgId, domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Spec: domain.CertificateSigningRequestSpec{
				SignerName: cfg.DeviceEnrollmentSignerName,
				Request:    csrPEM(t, name),
				Usages:     validUsages(),
			},
		})
		require.Equal(t, statusCreatedCode, status.Code)

		first, status := h.RevokeCertificateSigningRequest(ctx, orgId, name, domain.EnrollmentCertificateRevocationRequest{
			Reason: "Credential compromised",
		})
		require.Equal(t, statusSuccessCode, status.Code)
		second, status := h.RevokeCertificateSigningRequest(ctx, orgId, name, domain.EnrollmentCertificateRevocationRequest{
			Reason: "Different reason",
		})

		require.Equal(t, statusSuccessCode, status.Code)
		require.Equal(t, first.Status.Revocation, second.Status.Revocation)
		require.Equal(t, "Credential compromised", second.Status.Revocation.Reason)
		revocationEventCount := 0
		for _, event := range fakeEvents.created {
			if event.Reason == domain.EventReasonEnrollmentCredentialRevoked {
				revocationEventCount++
			}
		}
		require.Equal(t, 1, revocationEventCount)
	})

	t.Run("When revocation event persistence fails it should roll back the revocation", func(t *testing.T) {
		h, _, _, fakeEvents, cfg := newTestHandler(t)
		fakeEvents.createEventErr = errors.New("event store unavailable")
		ctx := context.WithValue(context.Background(), consts.MappedIdentityCtxKey,
			identity.NewMappedIdentity("security-officer", "uid-security-officer", nil, nil, true, nil))
		orgId := uuid.New()
		name := "event-failure-enrollment-csr"
		created, status := h.CreateCertificateSigningRequest(ctx, orgId, domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Spec: domain.CertificateSigningRequestSpec{
				SignerName: cfg.DeviceEnrollmentSignerName,
				Request:    csrPEM(t, name),
				Usages:     validUsages(),
			},
		})
		require.Equal(t, statusCreatedCode, status.Code)

		_, status = h.RevokeCertificateSigningRequest(ctx, orgId, name, domain.EnrollmentCertificateRevocationRequest{
			Reason: "Credential compromised",
		})

		require.Equal(t, statusServerErrorCode, status.Code)
		current, getStatus := h.GetCertificateSigningRequest(ctx, orgId, name)
		require.Equal(t, statusSuccessCode, getStatus.Code)
		require.Nil(t, current.Status.Revocation)
		require.Equal(t, created.Status.Certificate, current.Status.Certificate)
		var revocationEvents []*domain.Event
		for _, event := range fakeEvents.created {
			if event.Reason == domain.EventReasonEnrollmentCredentialRevoked {
				revocationEvents = append(revocationEvents, event)
			}
		}
		require.Len(t, revocationEvents, 1)
	})

	t.Run("When the reason is blank or oversized it should reject the request without revoking", func(t *testing.T) {
		h, _, _, _, cfg := newTestHandler(t)
		ctx := context.WithValue(context.Background(), consts.MappedIdentityCtxKey,
			identity.NewMappedIdentity("security-officer", "uid-security-officer", nil, nil, true, nil))
		orgId := uuid.New()
		name := "invalid-revocation-reason-csr"
		created, status := h.CreateCertificateSigningRequest(ctx, orgId, domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Spec: domain.CertificateSigningRequestSpec{
				SignerName: cfg.DeviceEnrollmentSignerName,
				Request:    csrPEM(t, name),
				Usages:     validUsages(),
			},
		})
		require.Equal(t, statusCreatedCode, status.Code)

		for reason, message := range map[string]string{
			"":                        "reason is required and cannot be blank",
			"   ":                     "reason is required and cannot be blank",
			strings.Repeat("x", 1025): "reason must be at most 1024 bytes",
		} {
			_, status := h.RevokeCertificateSigningRequest(ctx, orgId, name, domain.EnrollmentCertificateRevocationRequest{
				Reason: reason,
			})
			require.Equal(t, statusBadRequestCode, status.Code)
			require.Equal(t, message, status.Message)
		}

		current, status := h.GetCertificateSigningRequest(ctx, orgId, name)
		require.Equal(t, statusSuccessCode, status.Code)
		require.Nil(t, current.Status.Revocation)
		require.Equal(t, created.Status.Certificate, current.Status.Certificate)
	})

	t.Run("When the target is not an issued enrollment credential it should reject it without mutation", func(t *testing.T) {
		h, fakeStore, _, _, cfg := newTestHandler(t)
		ctx := context.WithValue(context.Background(), consts.MappedIdentityCtxKey,
			identity.NewMappedIdentity("security-officer", "uid-security-officer", nil, nil, true, nil))
		orgId := uuid.New()

		serviceClient := domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("service-client-csr")},
			Spec: domain.CertificateSigningRequestSpec{
				SignerName: cfg.DeviceSvcClientSignerName,
				Request:    csrPEM(t, "service-client-csr"),
				Usages:     validUsages(),
			},
			Status: &domain.CertificateSigningRequestStatus{
				Conditions:  []domain.Condition{{Type: domain.ConditionTypeCertificateSigningRequestApproved, Status: domain.ConditionStatusTrue}},
				Certificate: lo.ToPtr([]byte("issued-service-client-certificate")),
			},
		}
		fakeStore.items["service-client-csr"] = &serviceClient

		unissued := domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("unissued-enrollment-csr")},
			Spec: domain.CertificateSigningRequestSpec{
				SignerName: cfg.DeviceEnrollmentSignerName,
				Request:    csrPEM(t, "unissued-enrollment-csr"),
				Usages:     validUsages(),
			},
			Status: &domain.CertificateSigningRequestStatus{Conditions: []domain.Condition{}},
		}
		fakeStore.items["unissued-enrollment-csr"] = &unissued

		managementRenewal := domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("management-renewal-csr")},
			Spec: domain.CertificateSigningRequestSpec{
				SignerName: cfg.DeviceManagementRenewalSignerName,
				Request:    csrPEM(t, "management-renewal-csr"),
				Usages:     validUsages(),
			},
			Status: &domain.CertificateSigningRequestStatus{
				Conditions:  []domain.Condition{{Type: domain.ConditionTypeCertificateSigningRequestApproved, Status: domain.ConditionStatusTrue}},
				Certificate: lo.ToPtr([]byte("issued-management-certificate")),
			},
		}
		fakeStore.items["management-renewal-csr"] = &managementRenewal

		for name, expectedStatus := range map[string]domain.Status{
			"service-client-csr":      {Code: statusBadRequestCode, Message: "only enrollment credentials may be revoked"},
			"management-renewal-csr":  {Code: statusBadRequestCode, Message: "only enrollment credentials may be revoked"},
			"unissued-enrollment-csr": {Code: statusConflictCode, Message: "only issued enrollment credentials may be revoked"},
		} {
			_, status := h.RevokeCertificateSigningRequest(ctx, orgId, name, domain.EnrollmentCertificateRevocationRequest{
				Reason: "Credential compromised",
			})
			require.Equal(t, expectedStatus.Code, status.Code)
			require.Equal(t, expectedStatus.Message, status.Message)
		}

		require.Nil(t, serviceClient.Status.Revocation)
		require.Nil(t, managementRenewal.Status.Revocation)
		require.Nil(t, unissued.Status.Revocation)
	})

	t.Run("When no authenticated actor is present it should reject the request without revoking", func(t *testing.T) {
		h, fakeStore, _, _, cfg := newTestHandler(t)
		orgId := uuid.New()
		name := "missing-actor-enrollment-csr"
		csr := domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Spec: domain.CertificateSigningRequestSpec{
				SignerName: cfg.DeviceEnrollmentSignerName,
				Request:    csrPEM(t, name),
				Usages:     validUsages(),
			},
			Status: &domain.CertificateSigningRequestStatus{
				Conditions:  []domain.Condition{{Type: domain.ConditionTypeCertificateSigningRequestApproved, Status: domain.ConditionStatusTrue}},
				Certificate: lo.ToPtr([]byte("issued-enrollment-certificate")),
			},
		}
		fakeStore.items[name] = &csr

		_, status := h.RevokeCertificateSigningRequest(context.Background(), orgId, name, domain.EnrollmentCertificateRevocationRequest{
			Reason: "Credential compromised",
		})

		require.Equal(t, int32(403), status.Code)
		require.Nil(t, csr.Status.Revocation)
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
