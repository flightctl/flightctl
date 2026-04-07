package k8s

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

const (
	tpmCACertsConfigMapName = "tpm-ca-certs"
	tpmCACertsMountPath     = "/etc/flightctl/tpm-cas"
	tpmCAVolumeName         = "tpm-ca-certs"
	deploymentRolloutWait   = 5 * time.Minute
)

// TPMProvider implements infra.TPMProvider for Kubernetes environments.
type TPMProvider struct {
	client    kubernetes.Interface
	infraP    *InfraProvider
	lifecycle *ServiceLifecycleProvider
}

// NewTPMProvider creates a new K8s TPMProvider.
func NewTPMProvider(client kubernetes.Interface, infraP *InfraProvider, lifecycle *ServiceLifecycleProvider) *TPMProvider {
	return &TPMProvider{
		client:    client,
		infraP:    infraP,
		lifecycle: lifecycle,
	}
}

// InjectCerts configures TPM CA certificates for the API server.
// It skips the deployment restart if all certs, config, and volume/mount are already in place.
func (p *TPMProvider) InjectCerts(ctx context.Context, certs map[string][]byte) error {
	namespace := p.infraP.GetExternalNamespace()

	cmChanged, err := p.ensureTPMCertsConfigMap(ctx, namespace, certs)
	if err != nil {
		return fmt.Errorf("failed to create/update TPM certs ConfigMap: %w", err)
	}

	certPaths := make([]string, 0, len(certs))
	for name := range certs {
		certPaths = append(certPaths, filepath.Join(tpmCACertsMountPath, name))
	}
	sort.Strings(certPaths)

	configChanged, err := p.updateAPIConfigTPMPaths(ctx, namespace, certPaths)
	if err != nil {
		return fmt.Errorf("failed to update API config with TPM CA paths: %w", err)
	}

	volumeExists, err := p.apiDeploymentHasTPMVolume(ctx, namespace)
	if err != nil {
		return fmt.Errorf("failed to check API deployment for TPM volume: %w", err)
	}

	if !cmChanged && !configChanged && volumeExists {
		logrus.Info("TPM certs already configured, skipping deployment restart")
		return nil
	}

	if err := p.patchAndRestartAPIDeployment(ctx, namespace); err != nil {
		return fmt.Errorf("failed to patch/restart API deployment: %w", err)
	}

	return nil
}

func (p *TPMProvider) ensureTPMCertsConfigMap(ctx context.Context, namespace string, certs map[string][]byte) (bool, error) {
	cmClient := p.client.CoreV1().ConfigMaps(namespace)

	data := make(map[string]string, len(certs))
	for name, certData := range certs {
		data[name] = string(certData)
	}

	existing, err := cmClient.Get(ctx, tpmCACertsConfigMapName, metav1.GetOptions{})
	if err != nil {
		if !k8serrors.IsNotFound(err) {
			return false, fmt.Errorf("failed to get ConfigMap %s: %w", tpmCACertsConfigMapName, err)
		}
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      tpmCACertsConfigMapName,
				Namespace: namespace,
			},
			Data: data,
		}
		_, err = cmClient.Create(ctx, cm, metav1.CreateOptions{})
		if err != nil {
			return false, fmt.Errorf("failed to create ConfigMap %s: %w", tpmCACertsConfigMapName, err)
		}
		logrus.Infof("Created TPM CA certs ConfigMap %s/%s with %d certs", namespace, tpmCACertsConfigMapName, len(certs))
		return true, nil
	}

	if maps.Equal(existing.Data, data) {
		logrus.Infof("TPM CA certs ConfigMap %s/%s already up to date", namespace, tpmCACertsConfigMapName)
		return false, nil
	}

	existing.Data = data
	_, err = cmClient.Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to update ConfigMap %s: %w", tpmCACertsConfigMapName, err)
	}
	logrus.Infof("Updated TPM CA certs ConfigMap %s/%s with %d certs", namespace, tpmCACertsConfigMapName, len(certs))
	return true, nil
}

