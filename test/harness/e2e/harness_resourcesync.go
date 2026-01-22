package e2e

import (
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	. "github.com/onsi/gomega"
)

func (h *Harness) GetResourceSync(name string) (*v1beta1.ResourceSync, error) {
	resp, err := h.Client.GetResourceSyncWithResponse(h.Context, name)
	if err != nil {
		return nil, fmt.Errorf("failed to get ResourceSync %s: %w", name, err)
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("ResourceSync %s not found (status: %d)", name, resp.StatusCode())
	}
	return resp.JSON200, nil
}

func (h *Harness) WaitForResourceSyncCondition(name string, condType v1beta1.ConditionType, expectedStatus v1beta1.ConditionStatus, timeout, polling time.Duration) {
	Eventually(func() (v1beta1.ConditionStatus, error) {
		rs, err := h.GetResourceSync(name)
		if err != nil {
			return "", err
		}
		if rs.Status == nil || rs.Status.Conditions == nil {
			return "", fmt.Errorf("ResourceSync %s has no status conditions", name)
		}
		cond := v1beta1.FindStatusCondition(rs.Status.Conditions, condType)
		if cond == nil {
			return "", fmt.Errorf("condition %s not found on ResourceSync %s", condType, name)
		}
		return cond.Status, nil
	}, timeout, polling).Should(Equal(expectedStatus),
		fmt.Sprintf("Expected ResourceSync %s condition %s to be %s", name, condType, expectedStatus))
}

func (h *Harness) GetResourceSyncConditionMessage(name string, condType v1beta1.ConditionType) (string, error) {
	rs, err := h.GetResourceSync(name)
	if err != nil {
		return "", err
	}
	if rs.Status == nil || rs.Status.Conditions == nil {
		return "", fmt.Errorf("ResourceSync %s has no status conditions", name)
	}
	cond := v1beta1.FindStatusCondition(rs.Status.Conditions, condType)
	if cond == nil {
		return "", fmt.Errorf("condition %s not found on ResourceSync %s", condType, name)
	}
	return cond.Message, nil
}
