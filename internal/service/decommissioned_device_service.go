package service

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// DecommissionedDeviceService handles all decommissioned device related operations
type DecommissionedDeviceService struct {
	store store.Store
	log   logrus.FieldLogger
}

// NewDecommissionedDeviceService creates a new DecommissionedDeviceService
func NewDecommissionedDeviceService(store store.Store, log logrus.FieldLogger) *DecommissionedDeviceService {
	return &DecommissionedDeviceService{
		store: store,
		log:   log,
	}
}

// GetDecommissionedDevice retrieves a decommissioned device by ID
func (s *DecommissionedDeviceService) GetDecommissionedDevice(ctx context.Context, orgId uuid.UUID, deviceId string) (*model.DecommissionedDevice, error) {
	return s.store.DecommissionedDevice().GetDecommissionedDevice(ctx, orgId, deviceId)
}

// ListDecommissionedDevices lists decommissioned devices with pagination
func (s *DecommissionedDeviceService) ListDecommissionedDevices(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) ([]model.DecommissionedDevice, error) {
	return s.store.DecommissionedDevice().ListDecommissionedDevices(ctx, orgId, listParams)
}

// CreateDecommissionedDevice creates a new entry in the decommissioned devices table
func (s *DecommissionedDeviceService) CreateDecommissionedDevice(ctx context.Context, orgId uuid.UUID, deviceId string, certificateExpirationDate time.Time) error {
	return s.store.DecommissionedDevice().CreateDecommissionedDevice(ctx, orgId, deviceId, certificateExpirationDate)
}

// HandleDeviceDecommission handles the complete decommission process for a device
func (s *DecommissionedDeviceService) HandleDeviceDecommission(ctx context.Context, orgId uuid.UUID, deviceName string) {
	// Try to get certificate expiration date from enrollment request
	certExpiration, certErr := s.getCertificateExpirationFromEnrollmentRequest(ctx, orgId, deviceName)
	if certErr != nil {
		s.log.WithError(certErr).Warnf("Failed to get certificate expiration for decommissioned device %s, using current time", deviceName)
		certExpiration = time.Now()
	}

	// Add to decommissioned devices table
	if err := s.CreateDecommissionedDevice(ctx, orgId, deviceName, certExpiration); err != nil {
		s.log.WithError(err).Errorf("Failed to add device %s to decommissioned devices table", deviceName)
	} else {
		s.log.Infof("Added device %s to decommissioned devices table with certificate expiration %v", deviceName, certExpiration)
	}
}

// extractCertificateExpirationDate extracts the expiration date from a PEM-encoded certificate
func (s *DecommissionedDeviceService) extractCertificateExpirationDate(certPEM string) (time.Time, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return time.Time{}, fmt.Errorf("failed to decode PEM block")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse certificate: %w", err)
	}

	return cert.NotAfter, nil
}

// getCertificateExpirationFromEnrollmentRequest retrieves the certificate expiration date from the enrollment request
func (s *DecommissionedDeviceService) getCertificateExpirationFromEnrollmentRequest(ctx context.Context, orgId uuid.UUID, deviceName string) (time.Time, error) {
	// Get the enrollment request for this device
	enrollmentReq, err := s.store.EnrollmentRequest().Get(ctx, orgId, deviceName)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get enrollment request: %w", err)
	}

	if enrollmentReq.Status == nil || enrollmentReq.Status.Certificate == nil {
		return time.Time{}, fmt.Errorf("no certificate found in enrollment request")
	}

	return s.extractCertificateExpirationDate(*enrollmentReq.Status.Certificate)
}

// IsDeviceDecommissioned checks if a device is in the decommissioned devices table
func (s *DecommissionedDeviceService) IsDeviceDecommissioned(ctx context.Context, orgId uuid.UUID, deviceName string) (bool, error) {
	_, err := s.store.DecommissionedDevice().GetDecommissionedDevice(ctx, orgId, deviceName)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, flterrors.ErrResourceNotFound) {
		return false, nil
	}
	return false, err
}
