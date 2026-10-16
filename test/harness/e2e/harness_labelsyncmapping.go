package e2e

import (
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	. "github.com/onsi/gomega"
)

// CreateLabelSyncMapping creates a LabelSyncMapping resource.
func (h *Harness) CreateLabelSyncMapping(mapping v1beta1.LabelSyncMapping) error {
	if mapping.Metadata.Name == nil || *mapping.Metadata.Name == "" {
		return fmt.Errorf("LabelSyncMapping name cannot be empty")
	}
	name := *mapping.Metadata.Name
	response, err := h.Client.CreateLabelSyncMappingWithResponse(h.Context, mapping)
	if err != nil {
		return fmt.Errorf("failed to create LabelSyncMapping %q: %w", name, err)
	}
	if response == nil || response.JSON201 == nil {
		if response == nil {
			return fmt.Errorf("failed to create LabelSyncMapping %q: response is nil", name)
		}
		return fmt.Errorf("failed to create LabelSyncMapping %q: unexpected status %d: %s", name, response.StatusCode(), string(response.Body))
	}
	return nil
}

// GetLabelSyncMapping retrieves a LabelSyncMapping by name.
func (h *Harness) GetLabelSyncMapping(name string) (*v1beta1.LabelSyncMapping, error) {
	if name == "" {
		return nil, fmt.Errorf("LabelSyncMapping name cannot be empty")
	}
	response, err := h.Client.GetLabelSyncMappingWithResponse(h.Context, name)
	if err != nil {
		return nil, fmt.Errorf("failed to get LabelSyncMapping %q: %w", name, err)
	}
	if response == nil || response.JSON200 == nil {
		if response == nil {
			return nil, fmt.Errorf("failed to get LabelSyncMapping %q: response is nil", name)
		}
		return nil, fmt.Errorf("failed to get LabelSyncMapping %q: unexpected status %d: %s", name, response.StatusCode(), string(response.Body))
	}
	return response.JSON200, nil
}

// DeleteLabelSyncMapping deletes a LabelSyncMapping, ignoring resources that are already absent.
func (h *Harness) DeleteLabelSyncMapping(name string) error {
	if name == "" {
		return fmt.Errorf("LabelSyncMapping name cannot be empty")
	}
	response, err := h.Client.DeleteLabelSyncMappingWithResponse(h.Context, name)
	if err != nil {
		return fmt.Errorf("failed to delete LabelSyncMapping %q: %w", name, err)
	}
	if response == nil {
		return fmt.Errorf("failed to delete LabelSyncMapping %q: response is nil", name)
	}
	if response.StatusCode() != http.StatusOK && response.StatusCode() != http.StatusNotFound {
		return fmt.Errorf("failed to delete LabelSyncMapping %q: unexpected status %d: %s", name, response.StatusCode(), string(response.Body))
	}
	return nil
}

// WaitForLabelSyncMappingReady waits for the current mapping generation to have the expected Ready status and reason.
func (h *Harness) WaitForLabelSyncMappingReady(name string, expectedStatus v1beta1.ConditionStatus, expectedReason string, timeout, polling time.Duration) {
	var generation string
	var condition *v1beta1.Condition
	Eventually(func() (bool, error) {
		mapping, err := h.GetLabelSyncMapping(name)
		if err != nil {
			return false, err
		}
		generation = "<unset>"
		if mapping.Metadata.Generation != nil {
			generation = fmt.Sprint(*mapping.Metadata.Generation)
		}
		condition = nil
		if mapping.Status != nil && mapping.Status.Conditions != nil {
			condition = v1beta1.FindStatusCondition(*mapping.Status.Conditions, v1beta1.ConditionTypeLabelSyncMappingReady)
		}
		if mapping.Metadata.Generation == nil || condition == nil || condition.ObservedGeneration == nil || *condition.ObservedGeneration != *mapping.Metadata.Generation {
			return false, nil
		}
		return condition.Status == expectedStatus && condition.Reason == expectedReason, nil
	}, timeout, polling).Should(BeTrue(), "LabelSyncMapping %q Ready condition should be %s/%s for its current generation; observed generation: %v, condition: %+v", name, expectedStatus, expectedReason, generation, condition)
}

// WaitForLabelSyncMappingDeleted waits for asynchronous cleanup and finalization to remove a mapping.
func (h *Harness) WaitForLabelSyncMappingDeleted(name string, timeout, polling time.Duration) {
	Eventually(func() (bool, error) {
		response, err := h.Client.GetLabelSyncMappingWithResponse(h.Context, name)
		if err != nil {
			return false, fmt.Errorf("failed to check deletion of LabelSyncMapping %q: %w", name, err)
		}
		if response == nil {
			return false, fmt.Errorf("failed to check deletion of LabelSyncMapping %q: response is nil", name)
		}
		if response.StatusCode() == http.StatusNotFound {
			return true, nil
		}
		if response.StatusCode() != http.StatusOK {
			return false, fmt.Errorf("failed to check deletion of LabelSyncMapping %q: unexpected status %d: %s", name, response.StatusCode(), string(response.Body))
		}
		return false, nil
	}, timeout, polling).Should(BeTrue(), "LabelSyncMapping %q should be deleted", name)
}
