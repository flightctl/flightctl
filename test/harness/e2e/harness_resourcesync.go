package e2e

import (
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

func (h *Harness) GetResourceSync(name string) (*v1beta1.ResourceSync, error) {
	resp, err := h.Client.GetResourceSyncWithResponse(h.Context, name)
	if err != nil {
		return nil, fmt.Errorf("failed to get ResourceSync %s: %w", name, err)
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("unexpected response status getting ResourceSync %s: %d", name, resp.StatusCode())
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

// CreateResourceSync creates a ResourceSync resource
func (h *Harness) CreateResourceSync(name, repoName string, spec v1beta1.ResourceSyncSpec) error {
	if name == "" {
		return fmt.Errorf("ResourceSync name cannot be empty")
	}
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}

	// Set the repository name in the spec if not already set
	if spec.Repository == "" {
		spec.Repository = repoName
	}

	resourceSync := v1beta1.ResourceSync{
		ApiVersion: v1beta1.ResourceSyncAPIVersion,
		Kind:       v1beta1.ResourceSyncKind,
		Metadata: v1beta1.ObjectMeta{
			Name: &name,
		},
		Spec: spec,
	}

	_, err := h.Client.CreateResourceSyncWithResponse(h.Context, resourceSync)
	if err != nil {
		return fmt.Errorf("failed to create ResourceSync: %w", err)
	}

	logrus.Infof("Created ResourceSync %s pointing to repository %s", name, repoName)
	return nil
}

func (h *Harness) CreateResourceSyncForRepo(resourceSyncName, repoName, branchName string) error {
	resourceSyncSpec := v1beta1.ResourceSyncSpec{
		Repository:     repoName,
		TargetRevision: branchName,
		Path:           "/",
	}
	return h.CreateResourceSync(resourceSyncName, repoName, resourceSyncSpec)
}

// ReplaceResourceSync replaces an existing ResourceSync resource
func (h *Harness) ReplaceResourceSync(name, repoName string, spec v1beta1.ResourceSyncSpec) error {
	if name == "" {
		return fmt.Errorf("ResourceSync name cannot be empty")
	}
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}

	// Set the repository name in the spec if not already set
	if spec.Repository == "" {
		spec.Repository = repoName
	}

	resourceSync := v1beta1.ResourceSync{
		ApiVersion: v1beta1.ResourceSyncAPIVersion,
		Kind:       v1beta1.ResourceSyncKind,
		Metadata: v1beta1.ObjectMeta{
			Name: &name,
		},
		Spec: spec,
	}

	_, err := h.Client.ReplaceResourceSyncWithResponse(h.Context, name, resourceSync)
	if err != nil {
		return fmt.Errorf("failed to replace ResourceSync: %w", err)
	}

	logrus.Infof("Replaced ResourceSync %s pointing to repository %s", name, repoName)
	return nil
}

// DeleteResourceSync deletes the specified ResourceSync
func (h *Harness) DeleteResourceSync(name string) error {
	if name == "" {
		return fmt.Errorf("ResourceSync name cannot be empty")
	}

	_, err := h.Client.DeleteResourceSync(h.Context, name)
	if err != nil {
		return fmt.Errorf("failed to delete ResourceSync: %w", err)
	}

	logrus.Infof("Deleted ResourceSync %s", name)
	return nil
}
