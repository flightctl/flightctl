package util

import (
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/server"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

const (
	caCertValidityDays          = 365 * 10
	serverCertValidityDays      = 365 * 1
	clientBootStrapValidityDays = 365 * 1
	signerCertName              = "ca"
	serverCertName              = "server"
	clientBootstrapCertName     = "client-enrollment"
)

// NewTestServer creates a new test server and returns the server and the listener listening on localhost's next available port.
func NewTestServer(t testing.TB, log logrus.FieldLogger, cfg *config.Config, store store.Store, ca *crypto.CA, serverCerts *crypto.TLSCertificateConfig) (*server.Server, net.Listener) {
	t.Helper()
	require := require.New(t)
	// create a listener using the next available port
	tlsConfig, err := crypto.TLSConfigForServer(ca.Config, serverCerts)
	require.NoError(err)

	// create a listener using the next available port
	listener, err := server.NewTLSListener("", tlsConfig)
	require.NoError(err)

	return server.New(log, cfg, store, ca, listener), listener
}

// NewTestStore creates a new test store and returns the store and the database name.
func NewTestStore(t testing.TB, cfg config.Config, log *logrus.Logger) (store.Store, string) {
	t.Helper()

	require := require.New(t)
	// cfg.Database.Name = ""
	dbTemp, err := store.InitDB(&cfg)
	require.NoError(err)
	defer store.CloseDB(dbTemp)

	randomDBName := fmt.Sprintf("_%s", strings.ReplaceAll(uuid.New().String(), "-", "_"))
	t.Logf("DB name: %s", randomDBName)
	dbTemp = dbTemp.Exec(fmt.Sprintf("CREATE DATABASE %s;", randomDBName))
	require.NoError(dbTemp.Error)

	cfg.Database.Name = randomDBName
	db, err := store.InitDB(&cfg)
	require.NoError(err)

	store := store.NewStore(db, log.WithField("pkg", "store"))
	err = store.InitialMigration()
	require.NoError(err)

	return store, randomDBName
}

// NewTestCerts creates new test certificates in the service certstore and returns the CA, server certificate, and client certificate.
func NewTestCerts(t testing.TB, cfg *config.Config) (*crypto.CA, *crypto.TLSCertificateConfig, *crypto.TLSCertificateConfig) {
	t.Helper()

	require := require.New(t)
	ca, _, err := crypto.EnsureCA(filepath.Join(cfg.Service.CertStore, "ca.crt"), filepath.Join(cfg.Service.CertStore, "ca.key"), "", "ca", caCertValidityDays)
	require.NoError(err)

	// default certificate hostnames to localhost if nothing else is configured
	if len(cfg.Service.AltNames) == 0 {
		cfg.Service.AltNames = []string{"localhost", "127.0.0.1"}
	}

	serverCerts, _, err := ca.EnsureServerCertificate(filepath.Join(cfg.Service.CertStore, "server.crt"), filepath.Join(cfg.Service.CertStore, "server.key"), cfg.Service.AltNames, serverCertValidityDays)
	require.NoError(err)

	clientCerts, _, err := ca.EnsureClientCertificate(filepath.Join(cfg.Service.CertStore, "client-enrollment.crt"), filepath.Join(cfg.Service.CertStore, "client-enrollment.key"), clientBootstrapCertName, clientBootStrapValidityDays)
	require.NoError(err)

	return ca, serverCerts, clientCerts
}

// NewClient creates a new client with the given server URL and certificates.
func NewClient(serverUrl string, caCert, clientCert *crypto.TLSCertificateConfig) (*client.ClientWithResponses, error) {
	tlsConfig, err := crypto.TLSConfigForClient(caCert, clientCert)
	if err != nil {
		return nil, fmt.Errorf("creating TLS config: %v", err)
	}
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}
	return client.NewClientWithResponses(serverUrl, client.WithHTTPClient(httpClient))
}
