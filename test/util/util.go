package util

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/client"
	agentclient "github.com/flightctl/flightctl/internal/api/client/agent"
	apiserver "github.com/flightctl/flightctl/internal/api_server"
	"github.com/flightctl/flightctl/internal/api_server/agentserver"
	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	"github.com/sirupsen/logrus"
)

const (
	caCertValidityDays          = 365 * 10
	serverCertValidityDays      = 365 * 1
	clientBootStrapValidityDays = 365 * 1
	adminCertValidityDays       = 365 * 1
	signerCertName              = "ca"
	serverCertName              = "server"
	clientBootstrapCertName     = "client-enrollment"
)

type testProvider struct {
	queue   chan []byte
	stopped atomic.Bool
	wg      *sync.WaitGroup
	log     logrus.FieldLogger
}

func NewTestProvider(log logrus.FieldLogger) queues.Provider {
	var wg sync.WaitGroup
	wg.Add(1)
	return &testProvider{
		queue: make(chan []byte, 20),
		wg:    &wg,
		log:   log,
	}
}

func (t *testProvider) NewPublisher(_ context.Context, _ string) (queues.Publisher, error) {
	return t, nil
}

func (t *testProvider) NewConsumer(_ context.Context, _ string) (queues.Consumer, error) {
	return t, nil
}

func (t *testProvider) Stop() {
	if !t.stopped.Swap(true) {
		t.wg.Done()
		close(t.queue)
	}
}

func (t *testProvider) Wait() {
	t.wg.Wait()
}

func (t *testProvider) CheckHealth(_ context.Context) error {
	return nil
}

func (t *testProvider) Publish(_ context.Context, b []byte, timestamp int64) error {
	t.queue <- b
	return nil
}

func (t *testProvider) Close() {
}

func (t *testProvider) Consume(ctx context.Context, handler queues.ConsumeHandler) error {
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		log := logrus.New()
		for {
			select {
			case <-ctx.Done():
				return
			case b, ok := <-t.queue:
				if !ok {
					return
				}
				if err := handler(ctx, b, "test-entry-id", t, log); err != nil {
					log.WithError(err).Errorf("handling message: %s", string(b))
				}
			}
		}
	}()
	return nil
}

func (t *testProvider) Complete(ctx context.Context, entryID string, body []byte, err error) error {
	// For test provider, this is a no-op since we don't track in-flight messages
	return nil
}

func (t *testProvider) ProcessTimedOutMessages(ctx context.Context, queueName string, timeout time.Duration, handler func(entryID string, body []byte) error) (int, error) {
	// For test provider, this is a no-op since we don't track in-flight messages
	return 0, nil
}

func (t *testProvider) RetryFailedMessages(ctx context.Context, queueName string, config queues.RetryConfig, handler func(entryID string, body []byte, retryCount int) error) (int, error) {
	// For test provider, this is a no-op since we don't track failed messages
	return 0, nil
}

func (t *testProvider) GetLatestProcessedTimestamp(ctx context.Context) (time.Time, error) {
	// For test provider, return zero time since we don't track checkpoints
	return time.Time{}, nil
}

func (t *testProvider) AdvanceCheckpointAndCleanup(ctx context.Context) error {
	// For test provider, this is a no-op since we don't track checkpoints
	return nil
}

func (t *testProvider) SetCheckpointTimestamp(ctx context.Context, timestamp time.Time) error {
	// For test provider, this is a no-op since we don't track checkpoints
	return nil
}

// NewTestServer creates a new test server and returns the server and the listener listening on localhost's next available port.
func NewTestApiServer(log logrus.FieldLogger, cfg *config.Config, store store.Store, ca *crypto.CAClient, serverCerts *crypto.TLSCertificateConfig, queuesProvider queues.Provider) (*apiserver.Server, net.Listener, error) {
	// create a listener using the next available port
	tlsConfig, _, err := crypto.TLSConfigForServer(ca.GetCABundleX509(), serverCerts)
	if err != nil {
		return nil, nil, fmt.Errorf("NewTestServer: error creating TLS certs: %w", err)
	}

	// create a listener using the next available port
	listener, err := middleware.NewTLSListener("", tlsConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("NewTLSListener: error creating TLS certs: %w", err)
	}

	return apiserver.New(log, cfg, store, ca, listener, queuesProvider, nil), listener, nil
}

