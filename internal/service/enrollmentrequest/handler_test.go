package enrollmentrequest

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
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service/device"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/service/fleet"
	"github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
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

// fakeEnrollmentRequestStore is a small in-memory implementation of
// internal/store/enrollmentrequest.Store.
type fakeEnrollmentRequestStore struct {
	items map[string]*domain.EnrollmentRequest
}

func newFakeEnrollmentRequestStore() *fakeEnrollmentRequestStore {
	return &fakeEnrollmentRequestStore{items: map[string]*domain.EnrollmentRequest{}}
}

func (f *fakeEnrollmentRequestStore) InitialMigration(ctx context.Context) error { return nil }

func (f *fakeEnrollmentRequestStore) Create(ctx context.Context, orgId uuid.UUID, req *domain.EnrollmentRequest) (*domain.EnrollmentRequest, error) {
	name := lo.FromPtr(req.Metadata.Name)
	if _, exists := f.items[name]; exists {
		return nil, flterrors.ErrDuplicateName
	}
	f.items[name] = req
	return req, nil
}

func (f *fakeEnrollmentRequestStore) Update(ctx context.Context, orgId uuid.UUID, req *domain.EnrollmentRequest) (*domain.EnrollmentRequest, *domain.EnrollmentRequest, error) {
	name := lo.FromPtr(req.Metadata.Name)
	old, exists := f.items[name]
	if !exists {
		return nil, nil, flterrors.ErrResourceNotFound
	}
	f.items[name] = req
	return req, old, nil
}

func (f *fakeEnrollmentRequestStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, req *domain.EnrollmentRequest) (*domain.EnrollmentRequest, *domain.EnrollmentRequest, bool, error) {
	name := lo.FromPtr(req.Metadata.Name)
	if _, exists := f.items[name]; exists {
		result, _, err := f.Update(ctx, orgId, req)
		return result, nil, false, err
	}
	result, err := f.Create(ctx, orgId, req)
	return result, nil, true, err
}

func (f *fakeEnrollmentRequestStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.EnrollmentRequest, error) {
	er, ok := f.items[name]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	return er, nil
}

func (f *fakeEnrollmentRequestStore) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.EnrollmentRequestList, error) {
	var items []domain.EnrollmentRequest
	for _, er := range f.items {
		items = append(items, *er)
	}
	return &domain.EnrollmentRequestList{Items: items}, nil
}

func (f *fakeEnrollmentRequestStore) Delete(ctx context.Context, orgId uuid.UUID, name string) (bool, error) {
	if _, exists := f.items[name]; !exists {
		return false, nil
	}
	delete(f.items, name)
	return true, nil
}

func (f *fakeEnrollmentRequestStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, req *domain.EnrollmentRequest) (*domain.EnrollmentRequest, *domain.EnrollmentRequest, error) {
	name := lo.FromPtr(req.Metadata.Name)
	if _, exists := f.items[name]; !exists {
		return nil, nil, flterrors.ErrResourceNotFound
	}
	old := f.items[name]
	f.items[name] = req
	return req, old, nil
}

// fakeDeviceStore embeds devicestore.Store (nil) and overrides Get/Create/CreateOrUpdate
// used by allowCreationOrUpdate/deviceExists/createDeviceFromEnrollmentRequest.
type fakeDeviceStore struct {
	devicestore.Store
	items map[string]*domain.Device
}

func newFakeDeviceStore() *fakeDeviceStore {
	return &fakeDeviceStore{items: map[string]*domain.Device{}}
}

func (f *fakeDeviceStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, error) {
	d, ok := f.items[name]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	return d, nil
}

func (f *fakeDeviceStore) Create(ctx context.Context, orgId uuid.UUID, device *domain.Device) (*domain.Device, error) {
	name := lo.FromPtr(device.Metadata.Name)
	if _, exists := f.items[name]; exists {
		return nil, flterrors.ErrDuplicateName
	}
	f.items[name] = device
	return device, nil
}

func (f *fakeDeviceStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, device *domain.Device, fieldsToUnset []string) (*domain.Device, *domain.Device, bool, error) {
	name := lo.FromPtr(device.Metadata.Name)
	existing := f.items[name]
	created := existing == nil
	f.items[name] = device
	return device, existing, created, nil
}

// fakeKVStore embeds kvstore.KVStore (nil) and overrides only SetNX, the sole method
// createDeviceFromEnrollmentRequest calls.
type fakeKVStore struct {
	kvstore.KVStore
	setNXKeys []string
}

