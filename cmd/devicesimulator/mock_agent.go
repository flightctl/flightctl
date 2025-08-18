package main

import (
	"context"
	"crypto"
	"encoding/base32"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	agentclient "github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device"
	"github.com/flightctl/flightctl/internal/agent/device/publisher"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	apiClient "github.com/flightctl/flightctl/internal/api/client"
	baseclient "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/config"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	agentlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/util/wait"
)

// simulatorIdentity manages cryptographic identity for simulated devices
type simulatorIdentity struct {
	deviceName string
	privateKey crypto.PrivateKey
	publicKey  crypto.PublicKey
}

// enrollmentArtifacts contains the identity and certificate data from enrollment
type enrollmentArtifacts struct {
	identity    *simulatorIdentity
	certificate []byte
}

// mockAgent holds the state needed for running the mock agent simulation
type mockAgent struct {
	deviceName    string
	statusManager status.Manager
	subscription  publisher.Subscription
	log           *logrus.Logger
	currentSpec   *v1alpha1.Device
	mu            sync.RWMutex
}

// syncDeviceSpec handles spec updates from the publisher subscription
func (m *mockAgent) syncDeviceSpec(ctx context.Context) {
	// Try to get latest spec from subscription
	if newSpec, ok, err := m.subscription.TryPop(); err == nil && ok {
		m.updateCurrentSpec(newSpec)
		m.log.Debugf("Device %s: received spec update version %s", m.deviceName, newSpec.Version())
	}
}

// statusUpdate handles periodic status updates
func (m *mockAgent) statusUpdate(ctx context.Context) {
	if err := m.statusManager.Sync(ctx); err != nil {
		m.log.Warnf("Device %s: failed to sync status: %v", m.deviceName, err)
	}
}

// Status implements the status.Exporter interface
func (m *mockAgent) Status(ctx context.Context, deviceStatus *v1alpha1.DeviceStatus, opts ...status.CollectorOpt) error {
	m.mu.RLock()
	currentSpec := m.currentSpec
	m.mu.RUnlock()

	now := time.Now()

	// Determine the version for the condition message
	version := "1"
	if currentSpec != nil && currentSpec.Spec != nil {
		version = currentSpec.Version()
	}

	// Populate device status with simulated data
	deviceStatus.Summary = v1alpha1.DeviceSummaryStatus{
		Status: v1alpha1.DeviceSummaryStatusOnline,
		Info:   lo.ToPtr("Simulated device running"),
	}

	deviceStatus.SystemInfo = v1alpha1.DeviceSystemInfo{
		AgentVersion:    "simulator-1.0.0",
		Architecture:    "sim64",
		BootID:          "sim-boot-12345",
		OperatingSystem: "SimulatorOS",
	}

	deviceStatus.ApplicationsSummary = v1alpha1.DeviceApplicationsSummaryStatus{
		Status: v1alpha1.ApplicationsSummaryStatusHealthy,
	}

	deviceStatus.Integrity = v1alpha1.DeviceIntegrityStatus{
		Status: v1alpha1.DeviceIntegrityStatusVerified,
		Info:   lo.ToPtr("Simulated device integrity verified"),
	}

	deviceStatus.Lifecycle = v1alpha1.DeviceLifecycleStatus{
		Status: v1alpha1.DeviceLifecycleStatusEnrolled,
		Info:   lo.ToPtr("Simulated device enrolled"),
	}

	deviceStatus.Resources = v1alpha1.DeviceResourceStatus{
		Cpu:    v1alpha1.DeviceResourceStatusHealthy,
		Disk:   v1alpha1.DeviceResourceStatusHealthy,
		Memory: v1alpha1.DeviceResourceStatusHealthy,
	}

	deviceStatus.Updated = v1alpha1.DeviceUpdatedStatus{
		Status: v1alpha1.DeviceUpdatedStatusUpToDate,
		Info:   lo.ToPtr("Simulated device is up to date"),
	}

	deviceStatus.Conditions = []v1alpha1.Condition{
		{
			Type:    v1alpha1.ConditionTypeDeviceUpdating,
			Status:  v1alpha1.ConditionStatusFalse,
			Reason:  string(v1alpha1.UpdateStateUpdated),
			Message: fmt.Sprintf("Updated to desired renderedVersion: %s", version),
		},
	}

	deviceStatus.LastSeen = now

	// If we have a current spec, simulate that we're running that version
	if currentSpec != nil && currentSpec.Spec != nil {
		deviceStatus.Config.RenderedVersion = currentSpec.Version()
	}

	return nil
}

// updateCurrentSpec updates the current spec for status generation
func (m *mockAgent) updateCurrentSpec(spec *v1alpha1.Device) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.currentSpec = spec
}

// startMockSimulation handles the enrollment and starts the mock simulation
func startMockSimulation(ctx context.Context, params agentLaunchParams) {
	// Generate friendly device name
	friendlyDeviceName := fmt.Sprintf("device-%05d", params.initialDeviceIndex+params.agentIndex)

	// Use standard service client enrollment
	artifacts, err := enrollAndApproveDevice(ctx, params)
	if err != nil {
		params.log.Errorf("Failed to enroll device: %v", err)
		return
	}

	go runMockAgent(ctx, params, artifacts, friendlyDeviceName)
}