// NewTestServer creates a new test server and returns the server and the listener listening on localhost's next available port.
func NewTestAgentServer(log logrus.FieldLogger, cfg *config.Config, store store.Store, ca *crypto.CAClient, serverCerts *crypto.TLSCertificateConfig, queuesProvider queues.Provider) (*agentserver.AgentServer, net.Listener, error) {
	// create a listener using the next available port
	_, tlsConfig, err := crypto.TLSConfigForServer(ca.GetCABundleX509(), serverCerts)
	if err != nil {
		return nil, nil, fmt.Errorf("NewTestAgentServer: error creating TLS certs: %w", err)
	}

	// create a listener using the next available port
	listener, err := net.Listen("tcp", "")
	if err != nil {
		return nil, nil, fmt.Errorf("NewTestAgentServer: error creating TLS certs: %w", err)
	}

	return agentserver.New(log, cfg, store, ca, listener, queuesProvider, tlsConfig), listener, nil
}

// NewTestStore creates a new test store and returns the store and the database name.
func NewTestStore(ctx context.Context, cfg config.Config, log *logrus.Logger) (store.Store, string, error) {
	// cfg.Database.Name = ""
	dbTemp, err := store.InitDB(&cfg, log)
	if err != nil {
		return nil, "", fmt.Errorf("NewTestStore: error initializing test DB: %w", err)
	}
	defer store.CloseDB(dbTemp)

	randomDBName := fmt.Sprintf("_%s", strings.ReplaceAll(uuid.New().String(), "-", "_"))
	log.Infof("DB name: %s", randomDBName)
	dbTemp = dbTemp.WithContext(ctx).Exec(fmt.Sprintf("CREATE DATABASE %s;", randomDBName))
	if dbTemp.Error != nil {
		return nil, "", fmt.Errorf("NewTestStore: creating test db %s: %w", randomDBName, dbTemp.Error)
	}

	cfg.Database.Name = randomDBName
	db, err := store.InitDB(&cfg, log)
	if err != nil {
		return nil, "", fmt.Errorf("NewTestStore: initializing test db %s: %w", randomDBName, err)
	}

	dbStore := store.NewStore(db, log.WithField("pkg", "store"))
	err = dbStore.RunMigrations(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("NewTestStore: performing migrations: %w", err)
	}

	return dbStore, randomDBName, nil
}

// NewTestCerts creates new test certificates in the service certstore and returns the CA, server certificate, and enrollment certificate.
func NewTestCerts(cfg *config.Config) (*crypto.CAClient, *crypto.TLSCertificateConfig, *crypto.TLSCertificateConfig, error) {
	ctx := context.Background()

	ca, _, err := crypto.EnsureCA(cfg.CA)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("NewTestCerts: Ensuring CA: %w", err)
	}

	// default certificate hostnames to localhost if nothing else is configured
	if len(cfg.Service.AltNames) == 0 {
		cfg.Service.AltNames = []string{"localhost", "127.0.0.1", "::"}
	}

	serverCerts, _, err := ca.EnsureServerCertificate(ctx, crypto.CertStorePath("server.crt", cfg.Service.CertStore), crypto.CertStorePath("server.key", cfg.Service.CertStore), cfg.Service.AltNames, serverCertValidityDays)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("NewTestCerts: Ensuring server certificate: %w", err)
	}

	enrollmentCerts, _, err := ca.EnsureClientCertificate(ctx, crypto.CertStorePath("client-enrollment.crt", cfg.Service.CertStore), crypto.CertStorePath("client-enrollment.key", cfg.Service.CertStore), clientBootstrapCertName, clientBootStrapValidityDays)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("NewTestCerts: Ensuring client enrollment certificate: %w", err)
	}

	return ca, serverCerts, enrollmentCerts, nil
}

