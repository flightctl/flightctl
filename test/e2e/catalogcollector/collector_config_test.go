package catalogcollector_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/catalogcollector/config"
	"github.com/flightctl/flightctl/pkg/catalogcollector/destination/flightctldestination"
	"github.com/flightctl/flightctl/pkg/catalogcollector/source/kubeflowmodelregistrysource"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/stretchr/testify/require"
)

// These are plain Go tests: they cover the configuration this suite generates
// and the log attribution it asserts on, both of which are decided before any
// cluster is involved. Run them with
//
//	go test ./test/e2e/catalogcollector/ -run 'TestRenderCollectorConfig|TestCountRejectedCollections|TestNewTestResources'
//
// The rendered configuration is checked against the collector's own parser and
// the real source and destination config validators rather than against a
// hand-written expectation. A configuration this suite renders but the
// collector rejects would otherwise only surface as a CrashLoopBackOff in CI,
// where the failure is a readiness timeout rather than the actual error.

const (
	testRegistryEndpoint = "http://e2e-model-registry:8080"
	testFlightctlServer  = "https://flightctl-api.flightctl-external.svc.cluster.local:3443"
	testTokenURL         = "https://keycloak.example.com/realms/e2e/protocol/openid-connect/token"
)

// testDeploymentContext stands in for what a real deployment reports: where
// the collector reaches Flight Control from inside it, and where it places the
// credentials and CA bundle it was handed. The exact values are the
// deployment's business; what this file checks is that the suite renders a
// configuration the collector accepts around whatever it is told.
var testDeploymentContext = infra.CollectorConfigContext{
	FlightControlServer:      testFlightctlServer,
	OAuthClientIDFile:        "/etc/flightctl/catalog-collector/oauth/client-id",
	OAuthClientSecretFile:    "/etc/flightctl/catalog-collector/oauth/client-secret",
	CertificateAuthorityFile: "/etc/flightctl/catalog-collector/certs/flightctl-ca.crt",
}

// parseRenderedConfig renders this suite's collector configuration for res and
// decodes it exactly as the collector would.
func parseRenderedConfig(t *testing.T, res *testResources) *config.Config {
	t.Helper()

	rendered := renderCollectorConfig(res, testDeploymentContext, testRegistryEndpoint, testTokenURL)
	parsed, err := config.Parse([]byte(rendered))
	require.NoError(t, err, "the collector must accept the configuration this suite renders:\n%s", rendered)
	return parsed
}

// decodeSource decodes one rendered source entry through the real factory
// default and validator, and returns the typed configuration.
func decodeSource(t *testing.T, parsed *config.Config, sourceID string) *kubeflowmodelregistrysource.Config {
	t.Helper()

	component, found := parsed.Sources[sourceID]
	require.True(t, found, "source %q must be configured", sourceID)

	defaults := kubeflowmodelregistrysource.NewFactory().CreateDefaultConfig()
	require.NoError(t, config.DecodeComponent(component.Config, defaults, "sources."+sourceID),
		"source %q must be a valid Model Registry source configuration", sourceID)

	typed, ok := defaults.(*kubeflowmodelregistrysource.Config)
	require.True(t, ok, "unexpected source config type %T", defaults)
	return typed
}