// runMockAgent runs the mock agent simulation using publisher
func runMockAgent(ctx context.Context, params agentLaunchParams, artifacts *enrollmentArtifacts, friendlyDeviceName string) {
	deviceName := artifacts.identity.deviceName

	// Delay randomly to avoid thundering herd
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Duration(rand.Float64() * float64(params.jitterDuration))): //nolint:gosec
	}

	// Create properly configured logger with friendly name and correct log level
	logWithPrefix := agentlog.NewPrefixLogger(friendlyDeviceName)
	logWithPrefix.Level(params.agentConfig.LogLevel)
	// Create mTLS management client using enrollment artifacts
	managementClient, err := createManagementClient(artifacts, &params.agentInfos[params.agentIndex].config.ManagementService, logWithPrefix)
	if err != nil {
		params.log.Errorf("Device %s: failed to create mTLS management client: %v", friendlyDeviceName, err)
		return
	}
	params.log.Debugf("Device %s: using mTLS identity client", friendlyDeviceName)

	// Create and configure publisher
	backoff := wait.Backoff{
		Duration: 10 * time.Second,
		Factor:   1.5,
		Cap:      1 * time.Minute,
		Steps:    6,
	}

	// Use the same logger for all components
	devicePublisher := publisher.New(
		deviceName,
		time.Duration(params.agentConfig.SpecFetchInterval),
		backoff,
		logWithPrefix,
	)
	devicePublisher.SetClient(managementClient)

	// Create status manager
	statusManager := status.NewManager(deviceName, logWithPrefix)
	statusManager.SetClient(managementClient)

	// Subscribe to spec updates
	subscription := devicePublisher.Subscribe()

	// Create mock agent instance to hold state for Engine functions
	mockAgentInstance := &mockAgent{
		deviceName:    friendlyDeviceName,
		statusManager: statusManager,
		subscription:  subscription,
		log:           params.log,
	}

	// Register the mock agent itself as the status exporter
	statusManager.RegisterStatusExporter(mockAgentInstance)

	var wg sync.WaitGroup
	wg.Add(1)

	// Goroutine 1: Publisher (spec fetcher)
	go devicePublisher.Run(ctx, &wg)

	// Create and run Engine that orchestrates spec and status syncing
	engine := device.NewEngine(
		params.agentConfig.SpecFetchInterval,
		mockAgentInstance.syncDeviceSpec,
		params.agentConfig.StatusUpdateInterval,
		mockAgentInstance.statusUpdate,
	)

	params.log.Infof("Device %s: mock simulation started", friendlyDeviceName)
	err = engine.Run(ctx)
	wg.Wait()
	params.log.Infof("Device %s: mock simulation stopped", friendlyDeviceName)
	if err != nil {
		params.log.Errorf("Device %s: engine stopped with error: %v", friendlyDeviceName, err)
	}
}

// createSimulatorIdentity creates a new cryptographic identity for a simulated device
func createSimulatorIdentity() (*simulatorIdentity, error) {
	// Generate a new key pair for this simulated device
	publicKey, privateKey, _ := fccrypto.NewKeyPair()

	// Generate device name from public key hash (same as identity package)
	deviceName, err := generateDeviceName(publicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to generate device name: %w", err)
	}

	return &simulatorIdentity{
		deviceName: deviceName,
		privateKey: privateKey,
		publicKey:  publicKey,
	}, nil
}

// generateDeviceName creates a device name from a public key hash (same as identity package)
func generateDeviceName(publicKey crypto.PublicKey) (string, error) {
	publicKeyHash, err := fccrypto.HashPublicKey(publicKey)
	if err != nil {
		return "", fmt.Errorf("failed to hash public key: %w", err)
	}
	return strings.ToLower(base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(publicKeyHash)), nil
}

// generateCSR creates a certificate signing request using the device's private key
func (s *simulatorIdentity) generateCSR() (string, error) {
	signer, ok := s.privateKey.(crypto.Signer)
	if !ok {
		return "", fmt.Errorf("private key does not implement crypto.Signer")
	}

	csrBytes, err := fccrypto.MakeCSR(signer, s.deviceName)
	if err != nil {
		return "", fmt.Errorf("failed to create CSR: %w", err)
	}

	return string(csrBytes), nil
}

