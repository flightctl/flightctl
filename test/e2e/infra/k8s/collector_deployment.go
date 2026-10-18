package k8s

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
)

const (
	// collectorChartPath is the shipped chart, relative to the repository root.
	// The collector is deployed from what it ships with, not from a fixture, so
	// a chart that stops rendering a working deployment fails the suite.
	collectorChartPath = "deploy/helm/flightctl-catalog-collector"
	// collectorChartName is the chart name, which is also the value of the
	// app.kubernetes.io/name selector label the chart renders.
	collectorChartName = "flightctl-catalog-collector"

	// collectorImageEL9 and collectorImageGeneric are the tags "make
	// flightctl-catalog-collector-container" produces for the branch under
	// test, in the same preference order as choose_image in
	// test/scripts/deploy_with_helm.sh. They are the local development path:
	// built on the host, then loaded into the cluster by this package. The
	// collector is not part of "make build-containers", so that target has to
	// be run on its own.
	collectorImageEL9     = "localhost/flightctl-catalog-collector-el9:latest"
	collectorImageGeneric = "localhost/flightctl-catalog-collector:latest"

	// Paths inside the collector container. The config mount path is the chart
	// default; the other two are the extraVolumeMounts this package adds, and
	// are reported to callers through infra.CollectorConfigContext.
	collectorConfigMountPath = "/etc/flightctl/catalog-collector"
	collectorOAuthMountPath  = collectorConfigMountPath + "/oauth"
	collectorCertsMountPath  = collectorConfigMountPath + "/certs"
	collectorCAFileName      = "flightctl-ca.crt"

	// Names of the Secret and ConfigMap mounted into the collector.
	collectorOAuthSecretName  = "e2e-catalog-collector-oauth"
	collectorCAConfigMapName  = "e2e-catalog-collector-ca"
	collectorOAuthClientIDKey = "client-id"
	collectorOAuthSecretKey   = "client-secret" //nolint:gosec // G101: ConfigMap/Secret key name, not a credential

	// The Flight Control deployment's own CA bundle, which the collector
	// verifies the API endpoint against. Discovering it is part of deploying
	// the collector, so the caller never names it.
	flightctlCABundleSecret    = "flightctl-ca-bundle"
	flightctlCABundleSecretKey = "ca-bundle.crt"

	// collectorNamespaceDeleteTimeout bounds removing the collector namespace,
	// both as the stop fallback and as ordinary cleanup. It is longer than the
	// other teardown bounds because a namespace deletion also waits for every
	// object in it to finalize.
	collectorNamespaceDeleteTimeout = 3 * time.Minute
)

// CollectorDeployer deploys the catalog collector into a Kubernetes cluster
// from the chart it ships with.
//
// It implements infra.CollectorDeployer. Everything cluster-shaped is here:
// the namespace, the mounted Secret and ConfigMap, the CA bundle lookup, the
// Helm values, the image load, the release, readiness, and teardown. Callers
// get a configuration context and an instance handle, and nothing else.
type CollectorDeployer struct {
	runtime collectorRuntime
	secrets collectorTrustSource
	env     collectorEnvironment
	envType string

	// image memoizes the resolved image so Available and Start agree on what
	// is being deployed, and the container runtime is consulted only once.
	image *resolvedImage
}

// collectorEnvironment is the part of the surrounding Flight Control
// deployment a collector deployment has to ask about: which namespace it runs
// in, and how its API is reached. It is narrowed to those two questions so the
// deployment can be exercised without a cluster. *InfraProvider satisfies it.
type collectorEnvironment interface {
	GetExternalNamespace() string
	GetServiceEndpoint(service infra.ServiceName) (host string, port int, err error)
}

// collectorTrustSource reads the surrounding Flight Control deployment's CA
// bundle, so the collector verifies the endpoint it is pointed at. It is
// narrowed to that one read for the same reason as collectorEnvironment.
// infra.SecretsProvider satisfies it.
type collectorTrustSource interface {
	GetSecretData(ctx context.Context, namespace, secretName, key string) ([]byte, error)
}

// resolvedImage is the outcome of deciding which collector image to deploy and
// how it reaches the cluster nodes.
type resolvedImage struct {
	// reference is the image to deploy, or empty when none was found.
	reference string
	// preloaded reports that reference is already in the cluster image store,
	// so it must not be loaded from the host runtime.
	preloaded bool
	// required reports that a missing image must fail rather than skip.
	required bool
	// source describes where reference came from, for the run log.
	source string
}

