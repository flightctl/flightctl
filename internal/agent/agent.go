package agent

import (
	"bytes"
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/client"
	fccrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/lthibault/jitterbug"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/util/cert"
	ctrl "sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	// DefaultFetchSpecInterval is the default interval between two reads of the remote device spec
	DefaultFetchSpecInterval = time.Second * 60

	// DefaultStatusUpdateInterval is the default interval between two status updates
	DefaultStatusUpdateInterval = time.Second * 60
)

const (
	// name of the CA bundle file
	caBundleFile = "ca.crt"

	// name of the agent's key file
	agentKeyFile = "agent.key"

	// name of the enrollment certificate file
	enrollmentCertFile = "client-enrollment.crt"

	// name of the enrollment key file
	enrollmentKeyFile = "client-enrollment.key"

	// name of the client certificate file
	clientCertFile = "client.crt"
)

type DeviceAgent struct {
	// The agent's logPrefix, which is only used to prefix log messages when running multipe agents.
	logPrefix string

	// The agent's crypto identity used in calls to the FlightCtl API.
	// It is the hash of the agent's public key.
	fingerprint string

	// The agent-generated public/private key pair.
	key *crypto.PrivateKey

	// The location where the agent's certificates and keys are stored.
	certDir string

	// The CA cert bundle of the FlightCtl service.
	caCertPool *x509.CertPool

	// The URL of the enrollment service
	enrollmentServerUrl string

	// The URL of the enrollment UI
	enrollmentUiUrl string

	// The client certificate used to authenticate with the enrollment service
	enrollmentClientCert *tls.Certificate

	// The client used to communicate with the enrollment service
	enrollmentClient *client.ClientWithResponses

	// The URL of the management service
	managementServerUrl string

	// The client certificate used to authenticate with the management service
	managementClientCert *tls.Certificate

	// The client used to communicate with the management service
	managementClient *client.ClientWithResponses

	// The device resource manifest
	device api.Device

	// The list of controllers reconciling device resouces against the device manifest
	controllers []DeviceAgentController

	fetchSpecInterval time.Duration

	fetchSpecJitter time.Duration

	statusUpdateInterval time.Duration

	statusUpdateJitter time.Duration

	rpcMetricsCallbackFunc func(operation string, duractionSeconds float64, err error)

	log logrus.FieldLogger
}

func NewDeviceAgent(enrollmentServerUrl, managementServerUrl, enrollmentUiUrl, certificateDir string, log logrus.FieldLogger) *DeviceAgent {
	return &DeviceAgent{
		logPrefix:            "",
		log:                  log,
		fingerprint:          "",
		key:                  nil,
		certDir:              certificateDir,
		caCertPool:           nil,
		enrollmentServerUrl:  enrollmentServerUrl,
		enrollmentUiUrl:      enrollmentUiUrl,
		enrollmentClientCert: nil,
		enrollmentClient:     nil,
		managementServerUrl:  managementServerUrl,
		managementClientCert: nil,
		managementClient:     nil,
		device: api.Device{
			ApiVersion: "v1alpha1",
			Kind:       "Device",
			Metadata:   api.ObjectMeta{},
			Spec:       api.DeviceSpec{},
			Status:     &api.DeviceStatus{},
		},
		controllers:            []DeviceAgentController{},
		fetchSpecInterval:      DefaultFetchSpecInterval,
		fetchSpecJitter:        0,
		statusUpdateInterval:   DefaultStatusUpdateInterval,
		statusUpdateJitter:     0,
		rpcMetricsCallbackFunc: nil,
	}
}

func (a *DeviceAgent) GetName() string {
	return a.logPrefix
}

func (a *DeviceAgent) SetDisplayName(name string) *DeviceAgent {
	// for the time being, the display name is not persisted in the API, but we use it as a log prefix
	// which is useful in device simulator when running multiple agents at once.
	a.logPrefix = name + " "
	return a
}

func (a *DeviceAgent) SetFetchSpecInterval(interval time.Duration, jitter time.Duration) *DeviceAgent {
	a.fetchSpecInterval = interval
	a.fetchSpecJitter = jitter
	return a
}

func (a *DeviceAgent) SetStatusUpdateInterval(interval time.Duration, jitter time.Duration) *DeviceAgent {
	a.statusUpdateInterval = interval
	a.statusUpdateJitter = jitter
	return a
}

func (a *DeviceAgent) SetRpcMetricsCallbackFunction(callback func(operation string, duractionSeconds float64, err error)) *DeviceAgent {
	a.rpcMetricsCallbackFunc = callback
	return a
}