// enrollAndApproveDevice creates an enrollment request and immediately approves it
func enrollAndApproveDevice(ctx context.Context, params agentLaunchParams) (*enrollmentArtifacts, error) {
	// 1. Create simulator identity and generate real CSR
	identity, err := createSimulatorIdentity()
	if err != nil {
		return nil, fmt.Errorf("create simulator identity: %w", err)
	}

	deviceName := identity.deviceName

	csr, err := identity.generateCSR()
	if err != nil {
		return nil, fmt.Errorf("generate CSR: %w", err)
	}

	// 2. Create enrollment request with retry
	enrollmentReq := v1alpha1.EnrollmentRequest{
		ApiVersion: "v1alpha1",
		Kind:       "EnrollmentRequest",
		Metadata: v1alpha1.ObjectMeta{
			Name: &deviceName,
		},
		Spec: v1alpha1.EnrollmentRequestSpec{
			Csr:    csr,
			Labels: params.formattedLabels,
		},
	}

	var resp *apiClient.CreateEnrollmentRequestResponse
	backoff := wait.Backoff{
		Duration: 1 * time.Second,
		Factor:   2.0,
		Cap:      30 * time.Second,
		Steps:    10, // This gives us about 5 minutes total
	}

	err = wait.ExponentialBackoff(backoff, func() (bool, error) {
		var createErr error
		resp, createErr = params.serviceClient.CreateEnrollmentRequestWithResponse(ctx, enrollmentReq)
		if createErr != nil {
			params.log.Debugf("Device %s: enrollment creation failed, retrying: %v", deviceName, createErr)
			return false, nil // Retry on error
		}

		if resp.HTTPResponse == nil || resp.HTTPResponse.StatusCode != 201 || resp.JSON201 == nil {
			params.log.Debugf("Device %s: enrollment creation failed with status %d, retrying", deviceName, resp.HTTPResponse.StatusCode)
			return false, nil // Retry on bad response
		}

		return true, nil // Success
	})

	if err != nil {
		return nil, fmt.Errorf("create enrollment request failed after retries: %w", err)
	}

	// 3. Approve enrollment request with retry
	enrollmentID := *resp.JSON201.Metadata.Name
	approval := v1alpha1.EnrollmentRequestApproval{
		Approved: true,
		Labels:   params.formattedLabels,
	}

	var approveResp *apiClient.ApproveEnrollmentRequestResponse
	err = wait.ExponentialBackoff(backoff, func() (bool, error) {
		var approveErr error
		approveResp, approveErr = params.serviceClient.ApproveEnrollmentRequestWithResponse(ctx, enrollmentID, approval)
		if approveErr != nil {
			params.log.Debugf("Device %s: enrollment approval failed, retrying: %v", deviceName, approveErr)
			return false, nil // Retry on error
		}

		if approveResp.HTTPResponse == nil || approveResp.HTTPResponse.StatusCode < 200 || approveResp.HTTPResponse.StatusCode >= 300 {
			params.log.Debugf("Device %s: enrollment approval failed with status %d, retrying", deviceName, approveResp.HTTPResponse.StatusCode)
			return false, nil // Retry on bad response
		}

		return true, nil // Success
	})

	if err != nil {
		return nil, fmt.Errorf("approve enrollment request failed after retries: %w", err)
	}

	// 4. Get the certificate by fetching the enrollment request again with retry
	var enrollmentResp *apiClient.GetEnrollmentRequestResponse
	var certificate []byte

	err = wait.ExponentialBackoff(backoff, func() (bool, error) {
		var getErr error
		enrollmentResp, getErr = params.serviceClient.GetEnrollmentRequestWithResponse(ctx, enrollmentID)
		if getErr != nil {
			params.log.Debugf("Device %s: get enrollment request failed, retrying: %v", deviceName, getErr)
			return false, nil // Retry on error
		}

		if enrollmentResp.HTTPResponse == nil || enrollmentResp.HTTPResponse.StatusCode != 200 || enrollmentResp.JSON200 == nil {
			params.log.Debugf("Device %s: get enrollment request failed with status %d, retrying", deviceName, enrollmentResp.HTTPResponse.StatusCode)
			return false, nil // Retry on bad response
		}

		if enrollmentResp.JSON200.Status == nil || enrollmentResp.JSON200.Status.Certificate == nil {
			params.log.Debugf("Device %s: certificate not yet available, retrying", deviceName)
			return false, nil // Retry if certificate not ready
		}

		certificate = []byte(*enrollmentResp.JSON200.Status.Certificate)
		if len(certificate) == 0 {
			params.log.Debugf("Device %s: certificate is empty, retrying", deviceName)
			return false, nil // Retry if certificate is empty
		}

		return true, nil // Success
	})

	if err != nil {
		return nil, fmt.Errorf("get enrollment certificate failed after retries: %w", err)
	}

	params.log.Infof("Device %s enrolled and approved", deviceName)
	return &enrollmentArtifacts{
		identity:    identity,
		certificate: certificate,
	}, nil
}

// createManagementClient creates a management client using mTLS with the provided enrollment artifacts
func createManagementClient(artifacts *enrollmentArtifacts, managementService *config.ManagementService, logger *agentlog.PrefixLogger) (agentclient.Management, error) {
	keyPEM, err := fccrypto.PEMEncodeKey(artifacts.identity.privateKey)
	if err != nil {
		return nil, fmt.Errorf("encode private key to PEM: %w", err)
	}

	cfg := managementService.Config
	cfg.AuthInfo = baseclient.AuthInfo{
		ClientCertificateData: artifacts.certificate,
		ClientKeyData:         keyPEM,
	}

	// Create agent client from config
	agentClient, err := agentclient.NewFromConfig(&cfg, logger)
	if err != nil {
		return nil, fmt.Errorf("create agent client: %w", err)
	}

	return agentclient.NewManagement(agentClient, nil), nil
}
