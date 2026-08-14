package k8s

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/sirupsen/logrus"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// dbQueryJobTimeout is the maximum time to wait for the psql Job to complete.
	dbQueryJobTimeout = 3 * time.Minute

	// dbQueryJobPollInterval is how often to poll the Job status.
	dbQueryJobPollInterval = 2 * time.Second

	// dbAppSecretName is the default K8s Secret name holding the DB application user credentials.
	// Created by the Helm chart for both builtin and external DB types.
	dbAppSecretName = "flightctl-db-app-secret"

	// dbAppSecretUserKey is the key for the database username in dbAppSecretName.
	dbAppSecretUserKey = "user"

	// dbAppSecretPasswordKey is the key for the database password in dbAppSecretName.
	dbAppSecretPasswordKey = "userPassword"

	// dbMigrationJobLabel is the label applied by Helm to the db-migration Job.
	dbMigrationJobLabel = "flightctl.service=flightctl-db-migration"
)

// getDBConnectionParams reads DB connection parameters from the API ConfigMap and
// the DB application user Secret in the internal namespace.
func (p *InfraProvider) getDBConnectionParams() (infra.DBConnectionParams, error) {
	raw, err := p.GetServiceConfig(infra.ServiceAPI)
	if err != nil {
		return infra.DBConnectionParams{}, fmt.Errorf("getDBConnectionParams: read API service config: %w", err)
	}

	params, err := infra.ParseDBParamsFromAPIConfig(raw)
	if err != nil {
		return infra.DBConnectionParams{}, fmt.Errorf("getDBConnectionParams: %w", err)
	}

	// Discover the actual secret name and namespace from the API Deployment so we
	// work with both builtin and external DB deployments. The namespace is authoritative
	// (taken from the Deployment, not inferred from the name) so the Job is created in
	// the same namespace as the Secret and SecretKeyRef resolves without cross-ns access.
	secretName, secretNS := p.resolveDBAppSecret()

	if params.User == "" {
		user, err := p.getSecretValue(secretNS, secretName, dbAppSecretUserKey)
		if err != nil {
			return infra.DBConnectionParams{}, fmt.Errorf("getDBConnectionParams: read DB user from secret: %w", err)
		}
		params.User = user
	}

	// Password is always read from the Secret (not stored in config).
	password, err := p.getSecretValue(secretNS, secretName, dbAppSecretPasswordKey)
	if err != nil {
		return infra.DBConnectionParams{}, fmt.Errorf("getDBConnectionParams: read DB password from secret: %w", err)
	}
	params.Password = password

	return params, nil
}

// resolveDBAppSecret discovers the DB application user Secret name and namespace
// from the API Deployment's env vars (the DB_PASSWORD secretKeyRef.name). This
// handles both builtin ("flightctl-db-app-secret" in internalNamespace) and
// external DB deployments where applicationUserSecretName may be set to a custom
// value in a different namespace. The Job must be created in the same namespace
// as the Secret so that the SecretKeyRef resolves without cross-namespace access.
// Falls back to the chart default (name in internalNamespace) if the Deployment
// cannot be read.
func (p *InfraProvider) resolveDBAppSecret() (secretName, secretNamespace string) {
	deploymentName, _, ns, err := p.GetServiceNamespaceAndMetadata(infra.ServiceAPI)
	if err != nil {
		return dbAppSecretName, p.internalNamespace
	}
	deployment, err := p.client.AppsV1().Deployments(ns).Get(
		context.Background(), deploymentName, metav1.GetOptions{},
	)
	if err != nil {
		return dbAppSecretName, p.internalNamespace
	}
	for _, container := range deployment.Spec.Template.Spec.Containers {
		for _, env := range container.Env {
			if env.Name == "DB_PASSWORD" && env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
				// The Secret lives in the same namespace as the API Deployment.
				return env.ValueFrom.SecretKeyRef.Name, ns
			}
		}
	}
	return dbAppSecretName, p.internalNamespace
}

