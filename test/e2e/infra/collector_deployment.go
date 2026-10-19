// Package infra provides testcontainers-based infrastructure for E2E tests.
package infra

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment variables that select the catalog collector image under test and
// how strictly the run requires it.
//
// They are read here rather than in a suite because the answer they produce -
// "can this deployment run a catalog collector at all, and must it" - is a
// property of the deployment, not of any particular set of specs.
const (
	// EnvCollectorImage names the exact collector image to deploy. CI sets it
	// to the image produced by the build the run is testing.
	EnvCollectorImage = "E2E_CATALOG_COLLECTOR_IMAGE"
	// EnvCollectorImagePreloaded declares that EnvCollectorImage is already in
	// the deployment's image store, so it must neither be looked for in a
	// local container runtime nor loaded again.
	EnvCollectorImagePreloaded = "E2E_CATALOG_COLLECTOR_IMAGE_PRELOADED"
	// EnvCollectorRequired turns an unavailable collector deployment into a
	// failure instead of a skip. CI sets it so that a build which stops
	// shipping the collector image fails loudly rather than quietly reducing
	// the suite to zero executed specs.
	EnvCollectorRequired = "E2E_CATALOG_COLLECTOR_REQUIRED"
)

// ErrCollectorDeploymentUnsupported is returned by CollectorDeployer
// implementations for deployment types that cannot host a catalog collector.
// Callers branch on this instead of on the implementation type.
var ErrCollectorDeploymentUnsupported = errors.New("catalog collector deployment is not supported for this deployment type")

// CollectorAvailability reports whether a catalog collector can be deployed
// here, and what an unavailable deployment means for the run.
//
// It is deliberately separate from the error a deployer returns. An error means
// the question could not be answered - a broken container runtime, an
// unparseable setting - and always fails the caller. This value answers the
// question successfully; Available simply says no.
type CollectorAvailability struct {
	// Available reports that Start can be attempted.
	Available bool
	// Required reports that the run explicitly asked for a collector
	// deployment, so an unavailable one must fail rather than skip.
	Required bool
	// Reason explains an unavailable deployment in terms a caller can print,
	// both as a skip message and as a failure message. Empty when Available.
	Reason string
}

// CollectorImageSelection is the collector image policy the run was given. It
// is the generic half of image resolution: which image was named, whether the
// deployment already holds it, and whether its absence is fatal. Finding an
// image that was not named is deployment-specific and belongs to the
// implementation.
type CollectorImageSelection struct {
	// Reference is the image the run named, or empty when it named none.
	Reference string
	// Preloaded reports that Reference is already in the deployment's image
	// store and must not be loaded from a local container runtime.
	Preloaded bool
	// Required reports that a missing image must fail rather than skip. An
	// explicitly named image is required by default: whoever named it meant
	// the collector to be deployed from it.
	Required bool
}

// CollectorImageFromEnv reads the image policy the run was given.
//
// A setting that is present but unparseable is an error rather than a silent
// default, because a typo there would quietly change how the run behaves.
func CollectorImageFromEnv() (CollectorImageSelection, error) {
	preloaded, err := ParseBoolEnv(EnvCollectorImagePreloaded, false)
	if err != nil {
		return CollectorImageSelection{}, err
	}
	reference := strings.TrimSpace(os.Getenv(EnvCollectorImage))
	required, err := ParseBoolEnv(EnvCollectorRequired, reference != "")
	if err != nil {
		return CollectorImageSelection{}, err
	}
	return CollectorImageSelection{Reference: reference, Preloaded: preloaded, Required: required}, nil
}

// CollectorDeploymentRequired reports whether the run requires a catalog
// collector deployment, so that an unavailable one fails instead of skipping.
func CollectorDeploymentRequired(fallback bool) (bool, error) {
	return ParseBoolEnv(EnvCollectorRequired, fallback)
}