func (f *fakeKVStore) SetNX(ctx context.Context, key string, value []byte) (bool, error) {
	f.setNXKeys = append(f.setNXKeys, key)
	return true, nil
}

// fakeEventsService is a recording fake for events.Service. EnrollmentRequest's own event
// decision logic (in handler.go's callback* methods) now calls CreateEvent directly, so
// tests assert on the actual emitted events (filtered by Reason where a scenario can emit
// more than one, e.g. approval also creates a device) rather than intercepting
// resource-specific callbacks that no longer exist on the slimmed events.Service interface.
type fakeEventsService struct {
	events.Service
	createdEvents []*domain.Event
	deleted       []string
}

func (f *fakeEventsService) CreateEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event) {
	if event == nil {
		return
	}
	f.createdEvents = append(f.createdEvents, event)
}

func (f *fakeEventsService) HandleGenericResourceDeletedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	f.deleted = append(f.deleted, name)
}

func (f *fakeEventsService) createdWithReason(reason domain.EventReason) []*domain.Event {
	var matched []*domain.Event
	for _, e := range f.createdEvents {
		if e.Reason == reason {
			matched = append(matched, e)
		}
	}
	return matched
}

type fakeFleetService struct {
	fleet.Service
}

func newTestCA(t *testing.T) *crypto.CAClient {
	cfg := cacfg.NewDefault(t.TempDir())
	caClient, _, err := crypto.EnsureCA(cfg)
	require.NoError(t, err)
	return caClient
}

func newTestDeviceService(ev events.Service) (device.Service, *fakeDeviceStore) {
	devStore := newFakeDeviceStore()
	return device.NewDeviceServiceHandler(devStore, &fakeFleetService{}, ev, nil, "", logrus.New()), devStore
}

func newTestHandler(t *testing.T) (*ServiceHandler, *fakeEnrollmentRequestStore, *fakeDeviceStore, *fakeKVStore, *fakeEventsService) {
	erStore := newFakeEnrollmentRequestStore()
	kv := &fakeKVStore{}
	ev := &fakeEventsService{}
	caClient := newTestCA(t)
	logger := logrus.New()
	deviceSvc, devStore := newTestDeviceService(ev)
	return NewServiceHandler(erStore, deviceSvc, caClient, kv, ev, logger, nil), erStore, devStore, kv, ev
}

func adminContext() context.Context {
	mappedIdentity := identity.NewMappedIdentity("admin", "uid-admin", nil, nil, true, nil)
	return context.WithValue(context.Background(), consts.MappedIdentityCtxKey, mappedIdentity)
}

// csrPEM generates a throwaway PEM-encoded PKCS#10 CSR for the given common name.
func csrPEM(t *testing.T, cn string) string {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	csrTemplate := x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: cn},
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}
	csrBytes, err := x509.CreateCertificateRequest(rand.Reader, &csrTemplate, privateKey)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrBytes}))
}

func testEnrollmentRequest(name string) domain.EnrollmentRequest {
	return domain.EnrollmentRequest{
		ApiVersion: "v1beta1",
		Kind:       "EnrollmentRequest",
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: domain.EnrollmentRequestSpec{
			Csr: name,
		},
	}
}