// Compile-time proof that this is the generic capability, not a wider one.
var _ infra.CollectorDeployer = (*CollectorDeployer)(nil)

// NewCollectorDeployer creates the Kubernetes catalog collector deployer.
//
// infraP supplies the cluster coordinates and the namespace the Flight Control
// deployment runs in; secrets reads that deployment's CA bundle, so the
// collector trusts the endpoint it is pointed at without a caller handling
// trust material.
func NewCollectorDeployer(client kubernetes.Interface, infraP *InfraProvider, secrets infra.SecretsProvider) (*CollectorDeployer, error) {
	if infraP == nil {
		return nil, fmt.Errorf("k8s CollectorDeployer requires a non-nil InfraProvider")
	}
	if secrets == nil {
		return nil, fmt.Errorf("k8s CollectorDeployer requires a non-nil SecretsProvider")
	}
	runtime, err := newClusterRuntime(client, infraP)
	if err != nil {
		return nil, fmt.Errorf("k8s CollectorDeployer: %w", err)
	}
	return &CollectorDeployer{
		runtime: runtime,
		secrets: secrets,
		env:     infraP,
		envType: infraP.GetEnvironmentType(),
	}, nil
}

// Available reports whether a collector can be deployed into this cluster.
//
// Two things can make it unavailable, and both are settled answers rather than
// errors: a deployment type whose nodes cannot be fed a locally built image,
// and no collector image to deploy. Anything else - an unparseable setting, a
// container runtime that cannot be reached - is returned as an error, so a
// broken environment never degrades into a skip.
func (d *CollectorDeployer) Available(ctx context.Context) (infra.CollectorAvailability, error) {
	if d.envType != infra.EnvironmentKind {
		// Answered before the image is resolved. Deploying the collector means
		// putting the image built from this branch onto the cluster nodes,
		// which is only implemented for kind, so no image could make this
		// deployment type work - and probing a container runtime that is
		// irrelevant here could only turn a settled "no" into a failure.
		selection, err := infra.CollectorImageFromEnv()
		if err != nil {
			return infra.CollectorAvailability{}, err
		}
		return infra.CollectorAvailability{
			Required: selection.Required,
			Reason: fmt.Sprintf(
				"deploying the catalog collector loads the collector image into the cluster, which is only implemented for kind (deployment type %q)",
				d.envType),
		}, nil
	}

	image, err := d.resolveImage(ctx)
	if err != nil {
		return infra.CollectorAvailability{}, err
	}

	if image.reference == "" {
		return infra.CollectorAvailability{Required: image.required, Reason: missingImageMessage()}, nil
	}
	return infra.CollectorAvailability{Available: true, Required: image.required}, nil
}

// ConfigContext returns the endpoint and in-container paths a caller needs to
// render a collector configuration for this deployment.
func (d *CollectorDeployer) ConfigContext(context.Context) (infra.CollectorConfigContext, error) {
	host, port, err := d.env.GetServiceEndpoint(infra.ServiceAPI)
	if err != nil {
		return infra.CollectorConfigContext{}, fmt.Errorf("resolve the in-cluster Flight Control API endpoint: %w", err)
	}
	return infra.CollectorConfigContext{
		FlightControlServer:      fmt.Sprintf("https://%s:%d", host, port),
		OAuthClientIDFile:        collectorOAuthMountPath + "/" + collectorOAuthClientIDKey,
		OAuthClientSecretFile:    collectorOAuthMountPath + "/" + collectorOAuthSecretKey,
		CertificateAuthorityFile: collectorCertsMountPath + "/" + collectorCAFileName,
	}, nil
}

