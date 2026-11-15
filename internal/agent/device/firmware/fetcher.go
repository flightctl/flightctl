package firmware

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/firmwarepolicy"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/flightctl/flightctl/pkg/log"
)

// RegistryAuthFile is the containers auth file used for updater image pulls.
const RegistryAuthFile = "/root/.config/containers/auth.json"

// policyRejectionMarker is how containers-image reports a pull refused by the
// signature policy.
const policyRejectionMarker = "Source image rejected"

// VerifiedPackage identifies updater distribution content that passed
// authorization, publisher-signature, image-digest, closed-manifest and
// descriptor-binding, dependency-free and declared-access checks. It does not
// authorize execution: payload bytes have not been verified against the
// manifest file list, nothing has been extracted, and declared access is not
// runtime confinement.
type VerifiedPackage struct {
	Name, Version    string
	DescriptorDigest string
	PackageDigest    string
	// Reference is the digest-pinned image reference <repository>@<packageDigest>.
	Reference             string
	PublisherPolicyDigest string
	ManifestDigest        string
	// PolicyRevision is the accepted policy revision under which this fetch
	// found the release approved.
	PolicyRevision int64
	Manifest       Manifest
	// Cached reports that a verified record and the present image were reused.
	Cached bool
}

// Fetcher obtains and authenticates approved updater code without executing
// it. Registry credentials resolve only while the pull config resolver is
// initialized for an update, so Fetch belongs inside that update lifecycle.
type Fetcher struct {
	dataDir    string
	podman     *client.Podman
	receiver   *firmwarepolicy.Manager
	pullConfig dependency.PullConfigResolver
	rw         fileio.ReadWriter
	log        *log.PrefixLogger
	authFile   string
	settings   atomic.Pointer[settings]
}

// settings are the reloadable agent configuration values a fetch uses. Each
// fetch reads one snapshot so that they come from the same configuration.
type settings struct {
	access      config.UpdaterAccessPolicy
	pullTimeout time.Duration
}

func settingsFrom(cfg *config.Config) *settings {
	return &settings{access: cfg.Firmware.UpdaterAccess, pullTimeout: time.Duration(cfg.PullTimeout)}
}

// FetcherOption configures a Fetcher.
type FetcherOption func(*Fetcher)

// WithRegistryAuthFile overrides the containers auth file resolved for pulls.
func WithRegistryAuthFile(path string) FetcherOption {
	return func(f *Fetcher) { f.authFile = path }
}

// NewFetcher returns a fetcher; required dependencies must be non-nil.
func NewFetcher(dataDir string, podman *client.Podman, receiver *firmwarepolicy.Manager,
	pullConfig dependency.PullConfigResolver, rw fileio.ReadWriter, cfg *config.Config,
	log *log.PrefixLogger, opts ...FetcherOption) (*Fetcher, error) {
	if !filepath.IsAbs(dataDir) || podman == nil || receiver == nil || pullConfig == nil || rw == nil || cfg == nil || log == nil {
		return nil, errors.New("updater fetcher requires an absolute data directory, podman client, policy receiver, pull config resolver, fileio, configuration and logger")
	}
	f := &Fetcher{dataDir: dataDir, podman: podman, receiver: receiver, pullConfig: pullConfig, rw: rw, log: log, authFile: RegistryAuthFile}
	f.settings.Store(settingsFrom(cfg))
	for _, opt := range opts {
		opt(f)
	}
	return f, nil
}

// ReloadConfig replaces the local access policy and pull timeout after an
// agent configuration reload.
func (f *Fetcher) ReloadConfig(ctx context.Context, cfg *config.Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cfg == nil {
		return errors.New("updater fetcher requires a configuration")
	}
	f.settings.Store(settingsFrom(cfg))
	return nil
}

// Fetch returns the verified package for the approved release, pulling and
// authenticating it when no valid verified record exists. It never executes
// image content.
func (f *Fetcher) Fetch(ctx context.Context, name, version string) (*VerifiedPackage, error) {
	release := name + "/" + version
	s := f.settings.Load()
	view, binding, err := f.approvedBinding(name, version)
	if err != nil {
		return nil, unavailable(release, false, err)
	}
	d, err := policy.DecodeDescriptor(binding)
	if err != nil {
		return nil, unavailable(release, false, err)
	}
	if d.Protocol != SupportedProtocol {
		return nil, notSupported(release, fmt.Errorf("unsupported updater protocol %d", d.Protocol))
	}
	if err := CheckDependencies(d.Dependencies); err != nil {
		return nil, notSupported(release, err)
	}
	if err := CheckAccess(d.Access, s.access); err != nil {
		return nil, notSupported(release, err)
	}
	if pkg, ok := f.cachedPackage(ctx, view.Revision, binding.DescriptorDigest, d); ok {
		return pkg, nil
	}
	return f.fetch(ctx, s, view.Revision, binding.DescriptorDigest, d)
}

