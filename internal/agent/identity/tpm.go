package identity

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	agent_client "github.com/flightctl/flightctl/internal/api/client/agent"
	base_client "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/tpm"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	certutil "k8s.io/client-go/util/cert"
)

var _ Provider = (*tpmProvider)(nil)

// tpmProvider implements identity management using TPM-based keys
type tpmProvider struct {
	client          tpm.Client
	config          *agent_config.Config
	log             *log.PrefixLogger
	deviceName      string
	certificateData []byte
	clientCertPath  string
	rw              fileio.ReadWriter
	exec            executer.Executer
}

// newTPMProvider creates a new TPM-based identity provider
func newTPMProvider(
	client tpm.Client,
	config *agent_config.Config,
	clientCertPath string,
	rw fileio.ReadWriter,
	exec executer.Executer,
	log *log.PrefixLogger,
) *tpmProvider {
	return &tpmProvider{
		client:         client,
		config:         config,
		clientCertPath: clientCertPath,
		rw:             rw,
		exec:           exec,
		log:            log,
	}
}

type tpmExportableProvider struct {
	client tpm.Client
}

func newTPMExportableProvider(client tpm.Client) *tpmExportableProvider {
	return &tpmExportableProvider{
		client: client,
	}
}

func (t *tpmExportableProvider) NewExportable(name string) (*Exportable, error) {
	csr, keyPem, err := t.client.CreateApplicationKey(name)
	if err != nil {
		return nil, fmt.Errorf("creating application identity: %q: %w", name, err)
	}
	return &Exportable{
		name:   name,
		csr:    csr,
		keyPEM: keyPem,
	}, nil
}

func (t *tpmProvider) Initialize(ctx context.Context) error {
	if err := t.initializeTPMCredential(ctx); err != nil {
		return fmt.Errorf("failed to initialize TPM credential: %w", err)
	}

	publicKey := t.client.Public()
	if publicKey == nil {
		return fmt.Errorf("failed to get public key from TPM")
	}

	var err error
	t.deviceName, err = generateDeviceName(publicKey)
	if err != nil {
		return err
	}

	// load certificate from disk if it exists
	exists, err := t.rw.PathExists(t.clientCertPath)
	if err != nil {
		t.log.Errorf("Failed to check certificate existence: %v", err)
	} else if exists {
		certData, err := t.rw.ReadFile(t.clientCertPath)
		if err != nil {
			t.log.Errorf("Failed to load certificate from disk: %v", err)
		} else {
			t.certificateData = certData
			t.log.Infof("Loaded existing certificate from %s", t.clientCertPath)
		}
	}

	if err := t.client.UpdateNonce(make([]byte, 8)); err != nil {
		t.log.Errorf("Failed to update TPM nonce: %v", err)
	}
	return nil
}

func (t *tpmProvider) GetDeviceName() (string, error) {
	return t.deviceName, nil
}

func (t *tpmProvider) GenerateCSR(deviceName string) ([]byte, error) {
	// Use default qualifying data (nonce) for attestation freshness
	qualifyingData := make([]byte, 8)
	return t.client.MakeCSR(deviceName, qualifyingData)
}

// isTPMVerificationNeeded checks if TPM verification is necessary for the enrollment request
func (t *tpmProvider) isTPMVerificationNeeded(enrollmentRequest *v1alpha1.EnrollmentRequest) bool {
	if enrollmentRequest.Status != nil {
		if condition := v1alpha1.FindStatusCondition(enrollmentRequest.Status.Conditions, v1alpha1.ConditionTypeEnrollmentRequestTPMVerified); condition != nil {
			// if verification of the request failed, do not perform any additional verification
			if condition.Reason == v1alpha1.TPMVerificationFailedReason {
				t.log.Debug("TPM verification failed, identity proof not allowed")
				return false
			}
			if condition.Status == v1alpha1.ConditionStatusTrue {
				t.log.Debug("TPM already verified, skipping identity proof")
				return false
			}
			t.log.Debugf("TPM verification condition found but status is %s, reason: %s", condition.Status, condition.Reason)
		}
	}
	return true
}

