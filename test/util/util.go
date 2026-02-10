package util

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/api/client"
	agentclient "github.com/flightctl/flightctl/internal/api/client/agent"
	apiserver "github.com/flightctl/flightctl/internal/api_server"
	"github.com/flightctl/flightctl/internal/api_server/agentserver"
	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
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

// InitLogsWithDebug creates a logger with debug level if LOG_LEVEL=debug is set
func InitLogsWithDebug() *logrus.Logger {
	if logLevel := os.Getenv("LOG_LEVEL"); logLevel != "" {
		return flightlog.InitLogs(logLevel)
	}
	return flightlog.InitLogs()
}

type testProvider struct {
	queue       chan []byte
	pubsubQueue chan []byte
	stopped     atomic.Bool
	wg          *sync.WaitGroup
	log         logrus.FieldLogger
}

func (t *testProvider) NewPubSubPublisher(_ context.Context, channelName string) (queues.PubSubPublisher, error) {
	return t, nil
}

func (t *testProvider) NewPubSubSubscriber(_ context.Context, channelName string) (queues.PubSubSubscriber, error) {
	return t, nil
}

func NewTestProvider(log logrus.FieldLogger) queues.Provider {
	var wg sync.WaitGroup
	wg.Add(1)
	return &testProvider{
		queue:       make(chan []byte, 20),
		pubsubQueue: make(chan []byte, 20),
		wg:          &wg,
		log:         log,
	}
}

