package catalogcollector_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
)

const (
	// collectorChartPath is the shipped chart, relative to the repository root.
	collectorChartPath = "deploy/helm/flightctl-catalog-collector"
	// collectorChartName is the chart name, which is also the value of the
	// app.kubernetes.io/name selector label the chart renders.
	collectorChartName = "flightctl-catalog-collector"

	// collectorImageEnv names the exact collector image under test. CI sets it
	// to the image produced by the build job that this run is testing, which
	// never carries the local development tags below.
	collectorImageEnv = "E2E_CATALOG_COLLECTOR_IMAGE"
	// collectorImagePreloadedEnv declares that collectorImageEnv is already in
	// the cluster's image store. The standard CI job loads the backend image
	// bundle straight into kind and never has those images in the host
	// runtime, so the suite must neither look for them there nor try to load
	// them again.
	collectorImagePreloadedEnv = "E2E_CATALOG_COLLECTOR_IMAGE_PRELOADED"
	// collectorImageRequiredEnv turns a missing image into a setup failure
	// instead of a skip. CI sets it so that a build which stops shipping the
	// collector image fails the job loudly rather than quietly reducing the
	// suite to zero executed specs.
	collectorImageRequiredEnv = "E2E_CATALOG_COLLECTOR_REQUIRED"

	// collectorImageEL9 and collectorImageGeneric are the tags "make
	// build-containers" produces for the branch under test, in the same
	// preference order as choose_image in test/scripts/deploy_with_helm.sh.
	// They are the local development path: built on the host, then loaded into
	// kind by this suite.
	collectorImageEL9     = "localhost/flightctl-catalog-collector-el9:latest"
	collectorImageGeneric = "localhost/flightctl-catalog-collector:latest"

	// Paths inside the collector container. The config mount path is the chart
	// default; the other two are the extraVolumeMounts this suite adds.
	collectorConfigMountPath = "/etc/flightctl/catalog-collector"
	collectorOAuthMountPath  = collectorConfigMountPath + "/oauth"
	collectorCertsMountPath  = collectorConfigMountPath + "/certs"
	collectorCAFileName      = "flightctl-ca.crt"

	// Names of the Secret and ConfigMap this suite mounts into the collector.
	collectorOAuthSecretName   = "e2e-catalog-collector-oauth"
	collectorCAConfigMapName   = "e2e-catalog-collector-ca"
	collectorOAuthClientIDKey  = "client-id"
	collectorOAuthSecretKey    = "client-secret" //nolint:gosec // G101: ConfigMap/Secret key name, not a credential
	flightctlCABundleSecret    = "flightctl-ca-bundle"
	flightctlCABundleSecretKey = "ca-bundle.crt"

	// collectorPollInterval keeps the smoke test short: the collector polls the
	// Model Registry far more often than a production deployment would, so the
	// first synchronization lands well inside catalogSyncTimeout.
	collectorPollInterval = 15 * time.Second

	// Every wait in this suite is bounded so a stuck cluster fails the spec
	// with diagnostics instead of hanging the run.
	chartInstallTimeout    = 5 * time.Minute
	chartUninstallTimeout  = 2 * time.Minute
	namespaceDeleteTimeout = 3 * time.Minute
	workloadGoneTimeout    = 2 * time.Minute
	catalogSyncTimeout     = 4 * time.Minute
	catalogSyncPolling     = 5 * time.Second
	setupTimeout           = 10 * time.Minute

	// Ownership labels the Flight Control destination stamps on every resource
	// it manages. They are the pruning boundary, so the suite asserts on them.
	managedByLabel = "flightctl.io/managed-by"
	managedByValue = "flightctl-catalog-collector"
	pipelineLabel  = "flightctl.io/catalog-collector-pipeline"

	// artifactRepository is the version-less OCI repository the seeded ModelCar
	// artifact points at. Nothing pulls it; the collector only has to carry it
	// through to CatalogItem.spec.artifacts.
	artifactRepository = "quay.io/flightctl-tests/model-artifact"
	// artifactVersion is the SemVer version of the seeded ModelVersion.
	artifactVersion = "1.4.2"
	// modelOwner ends up in CatalogItem.spec.provider.
	modelOwner = "flightctl-e2e"
)

