package k8s

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	e2eDeltaWorkerRegistriesCM     = "e2e-delta-worker-registries"
	e2eDeltaWorkerRegistriesVolume = "e2e-delta-worker-registries"
	e2eDeltaWorkerRegistriesMount  = "/etc/containers/registries.conf.d"
	deltaWorkerContainerName       = "flightctl-delta-worker"
	workerContainerName            = "flightctl-worker"
)

func (p *InfraProvider) ApplyDeltaWorkerRegistryRemap(registryURL string) error {
	ctx := context.Background()
	remap, insecure := infra.DeltaWorkerRegistryRemapFiles(registryURL)
	targets := []struct {
		svc       infra.ServiceName
		container string
	}{
		{infra.ServiceDeltaWorker, deltaWorkerContainerName},
		{infra.ServiceWorker, workerContainerName},
	}
	for _, t := range targets {
		deploymentName, _, ns, err := p.GetServiceNamespaceAndMetadata(t.svc)
		if err != nil {
			return fmt.Errorf("%s remap: %w", t.container, err)
		}
		if err := p.upsertDeltaWorkerRegistriesConfigMap(ctx, ns, remap, insecure); err != nil {
			return err
		}
		if err := p.mountDeltaWorkerRegistries(ctx, ns, deploymentName, t.container); err != nil {
			return err
		}
	}
	return nil
}

func (p *InfraProvider) upsertDeltaWorkerRegistriesConfigMap(ctx context.Context, ns, remap, insecure string) error {
	cmClient := p.client.CoreV1().ConfigMaps(ns)
	data := map[string]string{
		"flightctl-remap.conf": remap,
		"flightctl-e2e.conf":   insecure,
	}
	existing, err := cmClient.Get(ctx, e2eDeltaWorkerRegistriesCM, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      e2eDeltaWorkerRegistriesCM,
				Namespace: ns,
			},
			Data: data,
		}
		if _, err := cmClient.Create(ctx, cm, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create ConfigMap %s/%s: %w", ns, e2eDeltaWorkerRegistriesCM, err)
		}
		logrus.Infof("K8s: created ConfigMap %s/%s for delta-worker registry remap", ns, e2eDeltaWorkerRegistriesCM)
		return nil
	}
	if err != nil {
		return fmt.Errorf("get ConfigMap %s/%s: %w", ns, e2eDeltaWorkerRegistriesCM, err)
	}
	existing.Data = data
	if _, err := cmClient.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update ConfigMap %s/%s: %w", ns, e2eDeltaWorkerRegistriesCM, err)
	}
	logrus.Infof("K8s: updated ConfigMap %s/%s for delta-worker registry remap", ns, e2eDeltaWorkerRegistriesCM)
	return nil
}

func (p *InfraProvider) mountDeltaWorkerRegistries(ctx context.Context, ns, deploymentName, containerName string) error {
	deplClient := p.client.AppsV1().Deployments(ns)
	depl, err := deplClient.Get(ctx, deploymentName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		logrus.Infof("K8s: deployment %s/%s not found; skip volume mount", ns, deploymentName)
		return nil
	}
	if err != nil {
		return fmt.Errorf("get deployment %s/%s: %w", ns, deploymentName, err)
	}

	hasVolume := false
	for _, v := range depl.Spec.Template.Spec.Volumes {
		if v.Name == e2eDeltaWorkerRegistriesVolume {
			hasVolume = true
			break
		}
	}
	if !hasVolume {
		mode := int32(0444)
		depl.Spec.Template.Spec.Volumes = append(depl.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: e2eDeltaWorkerRegistriesVolume,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: e2eDeltaWorkerRegistriesCM},
					DefaultMode:          &mode,
				},
			},
		})
	}

	idx := -1
	for i, c := range depl.Spec.Template.Spec.Containers {
		if c.Name == containerName {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("container %s not found in deployment %s/%s", containerName, ns, deploymentName)
	}

	hasMount := false
	for _, m := range depl.Spec.Template.Spec.Containers[idx].VolumeMounts {
		if m.Name == e2eDeltaWorkerRegistriesVolume {
			hasMount = true
			break
		}
	}
	if !hasMount {
		depl.Spec.Template.Spec.Containers[idx].VolumeMounts = append(
			depl.Spec.Template.Spec.Containers[idx].VolumeMounts,
			corev1.VolumeMount{
				Name:      e2eDeltaWorkerRegistriesVolume,
				MountPath: e2eDeltaWorkerRegistriesMount,
				ReadOnly:  true,
			},
		)
	}

	if hasVolume && hasMount {
		return nil
	}
	if _, err := deplClient.Update(ctx, depl, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update deployment %s/%s: %w", ns, deploymentName, err)
	}
	logrus.Infof("K8s: mounted e2e registries ConfigMap on %s/%s", ns, deploymentName)
	return nil
}