// getSecretValue reads a single key from a Secret in the given namespace.
// Returns the decoded string value. Separate from GetSecretValue because that one
// uses kubectl via exec; this one uses the Kubernetes client directly.
func (p *InfraProvider) getSecretValue(namespace, secretName, key string) (string, error) {
	secret, err := p.client.CoreV1().Secrets(namespace).Get(
		context.Background(), secretName, metav1.GetOptions{},
	)
	if err != nil {
		return "", fmt.Errorf("get secret %s/%s: %w", namespace, secretName, err)
	}
	val, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %s/%s", key, namespace, secretName)
	}
	return string(val), nil
}

// dbMigrationJobSpec holds the image, SSL env vars, and SSL volumes copied from the
// existing flightctl-db-migration Job so the query Job can connect to the DB with
// identical credentials and TLS configuration.
type dbMigrationJobSpec struct {
	// image is the container image (guaranteed cached on all nodes).
	image string
	// imagePullSecrets are copied from the migration Job so restricted registries work.
	imagePullSecrets []corev1.LocalObjectReference
	// sslEnv contains PGSSLMODE/PGSSLCERT/PGSSLKEY/PGSSLROOTCERT env vars (may be empty).
	sslEnv []corev1.EnvVar
	// sslVolumes are the projected SSL cert volumes from tlsConfigMapName/tlsSecretName.
	sslVolumes []corev1.Volume
	// sslVolumeMounts are the corresponding mount specs.
	sslVolumeMounts []corev1.VolumeMount
}

// resolveMigrationJobSpec looks up the most recent flightctl-db-migration Job and
// returns the image, SSL env vars, and SSL volumes from its run-migrations container.
// The migration Job always succeeds before any service starts, so it is guaranteed
// to exist and its image is guaranteed to be cached on all cluster nodes.
// Falls back to the API Deployment's db-setup init container image when the migration
// Job has been TTL-deleted (e.g., by Helm's before-hook-creation delete policy).
func (p *InfraProvider) resolveMigrationJobSpec() dbMigrationJobSpec {
	// Search both namespaces — the migration Job is always created in internalNamespace
	// but we check externalNamespace too for safety.
	for _, ns := range []string{p.internalNamespace, p.externalNamespace} {
		jobs, err := p.client.BatchV1().Jobs(ns).List(
			context.Background(),
			metav1.ListOptions{LabelSelector: dbMigrationJobLabel},
		)
		if err != nil || len(jobs.Items) == 0 {
			continue
		}
		job := &jobs.Items[0]
		spec := dbMigrationJobSpec{
			imagePullSecrets: job.Spec.Template.Spec.ImagePullSecrets,
		}

		// Extract image and SSL env from the run-migrations container.
		for _, c := range job.Spec.Template.Spec.Containers {
			if spec.image == "" && c.Image != "" {
				spec.image = c.Image
			}
			for _, env := range c.Env {
				switch env.Name {
				case "PGSSLMODE", "PGSSLCERT", "PGSSLKEY", "PGSSLROOTCERT":
					spec.sslEnv = append(spec.sslEnv, env)
				}
			}
		}

		// Copy the postgres-ssl-certs volume and its mount (present only when
		// db.external.tlsConfigMapName or db.external.tlsSecretName is set).
		// cert/CA items get mode 0444; client-key.pem stays at 0400 (the init container
		// copies it to a writable emptyDir and chmods it to 0600 — psql rejects keys
		// with world-readable permissions).
		for _, v := range job.Spec.Template.Spec.Volumes {
			if v.Name == "postgres-ssl-certs" {
				v = makeSSLVolumeCertReadable(v)
				spec.sslVolumes = append(spec.sslVolumes, v)
			}
		}
		for _, c := range job.Spec.Template.Spec.Containers {
			for _, vm := range c.VolumeMounts {
				if vm.Name == "postgres-ssl-certs" {
					spec.sslVolumeMounts = append(spec.sslVolumeMounts, vm)
					break
				}
			}
		}

		if spec.image != "" {
			return spec
		}
	}

	// Migration Job was TTL-deleted (Helm before-hook-creation policy deletes it on
	// upgrade). Fall back to the db-setup image and SSL config from the API Deployment's
	// init containers — that image has psql, is guaranteed to be cached on all nodes
	// because the API pod is running, and includes imagePullSecrets and SSL volumes so
	// the query Job can connect to an external DB with the correct TLS settings.
	apiSpec := p.resolveDBSetupImageFromAPI()
	if apiSpec.image != "" {
		logrus.Warnf("resolveMigrationJobSpec: migration Job not found; using db-setup image from API Deployment: %s", apiSpec.image)
		return apiSpec
	}

	logrus.Warnf("resolveMigrationJobSpec: neither migration Job nor API init container found; using hardcoded fallback")
	// Still fetch imagePullSecrets and SSL config from the API Deployment so the pull
	// succeeds in restricted registries and TLS config is correct for external DB.
	fallbackSpec := p.resolveDBSetupImageFromAPI()
	fallbackSpec.image = "quay.io/sclorg/postgresql-16-c9s:20250214"
	return fallbackSpec
}

