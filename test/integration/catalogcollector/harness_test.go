package catalogcollector_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"text/template"
	"time"

	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	"github.com/flightctl/flightctl/api/versioning"
	v1alpha1client "github.com/flightctl/flightctl/internal/api/client/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	collectorconfig "github.com/flightctl/flightctl/pkg/catalogcollector/config"
	"github.com/flightctl/flightctl/pkg/catalogcollector/destination/flightctldestination"
	"github.com/flightctl/flightctl/pkg/catalogcollector/extension/bearertokenauthextension"
	"github.com/flightctl/flightctl/pkg/catalogcollector/extension/healthcheckextension"
	"github.com/flightctl/flightctl/pkg/catalogcollector/processor/catalognameprocessor"
	"github.com/flightctl/flightctl/pkg/catalogcollector/service"
	"github.com/flightctl/flightctl/pkg/catalogcollector/source/httpsource"
	"github.com/flightctl/flightctl/pkg/queues"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const (
	// startupTimeout bounds how long a spec waits for the API server, the
	// collector readiness probe, and the snapshot listener to come up.
	startupTimeout = 60 * time.Second

	// shutdownTimeout bounds how long a spec waits for the collector service
	// to return from Run after its context is cancelled.
	shutdownTimeout = 30 * time.Second

	// pollInterval is the retry cadence for every bounded wait in this suite.
	pollInterval = 100 * time.Millisecond

	// probeTimeout bounds a single collector probe request. Probes are polled
	// repeatedly inside a surrounding Eventually, so each attempt must give up
	// quickly enough for the next one to still fit inside startupTimeout.
	probeTimeout = 5 * time.Second

	// apiRequestTimeout bounds a single Flight Control API request issued by
	// the harness client.
	apiRequestTimeout = 30 * time.Second

	// snapshotPostTimeout bounds a single snapshot POST. Delivery is
	// synchronous through the destination, which itself defaults to a 30s
	// request timeout, so the harness allows a slightly larger budget and lets
	// the destination surface the more specific error.
	snapshotPostTimeout = 45 * time.Second

	// defaultPipelineID is the pipeline name used by the generated collector
	// configuration. It also becomes the pipeline ownership label value.
	defaultPipelineID = "catalog-sync"

	snapshotPath = "/v1/snapshots"
	livePath     = "/livez"
	readyPath    = "/readyz"
)

// collectorConfigTemplate mirrors a production collector configuration: an
// HTTP snapshot source, an optional catalog-name processor, the Flight Control
// destination, and the healthcheck and bearer-token authentication extensions.
// It is rendered to disk and then read back through the production
// configuration loader.
var collectorConfigTemplate = template.Must(template.New("collector").Parse(
	`extensions:
  healthcheck:
    endpoint: {{ .HealthEndpoint }}
    livePath: {{ .LivePath }}
    readyPath: {{ .ReadyPath }}
  bearertokenauth/flightctl:
    tokenFile: {{ .TokenFile }}
sources:
  http/snapshots:
    listenAddress: {{ .SourceEndpoint }}
    path: {{ .SnapshotPath }}
{{- if .Mappings }}
processors:
  catalogname/rename:
    mappings:
{{- range $from, $to := .Mappings }}
      {{ $from }}: {{ $to }}
{{- end }}
{{- end }}
destinations:
  flightctl/primary:
    server: {{ .FlightctlServer }}
    certificateAuthority: {{ .CertificateAuthority }}
    auth:
      authenticator: bearertokenauth/flightctl
pipelines:
  {{ .PipelineID }}:
    source: http/snapshots
{{- if .Mappings }}
    processors:
      - catalogname/rename
{{- end }}
    destination: flightctl/primary
`))

type collectorConfigValues struct {
	HealthEndpoint       string
	LivePath             string
	ReadyPath            string
	SourceEndpoint       string
	SnapshotPath         string
	Mappings             map[string]string
	FlightctlServer      string
	CertificateAuthority string
	TokenFile            string
	PipelineID           string
}

