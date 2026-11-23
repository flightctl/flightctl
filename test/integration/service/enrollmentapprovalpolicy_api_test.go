package service_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	convert "github.com/flightctl/flightctl/internal/api/convert/v1beta1"
	server "github.com/flightctl/flightctl/internal/api/server"
	apiserver "github.com/flightctl/flightctl/internal/api_server"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/auth/authz"
	"github.com/flightctl/flightctl/internal/cli"
	clientconfig "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/identity"
	policyservice "github.com/flightctl/flightctl/internal/service/enrollmentapprovalpolicy"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	policystore "github.com/flightctl/flightctl/internal/store/enrollmentapprovalpolicy"
	"github.com/flightctl/flightctl/internal/store/model"
	transport "github.com/flightctl/flightctl/internal/transport/v1beta1"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"go.uber.org/mock/gomock"
	"gorm.io/gorm"
)

type policyAPIValidator struct{}

func (policyAPIValidator) ValidateExpression(context.Context, string) error {
	return nil
}

type rejectingPolicyValidator struct{}

func (rejectingPolicyValidator) ValidateExpression(_ context.Context, expression string) error {
	if expression != "true &&" {
		return nil
	}
	return policyservice.InvalidPolicyError{Message: "spec.matchExpression is invalid", Cause: errors.New("test compiler diagnostic")}
}

type policyAPIAuthorizer struct {
	auth.AuthZMiddleware
	static     *authz.StaticAuthZ
	denyQuota  bool
	denyAction string
}

func (authorizer policyAPIAuthorizer) CheckPermission(ctx context.Context, resource, operation string) (bool, error) {
	if authorizer.denyQuota && resource == "enrollmentapprovalpolicies/approvallimit" {
		return false, nil
	}
	if resource == "enrollmentapprovalpolicies" && operation == authorizer.denyAction {
		return false, nil
	}
	return authorizer.static.CheckPermission(ctx, resource, operation)
}

type blockedPolicyValidator struct {
	entered chan struct{}
	release <-chan struct{}
}