// testResources holds the per-run identifiers. Every name carries the same
// random suffix so concurrent runs, and reruns against a reused cluster, never
// collide on a Kubernetes namespace, a Helm release, a Flight Control catalog,
// or a Model Registry model.
type testResources struct {
	suffix string

	namespace   string
	releaseName string

	catalogName  string
	itemName     string
	pipelineName string

	modelName        string
	modelVersionName string
	artifactName     string
	artifactDigest   string

	authProviderName string

	// Populated while seeding so teardown can archive exactly what was created.
	modelID        string
	modelVersionID string
}

// newTestResources derives every per-run name from one random suffix.
func newTestResources() (*testResources, error) {
	suffix, err := testutil.RandString(6)
	if err != nil {
		return nil, fmt.Errorf("generate resource suffix: %w", err)
	}

	// A digest derived from the suffix is unique per run and still satisfies
	// the collector's "sha256 + 64 lowercase hex" requirement for an immutable
	// OCI reference.
	sum := sha256.Sum256([]byte("flightctl-e2e-catalog-collector-" + suffix))

	modelName := "e2e-collector-model-" + suffix
	return &testResources{
		suffix:      suffix,
		namespace:   "flightctl-e2e-cc-" + suffix,
		releaseName: "e2e-catalog-collector-" + suffix,

		catalogName: "e2e-collector-catalog-" + suffix,
		// The source normalizes the registered model name into the CatalogItem
		// name. modelName is already a DNS subdomain, so normalization is the
		// identity here and the expected name can be asserted exactly.
		itemName:     modelName,
		pipelineName: "e2e-collector-pipeline-" + suffix,

		modelName:        modelName,
		modelVersionName: artifactVersion,
		artifactName:     "e2e-collector-modelcar-" + suffix,
		artifactDigest:   "sha256:" + hex.EncodeToString(sum[:]),

		authProviderName: "e2e-collector-oidc-" + suffix,
	}, nil
}

// artifactURI is the immutable, digest-pinned ModelCar reference seeded into
// the Model Registry.
func (r *testResources) artifactURI() string {
	return fmt.Sprintf("oci://%s@%s", artifactRepository, r.artifactDigest)
}

// podSelector matches the collector pods rendered by the chart.
func (r *testResources) podSelector() string {
	return fmt.Sprintf("app.kubernetes.io/name=%s,app.kubernetes.io/instance=%s", collectorChartName, r.releaseName)
}

// collectorImage is the outcome of deciding which collector image this run
// installs and how it reaches the cluster nodes.
type collectorImage struct {
	// Reference is the image to install. Empty when no candidate was found.
	Reference string
	// Preloaded reports that Reference is already in the cluster's image
	// store, so the suite must not load it from the host runtime.
	Preloaded bool
	// Required reports that a missing image must fail setup rather than skip
	// it.
	Required bool
	// Source describes where Reference came from, for the run log and for the
	// failure message when nothing was found.
	Source string
}

// resolveCollectorImage decides which collector image the suite installs.
//
// Two paths are supported and they are deliberately different:
//
//   - CI passes the exact image from the build under test in
//     collectorImageEnv and sets collectorImagePreloadedEnv, because the job
//     loads the backend image bundle directly into kind. Nothing is looked up
//     in, or loaded from, the host runtime.
//   - Local development builds the collector with "make
//     flightctl-catalog-collector-container" and leaves it in the host
//     runtime under one of the well-known tags, which the suite finds here
//     and loads into kind itself.
//
// A lookup error is returned rather than treated as "image absent": a broken
// container runtime must not quietly turn the suite into a skip.
func resolveCollectorImage(ctx context.Context, charts infra.ChartDeployer) (collectorImage, error) {
	preloaded, err := boolFromEnv(collectorImagePreloadedEnv)
	if err != nil {
		return collectorImage{}, err
	}
	override := strings.TrimSpace(os.Getenv(collectorImageEnv))
	// An explicitly named image is required by default: whoever set it meant
	// this suite to run against it, so a missing one is a failure, not a skip.
	required, err := boolFromEnvDefault(collectorImageRequiredEnv, override != "")
	if err != nil {
		return collectorImage{}, err
	}

	result := collectorImage{Required: required}
	if override != "" {
		result.Reference = override
		result.Preloaded = preloaded
		result.Source = collectorImageEnv
		return result, nil
	}

	// Without an explicit reference there is nothing to trust as preloaded:
	// the local tags only mean anything if the host runtime really has them.
	for _, candidate := range []string{collectorImageEL9, collectorImageGeneric} {
		exists, err := charts.LocalImageExists(ctx, candidate)
		if err != nil {
			return collectorImage{}, fmt.Errorf("look up collector image %s: %w", candidate, err)
		}
		if exists {
			result.Reference = candidate
			result.Source = "local container runtime"
			return result, nil
		}
	}
	return result, nil
}