func (p *TPMProvider) updateAPIConfigTPMPaths(ctx context.Context, namespace string, certPaths []string) (bool, error) {
	cmClient := p.client.CoreV1().ConfigMaps(namespace)

	apiConfigName := k8sServiceRegistry[infra.ServiceAPI].ConfigMapName
	cm, err := cmClient.Get(ctx, apiConfigName, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to get ConfigMap %s: %w", apiConfigName, err)
	}

	configYAML, ok := cm.Data["config.yaml"]
	if !ok {
		return false, fmt.Errorf("config.yaml not found in ConfigMap %s", apiConfigName)
	}

	var configMap map[string]interface{}
	if err := yaml.Unmarshal([]byte(configYAML), &configMap); err != nil {
		return false, fmt.Errorf("failed to parse API config: %w", err)
	}

	service, ok := configMap["service"].(map[string]interface{})
	if !ok {
		return false, fmt.Errorf("service section not found in API config")
	}

	if existingPaths, ok := service["tpmCAPaths"].([]interface{}); ok {
		existing := make([]string, 0, len(existingPaths))
		for _, p := range existingPaths {
			if s, ok := p.(string); ok {
				existing = append(existing, s)
			}
		}
		sort.Strings(existing)
		if slices.Equal(existing, certPaths) {
			logrus.Info("TPM CA paths in API config already up to date")
			return false, nil
		}
	}

	service["tpmCAPaths"] = certPaths
	configMap["service"] = service

	updatedYAML, err := yaml.Marshal(configMap)
	if err != nil {
		return false, fmt.Errorf("failed to marshal updated API config: %w", err)
	}

	cm.Data["config.yaml"] = string(updatedYAML)
	_, err = cmClient.Update(ctx, cm, metav1.UpdateOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to update ConfigMap %s: %w", apiConfigName, err)
	}
	logrus.Infof("Updated API config with %d TPM CA paths", len(certPaths))
	return true, nil
}

func (p *TPMProvider) apiDeploymentHasTPMVolume(ctx context.Context, namespace string) (bool, error) {
	deplClient := p.client.AppsV1().Deployments(namespace)
	apiDeploymentName := k8sServiceRegistry[infra.ServiceAPI].ServiceName

	depl, err := deplClient.Get(ctx, apiDeploymentName, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to get deployment %s: %w", apiDeploymentName, err)
	}

	hasVolume := false
	for _, v := range depl.Spec.Template.Spec.Volumes {
		if v.Name == tpmCAVolumeName {
			hasVolume = true
			break
		}
	}
	if !hasVolume || len(depl.Spec.Template.Spec.Containers) == 0 {
		return false, nil
	}

	for _, m := range depl.Spec.Template.Spec.Containers[0].VolumeMounts {
		if m.Name == tpmCAVolumeName {
			return true, nil
		}
	}
	return false, nil
}

func (p *TPMProvider) patchAndRestartAPIDeployment(ctx context.Context, namespace string) error {
	deplClient := p.client.AppsV1().Deployments(namespace)
	apiDeploymentName := k8sServiceRegistry[infra.ServiceAPI].ServiceName

	depl, err := deplClient.Get(ctx, apiDeploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get deployment %s: %w", apiDeploymentName, err)
	}

	hasVolume := false
	for _, v := range depl.Spec.Template.Spec.Volumes {
		if v.Name == tpmCAVolumeName {
			hasVolume = true
			break
		}
	}

	if !hasVolume {
		depl.Spec.Template.Spec.Volumes = append(depl.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: tpmCAVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: tpmCACertsConfigMapName,
					},
				},
			},
		})
	}

	if len(depl.Spec.Template.Spec.Containers) > 0 {
		hasMount := false
		for _, m := range depl.Spec.Template.Spec.Containers[0].VolumeMounts {
			if m.Name == tpmCAVolumeName {
				hasMount = true
				break
			}
		}
		if !hasMount {
			depl.Spec.Template.Spec.Containers[0].VolumeMounts = append(
				depl.Spec.Template.Spec.Containers[0].VolumeMounts,
				corev1.VolumeMount{
					Name:      tpmCAVolumeName,
					MountPath: tpmCACertsMountPath,
					ReadOnly:  true,
				},
			)
		}
	}

	if depl.Spec.Template.Annotations == nil {
		depl.Spec.Template.Annotations = make(map[string]string)
	}
	depl.Spec.Template.Annotations["flightctl.io/restartedAt"] = time.Now().Format(time.RFC3339)

	_, err = deplClient.Update(ctx, depl, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update deployment %s: %w", apiDeploymentName, err)
	}

	logrus.Info("Waiting for API deployment rollout...")
	return p.waitForDeploymentRollout(ctx, namespace, apiDeploymentName, deploymentRolloutWait)
}

func (p *TPMProvider) waitForDeploymentRollout(ctx context.Context, namespace, name string, timeout time.Duration) error {
	deplClient := p.client.AppsV1().Deployments(namespace)
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		depl, err := deplClient.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get deployment %s: %w", name, err)
		}

		replicas := int32(1)
		if depl.Spec.Replicas != nil {
			replicas = *depl.Spec.Replicas
		}

		if depl.Status.UpdatedReplicas == replicas &&
			depl.Status.ReadyReplicas == replicas &&
			depl.Status.AvailableReplicas == replicas &&
			depl.Status.ObservedGeneration >= depl.Generation {
			logrus.Info("API deployment rollout completed")
			return nil
		}

		time.Sleep(5 * time.Second)
	}

	return fmt.Errorf("deployment %s rollout timed out after %v", name, timeout)
}