// snapshotPayload is the wire representation accepted by the HTTP source.
type snapshotPayload struct {
	Revision     string                    `json:"revision"`
	Catalogs     []apiv1alpha1.Catalog     `json:"catalogs"`
	CatalogItems []apiv1alpha1.CatalogItem `json:"catalogItems"`
}

// harnessOption customizes the generated collector configuration.
type harnessOption func(*collectorConfigValues)

// withCatalogNameMappings inserts a catalogname processor into the pipeline.
func withCatalogNameMappings(mappings map[string]string) harnessOption {
	return func(values *collectorConfigValues) {
		values.Mappings = mappings
	}
}

// collectorHarness runs one catalog collector instance against one dedicated
// Flight Control API server and database. Everything it creates is released
// through Ginkgo cleanup handlers registered during construction.
type collectorHarness struct {
	Ctx        context.Context
	Log        *logrus.Logger
	Client     *v1alpha1client.ClientWithResponses
	PipelineID string

	snapshotURL   string
	liveURL       string
	readyURL      string
	sourceAddress string

	// snapshotClient and probeClient are owned by this harness so that every
	// request it issues is bounded independently of any surrounding Eventually
	// deadline, which cannot interrupt a blocked HTTP call.
	snapshotClient *http.Client
	probeClient    *http.Client

	collectorCancel context.CancelFunc
	collectorDone   chan error
	stopOnce        sync.Once
	stopErr         error

	serverCfg *config.Config
	db        *gorm.DB
	dbName    string
	queues    queues.Provider
}

// newCollectorHarness starts a Flight Control API server backed by a fresh
// database, then starts a collector configured against it and waits until the
// collector reports ready and its snapshot listener accepts connections.
func newCollectorHarness(opts ...harnessOption) *collectorHarness {
	GinkgoHelper()

	harness := &collectorHarness{
		Ctx:            testutil.StartSpecTracerForGinkgo(suiteCtx),
		Log:            testutil.InitLogsWithDebug(),
		PipelineID:     defaultPipelineID,
		snapshotClient: newBoundedHTTPClient(snapshotPostTimeout),
		probeClient:    newBoundedHTTPClient(probeTimeout),
	}

	caBundlePath := harness.startAPIServer()
	harness.startCollector(caBundlePath, opts...)

	return harness
}

// newBoundedHTTPClient returns an HTTP client whose timeout bounds the whole
// exchange: connection setup, writing the request, waiting for the response
// headers, and reading the response body. Without it a stalled peer blocks the
// calling goroutine forever, and a surrounding Eventually deadline cannot
// interrupt it.
func newBoundedHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// bearerTokenEditor presents the suite's identity to the API server on every
// request the harness client issues.
func bearerTokenEditor(token string) v1alpha1client.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
}

