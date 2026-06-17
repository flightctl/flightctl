package e2e

import (
	"encoding/json"
	"fmt"
)

// K8sSecretAction specifies the operation to perform on a Kubernetes secret.
type K8sSecretAction string

const (
	K8sSecretCreate K8sSecretAction = "create"
	K8sSecretDelete K8sSecretAction = "delete"
	K8sSecretPatch  K8sSecretAction = "patch"
)

// ManageK8sSecret performs create, delete, or patch on a Kubernetes secret.
// releaseNamespace is only required for create (it sets the sync label).
// data is required for create and patch.
func (h *Harness) ManageK8sSecret(action K8sSecretAction, namespace, name, releaseNamespace string, data map[string]string) error {
	if namespace == "" || name == "" {
		return fmt.Errorf("namespace and name must not be empty")
	}

	switch action {
	case K8sSecretCreate:
		if releaseNamespace == "" {
			return fmt.Errorf("releaseNamespace must not be empty for create")
		}
		if len(data) == 0 {
			return fmt.Errorf("data must not be empty for create")
		}
		manifest := map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]string{
					fmt.Sprintf("flightctl.io/sync-%s", releaseNamespace): "true",
				},
			},
			"stringData": data,
		}
		manifestBytes, err := json.Marshal(manifest)
		if err != nil {
			return fmt.Errorf("failed to marshal secret manifest %s/%s: %w", namespace, name, err)
		}
		if _, err := h.SHWithStdin(string(manifestBytes), "kubectl", true, "apply", "-f", "-"); err != nil {
			return fmt.Errorf("failed to create secret %s/%s: %w", namespace, name, err)
		}

	case K8sSecretDelete:
		if _, err := h.SH("kubectl", "delete", "secret", name, "-n", namespace, "--ignore-not-found"); err != nil {
			return fmt.Errorf("failed to delete secret %s/%s: %w", namespace, name, err)
		}

	case K8sSecretPatch:
		if len(data) == 0 {
			return fmt.Errorf("data must not be empty for patch")
		}
		if releaseNamespace == "" {
			return fmt.Errorf("releaseNamespace must not be empty for patch")
		}
		manifest := map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]string{
					fmt.Sprintf("flightctl.io/sync-%s", releaseNamespace): "true",
				},
			},
			"stringData": data,
		}
		manifestBytes, err := json.Marshal(manifest)
		if err != nil {
			return fmt.Errorf("failed to marshal secret manifest %s/%s: %w", namespace, name, err)
		}
		if _, err := h.SHWithStdin(string(manifestBytes), "kubectl", true, "apply", "-f", "-"); err != nil {
			return fmt.Errorf("failed to patch secret %s/%s: %w", namespace, name, err)
		}

	default:
		return fmt.Errorf("unknown action: %s", action)
	}

	return nil
}