// handleTPMChallenge handles the complete TPM challenge-response flow
func (t *tpmProvider) handleTPMChallenge(ctx context.Context, stream grpc_v1.Enrollment_TPMChallengeClient, enrollmentRequestName string) error {
	challengeRequest := &grpc_v1.AgentChallenge{
		Payload: &grpc_v1.AgentChallenge_ChallengeRequest{
			ChallengeRequest: &grpc_v1.ChallengeRequest{
				EnrollmentRequestName: enrollmentRequestName,
			},
		},
	}

	if err := stream.Send(challengeRequest); err != nil {
		return fmt.Errorf("failed to send challenge request: %w", err)
	}

	t.log.Debug("Sent TPM challenge request, waiting for challenge")

	serverMsg, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("failed to receive challenge from server: %w", err)
	}

	switch payload := serverMsg.Payload.(type) {
	case *grpc_v1.ServerChallenge_Challenge:
		return t.processChallenge(ctx, stream, payload.Challenge)
	case *grpc_v1.ServerChallenge_Error:
		return fmt.Errorf("server error: %s", payload.Error.Message)
	default:
		return fmt.Errorf("unexpected server message type")
	}
}

// processChallenge handles the challenge solving and response
func (t *tpmProvider) processChallenge(ctx context.Context, stream grpc_v1.Enrollment_TPMChallengeClient, challenge *grpc_v1.Challenge) error {
	t.log.Debug("Received TPM challenge, solving with TPM")

	secret, err := t.client.SolveChallenge(challenge.CredentialBlob, challenge.EncryptedSecret)
	if err != nil {
		return fmt.Errorf("failed to solve TPM challenge: %w", err)
	}

	challengeResponse := &grpc_v1.AgentChallenge{
		Payload: &grpc_v1.AgentChallenge_ChallengeResponse{
			ChallengeResponse: &grpc_v1.ChallengeResponse{
				Secret: secret,
			},
		},
	}

	if err := stream.Send(challengeResponse); err != nil {
		return fmt.Errorf("failed to send challenge response: %w", err)
	}

	t.log.Debug("Sent TPM challenge response, waiting for result")

	finalMsg, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("failed to receive final result: %w", err)
	}

	switch finalPayload := finalMsg.Payload.(type) {
	case *grpc_v1.ServerChallenge_Success:
		t.log.Info("TPM challenge completed successfully")
		return nil
	case *grpc_v1.ServerChallenge_Error:
		return fmt.Errorf("TPM challenge failed: %s: %w", finalPayload.Error.Message, ErrIdentityProofFailed)
	default:
		return fmt.Errorf("unexpected server response type: %T", finalPayload)
	}
}

func (t *tpmProvider) ProveIdentity(ctx context.Context, enrollmentRequest *v1alpha1.EnrollmentRequest) error {
	if !t.isTPMVerificationNeeded(enrollmentRequest) {
		return nil
	}

	enrollmentRequestName := lo.FromPtr(enrollmentRequest.Metadata.Name)
	if enrollmentRequestName == "" {
		return fmt.Errorf("enrollment request name is empty")
	}

	t.log.Debug("Starting TPM challenge-response protocol for identity proof")
	grpcClient, closeConn, err := t.createEnrollmentGRPCClient()
	if err != nil {
		return fmt.Errorf("failed to create gRPC client: %w", err)
	}
	defer func() {
		if err := closeConn(); err != nil {
			t.log.Warnf("Failed to close gRPC connection: %v", err)
		}
	}()

	stream, err := grpcClient.TPMChallenge(ctx)
	if err != nil {
		return fmt.Errorf("starting TPM challenge stream: %w", err)
	}

	return t.handleTPMChallenge(ctx, stream, enrollmentRequestName)
}

// createTLSConfig creates a TLS configuration from a client config
func (t *tpmProvider) createTLSConfig(config *base_client.Config, clientCerts ...tls.Certificate) (*tls.Config, error) {
	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: config.Service.InsecureSkipVerify, //nolint:gosec
	}

	if len(clientCerts) > 0 {
		tlsConfig.Certificates = clientCerts
	}

	if config.Service.TLSServerName != "" {
		tlsConfig.ServerName = config.Service.TLSServerName
	} else {
		if u, err := url.Parse(config.Service.Server); err == nil {
			tlsConfig.ServerName = u.Hostname()
		}
	}

	if len(config.Service.CertificateAuthorityData) > 0 {
		caPool, err := certutil.NewPoolFromBytes(config.Service.CertificateAuthorityData)
		if err != nil {
			return nil, fmt.Errorf("failed to parse CA certificates: %w", err)
		}
		tlsConfig.RootCAs = caPool
	}

	return tlsConfig, nil
}

