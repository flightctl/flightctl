package integrity

import (
	"context"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/tpm"
)

var _ status.Exporter = (*Exporter)(nil)

type Exporter struct {
	tpmClient *tpm.Client
}

func NewExporter(tpmClient *tpm.Client) *Exporter {
	return &Exporter{
		tpmClient: tpmClient,
	}
}

func (e *Exporter) Status(ctx context.Context, deviceStatus *v1alpha1.DeviceStatus, opts ...status.CollectorOpt) error {
	if e.tpmClient != nil {
		// TODO: check TPM status and report Verified or Failed
		deviceStatus.Integrity.Status = v1alpha1.DeviceIntegrityStatusVerified
	} else {
		deviceStatus.Integrity.Status = v1alpha1.DeviceIntegrityStatusUnsupported
	}
	return nil
}