// missingImageMessage explains what was looked for and how to supply it.
func missingImageMessage() string {
	return fmt.Sprintf(
		"no collector image available: %s is unset and neither %s nor %s is in the local container runtime. "+
			"Build it from this branch with 'make flightctl-catalog-collector-container', or set %s "+
			"(plus %s=true when the image is already in the cluster image store)",
		collectorImageEnv, collectorImageEL9, collectorImageGeneric, collectorImageEnv, collectorImagePreloadedEnv)
}

// boolFromEnv parses an optional boolean environment variable, defaulting to
// false. A value that is set but unparseable is an error rather than a silent
// false, because a typo there would silently change how the suite runs.
func boolFromEnv(name string) (bool, error) {
	return boolFromEnvDefault(name, false)
}

func boolFromEnvDefault(name string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean, got %q: %w", name, raw, err)
	}
	return value, nil
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

// renderCollectorConfig builds the collector pipeline configuration.
//
// The pipeline is deliberately minimal: Kubeflow Model Registry source straight
// into the Flight Control destination, no processors. This is an initial
// synchronization smoke test, so anything that is not needed to prove a seeded
// model reaches the API is left out.
//
// TLS verification is on everywhere TLS is used: the Flight Control endpoint is
// verified against the deployment's own CA bundle and insecureSkipVerify is set
// nowhere.
func renderCollectorConfig(res *testResources, modelRegistryEndpoint, flightctlServer, tokenURL string) string {
	return fmt.Sprintf(`service:
  logLevel: debug
  metrics:
    endpoint: 0.0.0.0:8888

extensions:
  healthcheck:
    endpoint: 0.0.0.0:13133
    livePath: /livez
    readyPath: /readyz

  oauth2client/flightctl:
    clientIdFile: %[1]s/%[2]s
    clientSecretFile: %[1]s/%[3]s
    tokenUrl: %[4]s
    scopes:
      - openid
    timeout: 30s
    expiryBuffer: 30s

sources:
  kubeflowmodelregistry/e2e:
    endpoint: %[5]s
    catalog: %[6]s
    pollInterval: %[7]s
    requestTimeout: 30s
    collectionTimeout: 2m
    pageSize: 100
    selection:
      modelFilter: "state = 'LIVE'"
      versionFilter: "state = 'LIVE'"

destinations:
  flightctl/service:
    server: %[8]s
    auth:
      authenticator: oauth2client/flightctl
    certificateAuthority: %[9]s/%[10]s
    timeout: 30s

pipelines:
  %[11]s:
    source: kubeflowmodelregistry/e2e
    destination: flightctl/service
`,
		collectorOAuthMountPath,
		collectorOAuthClientIDKey,
		collectorOAuthSecretKey,
		tokenURL,
		modelRegistryEndpoint,
		res.catalogName,
		collectorPollInterval,
		flightctlServer,
		collectorCertsMountPath,
		collectorCAFileName,
		res.pipelineName,
	)
}

