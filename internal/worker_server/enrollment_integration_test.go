//go:build integration

package workerserver

import (
	"context"
	stdcrypto "crypto"
	"encoding/base32"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/identity"
	csrservice "github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	enrollmenthookpolicyservice "github.com/flightctl/flightctl/internal/service/enrollmenthookpolicy"
	"github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	secretstore "github.com/flightctl/flightctl/internal/store/enrollmenthooknotifysecrets"
	hookstore "github.com/flightctl/flightctl/internal/store/enrollmenthookpolicy"
	erstore "github.com/flightctl/flightctl/internal/store/enrollmentrequest"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/flightctl/flightctl/test/integration/integrationstack"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type workerTestMatcher struct{}

func (workerTestMatcher) Match(context.Context, domain.EnrollmentApprovalPolicy, domain.EnrollmentRequest) (bool, error) {
	return true, nil
}

type admissionPublication struct {
	t     *testing.T
	db    *gorm.DB
	count int
}

func (publisher *admissionPublication) EmitEvent(ctx context.Context, orgID uuid.UUID, event *domain.Event) {
	if event.Reason != domain.EventReasonEnrollmentRequestApproved && event.InvolvedObject.Kind != domain.DeviceKind {
		return
	}
	require.False(publisher.t, store.InTransaction(ctx))
	var admission model.EnrollmentAdmission
	require.NoError(publisher.t, publisher.db.Where("org_id = ? AND name = ?", orgID, event.InvolvedObject.Name).Take(&admission).Error)
	request, err := erstore.NewEnrollmentRequestStore(publisher.db, logrus.New()).Get(context.Background(), orgID, event.InvolvedObject.Name)
	require.NoError(publisher.t, err)
	require.NotNil(publisher.t, request.Status.Certificate)
	var devices int64
	require.NoError(publisher.t, publisher.db.Model(&model.Device{}).Where("org_id = ? AND name = ?", orgID, event.InvolvedObject.Name).Count(&devices).Error)
	require.EqualValues(publisher.t, 1, devices)
	publisher.count++
}

func workerRequest(t *testing.T) domain.EnrollmentRequest {
	t.Helper()
	publicKey, privateKey, err := fcrypto.NewKeyPair()
	require.NoError(t, err)
	hash, err := fcrypto.HashPublicKey(publicKey)
	require.NoError(t, err)
	name := strings.ToLower(base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(hash))
	csr, err := fcrypto.MakeCSR(privateKey.(stdcrypto.Signer), name)
	require.NoError(t, err)
	return domain.EnrollmentRequest{Metadata: domain.ObjectMeta{Name: &name}, Spec: domain.EnrollmentRequestSpec{Csr: string(csr)}}
}

func reevaluateQueuedEnrollment(t *testing.T, ctx context.Context, orgID uuid.UUID, event domain.Event, handler enrollmentrequest.Service, requests erstore.Store) {
	t.Helper()
	log := logrus.New()
	host, port, password, cleanup, err := testdb.CreateTestRedis(ctx, log)
	require.NoError(t, err)
	defer cleanup()
	provider, err := queues.NewRedisProvider(ctx, log, "worker-enrollment-test", host, port, password, queues.DefaultRetryConfig())
	require.NoError(t, err)
	defer func() { provider.Stop(); provider.Wait() }()
	require.NoError(t, tasks.LaunchConsumers(ctx, provider, tasks.TaskConsumer{EnrollmentRequestSvc: handler}, 1, 1))
	publisher, err := worker_client.QueuePublisher(ctx, provider)
	require.NoError(t, err)
	defer publisher.Close()
	payload, err := json.Marshal(worker_client.EventWithOrgId{OrgId: orgID, Event: event})
	require.NoError(t, err)
	require.NoError(t, publisher.Enqueue(ctx, payload, 0))
	require.Eventually(t, func() bool {
		request, err := requests.Get(ctx, orgID, event.InvolvedObject.Name)
		if err != nil || request.Status == nil {
			return false
		}
		condition := domain.FindStatusCondition(request.Status.Conditions, domain.ConditionTypeEnrollmentRequestApproved)
		return condition != nil && condition.Reason == "PolicyMatchingUnavailable"
	}, 10*time.Second, 25*time.Millisecond)
}

func TestWorkerEnrollmentAssembly(t *testing.T) {
	ctx := context.Background()
	require.NoError(t, integrationstack.EnsureRunning(ctx))
	for _, scenario := range []string{"hooks", "no policy", "foreign policy", "unavailable matcher", "rollback", "manual unbound", "manual bound", "manual serial", "TPM required", "revoked"} {
		t.Run("When worker enrollment uses "+scenario+" it should preserve admission contracts", func(t *testing.T) {
			log := logrus.New()
			dbCfg, dbName, database, err := testdb.CreateTestDB(ctx, log, "worker_admission", store.InitDB)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, testdb.DeleteTestDB(ctx, log, dbCfg, database, dbName)) })
			cfg := workerSigningConfig(t)
			if scenario == "manual serial" {
				cfg.CA.InternalConfig.SerialFile = filepath.Join(t.TempDir(), "serial")
				require.NoError(t, os.WriteFile(cfg.CA.InternalConfig.SerialFile, []byte("02\n"), 0600))
			}
			cfg.Service.TPMCAPaths = []string{t.TempDir() + "/unavailable-root.pem"}
			server, err := New(cfg, log, database, nil, nil, nil)
			require.NoError(t, err)
			orgID := uuid.New()
			require.NoError(t, database.Create(&model.Organization{ID: orgID, DisplayName: "worker-test", ExternalID: orgID.String()}).Error)
			ctx := util.WithOrganizationID(ctx, orgID)
			ctx = context.WithValue(ctx, consts.MappedIdentityCtxKey, identity.NewMappedIdentity("worker-test", "worker-test", nil, nil, false, nil))
			requests := erstore.NewEnrollmentRequestStore(database, log)
			devices := devicestore.NewDeviceStore(database, log)
			secrets := secretstore.NewStore(database, log)
			publication := &admissionPublication{t: t, db: database}
			eventSvc := events.NewServiceHandler(eventstore.NewEventStore(database, log), publication, log)
			csrs := csrstore.NewCertificateSigningRequestStore(database, log, cfg.CA.DeviceEnrollmentSignerName)
			reader := csrservice.NewServiceHandler(csrs, nil, nil, eventSvc, log, "", "")
			handler := server.newEnrollmentService(devices, reader, nil, eventSvc, secrets)
			if scenario != "unavailable matcher" {
				enrollmentrequest.WithPolicyMatcher(workerTestMatcher{})(handler)
			}
			backend, err := crypto.LoadInternalCA(cfg.CA)
			require.NoError(t, err)
			if scenario == "manual serial" {
				hookSvc := enrollmenthookpolicyservice.NewServiceHandler(hookstore.NewStore(database, log), eventSvc, log)
				handler = enrollmentrequest.NewServiceHandler(requests, devices, reader, crypto.NewCAClient(cfg.CA, backend), nil, eventSvc, log, nil, "", "", hookSvc, secrets)
			}
			issuer := csrservice.NewServiceHandler(csrs, nil, crypto.NewCAClient(cfg.CA, backend), eventSvc, log, "", "")
			credentialRequest := workerRequest(t)
			credential, status := issuer.CreateCertificateSigningRequest(ctx, orgID, domain.CertificateSigningRequest{
				Metadata: credentialRequest.Metadata,
				Spec:     domain.CertificateSigningRequestSpec{Request: []byte(credentialRequest.Spec.Csr), SignerName: cfg.CA.DeviceEnrollmentSignerName, Usages: &[]string{"clientAuth", "CA:false"}},
			})
			require.EqualValues(t, 201, status.Code, status.Message)
			require.NotNil(t, credential.Status.Certificate)
			if scenario == "revoked" {
				_, _, err := csrs.Revoke(ctx, orgID, *credential.Metadata.Name, domain.EnrollmentCertificateRevocation{RevokedAt: time.Now(), Reason: "fixture", RevokedBy: "fixture"}, nil)
				require.NoError(t, err)
			}
			policy, err := model.NewEnrollmentApprovalPolicyFromApiResource(&domain.EnrollmentApprovalPolicy{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr("policy")},
				Spec:     domain.EnrollmentApprovalPolicySpec{CertificateName: *credential.Metadata.Name, Enabled: true, MatchExpression: "true", RequireTPMVerification: scenario == "TPM required"},
				Status:   &domain.EnrollmentApprovalPolicyStatus{},
			})
			require.NoError(t, err)
			policy.OrgID, policy.ResourceVersion, policy.Generation = orgID, lo.ToPtr(int64(1)), lo.ToPtr(int64(1))
			require.NoError(t, database.Create(policy).Error)
			if scenario != "no policy" {
				hookOrg := orgID
				if scenario == "foreign policy" {
					hookOrg = store.NullOrgId
				}
				_, err := hookstore.NewStore(database, log).Create(ctx, hookOrg, &domain.EnrollmentHookPolicy{
					Metadata: domain.ObjectMeta{Name: lo.ToPtr("default")},
					Spec: domain.EnrollmentHookPolicySpec{AfterEnrolling: domain.EnrollmentHookStageSpec{
						FailurePolicy:       lo.ToPtr(domain.FailurePolicyBlock),
						ControlPlaneActions: &[]domain.EnrollmentHookHttpAction{{Url: "https://hooks.example.test/enrolled", Auth: &domain.EnrollmentHookAuth{BearerToken: lo.ToPtr("fixture-only")}}},
					}},
				})
				require.NoError(t, err)
			}
			request := workerRequest(t)
			if scenario != "manual unbound" {
				request.Status = &domain.EnrollmentRequestStatus{EnrollmentCertificateName: credential.Metadata.Name, Conditions: []domain.Condition{}}
			}
			created, err := requests.Create(ctx, orgID, &request)
			require.NoError(t, err)
			name := *created.Metadata.Name
			event := domain.Event{InvolvedObject: domain.ObjectReference{Kind: domain.EnrollmentRequestKind, Name: name}, Reason: domain.EventReasonResourceCreated}
			faultHit := false
			if scenario == "rollback" {
				require.NoError(t, database.Callback().Create().Before("gorm:create").Register("worker_device_failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "devices" {
						faultHit = true
						_ = tx.AddError(errors.New("device persistence failed"))
					}
				}))
			}
			if strings.HasPrefix(scenario, "manual") {
				_, status := handler.ApproveEnrollmentRequest(ctx, orgID, name, domain.EnrollmentRequestApproval{Approved: true})
				require.EqualValues(t, 200, status.Code, status.Message)
			} else if scenario == "unavailable matcher" {
				reevaluateQueuedEnrollment(t, ctx, orgID, event, handler, requests)
			} else {
				err = handler.ReevaluateEnrollment(ctx, orgID, event)
				if scenario == "rollback" {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
			}
			loaded, err := requests.Get(ctx, orgID, name)
			require.NoError(t, err)
			if scenario == "unavailable matcher" || scenario == "TPM required" || scenario == "rollback" || scenario == "revoked" {
				require.True(t, erstore.Pending(loaded))
				require.Nil(t, loaded.Status.Certificate)
				require.Zero(t, publication.count)
				var persistedEvents int64
				require.NoError(t, database.Model(&model.Event{}).Where("org_id = ? AND involved_object_name = ?", orgID, name).Count(&persistedEvents).Error)
				require.Zero(t, persistedEvents)
				for _, record := range []any{&model.Device{}, &model.EnrollmentAdmission{}, &model.EnrollmentHookNotifySecret{}} {
					var count int64
					require.NoError(t, database.Model(record).Where("org_id = ?", orgID).Count(&count).Error)
					require.Zero(t, count)
				}
				require.NoError(t, database.Where("org_id = ? AND name = ?", orgID, "policy").Take(policy).Error)
				require.Zero(t, policy.Status.Data.EnrollmentCount)
				if scenario != "rollback" {
					if scenario == "revoked" {
						_, status := handler.ApproveEnrollmentRequest(ctx, orgID, name, domain.EnrollmentRequestApproval{Approved: true})
						require.EqualValues(t, 409, status.Code, status.Message)
						return
					}
					reason := "PolicyMatchingUnavailable"
					if scenario == "TPM required" {
						reason = "TPMVerificationRequired"
					}
					require.Equal(t, reason, domain.FindStatusCondition(loaded.Status.Conditions, domain.ConditionTypeEnrollmentRequestApproved).Reason)
					return
				}
				require.True(t, faultHit)
				require.NoError(t, database.Callback().Create().Remove("worker_device_failure"))
				require.NoError(t, handler.ReevaluateEnrollment(ctx, orgID, event))
				loaded, err = requests.Get(ctx, orgID, name)
				require.NoError(t, err)
			}
			require.NotNil(t, loaded.Status.Certificate)
			certificate, err := fcrypto.ParseCertificatePEM([]byte(*loaded.Status.Certificate))
			require.NoError(t, err)
			require.NoError(t, certificate.CheckSignatureFrom(backend.GetCABundleX509()[0]))
			device, err := devices.Get(ctx, orgID, name)
			require.NoError(t, err)
			hasHooks := scenario != "no policy" && scenario != "foreign policy"
			require.Equal(t, hasHooks, domain.IsDeviceEnrollmentHooksGated(device))
			storedSecrets, err := secrets.ListByDevice(ctx, orgID, name)
			require.NoError(t, err)
			if !hasHooks {
				require.Empty(t, storedSecrets)
			} else {
				require.Len(t, storedSecrets, 1)
				require.Equal(t, "https://hooks.example.test/enrolled", (*device.Status.EnrollmentHooks.Snapshot.ControlPlaneActions)[0].Url)
			}
			require.Equal(t, 2, publication.count)
			var ledger model.EnrollmentAdmission
			require.NoError(t, database.Where("org_id = ? AND name = ?", orgID, name).Take(&ledger).Error)
			require.True(t, ledger.Published)
			require.NotEqual(t, uuid.Nil, ledger.AdmissionID)
			expectedSource := "policy"
			if strings.HasPrefix(scenario, "manual") {
				expectedSource = "manual"
			}
			require.Equal(t, expectedSource, ledger.Source)
			original := *loaded.Status.Certificate
			cfg.CA = nil
			unavailable, err := New(cfg, log, database, nil, nil, nil)
			require.NoError(t, err)
			handler = unavailable.newEnrollmentService(devices, reader, nil, eventSvc, secrets)
			_, status = handler.ApproveEnrollmentRequest(ctx, orgID, name, domain.EnrollmentRequestApproval{Approved: true})
			require.EqualValues(t, 200, status.Code, status.Message)
			require.NoError(t, handler.ReevaluateEnrollment(ctx, orgID, event))
			loaded, err = requests.Get(ctx, orgID, name)
			require.NoError(t, err)
			require.Equal(t, original, *loaded.Status.Certificate)
			require.Equal(t, 2, publication.count)
			require.NoError(t, database.Where("org_id = ? AND name = ?", orgID, "policy").Take(policy).Error)
			expectedCount := int64(1)
			if strings.HasPrefix(scenario, "manual") {
				expectedCount = 0
			}
			require.Equal(t, expectedCount, policy.Status.Data.EnrollmentCount)
		})
	}
}

