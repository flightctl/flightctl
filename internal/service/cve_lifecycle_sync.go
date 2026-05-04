package service

import (
	"context"
	"strconv"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
)

// SyncDeviceCVELifecycleEvents runs per-device CVE lifecycle phases.
// Each phase order is: resolution, Warning supersede, Critical, Warning.
// Events are generated based on CVE severity: Critical severity → Critical event, High severity → Warning event.
// It is a no-op when the vulnerability finding store or event handler is unavailable.
func (h *ServiceHandler) SyncDeviceCVELifecycleEvents(ctx context.Context) error {
	vf := h.store.VulnerabilityFinding()
	if vf == nil {
		return nil
	}
	if h.eventHandler == nil {
		return nil
	}

	resolveRows, err := vf.ListCVEEventResolutionCandidates(ctx)
	if err != nil {
		return err
	}
	for i := range resolveRows {
		ev, berr := buildDeviceCVELifecycleEvent(ctx, resolveRows[i].DeviceName,
			resolveRows[i].CveID, resolveRows[i].ImageDigest, resolveRows[i].ImageRef,
			resolveRows[i].CvssScore, resolveRows[i].Severity, domain.EventReasonDeviceVulnerabilityCVEResolved)
		if berr != nil {
			return berr
		}
		h.eventHandler.CreateEvent(ctx, resolveRows[i].OrgID, ev)
	}

	supersedeRows, err := vf.ListOpenWarningSupersedeCVEEventCandidates(ctx)
	if err != nil {
		return err
	}
	for i := range supersedeRows {
		ev, berr := buildDeviceCVELifecycleEventFromCandidate(ctx, supersedeRows[i],
			domain.EventReasonDeviceVulnerabilityCVEResolved)
		if berr != nil {
			return berr
		}
		h.eventHandler.CreateEvent(ctx, supersedeRows[i].OrgID, ev)
	}

	criticalRows, err := vf.ListCriticalCVEEventCandidates(ctx)
	if err != nil {
		return err
	}
	for i := range criticalRows {
		ev, berr := buildDeviceCVELifecycleEventFromCandidate(ctx, criticalRows[i],
			domain.EventReasonDeviceVulnerabilityCVECritical)
		if berr != nil {
			return berr
		}
		h.eventHandler.CreateEvent(ctx, criticalRows[i].OrgID, ev)
	}

	warningRows, err := vf.ListWarningCVEEventCandidates(ctx)
	if err != nil {
		return err
	}
	for i := range warningRows {
		ev, berr := buildDeviceCVELifecycleEventFromCandidate(ctx, warningRows[i],
			domain.EventReasonDeviceVulnerabilityCVEWarning)
		if berr != nil {
			return berr
		}
		h.eventHandler.CreateEvent(ctx, warningRows[i].OrgID, ev)
	}

	return nil
}

func buildDeviceCVELifecycleEventFromCandidate(ctx context.Context, row store.CVEEventCandidate, reason domain.EventReason) (*domain.Event, error) {
	return buildDeviceCVELifecycleEvent(ctx, row.DeviceName, row.CveID, row.ImageDigest, row.ImageRef,
		row.CvssScore, row.Severity, reason)
}

func buildDeviceCVELifecycleEvent(ctx context.Context, deviceName, cveID, imageDigest, imageRef string, cvss float64, severity string, reason domain.EventReason) (*domain.Event, error) {
	details := domain.EventDetails{}
	detail := domain.DeviceVulnerabilityCveDetails{
		DetailType:       domain.DeviceVulnerabilityCveDetailsDetailType("DeviceVulnerabilityCVE"),
		CveId:            cveID,
		FirstImageRef:    &imageRef,
		FirstImageDigest: &imageDigest,
	}
	if err := details.FromDeviceVulnerabilityCveDetails(detail); err != nil {
		return nil, err
	}

	msg := formatCVEDeviceEventMessage(reason, cveID, cvss, severity, imageRef)
	return domain.GetBaseEvent(ctx, domain.DeviceKind, deviceName, reason, msg, &details), nil
}

func formatCVEDeviceEventMessage(reason domain.EventReason, cveID string, cvss float64, severity, imageRef string) string {
	switch reason {
	case domain.EventReasonDeviceVulnerabilityCVEResolved:
		if imageRef != "" {
			return cveID + " resolved for image " + imageRef
		}
		return cveID + " resolved"
	case domain.EventReasonDeviceVulnerabilityCVECritical, domain.EventReasonDeviceVulnerabilityCVEWarning:
		if imageRef != "" {
			return cveID + " (" + severity + ", CVSS " + formatCvssScoreOneDecimal(cvss) + ") detected on image " + imageRef
		}
		return cveID + " (" + severity + ", CVSS " + formatCvssScoreOneDecimal(cvss) + ") detected"
	default:
		return cveID
	}
}

func formatCvssScoreOneDecimal(cvss float64) string {
	return strconv.FormatFloat(cvss, 'f', 1, 64)
}