// ParseBoolEnv reads an optional boolean environment variable. An unset or
// empty variable yields fallback; a value that is set but not a boolean is an
// error rather than a silent fallback.
func ParseBoolEnv(name string, fallback bool) (bool, error) {
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

// CollectorConfigContext is everything deployment-specific a caller needs in
// order to render a collector configuration: how the collector reaches Flight
// Control from inside the deployment, and where the deployment places the
// credentials and trust material it is handed.
//
// These are paths and URLs inside the collector's own runtime, so they stay
// generic: a different deployment type answers with different values, and the
// caller never learns how either was produced.
type CollectorConfigContext struct {
	// FlightControlServer is the base URL of the Flight Control API as the
	// collector reaches it from inside the deployment.
	FlightControlServer string
	// OAuthClientIDFile is where the deployment places
	// CollectorDeploymentConfig.OAuthClientID.
	OAuthClientIDFile string
	// OAuthClientSecretFile is where the deployment places
	// CollectorDeploymentConfig.OAuthClientSecret.
	OAuthClientSecretFile string
	// CertificateAuthorityFile is where the deployment places the Flight
	// Control CA bundle it discovered, for the collector to verify the
	// endpoint against.
	CertificateAuthorityFile string
}

// CollectorDeploymentConfig is the generic input to one collector deployment.
//
// It carries only what the caller owns: an identity for the instance, the
// configuration document to run, the credentials to mount, and the bounds on
// how long starting and stopping may take. Namespaces, releases, charts,
// images, selectors, and trust material are the implementation's business.
type CollectorDeploymentConfig struct {
	// InstanceID is a short logical identifier, unique per run, that the
	// implementation derives its own object names from so concurrent runs and
	// reruns against a reused deployment never collide.
	InstanceID string
	// Config is the collector configuration document to run.
	Config []byte
	// OAuthClientID and OAuthClientSecret are the client credentials the
	// collector authenticates to Flight Control with. They are placed at the
	// paths CollectorConfigContext reports.
	OAuthClientID     string
	OAuthClientSecret string
	// StartTimeout bounds starting the collector, including waiting for it to
	// come up. Must be positive.
	StartTimeout time.Duration
	// StopTimeout bounds each step of stopping it. Must be positive.
	StopTimeout time.Duration
}

// CollectorDeployer deploys the catalog collector into the environment under
// test.
//
// It is deliberately specific to the collector rather than a general deployment
// mechanism: every method exists because a caller needs exactly that answer,
// and none of them exposes how the deployment is actually performed. Callers
// never touch a container runtime, a cluster client, or a packaging tool.
type CollectorDeployer interface {
	// Available reports whether a collector can be deployed here.
	//
	// A nil error with Available false is a settled answer: the caller skips,
	// or fails when Required. A non-nil error means the question could not be
	// answered and always fails the caller, so a broken environment is never
	// silently reported as an unsupported one.
	Available(ctx context.Context) (CollectorAvailability, error)

	// ConfigContext returns the deployment-specific values a caller needs to
	// render a collector configuration. It is called before Start, because its
	// result is an input to the configuration Start is given.
	ConfigContext(ctx context.Context) (CollectorConfigContext, error)

	// Start deploys a collector and waits for it to come up.
	//
	// The returned instance is non-nil whenever anything was created, including
	// when the error is non-nil: a start that failed partway still leaves state
	// the caller has to tear down, and the instance is the only handle to it.
	Start(ctx context.Context, config CollectorDeploymentConfig) (CollectorInstance, error)
}

// CollectorInstance is one deployed collector.
type CollectorInstance interface {
	// WaitReady blocks until the collector reports itself ready, or the
	// timeout elapses.
	WaitReady(ctx context.Context, timeout time.Duration) error

	// Logs returns the collector's most recent output, bounded to the last
	// tailLines lines.
	//
	// Failures are returned rather than folded into the result: a caller that
	// waits for the collector to report something has to be able to tell "not
	// logged yet" from "the logs could not be read", and an empty,
	// successful-looking result would conflate the two.
	Logs(ctx context.Context, tailLines int64) (string, error)

	// Diagnostics returns a best-effort, human-readable dump of the
	// collector's state. It never fails: it is called on a failing path, where
	// an error would replace the information the caller needs.
	Diagnostics(ctx context.Context) string

	// Stop takes the collector down so it can no longer act on anything.
	//
	// It answers two different questions through two channels, because they
	// can disagree. stopped reports whether the collector is confirmed to be
	// down, which is what a caller keys dependent teardown off. The error
	// reports whether stopping left instance-owned state behind, which is a
	// problem in its own right. An implementation that had to fall back to a
	// blunter mechanism returns stopped true together with the original
	// failure; only when nothing confirmed the collector is down is stopped
	// false.
	Stop(ctx context.Context) (stopped bool, err error)

	// Cleanup removes whatever the instance still owns. It is idempotent and
	// safe to call after Stop, after a failed Start, and more than once.
	Cleanup(ctx context.Context) error
}

// unsupportedCollectorDeployer is the CollectorDeployer for deployment types
// that cannot host a catalog collector. The factory installs it rather than
// leaving the provider nil, so callers ask the same question everywhere and
// never branch on the deployment type themselves.
type unsupportedCollectorDeployer struct {
	deploymentType string
}

// NewUnsupportedCollectorDeployer returns a deployer that reports the catalog
// collector cannot be deployed on deploymentType.
func NewUnsupportedCollectorDeployer(deploymentType string) CollectorDeployer {
	return &unsupportedCollectorDeployer{deploymentType: deploymentType}
}

func (d *unsupportedCollectorDeployer) Available(context.Context) (CollectorAvailability, error) {
	// Required is still read from the environment: a run that explicitly asked
	// for a collector must fail here rather than skip, exactly as it would for
	// a supported deployment that cannot find an image.
	required, err := CollectorDeploymentRequired(false)
	if err != nil {
		return CollectorAvailability{}, err
	}
	return CollectorAvailability{
		Required: required,
		Reason: fmt.Sprintf("%s (deployment type %q)",
			ErrCollectorDeploymentUnsupported.Error(), d.deploymentType),
	}, nil
}

func (d *unsupportedCollectorDeployer) ConfigContext(context.Context) (CollectorConfigContext, error) {
	return CollectorConfigContext{}, d.err()
}

func (d *unsupportedCollectorDeployer) Start(context.Context, CollectorDeploymentConfig) (CollectorInstance, error) {
	// No instance: nothing was created, so there is nothing to tear down.
	return nil, d.err()
}

func (d *unsupportedCollectorDeployer) err() error {
	return fmt.Errorf("%w: deployment type %q", ErrCollectorDeploymentUnsupported, d.deploymentType)
}
