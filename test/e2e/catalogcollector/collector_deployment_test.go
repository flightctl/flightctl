package catalogcollector_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
)

const (
	// collectorPollInterval keeps the smoke test short: the collector polls the
	// Model Registry far more often than a production deployment would, so the
	// first synchronization lands well inside catalogSyncTimeout.
	collectorPollInterval = 15 * time.Second

	// collectorFailureRetryInterval pins the delay between the attempts of a
	// source that keeps failing, for the same reason collectorPollInterval
	// pins the delay between successful ones: a spec has to be able to wait on
	// the cadence, not on the production one.
	//
	// The shipped default doubles from one second towards five minutes, which
	// is right for a deployment and unusable here. The failed-collection
	// scenario has to observe several rejected cycles, change registry
	// content, and then observe several more; under the default the later
	// attempts would already be minutes apart, so the wait for them would be
	// bounded by the backoff rather than by anything the collector did.
	// Pinned flat, every rejected cycle is one interval behind the last.
	collectorFailureRetryInterval = 5 * time.Second

	// Every wait in this suite is bounded so a stuck deployment fails the spec
	// with diagnostics instead of hanging the run.
	collectorStartTimeout = 5 * time.Minute
	collectorStopTimeout  = 2 * time.Minute
	collectorReadyTimeout = 2 * time.Minute
	catalogSyncTimeout    = 4 * time.Minute
	catalogSyncPolling    = 5 * time.Second
	setupTimeout          = 10 * time.Minute

	// catalogChangeTimeout bounds a wait for the collector to publish a change
	// to registry content it has already imported once. The first cycle is the
	// slow one (chart rollout, preflight, token exchange), so this is shorter
	// than catalogSyncTimeout while still allowing several poll intervals.
	catalogChangeTimeout = 2 * time.Minute

	// unchangedObservationWindow is how long published content is watched
	// after a collection has been rejected. It spans several poll intervals so
	// the assertion covers subsequent cycles rather than the instant before
	// the next one: a destination that pruned or partially rewrote content on
	// a failed cycle would do it on one of those, not necessarily the first.
	unchangedObservationWindow = 3 * collectorPollInterval
	unchangedObservationPoll   = 5 * time.Second

	// collectorLogTailLines bounds the collector log read used to prove a
	// collection was attempted and rejected. The failing cycles that matter
	// are the most recent ones, and the collector logs at debug level.
	collectorLogTailLines = int64(1000)

	// collectionFailedMessage is the message the polling helper logs for every
	// rejected collection cycle (pkg/catalogcollector/source/pollsource).
	collectionFailedMessage = "collection failed; applying backoff"

	// minObservedCollectionFailures is how many rejected cycles must be seen
	// before published content is checked. More than one proves the collector
	// kept polling and kept rejecting, rather than the suite having caught a
	// single transient error.
	minObservedCollectionFailures = 2

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

	// scenarioProperty is the Model Registry custom property each scenario
	// stamps on the models it seeds, and filters its own source on. It is what
	// keeps one scenario's registry content out of every other scenario's
	// selection, so a deliberately broken fixture cannot fail a collection
	// cycle that another scenario depends on.
	scenarioProperty = "flightctl_e2e_scenario"
)

// scenarioName identifies one end-to-end flow in this suite. Each one owns a
// separate collector pipeline, source, Flight Control catalog, and slice of
// Model Registry content, so the specs do not depend on each other or on the
// order Ginkgo runs them in.
type scenarioName string

const (
	scenarioSmoke   scenarioName = "smoke"
	scenarioUpdate  scenarioName = "update"
	scenarioRemoval scenarioName = "removal"
	scenarioSafety  scenarioName = "safety"
)

// allScenarios is the fixed set, in the order they are written into the
// collector configuration.
var allScenarios = []scenarioName{
	scenarioSmoke,
	scenarioUpdate,
	scenarioRemoval,
	scenarioSafety,
}

// scenario holds the per-flow identifiers. Two of them are the isolation
// boundary and are deliberately not shared:
//
//   - selector scopes the source's server-side model selection to the models
//     this scenario seeded;
//   - pipelineName is the label value the Flight Control destination uses as
//     its reconciliation and pruning boundary, so one pipeline can never
//     delete or overwrite another's resources.
type scenario struct {
	name scenarioName

	// selector is the value of the scenarioProperty custom property.
	selector string
	// sourceID is the collector component ID of this scenario's source.
	sourceID string

	catalogName  string
	pipelineName string
}