// CleanupCerts removes TPM CA certificates from the API server configuration.
func (p *TPMProvider) CleanupCerts(ctx context.Context) error {
	namespace := p.infraP.GetExternalNamespace()

	if err := p.removeAPIConfigTPMPaths(ctx, namespace); err != nil {
		logrus.Warnf("Failed to remove TPM paths from API config: %v", err)
	}

	if err := p.removeVolumeAndRestartAPIDeployment(ctx, namespace); err != nil {
		return fmt.Errorf("failed to remove volume and restart API deployment: %w", err)
	}

	if err := p.deleteTPMCertsConfigMap(ctx, namespace); err != nil {
		logrus.Warnf("Failed to delete TPM certs ConfigMap: %v", err)
	}

	return nil
}

func (p *TPMProvider) deleteTPMCertsConfigMap(ctx context.Context, namespace string) error {
	cmClient := p.client.CoreV1().ConfigMaps(namespace)
	err := cmClient.Delete(ctx, tpmCACertsConfigMapName, metav1.DeleteOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to delete ConfigMap %s: %w", tpmCACertsConfigMapName, err)
	}
	logrus.Infof("Deleted TPM CA certs ConfigMap %s/%s", namespace, tpmCACertsConfigMapName)
	return nil
}

func (p *TPMProvider) removeAPIConfigTPMPaths(ctx context.Context, namespace string) error {
	cmClient := p.client.CoreV1().ConfigMaps(namespace)

	apiConfigName := k8sServiceRegistry[infra.ServiceAPI].ConfigMapName
	cm, err := cmClient.Get(ctx, apiConfigName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get ConfigMap %s: %w", apiConfigName, err)
	}

	configYAML, ok := cm.Data["config.yaml"]
	if !ok {
		return nil
	}

	var configMap map[string]any
	if err := yaml.Unmarshal([]byte(configYAML), &configMap); err != nil {
		return fmt.Errorf("failed to parse API config: %w", err)
	}

	service, ok := configMap["service"].(map[string]any)
	if !ok {
		return nil
	}

	delete(service, "tpmCAPaths")
	configMap["service"] = service

	updatedYAML, err := yaml.Marshal(configMap)
	if err != nil {
		return fmt.Errorf("failed to marshal updated API config: %w", err)
	}

	cm.Data["config.yaml"] = string(updatedYAML)
	_, err = cmClient.Update(ctx, cm, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update ConfigMap %s: %w", apiConfigName, err)
	}
	logrus.Info("Removed tpmCAPaths from API config")
	return nil
}

func (p *TPMProvider) removeVolumeAndRestartAPIDeployment(ctx context.Context, namespace string) error {
	deplClient := p.client.AppsV1().Deployments(namespace)
	apiDeploymentName := k8sServiceRegistry[infra.ServiceAPI].ServiceName

	depl, err := deplClient.Get(ctx, apiDeploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get deployment %s: %w", apiDeploymentName, err)
	}

	newVolumes := make([]corev1.Volume, 0, len(depl.Spec.Template.Spec.Volumes))
	for _, v := range depl.Spec.Template.Spec.Volumes {
		if v.Name != tpmCAVolumeName {
			newVolumes = append(newVolumes, v)
		}
	}
	depl.Spec.Template.Spec.Volumes = newVolumes

	if len(depl.Spec.Template.Spec.Containers) > 0 {
		newMounts := make([]corev1.VolumeMount, 0, len(depl.Spec.Template.Spec.Containers[0].VolumeMounts))
		for _, m := range depl.Spec.Template.Spec.Containers[0].VolumeMounts {
			if m.Name != tpmCAVolumeName {
				newMounts = append(newMounts, m)
			}
		}
		depl.Spec.Template.Spec.Containers[0].VolumeMounts = newMounts
	}

	if depl.Spec.Template.Annotations == nil {
		depl.Spec.Template.Annotations = make(map[string]string)
	}
	depl.Spec.Template.Annotations["flightctl.io/restartedAt"] = time.Now().Format(time.RFC3339)

	_, err = deplClient.Update(ctx, depl, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update deployment %s: %w", apiDeploymentName, err)
	}

	logrus.Info("Removed TPM volume from API deployment, waiting for rollout...")
	return p.waitForDeploymentRollout(ctx, namespace, apiDeploymentName, deploymentRolloutWait)
}