// GetConfigMapDataByJSONPath returns data from a ConfigMap using a jsonpath selector.
func GetConfigMapDataByJSONPath(namespace, name, jsonPath string) (string, error) {
	// #nosec G204 -- command args are fixed and controlled in test.
	out, err := exec.Command("kubectl", "get", "configmap", name,
		"-n", namespace,
		"-o", jsonPath,
	).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("kubectl get configmap: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (t *testProvider) NewQueueProducer(_ context.Context, _ string) (queues.QueueProducer, error) {
	return t, nil
}

func (t *testProvider) NewQueueConsumer(_ context.Context, _ string) (queues.QueueConsumer, error) {
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

func (t *testProvider) Enqueue(_ context.Context, b []byte, timestamp int64) error {
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

func (t *testProvider) Subscribe(ctx context.Context, handler queues.PubSubHandler) (queues.Subscription, error) {
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		log := logrus.New()
		for {
			select {
			case <-ctx.Done():
				return
			case b, ok := <-t.pubsubQueue:
				if !ok {
					return
				}
				if err := handler(ctx, b, log); err != nil {
					log.WithError(err).Errorf("handling broadcast message: %s", string(b))
				}
			}
		}
	}()
	return t, nil
}

func (t *testProvider) Publish(ctx context.Context, payload []byte) error {
	t.pubsubQueue <- payload
	return nil
}

// IsAcmInstalled checks if ACM is installed and if it is running.
// returns: isAcmInstalled, isAcmRunning, error
func IsAcmInstalled() (bool, bool, error) {
	if !BinaryExistsOnPath("oc") {
		return false, false, fmt.Errorf("oc not found on PATH")
	}
	cmd := exec.Command("oc", "get", "multiclusterhub", "-A")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, false, fmt.Errorf("error getting multiclusterhub: %w, %s", err, string(output))
	}
	outputString := string(output)
	if outputString == "error: the server doesn't have a resource type \"multiclusterhub\"" {
		return false, false, fmt.Errorf("ACM is not installed: %s", outputString)
	}
	if strings.Contains(outputString, "Running") || strings.Contains(outputString, "Paused") {
		logrus.Infof("The cluster has ACM installed and ACM is Running")
		return true, true, nil
	} else {
		logrus.Infof("The cluster has ACM installed and ACM is not Running. Status: %s", outputString)
		return true, false, nil
	}
}

// NewTestApiServer creates a new test server and returns the server and the listener listening on localhost's next available port.
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

// NewTestAgentServer creates a new test server and returns the server and the listener listening on localhost's next available port.
func NewTestAgentServer(ctx context.Context, log logrus.FieldLogger, cfg *config.Config, store store.Store, ca *crypto.CAClient, serverCerts *crypto.TLSCertificateConfig, queuesProvider queues.Provider) (*agentserver.AgentServer, net.Listener, error) {
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

	agentServer, err := agentserver.New(ctx, log, cfg, store, ca, listener, queuesProvider, tlsConfig)
	if err != nil {
		_ = listener.Close()
		return nil, nil, fmt.Errorf("NewTestAgentServer: error creating agent server: %w", err)
	}
	return agentServer, listener, nil
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

	// Create enrollment certificate with organization ID extension
	// Use the default organization ID for test certificates
	orgCtx := util.WithOrganizationID(ctx, org.DefaultID)
	enrollmentCerts, _, err := ca.EnsureClientCertificate(orgCtx, crypto.CertStorePath("client-enrollment.crt", cfg.Service.CertStore), crypto.CertStorePath("client-enrollment.key", cfg.Service.CertStore), clientBootstrapCertName, clientBootStrapValidityDays)
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

type TestOrgCache struct {
	orgs map[uuid.UUID]*model.Organization
	mu   sync.Mutex
}

func (c *TestOrgCache) Get(id uuid.UUID) *model.Organization {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.orgs[id]
}

func (c *TestOrgCache) Set(id uuid.UUID, org *model.Organization) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.orgs[id] = org
}

func TestEnrollmentApproval() *v1beta1.EnrollmentRequestApproval {
	return &v1beta1.EnrollmentRequestApproval{
		Approved: true,
		Labels:   &map[string]string{"label": "value"},
	}
}

// TestTempEnv sets one or more environment variables and returns a cleanup
// function that restores their original values.
func TestTempEnv(kv ...string) func() {
	if len(kv)%2 != 0 {
		panic("TestTempEnv requires even number of arguments: key, value pairs")
	}

	type original struct {
		key    string
		value  string
		exists bool
	}

	originals := make([]original, 0, len(kv)/2)

	for i := 0; i < len(kv); i += 2 {
		key, value := kv[i], kv[i+1]
		origVal, exists := os.LookupEnv(key)
		originals = append(originals, original{
			key:    key,
			value:  origVal,
			exists: exists,
		})
		_ = os.Setenv(key, value)
	}

	return func() {
		for _, o := range originals {
			if o.exists {
				_ = os.Setenv(o.key, o.value)
			} else {
				_ = os.Unsetenv(o.key)
			}
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

func EventuallySlow(actual any) types.AsyncAssertion {
	return Eventually(actual).WithTimeout(LONG_TIMEOUT).WithPolling(LONG_POLLING)
}

// CopyFile copies a file from src to dst, creating the destination directory if it does not exist.
func CopyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source file: %w", err)
	}
	defer srcFile.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}

	dstFile, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("creating destination file: %w", err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("copying file contents: %w", err)
	}

	return nil
}

// CreateTestNamespace creates a Kubernetes namespace with an org label.
// If orgLabel is empty, it defaults to DefaultOrgLabel.
func CreateTestNamespace(name string, orgLabel ...string) *corev1.Namespace {
	orgLabelValue := DefaultOrgLabel
	if len(orgLabel) > 0 && orgLabel[0] != "" {
		orgLabelValue = orgLabel[0]
	}
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				OrgLabelKey: orgLabelValue,
			},
		},
	}
}

// DeleteNamespace deletes a Kubernetes namespace using the provided Kubernetes client.
// It logs the deletion result using GinkgoWriter.
func DeleteNamespace(ctx context.Context, client kubernetes.Interface, namespace string) error {
	err := client.CoreV1().Namespaces().Delete(ctx, namespace, metav1.DeleteOptions{})
	if err != nil {
		GinkgoWriter.Printf("Warning: Failed to delete test namespace %s: %v\n", namespace, err)
	} else {
		GinkgoWriter.Printf("Deleted test namespace: %s\n", namespace)
	}
	return err
}