// TestRenderCollectorConfigIsAcceptedByTheCollector checks that the rendered
// configuration parses, that every component it references resolves, and that
// the source and destination bodies pass their own validators.
func TestRenderCollectorConfigIsAcceptedByTheCollector(t *testing.T) {
	res, err := newTestResources()
	require.NoError(t, err)

	parsed := parseRenderedConfig(t, res)

	require.Len(t, parsed.Sources, len(allScenarios), "one source per scenario")
	require.Len(t, parsed.Pipelines, len(allScenarios), "one pipeline per scenario")
	require.Len(t, parsed.Destinations, 1, "the destination is shared by every pipeline")

	destination, found := parsed.Destinations["flightctl/service"]
	require.True(t, found, "the Flight Control destination must be configured")
	destinationDefaults := flightctldestination.NewFactory().CreateDefaultConfig()
	require.NoError(t, config.DecodeComponent(destination.Config, destinationDefaults, "destinations.flightctl/service"),
		"the destination must be a valid Flight Control destination configuration")

	for _, name := range allScenarios {
		sc := res.scenario(name)

		pipeline, found := parsed.Pipelines[sc.pipelineName]
		require.True(t, found, "pipeline %q must be configured", sc.pipelineName)
		require.Equal(t, sc.sourceID, pipeline.Source,
			"pipeline %q must read from its own source", sc.pipelineName)
		require.Equal(t, "flightctl/service", pipeline.Destination)
		require.Empty(t, pipeline.Processors, "the suite maps registry content straight through")

		source := decodeSource(t, parsed, sc.sourceID)
		require.Equal(t, sc.catalogName, source.Catalog,
			"source %q must write to its own catalog", sc.sourceID)
		require.Equal(t, testRegistryEndpoint, source.Endpoint)
		require.NotNil(t, source.Selection)
		require.NotNil(t, source.Selection.ModelFilter)
		require.Equal(t, sc.modelFilter(), *source.Selection.ModelFilter)
		require.NotNil(t, source.Selection.VersionFilter)
		require.Equal(t, "state='LIVE'", *source.Selection.VersionFilter)

		// The retry cadence of a failing source is pinned flat rather than
		// left on the shipped default, which doubles from one second towards
		// five minutes. The failed-collection scenario waits for rejected
		// cycles before and after it changes registry content, and under the
		// default those later attempts would be minutes apart, so the wait
		// would be bounded by the backoff rather than by anything the
		// collector did.
		require.NoError(t, source.Backoff.Validate(),
			"source %q must render a backoff the collector accepts", sc.sourceID)
		require.Equal(t, util.Duration(collectorFailureRetryInterval), source.Backoff.InitialInterval)
		require.Equal(t, util.Duration(collectorFailureRetryInterval), source.Backoff.MaxInterval,
			"a failing source must not drift away from the pinned interval")
		require.Equal(t, 1.0, source.Backoff.Multiplier,
			"the interval must not grow between consecutive failures")
		require.Zero(t, source.Backoff.RandomizationFactor,
			"jitter would make the observed retry cadence unpredictable")
	}
}

// TestRenderCollectorConfigIsolatesScenarios pins the property the specs rely
// on to be independent of each other and of execution order: no two scenarios
// share a selection, a catalog, a pipeline, or a source, and no scenario's
// selection can match another's seeded content.
func TestRenderCollectorConfigIsolatesScenarios(t *testing.T) {
	res, err := newTestResources()
	require.NoError(t, err)

	parsed := parseRenderedConfig(t, res)

	seen := map[string]string{}
	requireUnique := func(kind, value string, scenario scenarioName) {
		t.Helper()
		key := kind + "=" + value
		previous, duplicate := seen[key]
		require.False(t, duplicate, "scenarios %s and %s share %s %q", previous, scenario, kind, value)
		seen[key] = string(scenario)
	}

	for _, name := range allScenarios {
		sc := res.scenario(name)
		requireUnique("selector", sc.selector, name)
		requireUnique("catalog", sc.catalogName, name)
		requireUnique("pipeline", sc.pipelineName, name)
		requireUnique("source", sc.sourceID, name)
	}

	// The selection is what keeps one scenario's registry content out of
	// another's collection cycle, so each filter must name its own selector
	// and no other's. Without this, the deliberately unusable fixture the
	// failed-collection scenario seeds would fail every scenario's cycle.
	for _, name := range allScenarios {
		sc := res.scenario(name)
		filter := *decodeSource(t, parsed, sc.sourceID).Selection.ModelFilter

		require.Contains(t, filter, fmt.Sprintf("%s.string_value = '%s'", scenarioProperty, sc.selector),
			"scenario %s must select on its own property value", name)
		require.Contains(t, filter, "state='LIVE'", "scenario %s must still select LIVE models only", name)

		for _, other := range allScenarios {
			if other == name {
				continue
			}
			require.NotContains(t, filter, res.scenario(other).selector,
				"scenario %s must not select scenario %s's content", name, other)
		}
	}
}

