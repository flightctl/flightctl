package e2e

import (
	"fmt"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	. "github.com/onsi/ginkgo/v2"
)

// RunGetEvents executes "get events" CLI command with optional arguments.
func (h *Harness) RunGetEvents(args ...string) (string, error) {
	allArgs := append([]string{"get", "events"}, args...)
	return h.CLI(allArgs...)
}

// GetDeviceEvents retrieves events for a device, optionally filtered by reason.
func (h *Harness) GetDeviceEvents(deviceName string, reason *v1beta1.EventReason) ([]v1beta1.Event, error) {
	fieldSelector := fmt.Sprintf("involvedObject.name=%s", deviceName)
	if reason != nil {
		fieldSelector = fmt.Sprintf("%s,reason=%s", fieldSelector, string(*reason))
	}

	resp, err := h.Client.ListEventsWithResponse(h.Context, &v1beta1.ListEventsParams{
		FieldSelector: &fieldSelector,
	})
	if err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("unexpected response: %d", resp.StatusCode())
	}
	return resp.JSON200.Items, nil
}

// ListEventsByReason retrieves events filtered by reason (not scoped to a specific device).
func (h *Harness) ListEventsByReason(reason string) ([]v1beta1.Event, error) {
	fieldSelector := fmt.Sprintf("reason=%s", reason)
	resp, err := h.Client.ListEventsWithResponse(h.Context, &v1beta1.ListEventsParams{
		FieldSelector: &fieldSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list events with reason %s: %w", reason, err)
	}
	if resp == nil {
		return nil, fmt.Errorf("nil response listing events with reason %s", reason)
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("unexpected status %d listing events with reason %s", resp.StatusCode(), reason)
	}
	return resp.JSON200.Items, nil
}

// HasEventWithReason checks if the event list contains an event with the specified reason.
func HasEventWithReason(events []v1beta1.Event, reason v1beta1.EventReason) bool {
	for _, ev := range events {
		if ev.Reason == reason {
			return true
		}
	}
	return false
}

// HasEventWithDetails checks for an event matching the reason and resourceKey. For
// DependencyChangeDetected it also requires an exact fingerprint match; for
// DependencySyncProbeFailed it requires a non-empty error field.
func (h *Harness) HasEventWithDetails(reason v1beta1.EventReason, expectedResourceKey, expectedFingerprint string) (bool, error) {
	events, err := h.ListEventsByReason(string(reason))
	if err != nil {
		return false, err
	}
	for _, ev := range events {
		switch reason {
		case v1beta1.EventReasonDependencyChangeDetected:
			details, detailErr := ev.Details.AsDependencyChangeDetectedDetails()
			if detailErr != nil {
				continue
			}
			if details.ResourceKey == expectedResourceKey && details.Fingerprint == expectedFingerprint {
				GinkgoWriter.Printf("DependencyChangeDetected: resourceKey=%s fingerprint=%s\n",
					details.ResourceKey, details.Fingerprint)
				return true, nil
			}
		case v1beta1.EventReasonDependencySyncProbeFailed:
			details, detailErr := ev.Details.AsDependencySyncProbeFailedDetails()
			if detailErr != nil {
				continue
			}
			if details.ResourceKey == expectedResourceKey && details.Error != "" {
				GinkgoWriter.Printf("DependencySyncProbeFailed: resourceKey=%s error=%s\n",
					details.ResourceKey, details.Error)
				return true, nil
			}
		}
	}
	return false, nil
}