// Start deploys the collector and waits for the release to roll out.
//
// The instance is returned alongside every failure that happened after the
// first object was created: a start that failed partway still owns a
// namespace, a Secret, or a release, and the instance is the only handle the
// caller has to remove them.
func (d *CollectorDeployer) Start(ctx context.Context, config infra.CollectorDeploymentConfig) (infra.CollectorInstance, error) {
	// Refused before anything is created, and before the image is even looked
	// at. Available already reports this deployment type as unavailable, but a
	// caller that skipped that question must not be able to deploy anyway:
	// an explicitly preloaded image would otherwise sail past the only other
	// guard and install a collector onto nodes this package cannot support.
	if d.envType != infra.EnvironmentKind {
		return nil, fmt.Errorf("%w: deploying the catalog collector is only implemented for kind, got %q",
			infra.ErrCollectorDeploymentUnsupported, d.envType)
	}
	if strings.TrimSpace(config.InstanceID) == "" {
		return nil, fmt.Errorf("collector deployment: instance ID is required")
	}
	if len(config.Config) == 0 {
		return nil, fmt.Errorf("collector deployment: configuration is required")
	}
	if config.StartTimeout <= 0 {
		return nil, fmt.Errorf("collector deployment: start timeout must be positive")
	}
	if config.StopTimeout <= 0 {
		return nil, fmt.Errorf("collector deployment: stop timeout must be positive")
	}

	image, err := d.resolveImage(ctx)
	if err != nil {
		return nil, err
	}
	if image.reference == "" {
		return nil, fmt.Errorf("%w: %s", infra.ErrCollectorDeploymentUnsupported, missingImageMessage())
	}

	instance := &collectorInstance{
		runtime:     d.runtime,
		namespace:   collectorNamespace(config.InstanceID),
		releaseName: collectorReleaseName(config.InstanceID),
		stopTimeout: config.StopTimeout,
	}
	instance.selector = fmt.Sprintf("app.kubernetes.io/name=%s,app.kubernetes.io/instance=%s",
		collectorChartName, instance.releaseName)

	if err := d.runtime.ensureNamespace(ctx, instance.namespace); err != nil {
		return instance, err
	}
	if err := d.runtime.applySecret(ctx, instance.namespace, collectorOAuthSecretName, map[string]string{
		collectorOAuthClientIDKey: config.OAuthClientID,
		collectorOAuthSecretKey:   config.OAuthClientSecret,
	}); err != nil {
		return instance, err
	}

	caBundle, err := d.secrets.GetSecretData(ctx, d.env.GetExternalNamespace(), flightctlCABundleSecret, flightctlCABundleSecretKey)
	if err != nil {
		return instance, fmt.Errorf("read the Flight Control CA bundle: %w", err)
	}
	if len(caBundle) == 0 {
		return instance, fmt.Errorf("the Flight Control CA bundle %s/%s is empty", flightctlCABundleSecret, flightctlCABundleSecretKey)
	}
	if err := d.runtime.applyConfigMap(ctx, instance.namespace, collectorCAConfigMapName, map[string]string{
		collectorCAFileName: string(caBundle),
	}); err != nil {
		return instance, err
	}

	logrus.Infof("K8s: deploying catalog collector image %s (source=%s, preloaded=%t, required=%t)",
		image.reference, image.source, image.preloaded, image.required)
	if !image.preloaded {
		if err := d.runtime.loadLocalImage(ctx, image.reference); err != nil {
			return instance, err
		}
	}

	valuesPath, err := renderCollectorValues(config.Config)
	if err != nil {
		return instance, err
	}
	// Recorded before the install so cleanup removes the file even when the
	// install itself fails.
	instance.valuesPath = valuesPath

	repository, tag := splitImageRef(image.reference)
	if err := d.runtime.installChart(ctx, chartRelease{
		name:        instance.releaseName,
		namespace:   instance.namespace,
		chartPath:   collectorChartPath,
		valuesFiles: []string{valuesPath},
		setValues: map[string]string{
			"image.image":      repository,
			"image.tag":        tag,
			"image.pullPolicy": "IfNotPresent",
		},
		timeout: config.StartTimeout,
	}); err != nil {
		// No "was it installed" flag is kept: an install that fails its
		// readiness wait still leaves the release and its pods behind, and one
		// that never ran leaves none. Stop handles both without being told,
		// because uninstalling a release that does not exist is not an error.
		return instance, err
	}
	return instance, nil
}