// resolveDBSetupImageFromAPI reads the image, imagePullSecrets, and SSL volumes
// from the API Deployment. The Helm chart injects a "wait-for-database-app" init
// container that uses the same flightctl-db-setup image as the migration Job (which
// has psql). Since the API pod is always running, the image is guaranteed to be
// present on every cluster node, and the imagePullSecrets are correct for the registry.
//
// When the migration Job has been deleted by Helm's before-hook-creation policy,
// this function also extracts the postgres-ssl-certs volume and mount from the API
// Deployment so the query Job can connect to an external DB with the correct TLS
// configuration. The init container's DB_SSL_* env vars are translated to the
// psql-native PGSSL* names that psql understands.
//
// The named container is checked first to avoid picking up a service-mesh proxy
// init container that may be injected before it by the platform (e.g. Istio/OSSM).
// If the named container is not found, the first non-empty image is returned as a
// best-effort fallback.
func (p *InfraProvider) resolveDBSetupImageFromAPI() dbMigrationJobSpec {
	deploymentName, _, ns, err := p.GetServiceNamespaceAndMetadata(infra.ServiceAPI)
	if err != nil {
		return dbMigrationJobSpec{}
	}
	deployment, err := p.client.AppsV1().Deployments(ns).Get(
		context.Background(), deploymentName, metav1.GetOptions{},
	)
	if err != nil {
		return dbMigrationJobSpec{}
	}

	spec := dbMigrationJobSpec{
		imagePullSecrets: deployment.Spec.Template.Spec.ImagePullSecrets,
	}

	// Copy the postgres-ssl-certs volume from the API pod spec (present when
	// db.external.tlsConfigMapName or db.external.tlsSecretName is set).
	// cert/CA items get mode 0444; client-key.pem stays at 0400 (the init container
	// copies it to a writable emptyDir and chmods it to 0600 — psql rejects keys
	// with world-readable permissions).
	for _, v := range deployment.Spec.Template.Spec.Volumes {
		if v.Name == "postgres-ssl-certs" {
			v = makeSSLVolumeCertReadable(v)
			spec.sslVolumes = append(spec.sslVolumes, v)
		}
	}

	// Find the db-setup init container, preferring the named one over mesh-injected ones.
	var dbSetupContainer *corev1.Container
	for i := range deployment.Spec.Template.Spec.InitContainers {
		c := &deployment.Spec.Template.Spec.InitContainers[i]
		if c.Name == "wait-for-database-app" && c.Image != "" {
			dbSetupContainer = c
			break
		}
	}
	if dbSetupContainer == nil {
		// Best-effort fallback: first init container with a non-empty image.
		for i := range deployment.Spec.Template.Spec.InitContainers {
			if deployment.Spec.Template.Spec.InitContainers[i].Image != "" {
				dbSetupContainer = &deployment.Spec.Template.Spec.InitContainers[i]
				break
			}
		}
	}
	if dbSetupContainer == nil {
		return spec
	}

	spec.image = dbSetupContainer.Image

	// Copy the postgres-ssl-certs volume mount from the init container.
	for _, vm := range dbSetupContainer.VolumeMounts {
		if vm.Name == "postgres-ssl-certs" {
			spec.sslVolumeMounts = append(spec.sslVolumeMounts, vm)
			break
		}
	}

	// The Helm chart sets DB_SSL_* env vars on the init container (wrapper-script names).
	// Translate them to the psql-native PGSSL* names that psql reads directly.
	// DB_SSL_MODE → PGSSLMODE, DB_SSL_ROOT_CERT → PGSSLROOTCERT, etc.
	dbSSLToPostgres := map[string]string{
		"DB_SSL_MODE":      "PGSSLMODE",
		"DB_SSL_CERT":      "PGSSLCERT",
		"DB_SSL_KEY":       "PGSSLKEY",
		"DB_SSL_ROOT_CERT": "PGSSLROOTCERT",
	}
	for _, env := range dbSetupContainer.Env {
		if pgName, ok := dbSSLToPostgres[env.Name]; ok {
			spec.sslEnv = append(spec.sslEnv, corev1.EnvVar{
				Name:  pgName,
				Value: env.Value,
			})
		}
	}

	return spec
}