// TestNewTestResourcesDerivesDistinctFixtures covers the per-run identifiers
// the scenarios seed with: a model name must be unique per scenario, and a
// version digest must be unique per scenario and version while remaining a
// well-formed immutable OCI reference.
func TestNewTestResourcesDerivesDistinctFixtures(t *testing.T) {
	res, err := newTestResources()
	require.NoError(t, err)

	// Matches the immutable reference format the collector requires; see
	// ociDigestRe in the Model Registry source.
	digestFormat := regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	// A registered model name has to survive the source's normalization
	// unchanged for the expected catalog item name to be exact.
	dnsSubdomain := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

	names := map[string]scenarioName{}
	digests := map[string]string{}

	for _, name := range allScenarios {
		sc := res.scenario(name)

		// Every local name any scenario seeds with. The failed-collection
		// scenario needs two published models, so that it can change one and
		// archive the other while the cycle is failing.
		for _, local := range []string{"model", "model-a", "model-b", "broken"} {
			modelName := res.modelName(sc, local)
			require.Regexp(t, dnsSubdomain, modelName)
			previous, duplicate := names[modelName]
			require.False(t, duplicate, "scenarios %s and %s would seed the same model %q", previous, name, modelName)
			names[modelName] = name
		}

		for _, version := range []string{"1.0.0", "2.0.0", "2.1.0"} {
			seeded := eligibleVersion(res, sc, version)
			require.Regexp(t, digestFormat, seeded.digest)
			require.Equal(t, artifactURI(seeded.digest), seeded.uri)
			require.True(t, strings.HasPrefix(seeded.uri, "oci://"+artifactRepository+"@"),
				"every version must point at the one repository a catalog item can store")

			key := string(name) + "/" + version
			previous, duplicate := digests[seeded.digest]
			require.False(t, duplicate, "%s and %s would be pinned to the same digest", previous, key)
			digests[seeded.digest] = key
		}
	}
}

// TestCountRejectedCollections covers the attribution rules behind the
// failed-collection evidence: a rejection is only counted for the source that
// logged it, and only when it mentions the fixture the spec introduced.
func TestCountRejectedCollections(t *testing.T) {
	res, err := newTestResources()
	require.NoError(t, err)

	safety := res.scenario(scenarioSafety)
	update := res.scenario(scenarioUpdate)

	// Shaped like the collector's logrus text output, which carries the
	// component ID and the message on one line.
	logLine := func(sourceID, message, detail string) string {
		return fmt.Sprintf(
			`time="2026-01-01T00:00:00Z" level=error msg=%q component_id=%s component_kind=source error=%q retry_after=2s`,
			message, sourceID, detail)
	}

	const brokenModel = "e2e-cc-safety-broken-abcdef"

	tests := []struct {
		name  string
		lines []string
		want  int
	}{
		{
			name:  "When nothing failed it should count no rejection",
			lines: []string{logLine(safety.sourceID, "collection complete", "")},
			want:  0,
		},
		{
			name: "When this source rejected the fixture it should count every attempt",
			lines: []string{
				logLine(safety.sourceID, collectionFailedMessage, "model "+brokenModel+" has an invalid immutable OCI reference"),
				logLine(safety.sourceID, collectionFailedMessage, "model "+brokenModel+" has an invalid immutable OCI reference"),
			},
			want: 2,
		},
		{
			name: "When another source failed it should not count that rejection",
			// Attribution matters: a failure in a different pipeline says
			// nothing about the content this spec is guarding.
			lines: []string{logLine(update.sourceID, collectionFailedMessage, "model "+brokenModel+" is broken")},
			want:  0,
		},
		{
			name:  "When this source failed on something else it should not count that rejection",
			lines: []string{logLine(safety.sourceID, collectionFailedMessage, "connection refused")},
			want:  0,
		},
		{
			name:  "When the message is a downstream failure it should not count it as a rejected collection",
			lines: []string{logLine(safety.sourceID, "downstream consumption failed; applying backoff", brokenModel)},
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, countRejectedCollections(tt.lines, safety, brokenModel))
		})
	}
}

// TestCountRejectedCollectionsWithoutDetail covers the detail-less form, where
// any rejection by the named source counts.
func TestCountRejectedCollectionsWithoutDetail(t *testing.T) {
	res, err := newTestResources()
	require.NoError(t, err)

	safety := res.scenario(scenarioSafety)
	lines := []string{
		fmt.Sprintf("msg=%q component_id=%s", collectionFailedMessage, safety.sourceID),
		fmt.Sprintf("msg=%q component_id=%s", collectionFailedMessage, res.scenario(scenarioSmoke).sourceID),
	}

	require.Equal(t, 1, countRejectedCollections(lines, safety, ""))
}