func TestCreateEnrollmentRequest(t *testing.T) {
	t.Run("When the CSR is valid it should create the enrollment request", func(t *testing.T) {
		h, fakeStore, _, _, fakeEvents := newTestHandler(t)
		cn := "test-device"
		er := domain.EnrollmentRequest{
			ApiVersion: "v1beta1",
			Kind:       "EnrollmentRequest",
			Metadata:   domain.ObjectMeta{Name: lo.ToPtr(cn)},
			Spec:       domain.EnrollmentRequestSpec{Csr: csrPEM(t, cn)},
		}

		result, status := h.CreateEnrollmentRequest(context.Background(), uuid.New(), er)
		require.Equal(t, statusCreatedCode, status.Code)
		require.NotNil(t, result)
		require.Contains(t, fakeStore.items, cn)
		require.Len(t, fakeEvents.createdWithReason(domain.EventReasonResourceCreated), 1)
	})

	t.Run("When the CSR is malformed it should return bad request", func(t *testing.T) {
		h, _, _, _, _ := newTestHandler(t)
		er := testEnrollmentRequest("not-a-csr")

		_, status := h.CreateEnrollmentRequest(context.Background(), uuid.New(), er)
		require.Equal(t, statusBadRequestCode, status.Code)
	})

	t.Run("When managed metadata fields are set by the caller CreateEnrollmentRequestFromUntrusted should clear them before creation", func(t *testing.T) {
		h, fakeStore, _, _, _ := newTestHandler(t)
		cn := "untrusted-device"
		er := domain.EnrollmentRequest{
			ApiVersion: "v1beta1",
			Kind:       "EnrollmentRequest",
			Metadata: domain.ObjectMeta{
				Name:       lo.ToPtr(cn),
				Owner:      lo.ToPtr("someone"),
				Generation: lo.ToPtr(int64(5)),
			},
			Spec: domain.EnrollmentRequestSpec{Csr: csrPEM(t, cn)},
		}

		_, status := CreateEnrollmentRequestFromUntrusted(context.Background(), h, uuid.New(), er)
		require.Equal(t, statusCreatedCode, status.Code)
		require.Nil(t, fakeStore.items[cn].Metadata.Owner)
		require.Equal(t, int64(1), lo.FromPtr(fakeStore.items[cn].Metadata.Generation))
	})

	t.Run("When managed metadata fields are set by the caller CreateEnrollmentRequest (trusted) should preserve owner and set generation to 1", func(t *testing.T) {
		h, fakeStore, _, _, _ := newTestHandler(t)
		cn := "trusted-device"
		er := domain.EnrollmentRequest{
			ApiVersion: "v1beta1",
			Kind:       "EnrollmentRequest",
			Metadata: domain.ObjectMeta{
				Name:       lo.ToPtr(cn),
				Owner:      lo.ToPtr("someone"),
				Generation: lo.ToPtr(int64(5)),
			},
			Spec: domain.EnrollmentRequestSpec{Csr: csrPEM(t, cn)},
		}

		_, status := h.CreateEnrollmentRequest(context.Background(), uuid.New(), er)
		require.Equal(t, statusCreatedCode, status.Code)
		require.Equal(t, "someone", lo.FromPtr(fakeStore.items[cn].Metadata.Owner))
		require.Equal(t, int64(1), lo.FromPtr(fakeStore.items[cn].Metadata.Generation))
	})
}

func TestListEnrollmentRequests(t *testing.T) {
	h, fakeStore, _, _, _ := newTestHandler(t)
	er := testEnrollmentRequest("foo")
	fakeStore.items["foo"] = &er

	result, status := h.ListEnrollmentRequests(context.Background(), uuid.New(), domain.ListEnrollmentRequestsParams{})
	require.Equal(t, statusSuccessCode, status.Code)
	require.Len(t, result.Items, 1)
}

func TestGetEnrollmentRequest(t *testing.T) {
	h, fakeStore, _, _, _ := newTestHandler(t)
	er := testEnrollmentRequest("foo")
	fakeStore.items["foo"] = &er

	result, status := h.GetEnrollmentRequest(context.Background(), uuid.New(), "foo")
	require.Equal(t, statusSuccessCode, status.Code)
	require.Equal(t, "foo", lo.FromPtr(result.Metadata.Name))

	_, status = h.GetEnrollmentRequest(context.Background(), uuid.New(), "missing")
	require.Equal(t, statusNotFoundCode, status.Code)
}