// NewClient creates a new client with the given server URL and certificates. If the certs are nil a http client will be created.
func NewClient(serverUrl string, caBundle []*x509.Certificate) (*client.ClientWithResponses, error) {
	httpClient, err := NewBareHTTPsClient(caBundle, nil)
	if err != nil {
		return nil, fmt.Errorf("creating TLS config: %v", err)
	}

	return client.NewClientWithResponses(serverUrl, client.WithHTTPClient(httpClient))
}

// NewClient creates a new client with the given server URL and certificates. If the certs are nil a http client will be created.
func NewAgentClient(serverUrl string, caBundle []*x509.Certificate, clientCert *crypto.TLSCertificateConfig) (*agentclient.ClientWithResponses, error) {
	httpClient, err := NewBareHTTPsClient(caBundle, clientCert)
	if err != nil {
		return nil, fmt.Errorf("creating TLS config: %v", err)
	}

	return agentclient.NewClientWithResponses(serverUrl, agentclient.WithHTTPClient(httpClient))
}

func NewBareHTTPsClient(caBundle []*x509.Certificate, clientCert *crypto.TLSCertificateConfig) (*http.Client, error) {

	httpClient := &http.Client{}
	if len(caBundle) > 0 || clientCert != nil {
		tlsConfig, err := crypto.TLSConfigForClient(caBundle, clientCert)
		if err != nil {
			return nil, fmt.Errorf("creating TLS config: %v", err)
		}
		httpClient.Transport = &http.Transport{
			TLSClientConfig: tlsConfig,
		}
	}

	return httpClient, nil

}

func TestEnrollmentApproval() *v1alpha1.EnrollmentRequestApproval {
	return &v1alpha1.EnrollmentRequestApproval{
		Approved: true,
		Labels:   &map[string]string{"label": "value"},
	}
}

// TestTempEnv sets the environment variable key to value and returns a function that will reset the environment variable to its original value.
func TestTempEnv(key, value string) func() {
	originalValue, hadOriginalValue := os.LookupEnv(key)
	os.Setenv(key, value)
	return func() {
		if hadOriginalValue {
			os.Setenv(key, originalValue)
		} else {
			os.Unsetenv(key)
		}
	}
}

// GetEnrollmentIdFromText returns the enrollment ID from the given text.
// The enrollment ID is expected to be part of url path like https://example.com/enroll/1234
func GetEnrollmentIdFromText(text string) string {
	valuesRe := regexp.MustCompile(`/enroll/(\w+)`)
	if valuesRe.MatchString(text) {
		return valuesRe.FindStringSubmatch(text)[1]
	}
	return ""
}

// RandString generates a random string of length 'n' using lowercase alphabetic characters.
func RandString(n int) (string, error) {
	const alphanum = "abcdefghijklmnopqrstuvwxyz"
	var bytes = make([]byte, n)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random string: %w", err)
	}
	for i, b := range bytes {
		bytes[i] = alphanum[b%byte(len(alphanum))]
	}
	return string(bytes), nil
}

// GetCurrentYearBounds returns start of current and next year in RFC3339 format.
func GetCurrentYearBounds() (string, string) {
	now := time.Now()
	startOfYear := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	endOfYear := time.Date(now.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC)

	return startOfYear.Format(time.RFC3339), endOfYear.Format(time.RFC3339)
}

// RunTable runs a table of test cases with the given run function.
// Each test case has a description and parameters of type T.
type TestCase[T any] struct {
	Description string
	Params      T
}

func Cases[T any](items ...TestCase[T]) []TestCase[T] {
	return items
}

// RunTable executes the provided run function for each test case in the cases slice.
func RunTable[T any](cases []TestCase[T], runFunc func(T)) {
	for _, tc := range cases {
		By("Case: " + tc.Description)
		runFunc(tc.Params)
	}
}