func (v blockedPolicyValidator) ValidateExpression(ctx context.Context, _ string) error {
	select {
	case v.entered <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-v.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type policyReadRaceKey struct{}

type policyRetryObserver struct {
	policystore.Store
	db           *gorm.DB
	transactions []int64
	onAbort      func(*pgconn.PgError)
}

func (observer *policyRetryObserver) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	err := observer.Store.WithTransaction(ctx, func(txCtx context.Context) error {
		var transaction int64
		if err := store.DB(txCtx, observer.db).Raw("SELECT txid_current()").Scan(&transaction).Error; err != nil {
			return err
		}
		observer.transactions = append(observer.transactions, transaction)
		return fn(txCtx)
	})
	var abort *pgconn.PgError
	if errors.As(err, &abort) && observer.onAbort != nil {
		observer.onAbort(abort)
	}
	return err
}

func boundedPolicyTest(ctx context.Context) context.Context {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	DeferCleanup(cancel)
	return ctx
}

var _ = Describe("EnrollmentApprovalPolicyAPI", func() {
	var suite *ServiceTestSuite
	BeforeEach(func() {
		suite = &ServiceTestSuite{}
		suite.Setup()
		suite.Ctx = util.WithOrganizationID(suite.Ctx, suite.OrgID)
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		certificate := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
		der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
		Expect(err).NotTo(HaveOccurred())
		issued := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		Expect(suite.DB.Create(&model.CertificateSigningRequest{Resource: model.Resource{OrgID: suite.OrgID, Name: "commissioning"}, Spec: model.MakeJSONField(domain.CertificateSigningRequestSpec{SignerName: suite.cfg.CA.DeviceEnrollmentSignerName}), Status: model.MakeJSONField(domain.CertificateSigningRequestStatus{Certificate: &issued, IssuedAt: lo.ToPtr(time.Now())})}).Error).To(Succeed())
	})
	AfterEach(func() { suite.Teardown() })
	newStore := func() policystore.Store {
		policies, err := policystore.NewStore(suite.DB, suite.Log)
		Expect(err).NotTo(HaveOccurred())
		return policies
	}
	newService := func(policies policystore.Store, validator policyservice.ExpressionValidator) *policyservice.ServiceHandler {
		service, err := policyservice.NewServiceHandler(policies, validator, suite.cfg.CA.DeviceEnrollmentSignerName, nil, suite.Log)
		Expect(err).NotTo(HaveOccurred())
		return service
	}
	makeHandlerWithAuth := func(validator policyservice.ExpressionValidator, authorizer policyAPIAuthorizer) http.Handler {
		policies := newService(newStore(), validator)
		authorizer.static = authz.NewStaticAuthZ(suite.Log)
		handler := transport.NewTransportHandler(nil, suite.CertificateSigningRequest, nil, policies, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, convert.NewConverter(), nil, nil, nil, authorizer)
		router := chi.NewRouter()
		router.Use(auth.CreateAuthZMiddleware(authorizer, suite.Log))
		specification, err := api.GetSpec()
		Expect(err).NotTo(HaveOccurred())
		router.Use(oapimiddleware.OapiRequestValidatorWithOptions(specification, &oapimiddleware.Options{SilenceServersWarning: true}))
		return server.HandlerWithOptions(handler, server.ChiServerOptions{BaseURL: "/api/v1", BaseRouter: router})
	}
	makeHandler := func(validator policyservice.ExpressionValidator, denyQuota ...bool) http.Handler {
		return makeHandlerWithAuth(validator, policyAPIAuthorizer{denyQuota: len(denyQuota) > 0 && denyQuota[0]})
	}
	request := func(handler http.Handler, method, path string, body []byte, ctx context.Context) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/v1/enrollmentapprovalpolicies"+path, bytes.NewReader(body)).WithContext(ctx)
		if method == http.MethodPatch {
			req.Header.Set("Content-Type", "application/json-patch+json")
		} else {
			req.Header.Set("Content-Type", "application/json")
		}
		handler.ServeHTTP(response, req)
		return response
	}
	policyJSON := []byte(`{"apiVersion":"v1beta1","kind":"EnrollmentApprovalPolicy","metadata":{"name":"west"},"spec":{"certificateName":"commissioning","matchExpression":"true","deviceLabels":{}}}`)
	It("When the policy signer is missing the API should report it during service initialization", func() {
		ctx := boundedPolicyTest(suite.Ctx)
		cfg := *suite.cfg
		caConfig := *cfg.CA
		caConfig.DeviceEnrollmentSignerName = ""
		cfg.CA = &caConfig
		kvConfig := *cfg.KV
		kvConfig.Hostname, kvConfig.Port, kvConfig.Password = redisHost, redisPort, redisPassword
		cfg.KV = &kvConfig
		Expect(config.Validate(&cfg)).To(Succeed())
		broker, err := queues.NewRedisProvider(ctx, suite.Log, uuid.NewString(), redisHost, redisPort, redisPassword, queues.DefaultRetryConfig())
		Expect(err).NotTo(HaveOccurred())
		defer func() { broker.Stop(); broker.Wait() }()
		apiServer := apiserver.New(suite.Log, &cfg, suite.DB, suite.caClient, nil, broker, nil)
		err = apiServer.Run(ctx)
		Expect(err).To(MatchError("initialize enrollment approval policy service: device enrollment signer name is required"))
		Expect(errors.Unwrap(err)).To(MatchError("device enrollment signer name is required"))
	})
	DescribeTable("When the CLI applies a policy it should preserve ordinary and quota authorization", func(existing, denyQuota bool, denyAction string, limit int, methods []string, wantSuccess, enabled, unavailable bool) {
		manifest := func(cap int) []byte {
			return bytes.Replace(policyJSON, []byte(`"deviceLabels":{}`), fmt.Appendf(nil, `"deviceLabels":{"region":"west"},"approvalLimit":%d,"enabled":%t`, cap, enabled), 1)
		}
		fullAccess := makeHandler(policyAPIValidator{})
		if existing {
			created := request(fullAccess, http.MethodPost, "", manifest(10), suite.Ctx)
			Expect(created.Code).To(Equal(http.StatusCreated), created.Body.String())
		}
		var validator policyservice.ExpressionValidator = policyAPIValidator{}
		if unavailable {
			validator = policyservice.UnavailableExpressionValidator{}
		}
		handler := makeHandlerWithAuth(validator, policyAPIAuthorizer{denyQuota: denyQuota, denyAction: denyAction})
		var actual []string
		var methodsLock sync.Mutex
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			methodsLock.Lock()
			actual = append(actual, r.Method)
			methodsLock.Unlock()
			ctx := util.WithOrganizationID(r.Context(), suite.OrgID)
			ctx = context.WithValue(ctx, consts.MappedIdentityCtxKey, suite.Ctx.Value(consts.MappedIdentityCtxKey))
			handler.ServeHTTP(w, r.WithContext(ctx))
		}))
		defer srv.Close()
		dir := GinkgoT().TempDir()
		cfg := clientconfig.NewDefault()
		cfg.Service.Server = srv.URL
		Expect(cfg.Persist(filepath.Join(dir, "client.yaml"))).To(Succeed())
		file := filepath.Join(dir, "policy.json")
		Expect(os.WriteFile(file, manifest(limit), 0600)).To(Succeed())
		cmd := cli.NewCmdApply()
		cmd.SetArgs([]string{"--config-dir", dir, "--request-timeout", "5", "-f", file})
		err := cmd.ExecuteContext(suite.Ctx)
		if wantSuccess {
			Expect(err).NotTo(HaveOccurred())
		} else {
			Expect(err).To(HaveOccurred())
			var denied *cli.APIError
			Expect(errors.As(err, &denied)).To(BeTrue(), "%v", err)
			Expect(denied.Status).NotTo(BeNil())
			Expect(denied.Status.Code).To(Equal(int32(http.StatusForbidden)))
			Expect(denied.Status.Reason).To(Equal(http.StatusText(http.StatusForbidden)))
		}
		methodsLock.Lock()
		observed := append([]string(nil), actual...)
		methodsLock.Unlock()
		Expect(observed).To(Equal(methods))
		saved := request(fullAccess, http.MethodGet, "/west", nil, suite.Ctx)
		if existing || wantSuccess {
			Expect(saved.Code).To(Equal(http.StatusOK), saved.Body.String())
			var policy api.EnrollmentApprovalPolicy
			Expect(json.Unmarshal(saved.Body.Bytes(), &policy)).To(Succeed())
			Expect(policy.Spec.ApprovalLimit).To(Equal(lo.ToPtr(int32(10))))
			Expect(policy.Spec.Enabled).To(Equal(enabled))
			if existing && wantSuccess {
				Expect(policy.Metadata.ResourceVersion).To(Equal(lo.ToPtr("2")))
			} else {
				Expect(policy.Metadata.ResourceVersion).To(Equal(lo.ToPtr("1")))
			}
		} else {
			Expect(saved.Code).To(Equal(http.StatusNotFound))
		}
	},
		Entry("When the cap is unchanged it should apply with ordinary permissions", true, true, "", 10, []string{"POST", "GET", "PUT"}, true, false, false),
		Entry("When the cap changes it should preserve the quota denial", true, true, "", 11, []string{"POST", "GET", "PUT"}, false, false, false),
		Entry("When quota-authorized create lacks read permission it should still create", false, false, "get", 10, []string{"POST"}, true, false, false),
		Entry("When a finite policy is absent and quota is denied it should not create", false, true, "", 10, []string{"POST", "GET"}, false, false, false),
		Entry("When create is denied it should not attempt to read or update", true, true, "create", 10, []string{"POST"}, false, false, false),
		Entry("When update is denied it should not write the existing policy", true, true, "update", 10, []string{"POST", "GET", "PUT"}, false, false, false),
		Entry("When read is denied it should not attempt to update", true, true, "get", 10, []string{"POST", "GET"}, false, false, false),
		Entry("When compilation is unavailable it should apply an unchanged enabled expression", true, false, "", 10, []string{"POST", "GET", "PUT"}, true, true, true),
		Entry("When compilation is unavailable it should still require quota permission for an enabled policy", true, true, "", 11, []string{"POST", "GET", "PUT"}, false, true, true),
	)
	It("When the cap changes after the PATCH read it should reject the stale no-quota request", func() {
		ctx, cancel := context.WithTimeout(suite.Ctx, 10*time.Second)
		defer cancel()
		fullAccess := makeHandler(policyAPIValidator{})
		initial := bytes.Replace(policyJSON, []byte(`"deviceLabels":{}`), []byte(`"deviceLabels":{},"approvalLimit":10`), 1)
		created := request(fullAccess, http.MethodPost, "", initial, ctx)
		Expect(created.Code).To(Equal(http.StatusCreated), created.Body.String())
		var original api.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(created.Body.Bytes(), &original)).To(Succeed())
		var fired atomic.Bool
		callbackName := "test:cap_change_between_read_and_lock"
		Expect(suite.DB.Callback().Query().After("gorm:after_query").Register(callbackName, func(query *gorm.DB) {
			if query.Statement.Context.Value(policyReadRaceKey{}) == nil || query.Statement.Table != "enrollment_approval_policies" || query.Error != nil || !fired.CompareAndSwap(false, true) {
				return
			}
			change := fmt.Appendf(nil, `[{"op":"test","path":"/metadata/resourceVersion","value":%q},{"op":"replace","path":"/spec/approvalLimit","value":12}]`, *original.Metadata.ResourceVersion)
			competing := request(fullAccess, http.MethodPatch, "/west", change, ctx)
			Expect(competing.Code).To(Equal(http.StatusOK), competing.Body.String())
		})).To(Succeed())
		defer func() { Expect(suite.DB.Callback().Query().Remove(callbackName)).To(Succeed()) }()
		patch := fmt.Appendf(nil, `[{"op":"test","path":"/metadata/resourceVersion","value":%q},{"op":"replace","path":"/spec","value":{"certificateName":"commissioning","matchExpression":"true","deviceLabels":{"region":"west"},"approvalLimit":10}}]`, *original.Metadata.ResourceVersion)
		stale := request(makeHandler(policyAPIValidator{}, true), http.MethodPatch, "/west", patch, context.WithValue(ctx, policyReadRaceKey{}, true))
		Expect(fired.Load()).To(BeTrue())
		Expect(stale.Code).To(Equal(http.StatusConflict), stale.Body.String())
		response := request(fullAccess, http.MethodGet, "/west", nil, ctx)
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		var saved api.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(response.Body.Bytes(), &saved)).To(Succeed())
		Expect(saved.Spec.ApprovalLimit).To(Equal(lo.ToPtr(int32(12))))
		Expect(saved.Spec.DeviceLabels).To(BeEmpty())
	})
	It("When the generated client clears both optional limits it should send explicit null values", func() {
		handler := makeHandler(policyAPIValidator{})
		initial := bytes.Replace(policyJSON, []byte(`"deviceLabels":{}`), []byte(`"deviceLabels":{},"approvalLimit":10,"autoApprovalWindowDays":3`), 1)
		response := request(handler, http.MethodPost, "", initial, suite.Ctx)
		Expect(response.Code).To(Equal(http.StatusCreated), response.Body.String())
		var policy api.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(response.Body.Bytes(), &policy)).To(Succeed())
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := util.WithOrganizationID(r.Context(), suite.OrgID)
			ctx = context.WithValue(ctx, consts.MappedIdentityCtxKey, suite.Ctx.Value(consts.MappedIdentityCtxKey))
			handler.ServeHTTP(w, r.WithContext(ctx))
		}))
		DeferCleanup(srv.Close)
		client, err := apiclient.NewClientWithResponses(srv.URL + "/api/v1")
		Expect(err).NotTo(HaveOccurred())
		patch := api.EnrollmentApprovalPolicyPatchRequest{
			{Op: "test", Path: "/metadata/resourceVersion", Value: fmt.Appendf(nil, "%q", *policy.Metadata.ResourceVersion)},
			{Op: "replace", Path: "/spec/approvalLimit", Value: nil},
			{Op: "replace", Path: "/spec/autoApprovalWindowDays", Value: json.RawMessage("null")},
		}
		missing, err := client.PatchEnrollmentApprovalPolicyWithApplicationJSONPatchPlusJSONBodyWithResponse(suite.Ctx, "west", patch)
		Expect(err).NotTo(HaveOccurred())
		Expect(missing.StatusCode()).To(Equal(http.StatusBadRequest), string(missing.Body))
		Expect(missing.JSON400.Message).To(ContainSubstring("patch value is required"))
		patch[1].Value = json.RawMessage("null")
		patched, err := client.PatchEnrollmentApprovalPolicyWithApplicationJSONPatchPlusJSONBodyWithResponse(suite.Ctx, "west", patch)
		Expect(err).NotTo(HaveOccurred())
		Expect(patched.StatusCode()).To(Equal(http.StatusOK), string(patched.Body))
		Expect(patched.JSON200.Spec.ApprovalLimit).To(BeNil())
		Expect(patched.JSON200.Spec.AutoApprovalWindowDays).To(BeNil())
		saved, err := client.GetEnrollmentApprovalPolicyWithResponse(suite.Ctx, "west")
		Expect(err).NotTo(HaveOccurred())
		Expect(saved.StatusCode()).To(Equal(http.StatusOK))
		Expect(saved.JSON200.Spec).To(Equal(patched.JSON200.Spec))
	})
	DescribeTable("When PostgreSQL aborts a policy update it should preserve transaction ownership", func(owned, interveningWrite bool) {
		ctx := boundedPolicyTest(suite.Ctx)
		policies := newStore()
		initial := newService(policies, policyAPIValidator{})
		var policy domain.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(policyJSON, &policy)).To(Succeed())
		created, status := initial.CreateEnrollmentApprovalPolicy(ctx, suite.OrgID, policy)
		Expect(status.Code).To(Equal(int32(http.StatusCreated)), status.Message)
		migrationDB, err := store.InitMigrationDB(suite.cfg, suite.Log)
		Expect(err).NotTo(HaveOccurred())
		defer func() {
			sqlDB, err := migrationDB.DB()
			Expect(err).NotTo(HaveOccurred())
			Expect(sqlDB.Close()).To(Succeed())
		}()
		ddl := migrationDB.WithContext(store.WithBypassSpanCheck(ctx))
		Expect(ddl.Exec(`CREATE SEQUENCE policy_retry_probe`).Error).To(Succeed())
		Expect(ddl.Exec(`CREATE FUNCTION policy_retry_probe() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
				IF nextval('public.policy_retry_probe') = 1 THEN
					RAISE serialization_failure USING MESSAGE = 'fixture aborted the first transaction';
				END IF;
				RETURN NEW;
			END
			$$`).Error).To(Succeed())
		Expect(ddl.Exec(`CREATE TRIGGER policy_retry_probe BEFORE UPDATE ON enrollment_approval_policies
			FOR EACH ROW EXECUTE FUNCTION policy_retry_probe()`).Error).To(Succeed())
		eventService := events.NewMockService(gomock.NewController(GinkgoT()))
		if owned && !interveningWrite {
			eventService.EXPECT().CreateEvent(gomock.Any(), suite.OrgID, gomock.Any()).Do(func(_ context.Context, orgID uuid.UUID, _ *domain.Event) {
				saved, err := policies.Get(ctx, orgID, "west")
				Expect(err).NotTo(HaveOccurred())
				Expect(*saved.Metadata.ResourceVersion).To(Equal("2"), "the event observes the committed update")
			}).Times(1)
		}
		var observed []string
		observer := &policyRetryObserver{Store: policies, db: suite.DB, onAbort: func(abort *pgconn.PgError) {
			observed = append(observed, abort.Code)
			if interveningWrite {
				competitor := policy
				competitor.Metadata.Labels = lo.ToPtr(map[string]string{"edited": "competitor"})
				_, status := initial.ReplaceEnrollmentApprovalPolicy(ctx, suite.OrgID, "west", competitor)
				Expect(status.Code).To(Equal(int32(http.StatusOK)), status.Message)
			}
		}}
		service, err := policyservice.NewServiceHandler(observer, policyAPIValidator{}, suite.cfg.CA.DeviceEnrollmentSignerName, eventService, suite.Log)
		Expect(err).NotTo(HaveOccurred())
		policy.Metadata.ResourceVersion = created.Metadata.ResourceVersion
		policy.Metadata.Labels = lo.ToPtr(map[string]string{"edited": "true"})
		if owned {
			_, status = service.ReplaceEnrollmentApprovalPolicy(ctx, suite.OrgID, "west", policy)
			if interveningWrite {
				Expect(status.Code).To(Equal(int32(http.StatusConflict)), status.Message)
				Expect(status.Reason).To(Equal("ResourceVersionConflict"))
			} else {
				Expect(status.Code).To(Equal(int32(http.StatusOK)), status.Message)
			}
			Expect(observer.transactions).To(HaveLen(2))
			Expect(observer.transactions[1]).NotTo(Equal(observer.transactions[0]))
		} else {
			rollback := errors.New("caller rolls back")
			Expect(store.WithTransaction(ctx, suite.DB, func(txCtx context.Context) error {
				_, status = service.ReplaceEnrollmentApprovalPolicy(txCtx, suite.OrgID, "west", policy)
				Expect(status.Code).To(Equal(int32(http.StatusInternalServerError)), status.Message)
				err := store.DB(txCtx, suite.DB).Exec("SELECT 1").Error
				var pgErr *pgconn.PgError
				Expect(errors.As(err, &pgErr)).To(BeTrue())
				Expect(pgErr.Code).To(Equal("25P02"))
				return rollback
			})).To(MatchError(rollback))
			Expect(observer.transactions).To(HaveLen(1))
		}
		Expect(observed).To(Equal([]string{"40001"}))
		_, _ = fmt.Fprintf(GinkgoWriter, "Policy retry: SQLSTATE=%v transaction IDs=%v HTTP=%d\n", observed, observer.transactions, status.Code)
		saved, err := policies.Get(ctx, suite.OrgID, "west")
		Expect(err).NotTo(HaveOccurred())
		Expect(saved.Status.EnrollmentCount).To(BeZero())
		var attempts int
		Expect(ddl.Raw(`SELECT last_value FROM policy_retry_probe`).Scan(&attempts).Error).To(Succeed())
		if owned {
			Expect(attempts).To(Equal(2))
			Expect(*saved.Metadata.ResourceVersion).To(Equal("2"))
			label := "true"
			if interveningWrite {
				label = "competitor"
			}
			Expect(*saved.Metadata.Labels).To(HaveKeyWithValue("edited", label))
		} else {
			Expect(attempts).To(Equal(1))
			Expect(saved).To(Equal(created))
		}
	},
		Entry("When the service owns the transaction it should commit once and publish one event", true, false),
		Entry("When a different write commits before the retry it should reject the original version", true, true),
		Entry("When the caller owns the transaction it should return the abort without replay", false, false),
	)
	DescribeTable("When nullable policy values are patched it should preserve the API contract", func(field, op, value, version string, denyQuota bool, want int) {
		ctx, cancel := context.WithTimeout(suite.Ctx, 15*time.Second)
		defer cancel()
		privileged := makeHandler(policyAPIValidator{})
		var initial api.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(policyJSON, &initial)).To(Succeed())
		initial.Spec.ApprovalLimit = lo.ToPtr(int32(5))
		initial.Spec.AutoApprovalWindowDays = lo.ToPtr(int32(2))
		initial.Spec.Enabled, initial.Spec.RequireTPMVerification = true, true
		body, err := json.Marshal(initial)
		Expect(err).NotTo(HaveOccurred())
		created := request(privileged, http.MethodPost, "", body, ctx)
		Expect(created.Code).To(Equal(201), created.Body.String())
		Expect(suite.DB.WithContext(ctx).Model(&model.EnrollmentApprovalPolicy{}).Where("org_id = ? AND name = ?", suite.OrgID, "west").UpdateColumn("status", model.MakeJSONField(domain.EnrollmentApprovalPolicyStatus{EnrollmentCount: math.MaxInt64})).Error).To(Succeed())
		before := request(privileged, http.MethodGet, "/west", nil, ctx)
		Expect(before.Code).To(Equal(200))
		var current api.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(before.Body.Bytes(), &current)).To(Succeed())
		if version == "current" {
			version = *current.Metadata.ResourceVersion
		}
		prefix := ""
		if version != "missing" {
			prefix = fmt.Sprintf(`{"op":"test","path":"/metadata/resourceVersion","value":%q},`, version)
		}
		path := "/spec/" + field
		if field == "spec" {
			path = "/spec"
		}
		valueField := ""
		if value != "missing" {
			valueField = `,"value":` + value
		}
		body = []byte(fmt.Sprintf(`[%s{"op":%q,"path":%q%s}]`, prefix, op, path, valueField))
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/enrollmentapprovalpolicies/west", bytes.NewReader(body)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json-patch+json")
		response := httptest.NewRecorder()
		makeHandler(policyAPIValidator{}, denyQuota).ServeHTTP(response, req)
		Expect(response.Code).To(Equal(want), response.Body.String())
		after := request(privileged, http.MethodGet, "/west", nil, ctx)
		Expect(after.Code).To(Equal(200))
		if want != 200 {
			if want == 400 {
				var failure api.Status
				Expect(json.Unmarshal(response.Body.Bytes(), &failure)).To(Succeed())
				Expect(failure.Reason).To(Equal("Bad Request"))
			}
			Expect(after.Body.String()).To(MatchJSON(before.Body.String()))
			return
		}
		var saved api.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(after.Body.Bytes(), &saved)).To(Succeed())
		Expect(response.Body.String()).To(MatchJSON(after.Body.String()))
		Expect(saved.Status.EnrollmentCount).To(Equal(int64(math.MaxInt64)))
		Expect(saved.Metadata.ResourceVersion).NotTo(Equal(current.Metadata.ResourceVersion))
		if field == "spec" {
			Expect(saved.Spec.ApprovalLimit).To(BeNil())
			Expect(saved.Spec.AutoApprovalWindowDays).To(BeNil())
		} else if field == "approvalLimit" {
			if value == "null" || op == "remove" {
				Expect(saved.Spec.ApprovalLimit).To(BeNil())
			} else {
				Expect(fmt.Sprint(*saved.Spec.ApprovalLimit)).To(Equal(value))
			}
			Expect(saved.Spec.AutoApprovalWindowDays).To(Equal(current.Spec.AutoApprovalWindowDays))
		} else {
			if value == "null" || op == "remove" {
				Expect(saved.Spec.AutoApprovalWindowDays).To(BeNil())
			} else {
				Expect(fmt.Sprint(*saved.Spec.AutoApprovalWindowDays)).To(Equal(value))
			}
			Expect(saved.Spec.ApprovalLimit).To(Equal(current.Spec.ApprovalLimit))
		}
	},
		Entry("When replacing the limit with null it should clear it", "approvalLimit", "replace", "null", "current", false, 200),
		Entry("When adding a null limit it should clear it", "approvalLimit", "add", "null", "current", false, 200),
		Entry("When removing the limit it should clear it", "approvalLimit", "remove", "missing", "current", false, 200),
		Entry("When replacing the window with null it should clear it without quota privilege", "autoApprovalWindowDays", "replace", "null", "current", true, 200),
		Entry("When adding a null window it should clear it", "autoApprovalWindowDays", "add", "null", "current", true, 200),
		Entry("When removing the window it should clear it", "autoApprovalWindowDays", "remove", "missing", "current", true, 200),
		Entry("When a limit value is missing it should reject", "approvalLimit", "replace", "missing", "current", false, 400),
		Entry("When a window value is missing it should reject", "autoApprovalWindowDays", "add", "missing", "current", false, 400),
		Entry("When a test value is missing it should reject", "approvalLimit", "test", "missing", "current", false, 400),
		Entry("When quota clear is denied it should preserve the limit", "approvalLimit", "replace", "null", "current", true, 403),
		Entry("When quota removal is denied it should preserve the limit", "approvalLimit", "remove", "missing", "current", true, 403),
		Entry("When a noncanonical limit path is used it should reject it", "ApprovalLimit", "replace", "null", "current", true, 400),
		Entry("When whole spec clear is denied it should preserve the limit", "spec", "replace", `{"certificateName":"commissioning","matchExpression":"true","deviceLabels":{},"approvalLimit":null}`, "current", true, 403),
		Entry("When the version is stale it should preserve both limits", "approvalLimit", "replace", "null", "0", false, 409),
		Entry("When the version is missing it should preserve both limits", "autoApprovalWindowDays", "replace", "null", "missing", false, 409),
		Entry("When the limit becomes zero it should remain explicit", "approvalLimit", "replace", "0", "current", false, 200),
		Entry("When the limit becomes maximal it should remain exact", "approvalLimit", "replace", "2147483647", "current", false, 200),
		Entry("When the limit is negative it should reject", "approvalLimit", "replace", "-1", "current", false, 400),
		Entry("When the limit overflows it should reject", "approvalLimit", "replace", "2147483648", "current", false, 400),
		Entry("When the limit is fractional it should reject", "approvalLimit", "replace", "0.5", "current", false, 400),
		Entry("When the limit is a string it should reject", "approvalLimit", "replace", `"1"`, "current", false, 400),
		Entry("When the window becomes maximal it should remain exact", "autoApprovalWindowDays", "replace", "106751", "current", true, 200),
		Entry("When the window is zero it should reject", "autoApprovalWindowDays", "replace", "0", "current", true, 400),
		Entry("When the window overflows it should reject", "autoApprovalWindowDays", "replace", "106752", "current", true, 400),
		Entry("When the window is a string it should reject", "autoApprovalWindowDays", "replace", `"2"`, "current", true, 400),
		Entry("When enabled is null in a whole spec it should reject before coercion", "spec", "replace", `{"certificateName":"commissioning","matchExpression":"true","deviceLabels":{},"enabled":null}`, "current", false, 400),
		Entry("When TPM is null in a whole spec it should reject before coercion", "spec", "replace", `{"certificateName":"commissioning","matchExpression":"true","deviceLabels":{},"requireTPMVerification":null}`, "current", false, 400),
		Entry("When both limits are null in a whole spec it should clear them", "spec", "replace", `{"certificateName":"commissioning","matchExpression":"true","deviceLabels":{},"approvalLimit":null,"autoApprovalWindowDays":null}`, "current", false, 200),
		Entry("When a whole spec is null it should reject", "spec", "replace", "null", "current", false, 400),
		Entry("When a label value is null it should reject before coercion", "deviceLabels", "replace", `{"region":null}`, "current", false, 400),
		Entry("When a label key is null it should reject before coercion", "allowedIncomingLabels", "replace", `[null]`, "current", false, 400),
		Entry("When a finite limit is tested against null it should fail without quota privilege", "approvalLimit", "test", "null", "current", true, 400),
	)
	It("When nullable values are tested after clearing it should retain explicit null through serialization", func() {
		ctx, cancel := context.WithTimeout(suite.Ctx, 15*time.Second)
		defer cancel()
		handler := makeHandler(policyAPIValidator{})
		created := request(handler, http.MethodPost, "", policyJSON, ctx)
		Expect(created.Code).To(Equal(201), created.Body.String())
		var policy api.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(created.Body.Bytes(), &policy)).To(Succeed())
		patch := fmt.Sprintf(`[{"op":"test","path":"/metadata/resourceVersion","value":%q},
			{"op":"add","path":"/spec/approvalLimit","value":null},
			{"op":"test","path":"/spec/approvalLimit","value":null},
			{"op":"add","path":"/spec/autoApprovalWindowDays","value":null},
			{"op":"test","path":"/spec/autoApprovalWindowDays","value":null},
			{"op":"remove","path":"/spec/autoApprovalWindowDays"}]`, *policy.Metadata.ResourceVersion)
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/enrollmentapprovalpolicies/west", bytes.NewReader([]byte(patch))).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json-patch+json")
		updated := httptest.NewRecorder()
		handler.ServeHTTP(updated, req)
		Expect(updated.Code).To(Equal(200), updated.Body.String())
		fetched := request(handler, http.MethodGet, "/west", nil, ctx)
		Expect(fetched.Code).To(Equal(200))
		Expect(fetched.Body.String()).To(MatchJSON(updated.Body.String()))
		Expect(json.Unmarshal(fetched.Body.Bytes(), &policy)).To(Succeed())
		Expect(policy.Spec.ApprovalLimit).To(BeNil())
		Expect(policy.Spec.AutoApprovalWindowDays).To(BeNil())
		Expect(policy.Status.EnrollmentCount).To(BeZero())
	})
	It("When expression validation waits it should allow credential revocation without a CSR write lock", func() {
		ctx, cancel := context.WithTimeout(suite.Ctx, 10*time.Second)
		defer cancel()
		entered, release := make(chan struct{}, 1), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		service := newService(newStore(), blockedPolicyValidator{entered, release})
		var policy domain.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(policyJSON, &policy)).To(Succeed())
		policy.Spec.Enabled = true
		created, createDone := make(chan domain.Status, 1), make(chan struct{})
		go func() {
			defer close(createDone)
			_, status := service.CreateEnrollmentApprovalPolicy(ctx, suite.OrgID, policy)
			created <- status
		}()
		defer func() { cancel(); unblock(); Eventually(createDone, 5*time.Second).Should(BeClosed()) }()
		Eventually(entered, 5*time.Second).Should(Receive())
		revoked, revokeDone := make(chan error, 1), make(chan struct{})
		credentials := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.cfg.CA.DeviceEnrollmentSignerName)
		go func() {
			defer close(revokeDone)
			_, _, _, err := credentials.Mutate(ctx, suite.OrgID, "commissioning", nil, func(m *csrstore.CertificateSigningRequestMutation) error {
				return m.ApplyRevocation(domain.EnrollmentCertificateRevocation{Reason: "retired", RevokedAt: time.Now(), RevokedBy: "test"})
			})
			revoked <- err
		}()
		defer func() { cancel(); unblock(); Eventually(revokeDone, 5*time.Second).Should(BeClosed()) }()
		var err error
		Eventually(revoked, 5*time.Second).Should(Receive(&err))
		Expect(err).NotTo(HaveOccurred())
		unblock()
		var status domain.Status
		Eventually(created, 5*time.Second).Should(Receive(&status))
		Expect(status.Code).To(Equal(int32(201)), status.Message)
		credential, err := credentials.Get(ctx, suite.OrgID, "commissioning")
		Expect(err).NotTo(HaveOccurred())
		Expect(credential.Status.HasRevocation()).To(BeTrue())
	})
	It("When accounting changes after policy load it should preserve the count and reject a stale write", func() {
		ctx, cancel := context.WithTimeout(suite.Ctx, 10*time.Second)
		defer cancel()
		policies := newStore()
		service := newService(policies, policyAPIValidator{})
		var policy domain.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(policyJSON, &policy)).To(Succeed())
		current, status := service.CreateEnrollmentApprovalPolicy(ctx, suite.OrgID, policy)
		Expect(status.Code).To(Equal(int32(201)))
		var fired atomic.Bool
		callbackName := "test:accounting_after_policy_load"
		Expect(suite.DB.Callback().Query().After("gorm:after_query").Register(callbackName, func(query *gorm.DB) {
			if query.Statement.Context.Value(policyReadRaceKey{}) == nil || query.Statement.Table != "enrollment_approval_policies" || !query.Statement.Unscoped || query.Error != nil || !fired.CompareAndSwap(false, true) {
				return
			}
			Expect(suite.DB.WithContext(ctx).Model(&model.EnrollmentApprovalPolicy{}).Where("org_id = ? AND name = ?", suite.OrgID, "west").
				Updates(map[string]interface{}{"status": model.MakeJSONField(domain.EnrollmentApprovalPolicyStatus{EnrollmentCount: math.MaxInt64}), "resource_version": 2}).Error).To(Succeed())
		})).To(Succeed())
		defer func() { Expect(suite.DB.Callback().Query().Remove(callbackName)).To(Succeed()) }()
		current.Status = nil
		current.Spec.Enabled = true
		_, status = service.ReplaceEnrollmentApprovalPolicy(context.WithValue(ctx, policyReadRaceKey{}, true), suite.OrgID, "west", *current)
		Expect(fired.Load()).To(BeTrue())
		Expect(status.Code).To(Equal(int32(409)), status.Message)
		preserved, err := policies.Get(ctx, suite.OrgID, "west")
		Expect(err).NotTo(HaveOccurred())
		Expect(preserved.Status.EnrollmentCount).To(Equal(int64(math.MaxInt64)))
		Expect(preserved.Metadata.ResourceVersion).To(Equal(lo.ToPtr("2")))
		Expect(preserved.Spec.Enabled).To(BeFalse())
	})
	for _, method := range []string{"PUT", "PATCH"} {
		It("When deletion races "+method+" after its initial read it should report the policy missing", func() {
			ctx, cancel := context.WithTimeout(suite.Ctx, 10*time.Second)
			defer cancel()
			policies := newStore()
			service := newService(policies, policyAPIValidator{})
			var policy domain.EnrollmentApprovalPolicy
			Expect(json.Unmarshal(policyJSON, &policy)).To(Succeed())
			current, status := service.CreateEnrollmentApprovalPolicy(ctx, suite.OrgID, policy)
			Expect(status.Code).To(Equal(int32(201)))
			var fired atomic.Bool
			callbackName := "test:policy_delete_between_read_and_lock"
			Expect(suite.DB.Callback().Query().After("gorm:after_query").Register(callbackName, func(query *gorm.DB) {
				if query.Statement.Context.Value(policyReadRaceKey{}) == nil || query.Statement.Table != "enrollment_approval_policies" || query.Error != nil || !fired.CompareAndSwap(false, true) {
					return
				}
				Expect(policies.WithTransaction(ctx, func(txCtx context.Context) error {
					return policies.Delete(txCtx, suite.OrgID, "west")
				})).To(Succeed())
			})).To(Succeed())
			defer func() { Expect(suite.DB.Callback().Query().Remove(callbackName)).To(Succeed()) }()
			marked := context.WithValue(ctx, policyReadRaceKey{}, true)
			if method == "PUT" {
				current.Status = nil
				_, status = service.ReplaceEnrollmentApprovalPolicy(marked, suite.OrgID, "west", *current)
			} else {
				_, status = service.PatchEnrollmentApprovalPolicy(marked, suite.OrgID, "west", domain.PatchRequest{{Op: "test", Path: "/metadata/resourceVersion", Value: *current.Metadata.ResourceVersion}, {Op: "replace", Path: "/spec/enabled", Value: true}})
			}
			Expect(fired.Load()).To(BeTrue())
			Expect(status.Code).To(Equal(int32(404)), status.Message)
		})
	}

	for _, field := range []string{"enabled", "requireTPMVerification"} {
		It("When a patch sets "+field+" to null it should reject without changing the policy", func() {
			handler := makeHandler(policyAPIValidator{})
			var policy api.EnrollmentApprovalPolicy
			Expect(json.Unmarshal(policyJSON, &policy)).To(Succeed())
			policy.Spec.Enabled, policy.Spec.RequireTPMVerification = true, true
			body, err := json.Marshal(policy)
			Expect(err).NotTo(HaveOccurred())
			created := request(handler, http.MethodPost, "", body, suite.Ctx)
			Expect(created.Code).To(Equal(201), created.Body.String())
			Expect(json.Unmarshal(created.Body.Bytes(), &policy)).To(Succeed())
			body = []byte(fmt.Sprintf(`[{"op":"test","path":"/metadata/resourceVersion","value":%q},{"op":"replace","path":"/spec/%s","value":null}]`, *policy.Metadata.ResourceVersion, field))
			req := httptest.NewRequest(http.MethodPatch, "/api/v1/enrollmentapprovalpolicies/west", bytes.NewReader(body)).WithContext(suite.Ctx)
			req.Header.Set("Content-Type", "application/json-patch+json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			Expect(response.Code).To(Equal(400), response.Body.String())
			retained := request(handler, http.MethodGet, "/west", nil, suite.Ctx)
			Expect(retained.Code).To(Equal(200))
			Expect(retained.Body.String()).To(MatchJSON(created.Body.String()))
		})
	}

	DescribeTable("When policy numeric inputs cross the API it should enforce nullable int32 limits", func(value string, expected *int32, valid bool) {
		handler := makeHandler(policyservice.UnavailableExpressionValidator{})
		document := func(name, version, limit string) []byte {
			var policy map[string]json.RawMessage
			Expect(json.Unmarshal(policyJSON, &policy)).To(Succeed())
			metadata := map[string]string{"name": name}
			if version != "" {
				metadata["resourceVersion"] = version
			}
			var err error
			policy["metadata"], err = json.Marshal(metadata)
			Expect(err).NotTo(HaveOccurred())
			if limit != "omitted" {
				var spec map[string]json.RawMessage
				Expect(json.Unmarshal(policy["spec"], &spec)).To(Succeed())
				spec["approvalLimit"] = json.RawMessage(limit)
				policy["spec"], err = json.Marshal(spec)
				Expect(err).NotTo(HaveOccurred())
			}
			body, err := json.Marshal(policy)
			Expect(err).NotTo(HaveOccurred())
			return body
		}
		assertLimit := func(response *httptest.ResponseRecorder, code int, limit *int32) api.EnrollmentApprovalPolicy {
			Expect(response.Code).To(Equal(code), response.Body.String())
			var decoded api.EnrollmentApprovalPolicy
			Expect(json.Unmarshal(response.Body.Bytes(), &decoded)).To(Succeed())
			Expect(decoded.Spec.ApprovalLimit).To(Equal(limit))
			Expect(decoded.Status.EnrollmentCount).To(BeZero())
			return decoded
		}
		create := func(name, limit string, want *int32) api.EnrollmentApprovalPolicy {
			response := request(handler, http.MethodPost, "", document(name, "", limit), suite.Ctx)
			created := assertLimit(response, 201, want)
			read := assertLimit(request(handler, http.MethodGet, "/"+name, nil, suite.Ctx), 200, want)
			Expect(read).To(Equal(created))
			return read
		}
		if valid {
			create("west", value, expected)
		} else {
			post := request(handler, http.MethodPost, "", document("west", "", value), suite.Ctx)
			Expect(post.Code).To(Equal(400), post.Body.String())
			Expect(request(handler, http.MethodGet, "/west", nil, suite.Ctx).Code).To(Equal(404))
		}

		putSeed := create("put-seed", "5", lo.ToPtr(int32(5)))
		patchSeed := create("patch-seed", "7", lo.ToPtr(int32(7)))
		put := request(handler, http.MethodPut, "/put-seed", document("put-seed", *putSeed.Metadata.ResourceVersion, value), suite.Ctx)
		patchValue := value
		if value == "omitted" {
			patchValue = "null"
		}
		body := fmt.Appendf(nil, `[{"op":"test","path":"/metadata/resourceVersion","value":%q},{"op":"add","path":"/spec/approvalLimit","value":%s}]`, *patchSeed.Metadata.ResourceVersion, patchValue)
		patch := request(handler, http.MethodPatch, "/patch-seed", body, suite.Ctx)
		for _, write := range []struct {
			path     string
			response *httptest.ResponseRecorder
			seed     api.EnrollmentApprovalPolicy
		}{
			{"/put-seed", put, putSeed},
			{"/patch-seed", patch, patchSeed},
		} {
			get := request(handler, http.MethodGet, write.path, nil, suite.Ctx)
			if valid {
				updated := assertLimit(write.response, 200, expected)
				Expect(updated.Metadata.ResourceVersion).NotTo(Equal(write.seed.Metadata.ResourceVersion))
				Expect(assertLimit(get, 200, expected)).To(Equal(updated))
			} else {
				Expect(write.response.Code).To(Equal(400), write.response.Body.String())
				Expect(assertLimit(get, 200, write.seed.Spec.ApprovalLimit)).To(Equal(write.seed))
			}
		}
	},
		Entry("When omitted it should remain unlimited", "omitted", (*int32)(nil), true),
		Entry("When null it should remain unlimited", "null", (*int32)(nil), true),
		Entry("When zero it should remain explicit", "0", lo.ToPtr(int32(0)), true),
		Entry("When maximal it should remain exact", "2147483647", lo.ToPtr(int32(math.MaxInt32)), true),
		Entry("When negative it should reject", "-1", (*int32)(nil), false),
		Entry("When overflowing it should reject", "2147483648", (*int32)(nil), false),
		Entry("When fractional it should reject", "0.5", (*int32)(nil), false),
		Entry("When wrong type it should reject", `"bad"`, (*int32)(nil), false),
	)

	It("When the issuing credential is missing it should identify the spec field", func() {
		handler := makeHandler(policyservice.UnavailableExpressionValidator{})
		body := bytes.ReplaceAll(policyJSON, []byte("commissioning"), []byte("unknown-credential"))
		response := request(handler, http.MethodPost, "", body, suite.Ctx)
		Expect(response.Code).To(Equal(400), response.Body.String())
		var status api.Status
		Expect(json.Unmarshal(response.Body.Bytes(), &status)).To(Succeed())
		Expect(status.Reason).To(Equal("InvalidPolicy"))
		Expect(status.Message).To(ContainSubstring("spec.certificateName"))
		Expect(status.Message).To(ContainSubstring("unknown-credential"))
	})

	It("When a credential exists only in another tenant it should return the public binding error", func() {
		other := uuid.New()
		Expect(suite.DB.WithContext(suite.Ctx).Create(&model.Organization{ID: other, ExternalID: other.String(), DisplayName: "Other tenant"}).Error).To(Succeed())
		Expect(suite.DB.WithContext(suite.Ctx).Create(&model.CertificateSigningRequest{
			Resource: model.Resource{OrgID: other, Name: "other-tenant-credential"},
			Spec:     model.MakeJSONField(domain.CertificateSigningRequestSpec{SignerName: suite.cfg.CA.DeviceEnrollmentSignerName}),
		}).Error).To(Succeed())
		body := bytes.ReplaceAll(policyJSON, []byte("commissioning"), []byte("other-tenant-credential"))
		response := request(makeHandler(policyservice.UnavailableExpressionValidator{}), http.MethodPost, "", body, suite.Ctx)
		Expect(response.Code).To(Equal(400), response.Body.String())
		var status api.Status
		Expect(json.Unmarshal(response.Body.Bytes(), &status)).To(Succeed())
		Expect(status.Reason).To(Equal("InvalidPolicy"))
		Expect(status.Message).To(ContainSubstring("spec.certificateName"))
	})

	It("When a credential awaits issuance it should allow a draft with a window and reject activation", func() {
		Expect(suite.DB.WithContext(suite.Ctx).Create(&model.CertificateSigningRequest{
			Resource: model.Resource{OrgID: suite.OrgID, Name: "awaiting-issuance"},
			Spec:     model.MakeJSONField(domain.CertificateSigningRequestSpec{SignerName: suite.cfg.CA.DeviceEnrollmentSignerName}),
		}).Error).To(Succeed())
		var draft api.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(policyJSON, &draft)).To(Succeed())
		draft.Spec.CertificateName = "awaiting-issuance"
		draft.Spec.AutoApprovalWindowDays = lo.ToPtr(int32(2))
		draft.Spec.ApprovalLimit = lo.ToPtr(int32(0))
		body, err := json.Marshal(draft)
		Expect(err).NotTo(HaveOccurred())
		handler := makeHandler(policyservice.UnavailableExpressionValidator{})
		created := request(handler, http.MethodPost, "", body, suite.Ctx)
		Expect(created.Code).To(Equal(201), created.Body.String())
		Expect(json.Unmarshal(created.Body.Bytes(), &draft)).To(Succeed())
		body = fmt.Appendf(nil, `[{"op":"test","path":"/metadata/resourceVersion","value":%q},{"op":"replace","path":"/spec/enabled","value":true}]`, *draft.Metadata.ResourceVersion)
		result := request(handler, http.MethodPatch, "/west", body, suite.Ctx)
		Expect(result.Code).To(Equal(400), result.Body.String())
		Expect(result.Body.String()).To(ContainSubstring("enrollment credential must be issued and unrevoked"))
		Expect(draft.Spec.ApprovalLimit).To(Equal(lo.ToPtr(int32(0))))
		Expect(request(handler, http.MethodGet, "/west", nil, suite.Ctx).Body.String()).To(MatchJSON(created.Body.String()))
	})

	It("When the credential is retained by a deleted policy it should return a stable CSR delete conflict", func() {
		handler := makeHandler(policyservice.UnavailableExpressionValidator{})
		Expect(request(handler, http.MethodPost, "", policyJSON, suite.Ctx).Code).To(Equal(201))
		Expect(request(handler, http.MethodDelete, "/west", nil, suite.Ctx).Code).To(Equal(200))
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/certificatesigningrequests/commissioning", nil).WithContext(suite.Ctx)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		Expect(response.Code).To(Equal(409), response.Body.String())
		var status api.Status
		Expect(json.Unmarshal(response.Body.Bytes(), &status)).To(Succeed())
		Expect(status.Message).To(Equal(flterrors.ErrEnrollmentCredentialRetained.Error()))
	})

	DescribeTable("When a credential becomes invalid it should allow disabling or deleting it without expression validation", func(state, method string) {
		handler := makeHandler(policyAPIValidator{})
		var candidate api.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(policyJSON, &candidate)).To(Succeed())
		candidate.Spec.Enabled = state == "revoked"
		if state == "expired" {
			// Seed the expired leaf before binding it. A retained leaf cannot
			// be replaced to simulate the passage of time.
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			Expect(err).NotTo(HaveOccurred())
			template := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(-2 * time.Hour), NotAfter: time.Now().Add(-time.Hour)}
			der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
			Expect(err).NotTo(HaveOccurred())
			certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
			Expect(suite.DB.WithContext(suite.Ctx).Model(&model.CertificateSigningRequest{}).Where("org_id = ? AND name = ?", suite.OrgID, "commissioning").
				UpdateColumn("status", model.MakeJSONField(domain.CertificateSigningRequestStatus{Certificate: &certificate})).Error).To(Succeed())
		}
		body, err := json.Marshal(candidate)
		Expect(err).NotTo(HaveOccurred())
		response := request(handler, http.MethodPost, "", body, suite.Ctx)
		Expect(response.Code).To(Equal(201), response.Body.String())
		Expect(json.Unmarshal(response.Body.Bytes(), &candidate)).To(Succeed())
		Expect(suite.DB.Model(&model.EnrollmentApprovalPolicy{}).Where("org_id = ? AND name = ?", suite.OrgID, "west").UpdateColumn("status", model.MakeJSONField(domain.EnrollmentApprovalPolicyStatus{EnrollmentCount: 9})).Error).To(Succeed())
		if state == "revoked" {
			credentials := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.cfg.CA.DeviceEnrollmentSignerName)
			_, _, err := credentials.Revoke(suite.Ctx, suite.OrgID, "commissioning", domain.EnrollmentCertificateRevocation{Reason: "retired", RevokedAt: time.Now(), RevokedBy: "test"}, nil)
			Expect(err).NotTo(HaveOccurred())
		} else {
			// Represent a policy enabled before the credential expired.
			candidate.Spec.Enabled = true
			spec := convert.NewConverter().EnrollmentApprovalPolicy().ToDomain(candidate).Spec
			Expect(suite.DB.WithContext(suite.Ctx).Model(&model.EnrollmentApprovalPolicy{}).
				Where("org_id = ? AND name = ?", suite.OrgID, "west").UpdateColumn("spec", model.MakeJSONField(spec)).Error).To(Succeed())
		}
		// An enabled metadata edit still requires an eligible credential.
		metadata := candidate
		metadata.Status = nil
		metadata.Metadata.Labels = lo.ToPtr(map[string]string{"region": "west"})
		body, err = json.Marshal(metadata)
		Expect(err).NotTo(HaveOccurred())
		unavailable := makeHandler(policyservice.UnavailableExpressionValidator{})
		rejected := request(unavailable, http.MethodPut, "/west", body, suite.Ctx)
		Expect(rejected.Code).To(Equal(400), rejected.Body.String())
		Expect(rejected.Body.String()).To(ContainSubstring("InvalidPolicy"))
		candidate.Status = nil
		candidate.Spec.Enabled = false
		body, err = json.Marshal(candidate)
		Expect(err).NotTo(HaveOccurred())
		if method == http.MethodPatch {
			body = []byte(`[{"op":"test","path":"/metadata/resourceVersion","value":"` + *candidate.Metadata.ResourceVersion + `"},{"op":"replace","path":"/spec/enabled","value":false}]`)
		}
		handler = makeHandler(policyservice.UnavailableExpressionValidator{})
		send := func(ctx context.Context) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, "/api/v1/enrollmentapprovalpolicies/west", bytes.NewReader(body)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			if method == http.MethodPatch {
				req.Header.Set("Content-Type", "application/json-patch+json")
			}
			result := httptest.NewRecorder()
			handler.ServeHTTP(result, req)
			return result
		}
		Expect(send(util.WithOrganizationID(suite.Ctx, uuid.New())).Code).To(Equal(404))
		organization := &model.Organization{ID: suite.OrgID}
		viewer := identity.NewMappedIdentity("viewer", "viewer", []*model.Organization{organization}, map[string][]string{suite.OrgID.String(): {api.RoleViewer}}, false, nil)
		Expect(send(context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, viewer)).Code).To(Equal(403))
		response = send(suite.Ctx)
		var retained model.EnrollmentApprovalPolicy
		Expect(suite.DB.Unscoped().Where("org_id = ? AND name = ?", suite.OrgID, "west").First(&retained).Error).To(Succeed())
		Expect(retained.Status.Data.EnrollmentCount).To(Equal(int64(9)))
		Expect(response.Code).To(Equal(200), response.Body.String())
		if method == http.MethodDelete {
			Expect(retained.DeletedAt.Valid).To(BeTrue())
			Expect(request(handler, http.MethodPost, "", policyJSON, suite.Ctx).Code).To(Equal(409))
		} else {
			Expect(retained.Spec.Data.Enabled).To(BeFalse())
			Expect(retained.DeletedAt.Valid).To(BeFalse())
			var disabled api.EnrollmentApprovalPolicy
			Expect(json.Unmarshal(response.Body.Bytes(), &disabled)).To(Succeed())
			disabled.Status, disabled.Spec.Enabled = nil, true
			body, err := json.Marshal(disabled)
			Expect(err).NotTo(HaveOccurred())
			if method == http.MethodPatch {
				body = fmt.Appendf(nil, `[{"op":"test","path":"/metadata/resourceVersion","value":%q},{"op":"replace","path":"/spec/enabled","value":true}]`, *disabled.Metadata.ResourceVersion)
			}
			rejected := request(handler, method, "/west", body, suite.Ctx)
			Expect(rejected.Code).To(Equal(400), rejected.Body.String())
			Expect(rejected.Body.String()).To(ContainSubstring("InvalidPolicy"))
		}
	},
		Entry("When a credential is revoked it should allow PUT to disable", "revoked", http.MethodPut),
		Entry("When a credential is revoked it should allow PATCH to disable", "revoked", http.MethodPatch),
		Entry("When a credential is revoked it should allow DELETE", "revoked", http.MethodDelete),
		Entry("When a credential is expired it should allow PUT to disable", "expired", http.MethodPut),
		Entry("When a credential is expired it should allow PATCH to disable", "expired", http.MethodPatch),
		Entry("When a credential is expired it should allow DELETE", "expired", http.MethodDelete),
	)
	It("When quota permission is denied it should reject quota mutations and unsupported source operations without writes", func() {
		privileged := makeHandler(policyAPIValidator{})
		limited := makeHandler(policyAPIValidator{}, true)
		response := request(privileged, http.MethodPost, "", policyJSON, suite.Ctx)
		Expect(response.Code).To(Equal(201), response.Body.String())
		var created api.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(response.Body.Bytes(), &created)).To(Succeed())
		patch := func(handler http.Handler, operation string) *httptest.ResponseRecorder {
			body := []byte(`[{"op":"test","path":"/metadata/resourceVersion","value":"` + *created.Metadata.ResourceVersion + `"},` + operation + `]`)
			req := httptest.NewRequest(http.MethodPatch, "/api/v1/enrollmentapprovalpolicies/west", bytes.NewReader(body)).WithContext(suite.Ctx)
			req.Header.Set("Content-Type", "application/json-patch+json")
			result := httptest.NewRecorder()
			handler.ServeHTTP(result, req)
			return result
		}
		response = patch(limited, `{"op":"replace","path":"/spec/approvalLimit","value":0}`)
		Expect(response.Code).To(Equal(403), response.Body.String())
		unlimited := request(privileged, http.MethodGet, "/west", nil, suite.Ctx)
		Expect(unlimited.Code).To(Equal(200), unlimited.Body.String())
		var unchanged api.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(unlimited.Body.Bytes(), &unchanged)).To(Succeed())
		Expect(unchanged.Spec.ApprovalLimit).To(BeNil())
		Expect(unchanged.Metadata.ResourceVersion).To(Equal(created.Metadata.ResourceVersion))
		response = patch(privileged, `{"op":"add","path":"/spec/approvalLimit","value":10}`)
		Expect(response.Code).To(Equal(200), response.Body.String())
		Expect(json.Unmarshal(response.Body.Bytes(), &created)).To(Succeed())
		before := request(limited, http.MethodGet, "/west", nil, suite.Ctx).Body.String()
		for _, operation := range []string{
			`{"op":"add","path":"/spec/approvalLimit","value":12}`,
			`{"op":"replace","path":"/spec/approvalLimit","value":12}`,
			`{"op":"remove","path":"/spec/approvalLimit"}`,
			`{"op":"replace","path":"/spec","value":{"certificateName":"commissioning","matchExpression":"true","deviceLabels":{}}}`,
		} {
			response = patch(limited, operation)
			Expect(response.Code).To(Equal(403), response.Body.String())
			Expect(request(limited, http.MethodGet, "/west", nil, suite.Ctx).Body.String()).To(Equal(before))
		}
		for _, operation := range []string{
			`{"op":"move","from":"/spec/approvalLimit","path":"/spec/autoApprovalWindowDays"}`,
			`{"op":"copy","from":"/spec/approvalLimit","path":"/spec/autoApprovalWindowDays"}`,
		} {
			response = patch(limited, operation)
			Expect(response.Code).To(Equal(400), response.Body.String())
			Expect(request(limited, http.MethodGet, "/west", nil, suite.Ctx).Body.String()).To(Equal(before))
		}
		response = patch(limited, `{"op":"add","path":"/spec/autoApprovalWindowDays","value":10}`)
		Expect(response.Code).To(Equal(200), response.Body.String())
		Expect(json.Unmarshal(response.Body.Bytes(), &created)).To(Succeed())
		Expect(*created.Spec.ApprovalLimit).To(Equal(int32(10)))
		response = patch(privileged, `{"op":"replace","path":"/spec/approvalLimit","value":2147483647}`)
		Expect(response.Code).To(Equal(200), response.Body.String())
		Expect(json.Unmarshal(response.Body.Bytes(), &created)).To(Succeed())
		Expect(*created.Spec.ApprovalLimit).To(Equal(int32(2147483647)))
		before = request(privileged, http.MethodGet, "/west", nil, suite.Ctx).Body.String()
		for _, operation := range []string{
			`{"op":"replace","path":"/spec/approvalLimit","value":9223372036854775808}`,
			`{"op":"add","path":"/spec/unknown","value":true}`,
		} {
			response = patch(privileged, operation)
			Expect(response.Code).To(Equal(400), response.Body.String())
			Expect(request(privileged, http.MethodGet, "/west", nil, suite.Ctx).Body.String()).To(Equal(before))
		}
	})
	It("When real validation is unavailable it should store a disabled draft and reject enabled creation", func() {
		handler := makeHandler(policyservice.UnavailableExpressionValidator{})
		draft := request(handler, http.MethodPost, "", policyJSON, suite.Ctx)
		Expect(draft.Code).To(Equal(201), draft.Body.String())
		var enabled api.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(policyJSON, &enabled)).To(Succeed())
		enabled.Metadata.Name = lo.ToPtr("enabled")
		enabled.Spec.Enabled = true
		body, err := json.Marshal(enabled)
		Expect(err).NotTo(HaveOccurred())
		response := request(handler, http.MethodPost, "", body, suite.Ctx)
		Expect(response.Code).To(Equal(503), response.Body.String())
		Expect(response.Body.String()).To(ContainSubstring("PolicyValidationUnavailable"))
		Expect(request(handler, http.MethodGet, "/enabled", nil, suite.Ctx).Code).To(Equal(404))
	})

	DescribeTable("When the compiler is unavailable it should gate only enabled expression writes", func(method, operation string, want int) {
		var initial api.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(policyJSON, &initial)).To(Succeed())
		initial.Spec.Enabled = operation != "enable" && operation != "draft edit"
		body, err := json.Marshal(initial)
		Expect(err).NotTo(HaveOccurred())
		created := request(makeHandler(policyAPIValidator{}), http.MethodPost, "", body, suite.Ctx)
		Expect(created.Code).To(Equal(201), created.Body.String())
		Expect(json.Unmarshal(created.Body.Bytes(), &initial)).To(Succeed())
		candidate := initial
		candidate.Status = nil
		var patch string
		switch operation {
		case "enable":
			candidate.Spec.Enabled = true
			patch = `{"op":"replace","path":"/spec/enabled","value":true}`
		case "expression", "draft edit":
			candidate.Spec.MatchExpression = "false"
			patch = `{"op":"replace","path":"/spec/matchExpression","value":"false"}`
		case "metadata":
			candidate.Metadata.Labels = lo.ToPtr(map[string]string{"region": "west"})
			patch = `{"op":"add","path":"/metadata/labels","value":{"region":"west"}}`
		}
		if method == http.MethodPut {
			body, err = json.Marshal(candidate)
			Expect(err).NotTo(HaveOccurred())
		} else {
			body = fmt.Appendf(nil, `[{"op":"test","path":"/metadata/resourceVersion","value":%q},%s]`, *initial.Metadata.ResourceVersion, patch)
		}
		handler := makeHandler(policyservice.UnavailableExpressionValidator{}, true)
		response := request(handler, method, "/west", body, suite.Ctx)
		Expect(response.Code).To(Equal(want), response.Body.String())
		read := request(handler, http.MethodGet, "/west", nil, suite.Ctx)
		Expect(read.Code).To(Equal(200))
		if want == 503 {
			Expect(response.Body.String()).To(ContainSubstring("PolicyValidationUnavailable"))
			Expect(read.Body.String()).To(MatchJSON(created.Body.String()))
			return
		}
		Expect(read.Body.String()).To(MatchJSON(response.Body.String()))
		var updated api.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(read.Body.Bytes(), &updated)).To(Succeed())
		Expect(updated.Spec).To(Equal(candidate.Spec))
		Expect(updated.Status.EnrollmentCount).To(BeZero())
		if operation == "metadata" {
			Expect(updated.Metadata.Labels).To(Equal(candidate.Metadata.Labels))
		}
	},
		Entry("When PUT enables a draft it should require validation", http.MethodPut, "enable", 503),
		Entry("When PATCH enables a draft it should require validation", http.MethodPatch, "enable", 503),
		Entry("When PUT changes an enabled expression it should require validation", http.MethodPut, "expression", 503),
		Entry("When PATCH changes an enabled expression it should require validation", http.MethodPatch, "expression", 503),
		Entry("When PUT changes a label it should allow the enabled policy edit", http.MethodPut, "metadata", 200),
		Entry("When PATCH changes a label it should allow the enabled policy edit", http.MethodPatch, "metadata", 200),
		Entry("When PUT edits a disabled expression it should save the draft", http.MethodPut, "draft edit", 200),
		Entry("When PATCH edits a disabled expression it should save the draft", http.MethodPatch, "draft edit", 200),
	)
	DescribeTable("When an enabled policy expression fails validation it should reject the edit", func(method string) {
		var initial api.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(policyJSON, &initial)).To(Succeed())
		initial.Spec.Enabled = true
		body, err := json.Marshal(initial)
		Expect(err).NotTo(HaveOccurred())
		handler := makeHandler(rejectingPolicyValidator{})
		created := request(handler, http.MethodPost, "", body, suite.Ctx)
		Expect(created.Code).To(Equal(201), created.Body.String())
		Expect(json.Unmarshal(created.Body.Bytes(), &initial)).To(Succeed())
		initial.Status = nil
		initial.Spec.MatchExpression = "true &&"
		if method == http.MethodPatch {
			body = fmt.Appendf(nil, `[{"op":"test","path":"/metadata/resourceVersion","value":%q},{"op":"replace","path":"/spec/matchExpression","value":"true &&"}]`, *initial.Metadata.ResourceVersion)
		} else {
			body, err = json.Marshal(initial)
			Expect(err).NotTo(HaveOccurred())
		}
		response := request(handler, method, "/west", body, suite.Ctx)
		Expect(response.Code).To(Equal(400), response.Body.String())
		var status api.Status
		Expect(json.Unmarshal(response.Body.Bytes(), &status)).To(Succeed())
		Expect(status.Reason).To(Equal("InvalidPolicy"))
		Expect(status.Message).To(Equal("spec.matchExpression is invalid"))
		Expect(request(handler, http.MethodGet, "/west", nil, suite.Ctx).Body.String()).To(MatchJSON(created.Body.String()))
	},
		Entry("When PUT supplies an invalid expression it should leave the policy unchanged", http.MethodPut),
		Entry("When PATCH supplies an invalid expression it should leave the policy unchanged", http.MethodPatch),
	)

	It("When authorized lifecycle uses a test-only provider it should protect versions, bindings and status", func() {
		handler := makeHandler(policyAPIValidator{})
		response := request(handler, http.MethodPost, "", policyJSON, suite.Ctx)
		Expect(response.Code).To(Equal(201), response.Body.String())
		var created api.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(response.Body.Bytes(), &created)).To(Succeed())
		Expect(created.Status).NotTo(BeNil())
		Expect(created.Status.EnrollmentCount).To(Equal(int64(0)))
		response = request(handler, http.MethodGet, "?limit=1&fieldSelector=metadata.name%3Dwest", nil, suite.Ctx)
		Expect(response.Code).To(Equal(200), response.Body.String())
		var listed api.EnrollmentApprovalPolicyList
		Expect(json.Unmarshal(response.Body.Bytes(), &listed)).To(Succeed())
		Expect(listed.Items).To(HaveLen(1))
		Expect(listed.Items[0].Metadata.Name).To(Equal(created.Metadata.Name))
		Expect(listed.Items[0].Status.EnrollmentCount).To(Equal(int64(0)))
		response = request(handler, http.MethodGet, "?fieldSelector=spec.unknown%3Dx", nil, suite.Ctx)
		Expect(response.Code).To(Equal(400), response.Body.String())
		response = request(handler, http.MethodGet, "/west", nil, suite.Ctx)
		Expect(response.Code).To(Equal(200))
		Expect(json.Unmarshal(response.Body.Bytes(), &created)).To(Succeed())
		Expect(created.Status.EnrollmentCount).To(Equal(int64(0)))
		Expect(request(handler, http.MethodGet, "/west/status", nil, suite.Ctx).Code).To(Equal(404))
		Expect(request(handler, http.MethodPut, "/west", policyJSON, suite.Ctx).Code).To(Equal(409))
		foreignContext := util.WithOrganizationID(suite.Ctx, uuid.New())
		Expect(request(handler, http.MethodGet, "/west", nil, foreignContext).Code).To(Equal(404))
		Expect(request(handler, http.MethodGet, "/west/status", nil, foreignContext).Code).To(Equal(404))
		response = request(handler, http.MethodGet, "", nil, foreignContext)
		Expect(response.Code).To(Equal(200), response.Body.String())
		Expect(json.Unmarshal(response.Body.Bytes(), &listed)).To(Succeed())
		Expect(listed.Items).To(BeEmpty())
		created.Status = nil
		created.Spec.Enabled = true
		changed, err := json.Marshal(created)
		Expect(err).NotTo(HaveOccurred())
		response = request(handler, http.MethodPut, "/west", changed, suite.Ctx)
		Expect(response.Code).To(Equal(200), response.Body.String())
		Expect(json.Unmarshal(response.Body.Bytes(), &created)).To(Succeed())
		patch := []byte(`[{"op":"test","path":"/metadata/resourceVersion","value":"` + *created.Metadata.ResourceVersion + `"},{"op":"replace","path":"/spec/enabled","value":false}]`)
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/enrollmentapprovalpolicies/west", bytes.NewReader(patch)).WithContext(suite.Ctx)
		req.Header.Set("Content-Type", "application/json-patch+json")
		patched := httptest.NewRecorder()
		handler.ServeHTTP(patched, req)
		Expect(patched.Code).To(Equal(200), patched.Body.String())
		Expect(json.Unmarshal(patched.Body.Bytes(), &created)).To(Succeed())
		created.Status = nil
		created.Spec.CertificateName = "other"
		changed, err = json.Marshal(created)
		Expect(err).NotTo(HaveOccurred())
		Expect(request(handler, http.MethodPut, "/west", changed, suite.Ctx).Code).To(Equal(409))
		organization := &model.Organization{ID: suite.OrgID}
		viewer := identity.NewMappedIdentity("viewer", "viewer", []*model.Organization{organization}, map[string][]string{suite.OrgID.String(): {api.RoleViewer}}, false, nil)
		viewerCtx := context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, viewer)
		Expect(request(handler, http.MethodPost, "", policyJSON, viewerCtx).Code).To(Equal(403))
		Expect(request(handler, http.MethodDelete, "/west", nil, suite.Ctx).Code).To(Equal(200))
		Expect(request(handler, http.MethodPost, "", policyJSON, suite.Ctx).Code).To(Equal(409))
	})
})