func TestReplaceEnrollmentRequest(t *testing.T) {
	t.Run("When no device exists it should replace the enrollment request", func(t *testing.T) {
		h, fakeStore, _, _, _ := newTestHandler(t)
		cn := "test-device-replace"
		er := domain.EnrollmentRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(cn)},
			Spec:     domain.EnrollmentRequestSpec{Csr: csrPEM(t, cn)},
		}
		fakeStore.items[cn] = &er

		result, status := h.ReplaceEnrollmentRequest(context.Background(), uuid.New(), cn, er)
		require.Equal(t, statusSuccessCode, status.Code)
		require.NotNil(t, result)
	})

	t.Run("When a device with the same name already exists it should return bad request", func(t *testing.T) {
		h, fakeStore, fakeDevices, _, _ := newTestHandler(t)
		cn := "test-device-conflict"
		er := domain.EnrollmentRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(cn)},
			Spec:     domain.EnrollmentRequestSpec{Csr: csrPEM(t, cn)},
		}
		fakeStore.items[cn] = &er
		fakeDevices.items[cn] = &domain.Device{Metadata: domain.ObjectMeta{Name: lo.ToPtr(cn)}}

		_, status := h.ReplaceEnrollmentRequest(context.Background(), uuid.New(), cn, er)
		require.Equal(t, statusBadRequestCode, status.Code)
	})

	t.Run("When managed metadata fields are set by the caller ReplaceEnrollmentRequestFromUntrusted should clear them before replacing", func(t *testing.T) {
		h, fakeStore, _, _, _ := newTestHandler(t)
		orgId := uuid.New()
		cn := "replace-untrusted"
		er := domain.EnrollmentRequest{
			Metadata: domain.ObjectMeta{
				Name:       lo.ToPtr(cn),
				Owner:      lo.ToPtr("someone"),
				Generation: lo.ToPtr(int64(5)),
			},
			Spec: domain.EnrollmentRequestSpec{Csr: csrPEM(t, cn)},
		}

		_, status := ReplaceEnrollmentRequestFromUntrusted(context.Background(), h, orgId, cn, er)
		require.Equal(t, statusCreatedCode, status.Code)
		require.Nil(t, fakeStore.items[cn].Metadata.Owner)
		require.Equal(t, int64(1), lo.FromPtr(fakeStore.items[cn].Metadata.Generation))
	})

	t.Run("When managed metadata fields are set by the caller ReplaceEnrollmentRequest (trusted) should preserve owner and set generation to 1", func(t *testing.T) {
		h, fakeStore, _, _, _ := newTestHandler(t)
		orgId := uuid.New()
		cn := "replace-trusted"
		er := domain.EnrollmentRequest{
			Metadata: domain.ObjectMeta{
				Name:       lo.ToPtr(cn),
				Owner:      lo.ToPtr("someone"),
				Generation: lo.ToPtr(int64(5)),
			},
			Spec: domain.EnrollmentRequestSpec{Csr: csrPEM(t, cn)},
		}

		_, status := h.ReplaceEnrollmentRequest(context.Background(), orgId, cn, er)
		require.Equal(t, statusCreatedCode, status.Code)
		require.Equal(t, "someone", lo.FromPtr(fakeStore.items[cn].Metadata.Owner))
		require.Equal(t, int64(1), lo.FromPtr(fakeStore.items[cn].Metadata.Generation))
	})

	t.Run("When replacing with a changed spec it should bump generation", func(t *testing.T) {
		h, fakeStore, _, _, _ := newTestHandler(t)
		orgId := uuid.New()
		cn := "replace-bump"
		er := domain.EnrollmentRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(cn), Generation: lo.ToPtr(int64(2))},
			Spec:     domain.EnrollmentRequestSpec{Csr: csrPEM(t, cn)},
		}
		fakeStore.items[cn] = &er

		updated := er
		updated.Spec.Labels = &map[string]string{"env": "prod"}

		_, status := h.ReplaceEnrollmentRequest(context.Background(), orgId, cn, updated)
		require.Equal(t, statusSuccessCode, status.Code)
		require.Equal(t, int64(3), lo.FromPtr(fakeStore.items[cn].Metadata.Generation))
	})

	t.Run("When replacing with the same spec it should keep generation", func(t *testing.T) {
		h, fakeStore, _, _, _ := newTestHandler(t)
		orgId := uuid.New()
		cn := "replace-same"
		er := domain.EnrollmentRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(cn), Generation: lo.ToPtr(int64(2))},
			Spec:     domain.EnrollmentRequestSpec{Csr: csrPEM(t, cn)},
		}
		fakeStore.items[cn] = &er

		updated := er
		updated.Metadata.Labels = &map[string]string{"env": "prod"}

		_, status := h.ReplaceEnrollmentRequest(context.Background(), orgId, cn, updated)
		require.Equal(t, statusSuccessCode, status.Code)
		require.Equal(t, int64(2), lo.FromPtr(fakeStore.items[cn].Metadata.Generation))
	})
}

func TestPatchEnrollmentRequest(t *testing.T) {
	h, fakeStore, _, _, _ := newTestHandler(t)
	cn := "test-device-patch"
	er := domain.EnrollmentRequest{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr(cn), Labels: &map[string]string{"a": "b"}},
		Spec:     domain.EnrollmentRequestSpec{Csr: csrPEM(t, cn)},
	}
	fakeStore.items[cn] = &er

	t.Run("When the patch attempts to change metadata.name it should fail", func(t *testing.T) {
		var value interface{} = "bar"
		patch := domain.PatchRequest{{Op: "replace", Path: "/metadata/name", Value: &value}}
		_, status := h.PatchEnrollmentRequest(context.Background(), uuid.New(), cn, patch)
		require.Equal(t, statusBadRequestCode, status.Code)
	})

	t.Run("When the enrollment request does not exist it should return not found", func(t *testing.T) {
		var value interface{} = "bar"
		patch := domain.PatchRequest{{Op: "replace", Path: "/metadata/labels/a", Value: &value}}
		_, status := h.PatchEnrollmentRequest(context.Background(), uuid.New(), "missing", patch)
		require.Equal(t, statusNotFoundCode, status.Code)
	})
}