func (f *Fetcher) approvedBinding(name, version string) (*policy.View, policy.Binding, error) {
	view, err := f.receiver.Current()
	if err != nil {
		return nil, policy.Binding{}, fmt.Errorf("approved updater policy is unavailable: %w", err)
	}
	document, err := policy.Decode(*view)
	if err != nil {
		return nil, policy.Binding{}, fmt.Errorf("approved updater policy is unavailable: %w", err)
	}
	i := slices.IndexFunc(document.Bindings, func(b policy.Binding) bool { return b.Name == name && b.Version == version })
	if i < 0 {
		return nil, policy.Binding{}, errors.New("release is not in the approved updater policy")
	}
	binding := document.Bindings[i]
	if !f.receiver.IsApproved(name, version, binding.DescriptorDigest) {
		return nil, policy.Binding{}, errors.New("release is not approved or has been revoked")
	}
	return view, binding, nil
}

func (f *Fetcher) fetch(ctx context.Context, s *settings, revision int64, descriptorDigest string, d *policy.ReleaseDescriptor) (*VerifiedPackage, error) {
	release := d.Name + "/" + d.Version
	reference := d.PackageReference()
	if err := f.pull(ctx, s, descriptorDigest, d); err != nil {
		return nil, err
	}
	digest, err := f.podman.ImageDigest(ctx, reference)
	if err != nil {
		return nil, unavailable(release, false, fmt.Errorf("inspect %s: %w", reference, sanitizedCause(err)))
	}
	if digest != d.PackageDigest {
		return nil, unavailable(release, false, fmt.Errorf("pulled image digest %q differs from the approved package digest %s", digest, d.PackageDigest))
	}
	data, err := f.readManifest(ctx, reference)
	if err != nil {
		return nil, unavailable(release, false, err)
	}
	manifest, err := ParseManifest(data)
	if err != nil {
		return nil, unavailable(release, false, err)
	}
	if err := VerifyManifestBinding(manifest, d); err != nil {
		return nil, unavailable(release, false, err)
	}
	pkg := &VerifiedPackage{
		Name:                  d.Name,
		Version:               d.Version,
		DescriptorDigest:      descriptorDigest,
		PackageDigest:         d.PackageDigest,
		Reference:             reference,
		PublisherPolicyDigest: d.Publisher.PolicyDigest,
		ManifestDigest:        policy.Hash(data),
		PolicyRevision:        revision,
		Manifest:              *manifest,
	}
	if err := f.writeRecord(pkg); err != nil {
		return nil, unavailable(release, false, fmt.Errorf("record verified updater package: %w", err))
	}
	f.log.Infof("Verified updater %s package %s", release, reference)
	return pkg, nil
}

// pull fetches the digest-pinned package under a per-call signature policy
// that admits only the approved publisher, removing the policy afterwards.
// The agent pull timeout bounds each pull attempt.
func (f *Fetcher) pull(ctx context.Context, s *settings, descriptorDigest string, d *policy.ReleaseDescriptor) error {
	release := d.Name + "/" + d.Version
	reference := d.PackageReference()
	signaturePolicy, err := SignaturePolicy(d)
	if err != nil {
		return unavailable(release, false, err)
	}
	policyPath := filepath.Join(f.dataDir, "firmware", "signature-policy", digestHex(descriptorDigest)+".json")
	if err := f.rw.WriteFileDurable(policyPath, signaturePolicy, 0o700, 0o600); err != nil {
		return unavailable(release, false, fmt.Errorf("write signature policy: %w", err))
	}
	defer func() {
		if err := f.rw.RemoveFile(policyPath); err != nil {
			f.log.Warnf("Failed to remove signature policy for updater %s: %v", release, err)
		}
	}()

	opts := f.pullConfig.Options(dependency.PullConfigSpec{Paths: []string{f.authFile}, OptionFn: client.WithPullSecret})()
	opts = append(opts, client.WithSignaturePolicy(f.rw.PathFor(policyPath)))
	if s.pullTimeout > 0 {
		opts = append(opts, client.Timeout(s.pullTimeout))
	}
	if _, err := f.podman.Pull(ctx, reference, opts...); err != nil {
		cause := sanitizedCause(err)
		if strings.Contains(err.Error(), policyRejectionMarker) {
			f.log.Warnf("Signature policy rejected updater %s image %s: %v", release, reference, cause)
			return signatureRejected(d.Name, d.Version, reference, d.Publisher.Policy.SignedIdentity.DockerRepository, cause)
		}
		return unavailable(release, true, fmt.Errorf("pull %s: %w", reference, cause))
	}
	return nil
}

// readManifest reads the manifest from a read-only image mount without
// following links, then unmounts the image.
func (f *Fetcher) readManifest(ctx context.Context, reference string) ([]byte, error) {
	mountPoint, err := f.podman.Mount(ctx, reference)
	if err != nil {
		return nil, fmt.Errorf("mount %s: %w", reference, sanitizedCause(err))
	}
	defer func() {
		if err := f.podman.Unmount(context.WithoutCancel(ctx), reference); err != nil {
			f.log.Warnf("Failed to unmount updater image %s: %v", reference, sanitizedCause(err))
		}
	}()
	data, err := f.rw.ReadFileInRoot(mountPoint, ManifestPath, MaxManifestBytes)
	if err != nil {
		return nil, fmt.Errorf("read %s from %s: %w", ManifestPath, reference, err)
	}
	return data, nil
}

func digestHex(digest string) string { return strings.TrimPrefix(digest, "sha256:") }