// resolveImage decides which collector image to deploy, once.
//
// Two paths are supported and they are deliberately different:
//
//   - A run that names an image also says whether the cluster already holds
//     it. CI does, because its job loads the backend image bundle straight
//     into the cluster; nothing is looked up in, or loaded from, the host
//     runtime.
//   - Otherwise the image is the local development build, left in the host
//     runtime under one of the well-known tags by "make
//     flightctl-catalog-collector-container", which is found here and loaded
//     into the cluster.
//
// A lookup failure is returned rather than treated as "image absent": a broken
// container runtime must not quietly turn a required deployment into a skip.
func (d *CollectorDeployer) resolveImage(ctx context.Context) (resolvedImage, error) {
	if d.image != nil {
		return *d.image, nil
	}

	selection, err := infra.CollectorImageFromEnv()
	if err != nil {
		return resolvedImage{}, err
	}
	result := resolvedImage{required: selection.Required}

	if selection.Reference != "" {
		result.reference = selection.Reference
		result.preloaded = selection.Preloaded
		result.source = infra.EnvCollectorImage
		d.image = &result
		return result, nil
	}

	// Without an explicit reference there is nothing to trust as preloaded:
	// the local tags only mean anything if the host runtime really has them.
	for _, candidate := range []string{collectorImageEL9, collectorImageGeneric} {
		exists, err := d.runtime.localImageExists(ctx, candidate)
		if err != nil {
			return resolvedImage{}, fmt.Errorf("look up collector image %s: %w", candidate, err)
		}
		if exists {
			result.reference = candidate
			result.source = "local container runtime"
			break
		}
	}
	d.image = &result
	return result, nil
}

// missingImageMessage explains what was looked for and how to supply it.
func missingImageMessage() string {
	return fmt.Sprintf(
		"no collector image available: %s is unset and neither %s nor %s is in the local container runtime. "+
			"Build it from this branch with 'make flightctl-catalog-collector-container', or set %s "+
			"(plus %s=true when the image is already in the cluster image store)",
		infra.EnvCollectorImage, collectorImageEL9, collectorImageGeneric,
		infra.EnvCollectorImage, infra.EnvCollectorImagePreloaded)
}

// collectorNamespace and collectorReleaseName derive the cluster-side names
// from the caller's logical instance ID, so concurrent runs and reruns against
// a reused cluster never collide and the caller never sees either name.
func collectorNamespace(instanceID string) string {
	return "flightctl-e2e-cc-" + instanceID
}

func collectorReleaseName(instanceID string) string {
	return "e2e-catalog-collector-" + instanceID
}

// splitImageRef splits "repository:tag" into the two chart values. A reference
// without a tag defaults to "latest", matching the locally built images.
func splitImageRef(image string) (repository, tag string) {
	// Only the last colon can introduce a tag, and only when it comes after the
	// last slash; a colon before that belongs to a registry port.
	lastColon := strings.LastIndex(image, ":")
	lastSlash := strings.LastIndex(image, "/")
	if lastColon > lastSlash {
		return image[:lastColon], image[lastColon+1:]
	}
	return image, "latest"
}

// createValuesFile creates the file the rendered values are written to. It is
// a variable so the write and close failures below - which cannot be provoked
// with a healthy temp file - can be exercised, because what they must do is
// remove the file they already created.
var createValuesFile = func() (*os.File, error) {
	return os.CreateTemp("", "e2e-catalog-collector-values-*.yaml")
}

// renderCollectorValues writes the Helm values that mount the OAuth2 client
// credentials and the Flight Control CA bundle into the collector, and returns
// the file's path. The instance removes it during cleanup.
func renderCollectorValues(config []byte) (string, error) {
	values := fmt.Sprintf(`config:
  content: |
%[1]s

health:
  enabled: true
  port: 13133

metrics:
  enabled: true
  port: 8888
  serviceMonitor:
    # The kind cluster used by e2e has no Prometheus Operator CRDs.
    enabled: false

service:
  enabled: true

serviceAccount:
  create: true
  automountServiceAccountToken: false

extraVolumes:
  - name: oauth
    secret:
      secretName: %[2]s
      # 0440, not 0400: the kubelet leaves a Secret volume owned by root:root
      # and the collector runs as a non-root uid, so the group bit is what
      # makes the files readable.
      defaultMode: 0440
  - name: ca
    configMap:
      name: %[3]s
      defaultMode: 0444

extraVolumeMounts:
  - name: oauth
    mountPath: %[4]s
    readOnly: true
  - name: ca
    mountPath: %[5]s
    readOnly: true
`,
		indentBlock(string(config), 4),
		collectorOAuthSecretName,
		collectorCAConfigMapName,
		collectorOAuthMountPath,
		collectorCertsMountPath,
	)

	file, err := createValuesFile()
	if err != nil {
		return "", fmt.Errorf("create values file: %w", err)
	}
	path := file.Name()
	// Written and closed behind a failure path that removes the file: this
	// function either returns a path its caller owns, or nothing at all, and
	// a half-written file nobody holds a handle to would never be cleaned up.
	if _, err := file.WriteString(values); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write values file %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close values file %s: %w", path, err)
	}
	return path, nil
}