func TestDeleteEnrollmentRequest(t *testing.T) {
	t.Run("When no device exists it should delete the enrollment request", func(t *testing.T) {
		h, fakeStore, _, _, fakeEvents := newTestHandler(t)
		er := testEnrollmentRequest("foo")
		fakeStore.items["foo"] = &er

		status := h.DeleteEnrollmentRequest(context.Background(), uuid.New(), "foo")
		require.Equal(t, statusSuccessCode, status.Code)
		require.NotContains(t, fakeStore.items, "foo")
		require.Len(t, fakeEvents.deleted, 1)
	})

	t.Run("When a device with the same name exists it should return conflict", func(t *testing.T) {
		h, fakeStore, fakeDevices, _, _ := newTestHandler(t)
		er := testEnrollmentRequest("foo")
		fakeStore.items["foo"] = &er
		fakeDevices.items["foo"] = &domain.Device{Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")}}

		status := h.DeleteEnrollmentRequest(context.Background(), uuid.New(), "foo")
		require.Equal(t, statusConflictCode, status.Code)
		require.Contains(t, fakeStore.items, "foo")
	})
}

func TestGetEnrollmentRequestStatus(t *testing.T) {
	h, fakeStore, _, _, _ := newTestHandler(t)
	er := testEnrollmentRequest("foo")
	fakeStore.items["foo"] = &er

	result, status := h.GetEnrollmentRequestStatus(context.Background(), uuid.New(), "foo")
	require.Equal(t, statusSuccessCode, status.Code)
	require.Equal(t, "foo", lo.FromPtr(result.Metadata.Name))
}

func TestApproveEnrollmentRequest(t *testing.T) {
	t.Run("When already approved it should return bad request and fire no event", func(t *testing.T) {
		h, fakeStore, _, _, fakeEvents := newTestHandler(t)
		er := domain.EnrollmentRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")},
			Spec:     domain.EnrollmentRequestSpec{Csr: "TestCSR"},
			Status: &domain.EnrollmentRequestStatus{
				Conditions: []domain.Condition{{
					Type:    domain.ConditionTypeEnrollmentRequestApproved,
					Status:  domain.ConditionStatusTrue,
					Reason:  "ManuallyApproved",
					Message: "Approved by test",
				}},
			},
		}
		fakeStore.items["foo"] = &er

		_, status := h.ApproveEnrollmentRequest(adminContext(), uuid.New(), "foo", domain.EnrollmentRequestApproval{Approved: true})
		require.Equal(t, statusBadRequestCode, status.Code)
		require.Empty(t, fakeEvents.createdEvents)
	})

	t.Run("When approving without a mapped identity it should fail and emit an approval-failed event", func(t *testing.T) {
		h, fakeStore, _, _, fakeEvents := newTestHandler(t)
		cn := "test-device-noidentity"
		er := domain.EnrollmentRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(cn)},
			Spec:     domain.EnrollmentRequestSpec{Csr: csrPEM(t, cn), DeviceStatus: lo.ToPtr(domain.NewDeviceStatus())},
			Status:   &domain.EnrollmentRequestStatus{Conditions: []domain.Condition{}},
		}
		fakeStore.items[cn] = &er

		_, status := h.ApproveEnrollmentRequest(context.Background(), uuid.New(), cn, domain.EnrollmentRequestApproval{Approved: true})
		require.Equal(t, int32(500), status.Code)
		require.Len(t, fakeEvents.createdEvents, 1)
	})

	t.Run("When approving a valid, non-TPM enrollment request it should sign it and create a device", func(t *testing.T) {
		h, fakeStore, fakeDevices, _, fakeEvents := newTestHandler(t)
		cn := "test-device-approve"
		er := domain.EnrollmentRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(cn)},
			Spec:     domain.EnrollmentRequestSpec{Csr: csrPEM(t, cn), DeviceStatus: lo.ToPtr(domain.NewDeviceStatus())},
			Status:   &domain.EnrollmentRequestStatus{Conditions: []domain.Condition{}},
		}
		fakeStore.items[cn] = &er

		orgId := uuid.New()
		result, status := h.ApproveEnrollmentRequest(adminContext(), orgId, cn, domain.EnrollmentRequestApproval{Approved: true})
		require.Equal(t, statusSuccessCode, status.Code)
		require.NotNil(t, result)
		require.True(t, result.Approved)

		device, err := fakeDevices.Get(context.Background(), orgId, cn)
		require.NoError(t, err)
		require.NotNil(t, device)
		require.Equal(t, domain.DeviceIntegrityStatusUnsupported, device.Status.Integrity.Status)
		require.Len(t, fakeEvents.createdWithReason(domain.EventReasonEnrollmentRequestApproved), 1)
	})
}