// createGRPCConnection creates a gRPC connection using the provided config
func (t *tpmProvider) createGRPCConnection(config *base_client.Config, clientCerts ...tls.Certificate) (*grpc.ClientConn, error) {
	tlsConfig, err := t.createTLSConfig(config, clientCerts...)
	if err != nil {
		return nil, err
	}

	// Clean up the endpoint for gRPC
	grpcEndpoint := config.Service.Server
	grpcEndpoint = strings.TrimPrefix(grpcEndpoint, "http://")
	grpcEndpoint = strings.TrimPrefix(grpcEndpoint, "https://")
	grpcEndpoint = strings.TrimSuffix(grpcEndpoint, "/")

	conn, err := grpc.NewClient(grpcEndpoint,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}))
	if err != nil {
		return nil, fmt.Errorf("failed to create gRPC connection: %w", err)
	}

	return conn, nil
}

// createEnrollmentGRPCClient creates a gRPC client for the enrollment service
func (t *tpmProvider) createEnrollmentGRPCClient() (grpc_v1.EnrollmentClient, func() error, error) {
	// Use the enrollment configuration to create a gRPC connection
	enrollConfig := t.config.EnrollmentService.Config.DeepCopy()
	if err := enrollConfig.Flatten(); err != nil {
		return nil, nil, err
	}

	var clientCerts []tls.Certificate
	if len(enrollConfig.AuthInfo.ClientCertificateData) > 0 && len(enrollConfig.AuthInfo.ClientKeyData) > 0 {
		cert, err := tls.X509KeyPair(enrollConfig.AuthInfo.ClientCertificateData, enrollConfig.AuthInfo.ClientKeyData)
		if err != nil {
			return nil, nil, fmt.Errorf("loading enrollment client certificate: %w", err)
		}
		clientCerts = append(clientCerts, cert)
	}

	conn, err := t.createGRPCConnection(enrollConfig, clientCerts...)
	if err != nil {
		return nil, nil, err
	}

	eClient := grpc_v1.NewEnrollmentClient(conn)
	return eClient, conn.Close, nil
}

func (t *tpmProvider) StoreCertificate(certPEM []byte) error {
	if err := t.rw.WriteFile(t.clientCertPath, certPEM, 0600); err != nil {
		return fmt.Errorf("failed to write certificate to disk: %w", err)
	}
	t.certificateData = certPEM
	return nil
}

func (t *tpmProvider) HasCertificate() bool {
	exists, err := t.rw.PathExists(t.clientCertPath)
	if err != nil {
		t.log.Warnf("Failed to check certificate existence: %v", err)
		return false
	}
	return exists
}

// initializeTPMCredential ensures the TPM password credential is set up
func (t *tpmProvider) initializeTPMCredential(ctx context.Context) error {
	available, err := checkParentCredentialAvailable(t.rw, t.log)
	if err != nil {
		return fmt.Errorf("failed to check credential availability: %w", err)
	}
	if available {
		t.log.Debug("TPM storage password credential already available")
		return nil
	}

	exists, err := t.rw.PathExists(ParentCredentialPath)
	if err != nil {
		return fmt.Errorf("checking for sealed credential: %w", err)
	}
	if exists {
		t.log.Debug("TPM storage password credential file exists")
		// Ensure the systemd drop-in exists even if credential already exists
		// This handles cases where the drop-in was deleted or service was reinstalled
		if err := t.ensureParentSystemdDropin(ctx); err != nil {
			return fmt.Errorf("failed to ensure systemd drop-in for existing credential: %w", err)
		}
		return nil
	}

	t.log.Info("Initializing new TPM storage password credential")
	sealer, err := NewSealer(t.log, t.rw, t.exec)
	if err != nil {
		return fmt.Errorf("failed to create password sealer: %w", err)
	}

	password, err := GeneratePassword(DefaultSecretLength)
	if err != nil {
		return fmt.Errorf("failed to generate password: %w", err)
	}

	// use host-only sealing for flightctl-agent hierarchy password
	// otherwise we have chicken/egg
	err = sealer.Seal(ctx, "flightctl-agent", SealKeyHost, password)
	if err != nil {
		fccrypto.SecureMemoryWipe(password)
		return fmt.Errorf("failed to seal password: %w", err)
	}

	defer fccrypto.SecureMemoryWipe(password)

	t.log.Info("TPM storage password credential initialized successfully")
	t.log.Infof("Sealed credential stored at: %s", ParentCredentialPath)

	if err := t.ensureParentSystemdDropin(ctx); err != nil {
		return fmt.Errorf("failed to create systemd drop-in: %w", err)
	}

	return nil
}

// ensureParentSystemdDropin creates a systemd drop-in file for flightctl-agent to load the TPM credential
func (t *tpmProvider) ensureParentSystemdDropin(ctx context.Context) error {
	return createSystemdDropIn(ctx, ParentService, "flightctl-agent", t.rw, t.exec, t.log)
}