// renderCollectorValues writes a Helm values file mounting the OAuth2 client
// credentials and the Flight Control CA bundle into the collector, and returns
// its path. The file is removed when the suite ends.
func renderCollectorValues(config string) (string, error) {
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
		indentBlock(config, 4),
		collectorOAuthSecretName,
		collectorCAConfigMapName,
		collectorOAuthMountPath,
		collectorCertsMountPath,
	)

	file, err := os.CreateTemp("", "e2e-catalog-collector-values-*.yaml")
	if err != nil {
		return "", fmt.Errorf("create values file: %w", err)
	}
	path := file.Name()
	if _, err := file.WriteString(values); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write values file %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
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

// seedModelRegistry creates the one LIVE registered model, SemVer version, and
// digest-pinned ModelCar artifact the collector is expected to import.
func seedModelRegistry(ctx context.Context, registry *auxiliary.ModelRegistry, res *testResources) error {
	model, err := registry.CreateRegisteredModel(ctx, auxiliary.RegisteredModel{
		Name:        res.modelName,
		Description: "Flight Control catalog collector e2e smoke test model",
		Owner:       modelOwner,
	})
	if err != nil {
		return err
	}
	res.modelID = model.ID

	version, err := registry.CreateModelVersion(ctx, model.ID, auxiliary.ModelVersion{
		Name:   res.modelVersionName,
		Author: modelOwner,
	})
	if err != nil {
		return err
	}
	res.modelVersionID = version.ID

	if _, err := registry.CreateModelArtifact(ctx, version.ID, auxiliary.ModelArtifact{
		Name:            res.artifactName,
		URI:             res.artifactURI(),
		ModelFormatName: "modelcar",
	}); err != nil {
		return err
	}
	return nil
}

// buildAuthProviderYAML renders the OIDC AuthProvider that lets the Flight
// Control API accept the collector's client-credentials tokens.
//
// usernameClaim is preferred_username because a Keycloak service-account token
// carries the service account name there and no other identity claim.
func buildAuthProviderYAML(name, issuerURL, clientID, clientSecret string) string {
	return fmt.Sprintf(`apiVersion: flightctl.io/v1beta1
kind: AuthProvider
metadata:
  name: %q
spec:
  providerType: oidc
  displayName: %q
  issuer: %q
  clientId: %q
  clientSecret: %q
  enabled: true
  scopes:
    - openid
  usernameClaim:
    - preferred_username
  organizationAssignment:
    type: static
    organizationName: default
  roleAssignment:
    type: static
    roles:
      - flightctl-admin
`, name, name, issuerURL, clientID, clientSecret)
}

// applyAuthProvider writes the manifest to a temp file and applies it with the
// CLI, as an authenticated admin.
func applyAuthProvider(harness *e2e.Harness, manifest string) (string, error) {
	file, err := os.CreateTemp("", "e2e-catalog-collector-authprovider-*.yaml")
	if err != nil {
		return "", fmt.Errorf("create authprovider manifest: %w", err)
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err := file.WriteString(manifest); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write authprovider manifest %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close authprovider manifest %s: %w", path, err)
	}
	return harness.ApplyResource(path)
}

// dumpDiagnostics writes collector pod state, events and logs plus the current
// catalog resources to the Ginkgo output. Called only on failure, while the
// deployment is still up, so a failed assertion is accompanied by the state
// that produced it.
func dumpDiagnostics(ctx context.Context, charts infra.ChartDeployer, harness *e2e.Harness, res *testResources) {
	if res == nil {
		return
	}
	if charts != nil {
		GinkgoWriter.Printf("[catalogcollector] %s\n", charts.WorkloadDiagnostics(ctx, res.namespace, res.podSelector()))
	}
	if harness == nil {
		return
	}
	if out, err := harness.CLI("get", "catalogs", "-o", "yaml"); err == nil {
		GinkgoWriter.Printf("[catalogcollector] catalogs:\n%s\n", out)
	} else {
		GinkgoWriter.Printf("[catalogcollector] listing catalogs failed: %v\n", err)
	}
	if out, err := harness.CLI("get", "catalogitems", "-o", "yaml"); err == nil {
		GinkgoWriter.Printf("[catalogcollector] catalog items:\n%s\n", out)
	} else {
		GinkgoWriter.Printf("[catalogcollector] listing catalog items failed: %v\n", err)
	}
}