// modelFilter is the server-side selection for this scenario's source. Only
// LIVE models stamped with this scenario's selector are ever fetched.
func (s *scenario) modelFilter() string {
	return fmt.Sprintf("state='LIVE' AND %s.string_value = '%s'", scenarioProperty, s.selector)
}

// seededVersion is one LIVE ModelVersion with its single ModelCar artifact.
type seededVersion struct {
	id   string
	name string
	// uri is what was written to the Model Registry. A scenario that seeds a
	// deliberately unusable artifact sets it to something the collector must
	// reject.
	uri string
	// digest is the immutable reference the CatalogItem version is expected to
	// carry. It is empty when uri is not a valid immutable reference.
	digest string
}

// seededModel records what one scenario created in the Model Registry, so
// specs can assert against it and teardown can retire exactly what was made.
type seededModel struct {
	scenario *scenario
	id       string
	name     string
	versions []*seededVersion
}

// itemName is the CatalogItem name the collector derives from the registered
// model name. Seeded names are already DNS subdomains, so the source's
// normalization is the identity here and the expected name is exact.
func (m *seededModel) itemName() string {
	return m.name
}

// version returns the seeded version with this name.
func (m *seededModel) version(name string) *seededVersion {
	for _, version := range m.versions {
		if version.name == name {
			return version
		}
	}
	return nil
}

// testResources holds the per-run identifiers. Every name carries the same
// random suffix so concurrent runs, and reruns against a reused environment,
// never collide on a Flight Control catalog, an AuthProvider, or a Model
// Registry model.
//
// The suffix is also the logical instance ID the collector deployment is given.
// Whatever the deployment derives from it - a namespace, a release, a unit - is
// its own business and never appears here.
//
// The deployment is shared by every scenario: one collector, one AuthProvider.
// What is not shared is everything a scenario can mutate - see scenario.
type testResources struct {
	suffix string

	authProviderName string

	// scenarios is keyed by scenarioName and populated for every entry of
	// allScenarios.
	scenarios map[scenarioName]*scenario
}

// newTestResources derives every per-run name from one random suffix.
func newTestResources() (*testResources, error) {
	suffix, err := testutil.RandString(6)
	if err != nil {
		return nil, fmt.Errorf("generate resource suffix: %w", err)
	}

	res := &testResources{
		suffix:           suffix,
		authProviderName: "e2e-collector-oidc-" + suffix,
		scenarios:        make(map[scenarioName]*scenario, len(allScenarios)),
	}

	for _, name := range allScenarios {
		res.scenarios[name] = &scenario{
			name:     name,
			selector: fmt.Sprintf("%s-%s", name, suffix),
			// Component IDs are "<type>/<name>"; the scenario name keeps each
			// source's logs and metrics attributable to one flow.
			sourceID:     fmt.Sprintf("kubeflowmodelregistry/%s", name),
			catalogName:  fmt.Sprintf("e2e-cc-%s-catalog-%s", name, suffix),
			pipelineName: fmt.Sprintf("e2e-cc-%s-pipeline-%s", name, suffix),
		}
	}
	return res, nil
}

// scenario returns the per-flow identifiers, which every spec reaches through
// rather than through package state it might share with another spec.
func (r *testResources) scenario(name scenarioName) *scenario {
	return r.scenarios[name]
}

// catalogNames lists every catalog this run can create, in scenario order.
func (r *testResources) catalogNames() []string {
	names := make([]string, 0, len(allScenarios))
	for _, name := range allScenarios {
		if sc := r.scenarios[name]; sc != nil {
			names = append(names, sc.catalogName)
		}
	}
	return names
}

// modelName builds a registered model name that is unique to this run and
// scenario, and is already a DNS subdomain so the source's name normalization
// leaves it unchanged.
func (r *testResources) modelName(sc *scenario, local string) string {
	return fmt.Sprintf("e2e-cc-%s-%s-%s", sc.name, local, r.suffix)
}