// startAPIServer brings up the production Flight Control API server over TLS
// on an ephemeral loopback port and returns the path of the CA bundle that
// clients must trust.
//
// The server is the same *apiserver.Server the flightctl-api binary runs: it
// is constructed by testutil.NewTestApiServer and started with Run, so the
// collector talks to the production middleware chain, version negotiation,
// OpenAPI validation, transport handlers, services, and stores, backed by a
// real Postgres database and a real key-value store. No route, handler, or
// middleware is re-implemented by the harness.
func (h *collectorHarness) startAPIServer() string {
	GinkgoHelper()

	var err error
	h.serverCfg, h.dbName, h.db, err = testdb.CreateTestDB(
		h.Ctx, h.Log, "catalogcollector", store.InitDB)
	Expect(err).ToNot(HaveOccurred())
	DeferCleanup(func() {
		Expect(testdb.DeleteTestDB(
			context.Background(), h.Log, h.serverCfg, h.db, h.dbName)).To(Succeed())
	})

	// The API server opens its own key-value store connection from the config,
	// so point it at the suite's ephemeral Redis.
	h.serverCfg.KV.Hostname = suiteRedisHost
	h.serverCfg.KV.Port = suiteRedisPort
	h.serverCfg.KV.Password = suiteRedisPassword

	// The API server authenticates and authorizes every request, so point it
	// at the suite's identity provider. The server validates the token via
	// /userinfo (the production provider caches valid responses for ~10 min;
	// each test database gets a fresh provider instance, so no validation
	// result is carried across specs).
	providerSpec, err := suiteAuthProvider.ProviderSpec()
	Expect(err).ToNot(HaveOccurred())
	h.serverCfg.Auth.OAuth2 = &providerSpec

	certStore := filepath.Join(GinkgoT().TempDir(), "certs")
	Expect(os.MkdirAll(certStore, 0o700)).To(Succeed())
	h.serverCfg.Service.CertStore = certStore
	h.serverCfg.CA.InternalConfig.CertStore = certStore

	ca, serverCerts, _, err := testutil.NewTestCerts(h.serverCfg)
	Expect(err).ToNot(HaveOccurred())

	h.queues = testutil.NewTestProvider(h.Log)

	apiServer, listener, err := testutil.NewTestApiServer(
		h.Log, h.serverCfg, h.db, ca, serverCerts, h.queues)
	Expect(err).ToNot(HaveOccurred())

	// Run reads the listen address back out of the config for its HTTP server,
	// so it has to be set before the server starts.
	h.serverCfg.Service.Address = listener.Addr().String()

	serverCtx, cancelServer := context.WithCancel(h.Ctx)
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- apiServer.Run(serverCtx)
	}()
	DeferCleanup(func() {
		cancelServer()
		var runErr error
		Eventually(serverDone, shutdownTimeout).Should(Receive(&runErr))
		Expect(runErr).ToNot(HaveOccurred())
		// Run stops the queues provider on shutdown; Stop is idempotent, so
		// this only matters when Run returned before reaching that point.
		h.queues.Stop()
		h.queues.Wait()
	})

	baseURL := "https://" + listener.Addr().String()

	caBundle, err := ca.GetCABundle()
	Expect(err).ToNot(HaveOccurred())
	caBundlePath := filepath.Join(certStore, "collector-ca-bundle.crt")
	Expect(os.WriteFile(caBundlePath, caBundle, 0o600)).To(Succeed())

	tlsClient, err := testutil.NewBareHTTPsClient(ca.GetCABundleX509(), nil)
	Expect(err).ToNot(HaveOccurred())
	// The helper returns a client with no timeout; bound it before handing it
	// to the generated API client, which offers no deadline of its own.
	tlsClient.Timeout = apiRequestTimeout
	tlsClient.Transport = versioning.NewTransport(
		tlsClient.Transport,
		versioning.WithAPIVersion(versioning.V1Alpha1),
	)
	h.Client, err = v1alpha1client.NewClientWithResponses(
		baseURL+v1alpha1client.ServerUrlApiv1,
		v1alpha1client.WithHTTPClient(tlsClient),
		v1alpha1client.WithRequestEditorFn(bearerTokenEditor(suiteAuthProvider.Token)),
	)
	Expect(err).ToNot(HaveOccurred())

	// Bounded wait: the API server is usable once it answers a real request.
	Eventually(func(g Gomega) {
		resp, err := h.Client.ListCatalogsWithResponse(
			h.Ctx, &apiv1alpha1.ListCatalogsParams{})
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(resp.JSON200).ToNot(BeNil())
	}, startupTimeout, pollInterval).Should(Succeed())

	return caBundlePath
}

