package util

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/client"
	agentclient "github.com/flightctl/flightctl/internal/api/client/agent"
	apiserver "github.com/flightctl/flightctl/internal/api_server"
	"github.com/flightctl/flightctl/internal/api_server/agentserver"
	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
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

func (t *testProvider) NewPublisher(_ string) (queues.Publisher, error) {
	return t, nil
}

func (t *testProvider) NewConsumer(_ string) (queues.Consumer, error) {
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

func (t *testProvider) Publish(b []byte) error {
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
				if err := handler(ctx, b, log); err != nil {
					log.WithError(err).Errorf("handling message: %s", string(b))
				}
			}
		}
	}()
	return nil
}

// NewTestServer creates a new test server and returns the server and the listener listening on localhost's next available port.
func NewTestApiServer(log logrus.FieldLogger, cfg *config.Config, store store.Store, ca *crypto.CA, serverCerts *crypto.TLSCertificateConfig, provider queues.Provider) (*apiserver.Server, net.Listener, error) {
	// create a listener using the next available port
	tlsConfig, _, err := crypto.TLSConfigForServer(ca.Config, serverCerts)
	if err != nil {
		return nil, nil, fmt.Errorf("NewTestServer: error creating TLS certs: %w", err)
	}

	// create a listener using the next available port
	listener, err := middleware.NewTLSListener("", tlsConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("NewTLSListener: error creating TLS certs: %w", err)
	}

	metrics := instrumentation.NewApiMetrics(cfg)

	return apiserver.New(log, cfg, store, ca, listener, provider, metrics, nil), listener, nil
}

// NewTestServer creates a new test server and returns the server and the listener listening on localhost's next available port.
func NewTestAgentServer(log logrus.FieldLogger, cfg *config.Config, store store.Store, ca *crypto.CA, serverCerts *crypto.TLSCertificateConfig) (*agentserver.AgentServer, net.Listener, error) {
	// create a listener using the next available port
	_, tlsConfig, err := crypto.TLSConfigForServer(ca.Config, serverCerts)
	if err != nil {
		return nil, nil, fmt.Errorf("NewTestAgentServer: error creating TLS certs: %w", err)
	}

	// create a listener using the next available port
	listener, err := net.Listen("tcp", "")
	if err != nil {
		return nil, nil, fmt.Errorf("NewTestAgentServer: error creating TLS certs: %w", err)
	}

	metrics := instrumentation.NewApiMetrics(cfg)

	return agentserver.New(log, cfg, store, ca, listener, tlsConfig, metrics), listener, nil
}

// NewTestStore creates a new test store and returns the store and the database name.
func NewTestStore(cfg config.Config, log *logrus.Logger) (store.Store, string, error) {
	// cfg.Database.Name = ""
	dbTemp, err := store.InitDB(&cfg, log)
	if err != nil {
		return nil, "", fmt.Errorf("NewTestStore: error initializing test DB: %w", err)
	}
	defer store.CloseDB(dbTemp)

	randomDBName := fmt.Sprintf("_%s", strings.ReplaceAll(uuid.New().String(), "-", "_"))
	log.Infof("DB name: %s", randomDBName)
	dbTemp = dbTemp.Exec(fmt.Sprintf("CREATE DATABASE %s;", randomDBName))
	if dbTemp.Error != nil {
		return nil, "", fmt.Errorf("NewTestStore: creating test db %s: %w", randomDBName, dbTemp.Error)
	}

	cfg.Database.Name = randomDBName
	db, err := store.InitDB(&cfg, log)
	if err != nil {
		return nil, "", fmt.Errorf("NewTestStore: initializing test db %s: %w", randomDBName, err)
	}

	dbStore := store.NewStore(db, log.WithField("pkg", "store"))
	err = dbStore.InitialMigration()
	if err != nil {
		return nil, "", fmt.Errorf("NewTestStore: performing initial migration: %w", err)
	}

	return dbStore, randomDBName, nil
}

// NewTestCerts creates new test certificates in the service certstore and returns the CA, server certificate, and enrollment certificate.
func NewTestCerts(cfg *config.Config) (*crypto.CA, *crypto.TLSCertificateConfig, *crypto.TLSCertificateConfig, error) {
	ca, _, err := crypto.EnsureCA(filepath.Join(cfg.Service.CertStore, "ca.crt"), filepath.Join(cfg.Service.CertStore, "ca.key"), "", "ca", caCertValidityDays)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("NewTestCerts: Ensuring CA: %w", err)
	}

	// default certificate hostnames to localhost if nothing else is configured
	if len(cfg.Service.AltNames) == 0 {
		cfg.Service.AltNames = []string{"localhost", "127.0.0.1", "::"}
	}

	serverCerts, _, err := ca.EnsureServerCertificate(filepath.Join(cfg.Service.CertStore, "server.crt"), filepath.Join(cfg.Service.CertStore, "server.key"), cfg.Service.AltNames, serverCertValidityDays)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("NewTestCerts: Ensuring server certificate: %w", err)
	}

	enrollmentCerts, _, err := ca.EnsureClientCertificate(filepath.Join(cfg.Service.CertStore, "client-enrollment.crt"), filepath.Join(cfg.Service.CertStore, "client-enrollment.key"), clientBootstrapCertName, clientBootStrapValidityDays)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("NewTestCerts: Ensuring client enrollment certificate: %w", err)
	}

	return ca, serverCerts, enrollmentCerts, nil
}

// NewClient creates a new client with the given server URL and certificates. If the certs are nil a http client will be created.
func NewClient(serverUrl string, caCert *crypto.TLSCertificateConfig) (*client.ClientWithResponses, error) {
	httpClient, err := NewBareHTTPsClient(caCert, nil)
	if err != nil {
		return nil, fmt.Errorf("creating TLS config: %v", err)
	}

	return client.NewClientWithResponses(serverUrl, client.WithHTTPClient(httpClient))
}

// NewClient creates a new client with the given server URL and certificates. If the certs are nil a http client will be created.
func NewAgentClient(serverUrl string, caCert, clientCert *crypto.TLSCertificateConfig) (*agentclient.ClientWithResponses, error) {
	httpClient, err := NewBareHTTPsClient(caCert, clientCert)
	if err != nil {
		return nil, fmt.Errorf("creating TLS config: %v", err)
	}

	return agentclient.NewClientWithResponses(serverUrl, agentclient.WithHTTPClient(httpClient))
}

func NewBareHTTPsClient(caCert, clientCert *crypto.TLSCertificateConfig) (*http.Client, error) {

	httpClient := &http.Client{}
	if caCert != nil || clientCert != nil {
		var err error
		tlsConfig, err := crypto.TLSConfigForClient(caCert, clientCert)
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