// queryDBViaJob executes a SQL query by spawning a short-lived batch/v1 Job in the
// internal namespace. The Job runs psql inside the cluster where the DB is reachable,
// collects output from the pod logs, then deletes the Job.
func (p *InfraProvider) queryDBViaJob(sql string) (string, error) {
	params, err := p.getDBConnectionParams()
	if err != nil {
		return "", fmt.Errorf("queryDBViaJob: get DB connection params: %w", err)
	}

	// The Job is always created in internalNamespace so that projected SSL cert volumes
	// (which reference ConfigMaps/Secrets in internalNamespace by name) resolve without
	// cross-namespace access. The password is passed as a plain env var value — it was
	// already retrieved from the Secret by getDBConnectionParams — so no SecretKeyRef
	// namespace coupling is needed.
	ns := p.internalNamespace
	jobName := fmt.Sprintf("flightctl-dbquery-%06x", rand.IntN(0xffffff)) //nolint:gosec // G404: job name only, not security-sensitive

	// Copy image and SSL configuration from the existing migration Job so the query
	// Job connects to the DB with the same TLS settings (PGSSLMODE, client cert/key,
	// CA cert) without duplicating Helm chart logic.
	migSpec := p.resolveMigrationJobSpec()
	logrus.Infof("queryDBViaJob: image=%s sslEnv=%v sslVolumes=%d sslMounts=%d",
		migSpec.image, migSpec.sslEnv, len(migSpec.sslVolumes), len(migSpec.sslVolumeMounts))

	// Build env: start with PGPASSWORD + timeout, then add SSL env vars from the
	// migration Job. If PGSSLKEY is present, redirect it to the writable emptyDir copy
	// (the init container will chmod it 0600 there — psql rejects keys with group/world
	// access, but OCP restricted-v2 SCC assigns a random UID so we cannot rely on the
	// projected Secret file being owned by the running UID).
	const sslKeyEmptyDir = "postgres-ssl-key"
	const sslKeyDest = "/ssl-key/client-key.pem"

	env := []corev1.EnvVar{
		{Name: "PGPASSWORD", Value: params.Password},
		{
			// PGCONNECT_TIMEOUT is the portable way to set a connection timeout;
			// the --connect-timeout CLI flag is not recognised by older psql versions.
			Name:  "PGCONNECT_TIMEOUT",
			Value: "15",
		},
	}
	hasPGSSLKEY := false
	var sslKeySourcePath string
	for _, e := range migSpec.sslEnv {
		if e.Name == "PGSSLKEY" {
			hasPGSSLKEY = true
			sslKeySourcePath = e.Value
			env = append(env, corev1.EnvVar{Name: "PGSSLKEY", Value: sslKeyDest})
		} else {
			env = append(env, e)
		}
	}

	// When a client key is present, add an emptyDir volume and an init container
	// that copies the projected (read-only) key file to the emptyDir and sets 0600.
	// This satisfies both OCP (readable by any UID) and psql (not world-accessible).
	volumes := append([]corev1.Volume(nil), migSpec.sslVolumes...)
	volumeMounts := append([]corev1.VolumeMount(nil), migSpec.sslVolumeMounts...)
	var initContainers []corev1.Container
	if hasPGSSLKEY && sslKeySourcePath != "" {
		volumes = append(volumes, corev1.Volume{
			Name:         sslKeyEmptyDir,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      sslKeyEmptyDir,
			MountPath: "/ssl-key",
			ReadOnly:  false,
		})
		initContainers = []corev1.Container{
			{
				Name:            "fix-ssl-key-perms",
				Image:           migSpec.image,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{"sh", "-c", fmt.Sprintf("cp %s %s && chmod 0600 %s", sslKeySourcePath, sslKeyDest, sslKeyDest)},
				VolumeMounts: append(
					append([]corev1.VolumeMount(nil), migSpec.sslVolumeMounts...),
					corev1.VolumeMount{Name: sslKeyEmptyDir, MountPath: "/ssl-key"},
				),
				SecurityContext: &corev1.SecurityContext{
					RunAsNonRoot:             boolPtr(true),
					AllowPrivilegeEscalation: boolPtr(false),
				},
			},
		}
	}

	ttl := int32(300)
	deadline := int64(dbQueryJobTimeout / time.Second)
	backoffLimit := int32(0)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: ns,
			Labels:    map[string]string{"app": "flightctl-dbquery"},
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: &ttl,
			ActiveDeadlineSeconds:   &deadline,
			BackoffLimit:            &backoffLimit,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy:    corev1.RestartPolicyNever,
					ImagePullSecrets: migSpec.imagePullSecrets,
					InitContainers:   initContainers,
					Volumes:          volumes,
					Containers: []corev1.Container{
						{
							Name:            "psql",
							Image:           migSpec.image,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Command:         []string{"psql"},
							Args: []string{
								"-h", params.Hostname,
								"-p", params.Port,
								"-U", params.User,
								"-d", params.DBName,
								"-t", "-A",
								"-w", // never prompt for password; fail immediately if password required
								"-c", sql,
							},
							Env:          env,
							VolumeMounts: volumeMounts,
							// Do not set RunAsUser — OCP restricted-v2 SCC rejects UIDs outside
							// the namespace-allocated range. Let the platform assign the UID.
							SecurityContext: &corev1.SecurityContext{
								RunAsNonRoot:             boolPtr(true),
								AllowPrivilegeEscalation: boolPtr(false),
							},
						},
					},
				},
			},
		},
	}

	ctx := context.Background()
	_, err = p.client.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("queryDBViaJob: create Job: %w", err)
	}

	defer func() {
		bg := context.Background()
		propagation := metav1.DeletePropagationBackground
		if delErr := p.client.BatchV1().Jobs(ns).Delete(bg, jobName, metav1.DeleteOptions{
			PropagationPolicy: &propagation,
		}); delErr != nil && !apierrors.IsNotFound(delErr) {
			logrus.Warnf("queryDBViaJob: cleanup Job %s: %v", jobName, delErr)
		}
	}()

	if completionErr := waitForJobCompletion(ctx, p, ns, jobName); completionErr != nil {
		// Collect pod logs before the deferred Job deletion removes the pod.
		// Include the pod output in the returned error so it appears in the
		// Ginkgo test report (Ginkgo shows error strings; logrus output is not
		// captured in the Ginkgo output section visible in CI test reports).
		logs, logErr := collectJobPodLogs(ctx, p, ns, jobName)
		if logErr != nil {
			logrus.Warnf("queryDBViaJob: collect pod logs for failed Job %s: %v", jobName, logErr)
			return "", fmt.Errorf("queryDBViaJob: %w", completionErr)
		}
		podOutput := strings.TrimSpace(logs)
		if podOutput != "" {
			return "", fmt.Errorf("queryDBViaJob: %w; pod output: %s", completionErr, podOutput)
		}
		return "", fmt.Errorf("queryDBViaJob: %w", completionErr)
	}

	output, err := collectJobPodLogs(ctx, p, ns, jobName)
	if err != nil {
		return "", fmt.Errorf("queryDBViaJob: collect logs: %w", err)
	}
	return strings.TrimSpace(output), nil
}