func (a *DeviceAgent) Run(ctx context.Context) error {
	a.log = log.WithReqID(reqid.NextRequestID(), a.log)

	if err := a.ensureKeyPairAndFingerprint(); err != nil {
		return err
	}

	if err := a.loadCABundle(); err != nil {
		return err
	}

	if err := a.loadClientEnrollmentCertAndKey(); err != nil {
		return err
	}

	if err := a.tryLoadingClientCertAndKey(); err != nil {
		return err
	}

	if !a.haveValidClientCerts() {
		if _, err := a.SetStatus(&a.device); err != nil {
			return err
		}

		if err := a.requestAndWaitForEnrollment(ctx); err != nil {
			return err
		}
		if err := a.tryLoadingClientCertAndKey(); err != nil {
			return err
		}
	}

	if err := a.writeManagementBanner(); err != nil {
		return err
	}

	if err := a.PostStatus(ctx); err != nil {
		return err
	}

	fetchSpecTicker := jitterbug.New(a.fetchSpecInterval, &jitterbug.Norm{Stdev: a.fetchSpecJitter, Mean: 0})
	defer fetchSpecTicker.Stop()
	statusUpdateTicker := jitterbug.New(a.statusUpdateInterval, &jitterbug.Norm{Stdev: a.statusUpdateJitter, Mean: 0})
	defer statusUpdateTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-fetchSpecTicker.C:
			a.log = log.WithReqID(reqid.NextRequestID(), a.log)
			if err := a.FetchSpec(ctx); err != nil {
				a.log.Errorf("%sfetching spec: %v", a.logPrefix, err)
			}
			_, err := a.Reconcile(ctx, ctrl.Request{})
			a.log.Errorf("%sreconcile failed: %v", a.logPrefix, err)
		case <-statusUpdateTicker.C:
			a.log = log.WithReqID(reqid.NextRequestID(), a.log)
			if _, err := a.SetStatus(&a.device); err != nil {
				a.log.Errorf("%ssetting status: %v", a.logPrefix, err)
			}
			err := a.PostStatus(ctx)
			a.log.Errorf("%sposting status: %v", a.logPrefix, err)
		}
	}
}

func (a *DeviceAgent) ensureKeyPairAndFingerprint() error {
	publicKey, privateKey, _, err := fccrypto.EnsureKey(filepath.Join(a.certDir, agentKeyFile))
	if err != nil {
		return err
	}
	publicKeyHash, err := fccrypto.HashPublicKey(publicKey)
	if err != nil {
		return err
	}
	a.key = &privateKey
	a.fingerprint = hex.EncodeToString(publicKeyHash)
	a.device.Metadata.Name = &a.fingerprint
	return nil
}

func (a *DeviceAgent) loadCABundle() error {
	var err error
	a.caCertPool, err = cert.NewPool(filepath.Join(a.certDir, caBundleFile))
	return err
}

func (a *DeviceAgent) loadClientEnrollmentCertAndKey() error {
	cert, err := tls.LoadX509KeyPair(filepath.Join(a.certDir, enrollmentCertFile), filepath.Join(a.certDir, enrollmentKeyFile))
	if err != nil {
		return err
	}
	a.enrollmentClientCert = &cert
	return nil
}

func (a *DeviceAgent) tryLoadingClientCertAndKey() error {
	if ok, _ := cert.CanReadCertAndKey(filepath.Join(a.certDir, clientCertFile), filepath.Join(a.certDir, agentKeyFile)); !ok {
		return nil
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(a.certDir, clientCertFile), filepath.Join(a.certDir, agentKeyFile))
	if err != nil {
		return err
	}
	a.managementClientCert = &cert
	return nil
}

func (a *DeviceAgent) haveValidClientCerts() bool {
	// TODO: check validity
	return a.managementClientCert != nil
}

func (a *DeviceAgent) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	a.log.Infof("%srunning reconciler", a.logPrefix)

	if !a.NeedsUpdate(&a.device) {
		return ctrl.Result{}, nil
	}

	complete, err := a.StageUpdate(&a.device)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !complete {
		return ctrl.Result{}, nil
	}

	complete, err = a.ApplyUpdate(&a.device)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !complete {
		return ctrl.Result{}, nil
	}

	complete, err = a.FinalizeUpdate(&a.device)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !complete {
		return ctrl.Result{}, nil
	}

	if err := a.PostStatus(ctx); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (a *DeviceAgent) FetchSpec(ctx context.Context) error {
	if err := a.ensureManagementClient(); err != nil {
		return err
	}

	a.log.Infof("%sfetching spec", a.logPrefix)

	t0 := time.Now()
	response, err := a.managementClient.ReadDeviceWithResponse(ctx, a.fingerprint)
	if a.rpcMetricsCallbackFunc != nil {
		a.rpcMetricsCallbackFunc("get_spec", time.Since(t0).Seconds(), err)
	}
	if err != nil {
		return fmt.Errorf("%sfetching spec: %v", a.logPrefix, err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return fmt.Errorf("%sfetching spec: %v", a.logPrefix, response.Status())
	}
	a.device = *response.JSON200
	return nil
}

func (a *DeviceAgent) PostStatus(ctx context.Context) error {

	if err := a.ensureManagementClient(); err != nil {
		return err
	}

	a.log.Infof("%sposting status", a.logPrefix)

	if _, err := a.SetStatus(&a.device); err != nil {
		return err
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(a.device); err != nil {
		return err
	}

	t0 := time.Now()
	_, err := a.managementClient.ReplaceDeviceStatusWithBodyWithResponse(ctx, a.fingerprint, "application/json", &buf)
	if a.rpcMetricsCallbackFunc != nil {
		a.rpcMetricsCallbackFunc("update_status", time.Since(t0).Seconds(), err)
	}
	if err != nil {
		return err
	}
	return nil
}

func (a *DeviceAgent) ensureManagementClient() error {
	if a.managementClient == nil {
		httpClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					RootCAs:      a.caCertPool,
					Certificates: []tls.Certificate{*a.managementClientCert},
					MinVersion:   tls.VersionTLS13,
				},
			},
		}
		ref := client.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
			req.Header.Set(middleware.RequestIDHeader, reqid.GetReqID())
			return nil
		})
		c, err := client.NewClientWithResponses(a.managementServerUrl, client.WithHTTPClient(httpClient), ref)
		if err != nil {
			return err
		}
		a.managementClient = c
	}
	return nil
}