func TestReplaceEnrollmentRequestStatus(t *testing.T) {
	h, _, _, _, _ := newTestHandler(t)
	er := testEnrollmentRequest("missing")

	_, status := h.ReplaceEnrollmentRequestStatus(context.Background(), uuid.New(), "missing", er)
	require.Equal(t, statusNotFoundCode, status.Code)
}

// TestCreateDeviceFromEnrollmentRequestNeverManaged is a regression guard for the deviceOnlyStore
// adapter's safety invariant: createDeviceFromEnrollmentRequest must never set Metadata.Owner on
// the device it builds. deviceOnlyStore only overrides Device() on its embedded nil store.Store;
// every other accessor panics if called. UpdateServiceSideStatus only calls fleet.Service.GetFleet
// when the device IsManaged() (i.e. has a non-nil Owner), so if this invariant
// were ever broken, this test would fail with a panic instead of a production nil-pointer panic.
func TestCreateDeviceFromEnrollmentRequestNeverManaged(t *testing.T) {
	h, _, fakeDevices, _, _ := newTestHandler(t)
	ctx := context.Background()
	orgId := uuid.New()

	er := &domain.EnrollmentRequest{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("regression-device")},
		Spec:     domain.EnrollmentRequestSpec{Csr: "TestCSR", DeviceStatus: lo.ToPtr(domain.NewDeviceStatus())},
	}

	require.NotPanics(t, func() {
		err := h.createDeviceFromEnrollmentRequest(ctx, orgId, er)
		require.NoError(t, err)
	})

	device, err := fakeDevices.Get(ctx, orgId, "regression-device")
	require.NoError(t, err)
	require.Nil(t, device.Metadata.Owner)
	require.False(t, device.IsManaged())
}

func TestCreateDeviceFromEnrollmentRequest(t *testing.T) {
	t.Run("When a device with the same name already exists it should refuse overwrite", func(t *testing.T) {
		h, _, fakeDevices, _, fakeEvents := newTestHandler(t)
		ctx := context.Background()
		orgId := uuid.New()
		name := "existing-device"

		existing := &domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status:   lo.ToPtr(domain.NewDeviceStatus()),
		}
		existing.Status.Lifecycle.Status = "AlreadyEnrolled"
		fakeDevices.items[name] = existing

		er := &domain.EnrollmentRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Spec:     domain.EnrollmentRequestSpec{Csr: "TestCSR", DeviceStatus: lo.ToPtr(domain.NewDeviceStatus())},
		}

		err := h.createDeviceFromEnrollmentRequest(ctx, orgId, er)
		require.Error(t, err)
		require.Contains(t, err.Error(), "already exists and cannot be overwritten during enrollment request approval")

		stored, getErr := fakeDevices.Get(ctx, orgId, name)
		require.NoError(t, getErr)
		require.Equal(t, domain.DeviceLifecycleStatusType("AlreadyEnrolled"), stored.Status.Lifecycle.Status)
		require.Empty(t, fakeEvents.createdWithReason(domain.EventReasonResourceCreated))
	})

	t.Run("When the device does not exist it should create it and emit a created event", func(t *testing.T) {
		h, _, fakeDevices, _, fakeEvents := newTestHandler(t)
		ctx := context.Background()
		orgId := uuid.New()
		name := "new-device"

		er := &domain.EnrollmentRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Spec:     domain.EnrollmentRequestSpec{Csr: "TestCSR", DeviceStatus: lo.ToPtr(domain.NewDeviceStatus())},
		}

		err := h.createDeviceFromEnrollmentRequest(ctx, orgId, er)
		require.NoError(t, err)

		stored, getErr := fakeDevices.Get(ctx, orgId, name)
		require.NoError(t, getErr)
		require.NotNil(t, stored)
		require.Len(t, fakeEvents.createdWithReason(domain.EventReasonResourceCreated), 1)
	})
}