// waitForJobCompletion polls the Job status until it succeeds, fails, or times out.
func waitForJobCompletion(ctx context.Context, p *InfraProvider, ns, jobName string) error {
	deadline := time.Now().Add(dbQueryJobTimeout)
	for time.Now().Before(deadline) {
		job, err := p.client.BatchV1().Jobs(ns).Get(ctx, jobName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("Job %s not found: %w", jobName, err)
		}
		if err != nil {
			return fmt.Errorf("poll Job %s: %w", jobName, err)
		}
		for _, cond := range job.Status.Conditions {
			if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
				return nil
			}
			if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
				return fmt.Errorf("Job %s failed: %s", jobName, cond.Message)
			}
		}
		time.Sleep(dbQueryJobPollInterval)
	}
	return fmt.Errorf("Job %s did not complete within %s", jobName, dbQueryJobTimeout)
}

// collectJobPodLogs returns the combined stdout of all pods created by the Job.
func collectJobPodLogs(ctx context.Context, p *InfraProvider, ns, jobName string) (string, error) {
	pods, err := p.client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("job-name=%s", jobName),
	})
	if err != nil {
		return "", fmt.Errorf("list pods for Job %s: %w", jobName, err)
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no pods found for Job %s", jobName)
	}

	// maxLogBytes caps per-pod log reads. One extra byte is read to detect
	// truncation without consuming unbounded memory.
	const maxLogBytes = 64 * 1024

	var sb strings.Builder
	for i := range pods.Items {
		req := p.client.CoreV1().Pods(ns).GetLogs(pods.Items[i].Name, &corev1.PodLogOptions{})
		stream, err := req.Stream(ctx)
		if err != nil {
			return "", fmt.Errorf("stream logs for pod %s: %w", pods.Items[i].Name, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(stream, maxLogBytes+1))
		closeErr := stream.Close()
		if readErr != nil {
			return "", fmt.Errorf("read logs for pod %s: %w", pods.Items[i].Name, readErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close logs for pod %s: %w", pods.Items[i].Name, closeErr)
		}
		if len(data) > maxLogBytes {
			data = append(data[:maxLogBytes], []byte("\n[... truncated at 64 KiB ...]")...)
		}
		sb.Write(data)
	}
	return sb.String(), nil
}

// makeSSLVolumeCertReadable returns a copy of v with all Secret sources in the
// postgres-ssl-certs projected volume having mode 0444. OCP restricted-v2 SCC assigns
// a random UID that does not own the projected files; 0400 causes "permission denied".
// client-key.pem is also set to 0444 here so the init container can copy it to a
// writable emptyDir and chmod it 0600 — psql reads only the emptyDir copy (PGSSLKEY
// is redirected there), so it never sees the 0444 projected key.
func makeSSLVolumeCertReadable(v corev1.Volume) corev1.Volume {
	if v.Projected == nil {
		return v
	}
	mode444 := int32(0o444)
	for i := range v.Projected.Sources {
		s := &v.Projected.Sources[i]
		if s.Secret == nil {
			continue
		}
		for j := range s.Secret.Items {
			s.Secret.Items[j].Mode = &mode444
		}
	}
	return v
}

func boolPtr(b bool) *bool { return &b }