func TestWorkerUnavailableSignerRunsFleetTasks(t *testing.T) {
	for _, defect := range []string{"missing", "unsupported", "serial", "invalid catalog"} {
		t.Run("When enrollment signing is "+defect+" it should still reconcile fleet ownership", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			log := logrus.New()
			dbCfg, dbName, database, err := testdb.CreateTestDB(ctx, log, "worker_nonfatal", store.InitDB)
			require.NoError(t, err)
			defer func() { require.NoError(t, testdb.DeleteTestDB(context.Background(), log, dbCfg, database, dbName)) }()
			host, port, password, cleanup, err := testdb.CreateTestRedis(ctx, log)
			require.NoError(t, err)
			defer cleanup()
			cfg := workerSigningConfig(t)
			cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password = host, port, password
			provider, err := queues.NewRedisProvider(ctx, log, "worker-nonfatal-"+defect, host, port, password, queues.DefaultRetryConfig())
			require.NoError(t, err)
			defer func() { provider.Stop(); provider.Wait() }()
			orgID := store.NullOrgId
			requests := erstore.NewEnrollmentRequestStore(database, log)
			csrs := csrstore.NewCertificateSigningRequestStore(database, log, cfg.CA.DeviceEnrollmentSignerName)
			backend, err := crypto.LoadInternalCA(cfg.CA)
			require.NoError(t, err)
			eventSvc := events.NewServiceHandler(eventstore.NewEventStore(database, log), nil, log)
			issuer := csrservice.NewServiceHandler(csrs, nil, crypto.NewCAClient(cfg.CA, backend), eventSvc, log, "", "")
			credentialRequest := workerRequest(t)
			credential, status := issuer.CreateCertificateSigningRequest(ctx, orgID, domain.CertificateSigningRequest{Metadata: credentialRequest.Metadata, Spec: domain.CertificateSigningRequestSpec{Request: []byte(credentialRequest.Spec.Csr), SignerName: cfg.CA.DeviceEnrollmentSignerName, Usages: &[]string{"clientAuth", "CA:false"}}})
			require.EqualValues(t, 201, status.Code, status.Message)
			request := workerRequest(t)
			request.Status = &domain.EnrollmentRequestStatus{EnrollmentCertificateName: credential.Metadata.Name, Conditions: []domain.Condition{}}
			_, err = requests.Create(ctx, orgID, &request)
			require.NoError(t, err)
			policy, err := model.NewEnrollmentApprovalPolicyFromApiResource(&domain.EnrollmentApprovalPolicy{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr("nonfatal-policy")},
				Spec:     domain.EnrollmentApprovalPolicySpec{CertificateName: *credential.Metadata.Name, Enabled: true, MatchExpression: "true"},
				Status:   &domain.EnrollmentApprovalPolicyStatus{},
			})
			require.NoError(t, err)
			policy.OrgID, policy.ResourceVersion, policy.Generation = orgID, lo.ToPtr(int64(1)), lo.ToPtr(int64(1))
			require.NoError(t, database.Create(policy).Error)
			devices := devicestore.NewDeviceStore(database, log)
			fleets := fleetstore.NewFleetStore(database, log)
			labels := map[string]string{"env": "worker-test"}
			_, err = fleets.Create(ctx, orgID, &domain.Fleet{Metadata: domain.ObjectMeta{Name: lo.ToPtr("worker-fleet")}, Spec: domain.FleetSpec{Selector: &domain.LabelSelector{MatchLabels: &labels}}})
			require.NoError(t, err)
			device := testutil.ReturnTestDevice(orgID, "worker-device", nil, nil, &labels)
			device.Spec.Config = nil
			_, err = devices.Create(ctx, orgID, &device, nil)
			require.NoError(t, err)
			switch defect {
			case "invalid catalog":
				cfg.CA = nil
				cfg.FirmwareUpdaterCatalogFile = filepath.Join(t.TempDir(), "missing-catalog.yaml")
			case "missing":
				cfg.CA = nil
			case "unsupported":
				cfg.CA.CAType = ca.AsyncInternalCA
			case "serial":
				cfg.CA.InternalConfig.SerialFile = filepath.Join(t.TempDir(), "serial")
			}
			server, err := New(cfg, log, database, provider, nil, nil)
			require.NoError(t, err)
			if defect == "invalid catalog" {
				require.ErrorContains(t, server.Run(ctx), "initialize approved updater catalog")
				return
			}
			finished := make(chan error, 1)
			go func() { finished <- server.Run(ctx) }()
			defer func() {
				cancel()
				provider.Stop()
				select {
				case err := <-finished:
					require.NoError(t, err)
				case <-time.After(20 * time.Second):
					t.Error("worker did not stop")
				}
			}()
			publisher, err := worker_client.QueuePublisher(ctx, provider)
			require.NoError(t, err)
			defer publisher.Close()
			for _, object := range []domain.ObjectReference{{Kind: domain.DeviceKind, Name: "worker-device"}, {Kind: domain.EnrollmentRequestKind, Name: *request.Metadata.Name}} {
				payload, err := json.Marshal(worker_client.EventWithOrgId{OrgId: orgID, Event: domain.Event{InvolvedObject: object, Reason: domain.EventReasonResourceCreated}})
				require.NoError(t, err)
				require.NoError(t, publisher.Enqueue(ctx, payload, 0))
			}
			require.Eventually(t, func() bool {
				current, err := devices.Get(ctx, orgID, "worker-device")
				return err == nil && lo.FromPtr(current.Metadata.Owner) == "Fleet/worker-fleet"
			}, 20*time.Second, 50*time.Millisecond)
			require.Eventually(t, func() bool {
				current, err := requests.Get(ctx, orgID, *request.Metadata.Name)
				if err != nil || current.Status == nil {
					return false
				}
				condition := domain.FindStatusCondition(current.Status.Conditions, domain.ConditionTypeEnrollmentRequestApproved)
				return condition != nil && condition.Reason == "EnrollmentSignerUnavailable" && current.Status.Certificate == nil && erstore.Pending(current)
			}, 20*time.Second, 50*time.Millisecond)
			for _, record := range []any{&model.Device{}, &model.EnrollmentAdmission{}} {
				var count int64
				require.NoError(t, database.Model(record).Where("org_id = ? AND name = ?", orgID, *request.Metadata.Name).Count(&count).Error)
				require.Zero(t, count)
			}
			require.NoError(t, database.Where("org_id = ? AND name = ?", orgID, "nonfatal-policy").Take(policy).Error)
			require.Zero(t, policy.Status.Data.EnrollmentCount)
			var approvedEvents int64
			require.NoError(t, database.Model(&model.Event{}).Where("org_id = ? AND involved_object_name = ? AND reason = ?", orgID, *request.Metadata.Name, domain.EventReasonEnrollmentRequestApproved).Count(&approvedEvents).Error)
			require.Zero(t, approvedEvents)
		})
	}
}