// digest derives an immutable OCI digest that is unique to this run and to the
// supplied parts, and satisfies the collector's "sha256 + 64 lowercase hex"
// requirement. Nothing pulls it, so its only job is to be well-formed and
// distinct.
func (r *testResources) digest(parts ...string) string {
	seed := strings.Join(append([]string{"flightctl-e2e-catalog-collector", r.suffix}, parts...), "/")
	sum := sha256.Sum256([]byte(seed))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// artifactURI is an immutable, digest-pinned ModelCar reference into the
// repository every version of every seeded model shares. A CatalogItem stores
// the repository once and the per-version digest in its version references, so
// all versions of one model must agree on the repository.
func artifactURI(digest string) string {
	return fmt.Sprintf("oci://%s@%s", artifactRepository, digest)
}

// renderCollectorConfig builds the collector configuration.
//
// Every pipeline is deliberately minimal: one Kubeflow Model Registry source
// straight into the shared Flight Control destination, no processors. Anything
// not needed to prove that registry content reaches the API is left out.
//
// There is one source and one pipeline per scenario rather than one shared
// pair, and that is the point of the design. A collection cycle is all or
// nothing: a single unusable artifact fails the whole cycle for its source, and
// the Flight Control destination prunes per pipeline. Sharing either component
// would let the failed-collection scenario's deliberately broken fixture stall
// the scenarios that assert on published content, and would make every spec
// depend on what the others had seeded.
//
// TLS verification is on everywhere TLS is used: the Flight Control endpoint is
// verified against the deployment's own CA bundle and insecureSkipVerify is set
// nowhere.
//
// deployment supplies the three things only the deployment can know: where the
// collector reaches Flight Control from inside it, and where the credentials
// and the CA bundle it was handed are placed. Everything else - the sources,
// the per-scenario selection, the pipelines - is this suite's.
func renderCollectorConfig(res *testResources, deployment infra.CollectorConfigContext, modelRegistryEndpoint, tokenURL string) string {
	var config strings.Builder

	fmt.Fprintf(&config, `service:
  logLevel: debug
  metrics:
    endpoint: 0.0.0.0:8888

extensions:
  healthcheck:
    endpoint: 0.0.0.0:13133
    livePath: /livez
    readyPath: /readyz

  oauth2client/flightctl:
    clientIdFile: %[1]s
    clientSecretFile: %[2]s
    tokenUrl: %[3]s
    scopes:
      - openid
    timeout: 30s
    expiryBuffer: 30s

sources:
`,
		deployment.OAuthClientIDFile,
		deployment.OAuthClientSecretFile,
		tokenURL,
	)

	for _, name := range allScenarios {
		sc := res.scenario(name)
		fmt.Fprintf(&config, `  %[1]s:
    endpoint: %[2]s
    catalog: %[3]s
    pollInterval: %[4]s
    requestTimeout: 30s
    collectionTimeout: 2m
    pageSize: 100
    selection:
      modelFilter: "%[5]s"
      versionFilter: "state='LIVE'"
    backoff:
      initialInterval: %[6]s
      maxInterval: %[6]s
      multiplier: 1
      randomizationFactor: 0
`,
			sc.sourceID,
			modelRegistryEndpoint,
			sc.catalogName,
			collectorPollInterval,
			sc.modelFilter(),
			collectorFailureRetryInterval,
		)
	}

	fmt.Fprintf(&config, `
destinations:
  flightctl/service:
    server: %[1]s
    auth:
      authenticator: oauth2client/flightctl
    certificateAuthority: %[2]s
    timeout: 30s

pipelines:
`,
		deployment.FlightControlServer,
		deployment.CertificateAuthorityFile,
	)

	for _, name := range allScenarios {
		sc := res.scenario(name)
		fmt.Fprintf(&config, `  %[1]s:
    source: %[2]s
    destination: flightctl/service
`,
			sc.pipelineName,
			sc.sourceID,
		)
	}

	return config.String()
}

// versionSpec describes one LIVE ModelVersion to seed, with the single
// ModelCar artifact attached to it.
type versionSpec struct {
	// name is the SemVer version name.
	name string
	// uri is the artifact URI written to the registry.
	uri string
	// digest is the immutable reference the published CatalogItem version is
	// expected to carry, or empty when uri is deliberately not usable.
	digest string
}

// eligibleVersion describes a version the collector must be able to import.
func eligibleVersion(res *testResources, sc *scenario, name string) versionSpec {
	digest := res.digest(string(sc.name), name)
	return versionSpec{name: name, uri: artifactURI(digest), digest: digest}
}

// seedModel creates one LIVE registered model carrying this scenario's
// selector property, with one LIVE version and one LIVE ModelCar artifact per
// entry of versions.
//
// Every model is recorded in the suite's fixture list as soon as the server
// accepts it, including when a later step of the same call fails, so teardown
// retires partially created content as well.
func seedModel(
	ctx context.Context,
	registry *auxiliary.ModelRegistry,
	res *testResources,
	sc *scenario,
	local string,
	description string,
	versions []versionSpec,
) (*seededModel, error) {
	created, err := registry.CreateRegisteredModel(ctx, auxiliary.RegisteredModel{
		Name:        res.modelName(sc, local),
		Description: description,
		Owner:       modelOwner,
		CustomProperties: map[string]auxiliary.MetadataStringValue{
			scenarioProperty: auxiliary.StringProperty(sc.selector),
		},
	})
	if err != nil {
		return nil, err
	}

	model := &seededModel{scenario: sc, id: created.ID, name: created.Name}
	recordSeededModel(model)

	for _, spec := range versions {
		if _, err := addModelVersion(ctx, registry, model, spec); err != nil {
			return nil, err
		}
	}
	return model, nil
}

// addModelVersion attaches one more LIVE version, with its single ModelCar
// artifact, to an already seeded model.
func addModelVersion(
	ctx context.Context,
	registry *auxiliary.ModelRegistry,
	model *seededModel,
	spec versionSpec,
) (*seededVersion, error) {
	created, err := registry.CreateModelVersion(ctx, model.id, auxiliary.ModelVersion{
		Name:   spec.name,
		Author: modelOwner,
	})
	if err != nil {
		return nil, err
	}

	version := &seededVersion{id: created.ID, name: spec.name, uri: spec.uri, digest: spec.digest}
	// Recorded before the artifact call so a version whose artifact could not
	// be attached is still archived by teardown.
	model.versions = append(model.versions, version)

	if _, err := registry.CreateModelArtifact(ctx, version.id, auxiliary.ModelArtifact{
		Name:            fmt.Sprintf("%s-%s-modelcar", model.name, strings.ReplaceAll(spec.name, ".", "-")),
		URI:             spec.uri,
		ModelFormatName: "modelcar",
	}); err != nil {
		return nil, err
	}
	return version, nil
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

// collectorLogLines returns the collector's most recent log lines.
//
// The bound is the same every time, so a caller comparing two readings is
// comparing the same window rather than a growing one. See
// waitForRejectedCollections for what that means for a count taken relative to
// an earlier one.
func collectorLogLines(ctx context.Context, instance infra.CollectorInstance) ([]string, error) {
	logs, err := instance.Logs(ctx, collectorLogTailLines)
	if err != nil {
		return nil, err
	}
	return strings.Split(logs, "\n"), nil
}

// countRejectedCollections counts the log lines reporting a rejected
// collection cycle for one scenario's source.
//
// Both the source component ID and the message have to be on the line, so a
// failure belonging to another scenario's source can never be counted as this
// one's. detail, when set, must appear as well; the scenarios use it to pin
// the rejection to the fixture they introduced rather than to some unrelated
// error that happens to fail the same cycle.
func countRejectedCollections(lines []string, sc *scenario, detail string) int {
	count := 0
	for _, line := range lines {
		if !strings.Contains(line, collectionFailedMessage) {
			continue
		}
		if !strings.Contains(line, sc.sourceID) {
			continue
		}
		if detail != "" && !strings.Contains(line, detail) {
			continue
		}
		count++
	}
	return count
}

// dumpDiagnostics writes the deployment's own diagnostics plus the current
// catalog resources to the Ginkgo output. Called only on failure, while the
// collector is still up, so a failed assertion is accompanied by the state
// that produced it.
func dumpDiagnostics(ctx context.Context, instance infra.CollectorInstance, harness *e2e.Harness) {
	if instance != nil {
		GinkgoWriter.Printf("[catalogcollector] %s\n", instance.Diagnostics(ctx))
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