// startCollector renders a collector configuration, loads it with the
// production loader, builds the service from the production factories, and
// runs it until the spec finishes.
func (h *collectorHarness) startCollector(caBundlePath string, opts ...harnessOption) {
	GinkgoHelper()

	healthEndpoint := reserveLoopbackEndpoint()
	sourceEndpoint := reserveLoopbackEndpoint()

	// The collector authenticates to the API server through the production
	// bearer-token extension, reading its credential from disk exactly as a
	// deployed collector does.
	tokenFile := filepath.Join(GinkgoT().TempDir(), "collector-token")
	Expect(os.WriteFile(tokenFile, []byte(suiteAuthProvider.Token), 0o600)).To(Succeed())

	values := &collectorConfigValues{
		HealthEndpoint:       healthEndpoint,
		LivePath:             livePath,
		ReadyPath:            readyPath,
		SourceEndpoint:       sourceEndpoint,
		SnapshotPath:         snapshotPath,
		FlightctlServer:      "https://" + h.serverCfg.Service.Address,
		CertificateAuthority: caBundlePath,
		TokenFile:            tokenFile,
		PipelineID:           h.PipelineID,
	}
	for _, opt := range opts {
		opt(values)
	}

	var rendered bytes.Buffer
	Expect(collectorConfigTemplate.Execute(&rendered, values)).To(Succeed())

	configPath := filepath.Join(GinkgoT().TempDir(), "collector.yaml")
	Expect(os.WriteFile(configPath, rendered.Bytes(), 0o600)).To(Succeed())

	cfg, err := collectorconfig.Load(configPath)
	Expect(err).ToNot(HaveOccurred(), "collector configuration:\n%s", rendered.String())

	collectorCtx, cancelCollector := context.WithCancel(h.Ctx)
	h.collectorCancel = cancelCollector

	svc, err := service.New(collectorCtx, cfg, productionFactories(), service.Settings{
		Logger: testutil.InitLogsWithDebug(),
	})
	if err != nil {
		cancelCollector()
	}
	Expect(err).ToNot(HaveOccurred())

	h.collectorDone = make(chan error, 1)
	go func() {
		h.collectorDone <- svc.Run(collectorCtx)
	}()
	// Assert on teardown: a collector that crashed or refused to stop must
	// fail the spec rather than disappear silently.
	DeferCleanup(func() {
		Expect(h.StopCollector()).To(Succeed())
	})

	h.sourceAddress = sourceEndpoint
	h.snapshotURL = "http://" + sourceEndpoint + snapshotPath
	h.liveURL = "http://" + healthEndpoint + livePath
	h.readyURL = "http://" + healthEndpoint + readyPath

	// Bounded wait: readiness is published once every source goroutine has
	// started, and the listener is reachable a moment later.
	Eventually(h.ProbeStatus, startupTimeout, pollInterval).
		WithArguments(h.readyURL).Should(Equal(http.StatusOK))
	Eventually(func() error {
		conn, err := net.DialTimeout("tcp", sourceEndpoint, time.Second)
		if err != nil {
			return err
		}
		return conn.Close()
	}, startupTimeout, pollInterval).Should(Succeed())
}

// productionFactories returns the component factories this suite exercises.
// Each constructor is the same one the flightctl-catalog-collector binary
// registers; no test double is substituted for any pipeline stage.
func productionFactories() catalogcollector.Factories {
	return catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			httpsource.NewFactory(),
		},
		Processors: []catalogcollector.ProcessorFactory{
			catalognameprocessor.NewFactory(),
		},
		Destinations: []catalogcollector.DestinationFactory{
			flightctldestination.NewFactory(),
		},
		Extensions: []catalogcollector.ExtensionFactory{
			healthcheckextension.NewFactory(),
			bearertokenauthextension.NewFactory(),
		},
	}
}

// StopCollector cancels the collector context and waits for Run to return.
// It is safe to call more than once; later calls return the first result.
func (h *collectorHarness) StopCollector() error {
	h.stopOnce.Do(func() {
		if h.collectorCancel == nil {
			return
		}
		h.collectorCancel()

		select {
		case h.stopErr = <-h.collectorDone:
		case <-time.After(shutdownTimeout):
			h.stopErr = fmt.Errorf(
				"collector did not stop within %s", shutdownTimeout)
		}
	})
	return h.stopErr
}