// indentBlock indents every line of a YAML block scalar.
func indentBlock(value string, spaces int) string {
	prefix := strings.Repeat(" ", spaces)
	lines := strings.Split(strings.TrimRight(value, "\n"), "\n")
	for i := range lines {
		if lines[i] == "" {
			continue
		}
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

// collectorInstance is one deployed collector.
type collectorInstance struct {
	runtime     collectorRuntime
	namespace   string
	releaseName string
	selector    string
	valuesPath  string
	stopTimeout time.Duration
}

var _ infra.CollectorInstance = (*collectorInstance)(nil)

// WaitReady waits until every collector pod reports ready.
func (i *collectorInstance) WaitReady(ctx context.Context, timeout time.Duration) error {
	return i.runtime.waitForWorkloadReady(ctx, i.namespace, i.selector, timeout)
}

// Logs returns the collector's most recent output, bounded to tailLines.
func (i *collectorInstance) Logs(ctx context.Context, tailLines int64) (string, error) {
	return i.runtime.workloadLogs(ctx, i.namespace, i.selector, tailLines)
}

// Diagnostics returns a best-effort dump of the collector's state.
func (i *collectorInstance) Diagnostics(ctx context.Context) string {
	return i.runtime.workloadDiagnostics(ctx, i.namespace, i.selector)
}

// Stop removes the release and confirms the workload is gone, so the collector
// can no longer act on anything.
//
// If any of that fails the namespace is deleted as a fallback, which stops the
// collector just as effectively. The original failure is still reported: the
// release record is deployment-owned state and leaving it behind is a problem
// in its own right.
//
// Whether the collector is gone and whether the step succeeded are two
// different answers, so they are reported through two different channels.
// stopped is true on both paths that end with the workload down - the normal
// removal and a fallback that worked - and only on those, because it is what a
// caller keys dependent teardown off. The returned error is unconditional once
// anything went wrong, and always wraps the original failures so the report
// names what actually broke.
func (i *collectorInstance) Stop(ctx context.Context) (bool, error) {
	err := errors.Join(
		i.runtime.uninstallChart(ctx, i.releaseName, i.namespace, i.stopTimeout),
		i.releaseGone(ctx),
		i.runtime.waitForWorkloadGone(ctx, i.namespace, i.selector, i.stopTimeout),
	)
	if err == nil {
		return true, nil
	}

	if deleteErr := i.runtime.deleteNamespace(ctx, i.namespace, collectorNamespaceDeleteTimeout); deleteErr != nil {
		// Neither path confirmed the collector is down, so the caller is told
		// it is still running and must not act as though it were stopped.
		return false, errors.Join(err, fmt.Errorf("namespace deletion fallback: %w", deleteErr))
	}
	return true, fmt.Errorf("%w (the collector was stopped by deleting namespace %s instead)", err, i.namespace)
}

// releaseGone reports an error unless the release no longer exists.
func (i *collectorInstance) releaseGone(ctx context.Context) error {
	installed, err := i.runtime.releaseInstalled(ctx, i.releaseName, i.namespace)
	if err != nil {
		return fmt.Errorf("check whether release %s is gone: %w", i.releaseName, err)
	}
	if installed {
		return fmt.Errorf("Helm release %s in namespace %s survived teardown", i.releaseName, i.namespace)
	}
	return nil
}

// Cleanup removes the namespace and the rendered values file.
//
// Both actions are idempotent, so this is safe after Stop already deleted the
// namespace as its fallback, after a Start that failed partway, and on a
// repeat call. Failures of the two are accumulated rather than short-circuited:
// a namespace that will not go away must not stop the local file from being
// removed.
func (i *collectorInstance) Cleanup(ctx context.Context) error {
	var errs []error
	if i.namespace != "" {
		errs = append(errs, i.runtime.deleteNamespace(ctx, i.namespace, collectorNamespaceDeleteTimeout))
	}
	if i.valuesPath != "" {
		if err := os.Remove(i.valuesPath); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("remove values file %s: %w", i.valuesPath, err))
		} else {
			// Cleared so a second call is a no-op rather than a repeated
			// not-exist check.
			i.valuesPath = ""
		}
	}
	return errors.Join(errs...)
}
