package e2e

import (
	"fmt"

	"github.com/flightctl/flightctl/api/core/v1beta1"
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

// HasEventWithReason checks if the event list contains an event with the specified reason.
func HasEventWithReason(events []v1beta1.Event, reason v1beta1.EventReason) bool {
	for _, ev := range events {
		if ev.Reason == reason {
			return true
		}
	}
	return false
}