// PostSnapshot delivers a snapshot to the HTTP source and returns the response
// status code together with the response body.
//
// Delivery is synchronous all the way to the destination, so a 204 response
// means the snapshot has already been persisted.
func (h *collectorHarness) PostSnapshot(snapshot snapshotPayload) (int, string) {
	GinkgoHelper()

	body, err := json.Marshal(snapshot)
	Expect(err).ToNot(HaveOccurred())

	return h.postRaw(body)
}

// PostRawSnapshot delivers an arbitrary request body to the HTTP source.
func (h *collectorHarness) PostRawSnapshot(body string) (int, string) {
	GinkgoHelper()
	return h.postRaw([]byte(body))
}

func (h *collectorHarness) postRaw(body []byte) (int, string) {
	GinkgoHelper()

	req, err := http.NewRequestWithContext(
		h.Ctx, http.MethodPost, h.snapshotURL, bytes.NewReader(body))
	Expect(err).ToNot(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.snapshotClient.Do(req)
	Expect(err).ToNot(HaveOccurred())
	defer func() {
		Expect(resp.Body.Close()).To(Succeed())
	}()

	responseBody, err := io.ReadAll(resp.Body)
	Expect(err).ToNot(HaveOccurred())

	return resp.StatusCode, string(responseBody)
}

// ProbeStatus performs a GET against a collector probe endpoint and returns the
// HTTP status code, or 0 when the endpoint is unreachable.
//
// The request context deliberately drops cancellation so that probes still run
// while the harness context is being torn down, which is what shutdown
// verification asserts on. The probe client's timeout keeps the request bounded
// regardless.
func (h *collectorHarness) ProbeStatus(url string) int {
	req, err := http.NewRequestWithContext(
		context.WithoutCancel(h.Ctx), http.MethodGet, url, nil)
	if err != nil {
		return 0
	}

	resp, err := h.probeClient.Do(req)
	if err != nil {
		return 0
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	return resp.StatusCode
}

// LiveURL returns the configured liveness probe URL.
func (h *collectorHarness) LiveURL() string { return h.liveURL }

// ReadyURL returns the configured readiness probe URL.
func (h *collectorHarness) ReadyURL() string { return h.readyURL }

// SourceAddress returns the host:port the HTTP snapshot source listens on, so
// a spec can check the listener itself rather than only the probe endpoints.
func (h *collectorHarness) SourceAddress() string { return h.sourceAddress }

// GetCatalog reads one Catalog, returning nil when it does not exist.
func (h *collectorHarness) GetCatalog(name string) *apiv1alpha1.Catalog {
	GinkgoHelper()

	resp, err := h.Client.GetCatalogWithResponse(h.Ctx, name)
	Expect(err).ToNot(HaveOccurred())
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	Expect(resp.JSON200).ToNot(BeNil(), "unexpected GET catalog response: %s", resp.Body)

	return resp.JSON200
}

// GetCatalogItem reads one CatalogItem, returning nil when it does not exist.
func (h *collectorHarness) GetCatalogItem(catalog, name string) *apiv1alpha1.CatalogItem {
	GinkgoHelper()

	resp, err := h.Client.GetCatalogItemWithResponse(h.Ctx, catalog, name)
	Expect(err).ToNot(HaveOccurred())
	if resp.StatusCode() == http.StatusNotFound {
		return nil
	}
	Expect(resp.JSON200).ToNot(BeNil(), "unexpected GET catalog item response: %s", resp.Body)

	return resp.JSON200
}

// ManagedCatalogNames returns the names of the Catalogs the pipeline owns,
// selected with the same management labels the destination stamps.
func (h *collectorHarness) ManagedCatalogNames() []string {
	GinkgoHelper()

	selector := h.managementSelector()
	resp, err := h.Client.ListCatalogsWithResponse(
		h.Ctx, &apiv1alpha1.ListCatalogsParams{LabelSelector: &selector})
	Expect(err).ToNot(HaveOccurred())
	Expect(resp.JSON200).ToNot(BeNil(), "unexpected LIST catalogs response: %s", resp.Body)

	names := make([]string, 0, len(resp.JSON200.Items))
	for i := range resp.JSON200.Items {
		names = append(names, nameOf(resp.JSON200.Items[i].Metadata.Name))
	}
	return names
}

// ManagedCatalogItemKeys returns the "catalog/name" identity of every
// CatalogItem the pipeline owns.
func (h *collectorHarness) ManagedCatalogItemKeys() []string {
	GinkgoHelper()

	selector := h.managementSelector()
	resp, err := h.Client.ListAllCatalogItemsWithResponse(
		h.Ctx, &apiv1alpha1.ListAllCatalogItemsParams{LabelSelector: &selector})
	Expect(err).ToNot(HaveOccurred())
	Expect(resp.JSON200).ToNot(BeNil(), "unexpected LIST catalog items response: %s", resp.Body)

	keys := make([]string, 0, len(resp.JSON200.Items))
	for i := range resp.JSON200.Items {
		item := resp.JSON200.Items[i]
		keys = append(keys, item.Metadata.Catalog+"/"+nameOf(item.Metadata.Name))
	}
	return keys
}

// managementSelector is the label selector identifying resources owned by this
// harness's pipeline.
func (h *collectorHarness) managementSelector() string {
	return managedByLabel + "=" + managedByValue + "," + pipelineLabel + "=" + h.PipelineID
}

// CreateCatalog writes a Catalog directly through the API, bypassing the
// collector pipeline. It is used to seed resources the pipeline must not own.
func (h *collectorHarness) CreateCatalog(catalog apiv1alpha1.Catalog) {
	GinkgoHelper()

	resp, err := h.Client.ReplaceCatalogWithResponse(
		h.Ctx, nameOf(catalog.Metadata.Name), catalog)
	Expect(err).ToNot(HaveOccurred())
	Expect(resp.StatusCode()).To(BeElementOf(http.StatusOK, http.StatusCreated),
		"unexpected PUT catalog response: %s", resp.Body)
}

// CreateCatalogItem writes a CatalogItem directly through the API, bypassing
// the collector pipeline.
func (h *collectorHarness) CreateCatalogItem(item apiv1alpha1.CatalogItem) {
	GinkgoHelper()

	resp, err := h.Client.ReplaceCatalogItemWithResponse(
		h.Ctx, item.Metadata.Catalog, nameOf(item.Metadata.Name), item)
	Expect(err).ToNot(HaveOccurred())
	Expect(resp.StatusCode()).To(BeElementOf(http.StatusOK, http.StatusCreated),
		"unexpected PUT catalog item response: %s", resp.Body)
}

// reserveLoopbackEndpoint returns a loopback host:port that was free a moment
// ago. Letting the kernel assign the port keeps parallel Ginkgo processes from
// colliding on a hard-coded value.
func reserveLoopbackEndpoint() string {
	GinkgoHelper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).ToNot(HaveOccurred())
	address := listener.Addr().String()
	Expect(listener.Close()).To(Succeed())

	return address
}

// WriteMarkers returns one marker per persisted Catalog and CatalogItem that
// changes whenever the server rewrites the row.
//
// Catalogs and CatalogItems do not share an API-visible mutation counter, so
// the stored update timestamp is read directly. A marker that is unchanged
// across two reconciliations proves the destination issued no write.
func (h *collectorHarness) WriteMarkers() map[string]string {
	GinkgoHelper()

	markers := make(map[string]string)

	var catalogRows []struct {
		Name      string
		UpdatedAt time.Time
	}
	Expect(h.db.WithContext(h.Ctx).
		Raw(`SELECT name, updated_at FROM catalogs WHERE deleted_at IS NULL`).
		Scan(&catalogRows).Error).To(Succeed())
	for _, row := range catalogRows {
		markers["catalog/"+row.Name] = row.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}

	var itemRows []struct {
		CatalogName string
		AppName     string
		UpdatedAt   time.Time
	}
	Expect(h.db.WithContext(h.Ctx).
		Raw(`SELECT catalog_name, app_name, updated_at FROM catalog_items`).
		Scan(&itemRows).Error).To(Succeed())
	for _, row := range itemRows {
		key := "catalogitem/" + row.CatalogName + "/" + row.AppName
		markers[key] = row.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}

	return markers
}

func nameOf(name *string) string {
	if name == nil {
		return ""
	}
	return *name
}

// stalledClientTimeout is the deliberately small budget the timeout regression
// specs give their client so a stalled peer is detected quickly.
const stalledClientTimeout = 500 * time.Millisecond

// stalledWatchdogTimeout is the independent request-context deadline the
// timeout regression specs install. It is deliberately longer than
// stalledClientTimeout so the two are distinguishable: the specs assert that
// the client's own timeout ended the exchange while the watchdog was still
// live. The watchdog exists only so a regression that removes the client
// timeout cancels the request instead of wedging the suite forever.
const stalledWatchdogTimeout = 4 * stalledClientTimeout

// expectClientTimeoutBeforeWatchdog asserts that err is the HTTP client's own
// timeout and that the request context's independent deadline has not expired,
// which is what proves the client timeout fired first rather than the failure
// being a context cancellation.
func expectClientTimeoutBeforeWatchdog(err error, watchdogCtx context.Context) {
	GinkgoHelper()

	Expect(err).To(HaveOccurred(), "a stalled peer must not be awaited forever")
	Expect(os.IsTimeout(err)).To(BeTrue(), "expected a timeout error, got: %v", err)

	Expect(watchdogCtx.Err()).ToNot(HaveOccurred(),
		"the client timeout must fire before the request context deadline")
	deadline, ok := watchdogCtx.Deadline()
	Expect(ok).To(BeTrue())
	Expect(deadline).To(BeTemporally(">", time.Now()),
		"the request context deadline must still be in the future")
}

// newStalledWatchdogContext returns a request context whose deadline is longer
// than stalledClientTimeout, released when the spec finishes.
func newStalledWatchdogContext() context.Context {
	GinkgoHelper()

	ctx, cancel := context.WithTimeout(suiteCtx, stalledWatchdogTimeout)
	DeferCleanup(cancel)

	return ctx
}

// startStalledServer starts an HTTP server that never finishes a response. It
// optionally flushes the response head first, so a caller can stall either
// before the headers arrive or part-way through the body, and then holds the
// connection open until the spec finishes.
func startStalledServer(flushHead bool) string {
	GinkgoHelper()

	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if flushHead {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	DeferCleanup(func() {
		close(release)
		server.Close()
	})

	return server.URL
}

var _ = Describe("Harness HTTP clients", func() {
	Context("when the peer accepts the request but never sends a response", func() {
		It("should fail the request instead of blocking the caller", func() {
			url := startStalledServer(false)

			watchdogCtx := newStalledWatchdogContext()
			req, err := http.NewRequestWithContext(
				watchdogCtx, http.MethodGet, url, nil)
			Expect(err).ToNot(HaveOccurred())

			resp, err := newBoundedHTTPClient(stalledClientTimeout).Do(req)
			if resp != nil {
				defer func() { _ = resp.Body.Close() }()
			}

			expectClientTimeoutBeforeWatchdog(err, watchdogCtx)
		})
	})

	Context("when the peer sends response headers but then stalls mid-body", func() {
		It("should fail the response body read instead of blocking the caller", func() {
			url := startStalledServer(true)

			watchdogCtx := newStalledWatchdogContext()
			req, err := http.NewRequestWithContext(
				watchdogCtx, http.MethodGet, url, nil)
			Expect(err).ToNot(HaveOccurred())

			resp, err := newBoundedHTTPClient(stalledClientTimeout).Do(req)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = resp.Body.Close() }()

			// The client timeout must also cover body reads.
			_, err = io.ReadAll(resp.Body)
			expectClientTimeoutBeforeWatchdog(err, watchdogCtx)
		})
	})
})