func (t *tpmProvider) createCertificate() (*tls.Certificate, error) {
	if t.client == nil {
		return nil, fmt.Errorf("TPM client not initialized")
	}
	if t.certificateData == nil {
		return nil, fmt.Errorf("no certificate data available for TPM authentication - device needs enrollment")
	}
	signer := t.client.GetSigner()
	// parse the certificate from PEM block
	certBlock, _ := pem.Decode(t.certificateData)
	if certBlock == nil {
		return nil, fmt.Errorf("failed to decode certificate PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	// create TLS certificate using the TPM private key and the parsed certificate
	tlsCert := &tls.Certificate{
		Certificate: [][]byte{cert.Raw},
		PrivateKey:  signer,
	}
	return tlsCert, nil
}

func normalizeManagementConfig(config *base_client.Config) (*base_client.Config, error) {
	configCopy := config.DeepCopy()
	// The ClientKey and ClientKeyData will never exist for the TPM. Ensure that any mention to those values are removed
	configCopy.AuthInfo.ClientKey = ""
	configCopy.AuthInfo.ClientKeyData = nil
	if err := configCopy.Flatten(); err != nil {
		return nil, fmt.Errorf("flattening config: %w", err)
	}
	return configCopy, nil
}

func (t *tpmProvider) CreateManagementClient(config *base_client.Config, metricsCallback client.RPCMetricsCallback) (client.Management, error) {
	tlsCert, err := t.createCertificate()
	if err != nil {
		return nil, err
	}
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{*tlsCert},
		MinVersion:   tls.VersionTLS13,
	}

	configCopy, err := normalizeManagementConfig(config)
	if err != nil {
		return nil, fmt.Errorf("normalizing config: %w", err)
	}
	if configCopy.Service.CertificateAuthorityData != nil {
		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(configCopy.Service.CertificateAuthorityData)
		tlsConfig.RootCAs = caCertPool
	}

	if configCopy.Service.TLSServerName != "" {
		tlsConfig.ServerName = configCopy.Service.TLSServerName
	} else {
		u, err := url.Parse(configCopy.Service.Server)
		if err == nil {
			tlsConfig.ServerName = u.Hostname()
		}
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	for _, opt := range configCopy.HTTPOptions {
		if err = opt(httpClient); err != nil {
			return nil, fmt.Errorf("applying HTTP option: %w", err)
		}
	}

	clientWithResponses, err := agent_client.NewClientWithResponses(configCopy.Service.Server, agent_client.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("creating client: %w", err)
	}

	managementClient := client.NewManagement(clientWithResponses, metricsCallback)
	return managementClient, nil
}

func (t *tpmProvider) CreateGRPCClient(config *base_client.Config) (grpc_v1.RouterServiceClient, error) {
	tlsCert, err := t.createCertificate()
	if err != nil {
		return nil, err
	}

	configCopy, err := normalizeManagementConfig(config)
	if err != nil {
		return nil, fmt.Errorf("normalizing config: %w", err)
	}

	conn, err := t.createGRPCConnection(configCopy, *tlsCert)
	if err != nil {
		return nil, fmt.Errorf("creating gRPC client: %w", err)
	}

	router := grpc_v1.NewRouterServiceClient(conn)
	return router, nil
}

func (t *tpmProvider) WipeCredentials() error {
	var errs []error

	// clear certificate data from memory
	t.certificateData = nil

	if t.clientCertPath != "" {
		t.log.Infof("Wiping certificate file %s", t.clientCertPath)
		if err := t.rw.OverwriteAndWipe(t.clientCertPath); err != nil {
			errs = append(errs, fmt.Errorf("failed to wipe certificate file %s: %w", t.clientCertPath, err))
		}
	}

	if err := t.client.Clear(); err != nil {
		errs = append(errs, fmt.Errorf("clearing TPM client: %w", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to wipe credentials: %v", errs)
	}

	t.log.Info("Successfully wiped TPM credentials from disk and memory")
	return nil
}

func (t *tpmProvider) WipeCertificateOnly() error {
	t.certificateData = nil
	if t.clientCertPath == "" {
		return fmt.Errorf("client certificate path is not set")
	}

	t.log.Infof("Wiping certificate file %s", t.clientCertPath)
	if err := t.rw.OverwriteAndWipe(t.clientCertPath); err != nil {
		return fmt.Errorf("failed to wipe certificate file %s: %w", t.clientCertPath, err)
	}

	t.log.Info("Successfully wiped certificate file")
	return nil
}

func (t *tpmProvider) Close(ctx context.Context) error {
	if t.client != nil {
		return t.client.Close(ctx)
	}
	return nil
}
