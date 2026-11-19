package firmware

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"runtime"
	"strings"
	"time"

	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	// DefaultUnmountTimeout bounds the source unmount after staging. It runs
	// even when the caller's context is canceled or the extraction deadline
	// has expired.
	DefaultUnmountTimeout = 2 * time.Minute
	// DefaultStagingCleanupTimeout bounds discarding an unpublished staging
	// directory, independently of the caller's context.
	DefaultStagingCleanupTimeout = 2 * time.Minute
)

// ImageMounter mounts digest-pinned images and releases them.
type ImageMounter interface {
	Mount(ctx context.Context, image string) (string, error)
	Unmount(ctx context.Context, image string) error
}

// ApprovalChecker reports whether a release is approved now.
type ApprovalChecker interface {
	IsApproved(name, version, digest string) bool
}

// Extractor publishes the payload of verified updater packages into the
// managed updater cache. It never runs updater code, creates containers or
// pulls images.
type Extractor struct {
	dataDir        string
	mounter        ImageMounter
	approval       ApprovalChecker
	rw             fileio.ReadWriter
	retention      *Retention
	log            *log.PrefixLogger
	ceilings       policy.ExtractionLimits
	goarch         string
	unmountTimeout time.Duration
	cleanupTimeout time.Duration
}

// ExtractorOption configures an Extractor.
type ExtractorOption func(*Extractor)

// WithExtractionCeilings sets the largest extraction budgets accepted.
func WithExtractionCeilings(c policy.ExtractionLimits) ExtractorOption {
	return func(e *Extractor) { e.ceilings = c }
}

// WithGOARCH sets the architecture that updater executables must target.
func WithGOARCH(goarch string) ExtractorOption {
	return func(e *Extractor) { e.goarch = goarch }
}

// WithUnmountTimeout sets the independent timeout of the source unmount.
func WithUnmountTimeout(d time.Duration) ExtractorOption {
	return func(e *Extractor) { e.unmountTimeout = d }
}

// WithCleanupTimeout sets the independent timeout of discarding staging.
func WithCleanupTimeout(d time.Duration) ExtractorOption {
	return func(e *Extractor) { e.cleanupTimeout = d }
}

// NewExtractor returns an Extractor for the updater cache beneath dataDir
// that retention manages.
func NewExtractor(dataDir string, mounter ImageMounter, approval ApprovalChecker, rw fileio.ReadWriter, retention *Retention, log *log.PrefixLogger, opts ...ExtractorOption) *Extractor {
	e := &Extractor{
		dataDir:        dataDir,
		mounter:        mounter,
		approval:       approval,
		rw:             rw,
		retention:      retention,
		log:            log,
		ceilings:       DefaultExtractionCeilings(),
		goarch:         runtime.GOARCH,
		unmountTimeout: DefaultUnmountTimeout,
		cleanupTimeout: DefaultStagingCleanupTimeout,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Extract publishes the payload of pkg, or reuses a verified published copy.
// Every check against the release budget, the current approval and any
// retention pin runs before the image is mounted, and approval is checked
// again once the updater cache is held: a pinned payload is only
// verified and reused, and a missing or changed pinned payload fails without
// mounting, extracting or repairing anything. A fresh extraction stages the
// payload from a read-only mount, closes the source and unmounts it under its
// own timeout before the single no-replace publication; when the final name
// is taken, the existing directory is classified and either adopted, left
// alone, or quarantined once before one retry with the same staging. A usable
// result has durable host evidence and holds its own lease until it is
// pinned or released.
func (e *Extractor) Extract(ctx context.Context, pkg *VerifiedPackage) (*PublishedPayload, error) {
	if pkg == nil {
		return nil, unavailable("", false, errors.New("no verified package"))
	}
	release := pkg.Name + "/" + pkg.Version
	if !schemaDigestPattern.MatchString(pkg.PackageDigest) || len(pkg.Reference) <= len(pkg.PackageDigest)+1 || !strings.HasSuffix(pkg.Reference, "@"+pkg.PackageDigest) {
		return nil, unavailable(release, false, fmt.Errorf("image reference %q is not pinned to package digest %s", pkg.Reference, pkg.PackageDigest))
	}
	limits, err := EffectiveExtractionLimits(pkg, e.ceilings)
	if err != nil {
		return nil, err
	}
	if _, err := planPayload(release, &pkg.Manifest, nil, limits); err != nil {
		return nil, err
	}
	if !e.approval.IsApproved(pkg.Name, pkg.Version, pkg.DescriptorDigest) {
		return nil, unavailable(release, false, errors.New("release is no longer approved"))
	}
	r := e.retention
	r.mu.Lock()
	defer r.mu.Unlock()
	if !e.approval.IsApproved(pkg.Name, pkg.Version, pkg.DescriptorDigest) {
		return nil, unavailable(release, false, errors.New("release is no longer approved"))
	}
	c, err := r.openCache(true)
	if err != nil {
		return nil, unavailable(release, false, fmt.Errorf("open the updater cache under %s: %w", e.dataDir, err))
	}
	defer c.Close()
	x := &extraction{e: e, ctx: ctx, pkg: pkg, release: release, limits: limits, cache: c, final: strings.TrimPrefix(pkg.PackageDigest, "sha256:")}
	return x.run()
}

type extraction struct {
	e          *Extractor
	ctx        context.Context
	pkg        *VerifiedPackage
	release    string
	limits     policy.ExtractionLimits
	cache      *cacheDirs
	final      string
	pending    []string
	cleanupErr string
}

func (x *extraction) run() (*PublishedPayload, error) {
	pin, pinned, err := pinFor(x.cache.firmware, x.pkg.PackageDigest)
	if err != nil {
		return nil, unavailable(x.release, false, err)
	}
	if pinned {
		if pin.ManifestDigest != x.pkg.ManifestDigest || pin.PayloadDir != x.final {
			return nil, unavailable(x.release, false, ErrPinConflict)
		}
		m, err := verifyPublishedTree(x.ctx, x.cache.updaters, x.final, pin.ManifestDigest, capLimits(pin.Limits, x.e.ceilings))
		if err != nil {
			return nil, retainedFailure(x.ctx, x.release, err)
		}
		return x.reuse(m)
	}
	if _, err := x.cache.updaters.Lstat(x.final); err == nil {
		m, verr := verifyPublishedTree(x.ctx, x.cache.updaters, x.final, x.pkg.ManifestDigest, x.limits)
		switch {
		case verr == nil:
			return x.reuse(m)
		case x.ctx.Err() != nil:
			return nil, retainedFailure(x.ctx, x.release, verr)
		case errors.Is(verr, ErrRetainedPayloadChanged):
			if err := x.quarantine(); err != nil {
				return nil, err
			}
		case !errors.Is(verr, ErrRetainedPayloadMissing):
			return nil, retainedFailure(x.ctx, x.release, verr)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, unavailable(x.release, false, err)
	}
	return x.fresh()
}

func (x *extraction) payloadFrom(m *Manifest) *PublishedPayload {
	files := make([]PublishedFile, 0, len(m.Files))
	for _, f := range m.Files {
		files = append(files, PublishedFile(f))
	}
	return &PublishedPayload{ManifestDigest: x.pkg.ManifestDigest, Dir: x.final, Entrypoint: m.Entrypoint, Files: files, Limits: x.limits, Reused: true}
}

// reuse confirms an existing verified version and records evidence for it.
func (x *extraction) reuse(m *Manifest) (*PublishedPayload, error) {
	if err := x.cache.updaters.Sync(); err != nil {
		return nil, publishFailure(x.release, PublishVisibleNotUsable, "sync-parent", ReasonUpdaterUnavailable, true, err)
	}
	if err := x.cache.updaters.Revalidate(); err != nil {
		return nil, publishFailure(x.release, PublishVisibleNotUsable, "revalidate", ReasonUpdaterUnavailable, true, err)
	}
	return x.finish(x.payloadFrom(m), false)
}

// finish records host evidence and leases the payload.
func (x *extraction) finish(p *PublishedPayload, fresh bool) (*PublishedPayload, error) {
	p.Reference, p.PackageDigest, p.DescriptorDigest = x.pkg.Reference, x.pkg.PackageDigest, x.pkg.DescriptorDigest
	event := evidenceEventReuse
	if fresh {
		event = evidenceEventPublish
	}
	id := evidenceIdentity{packageDigest: x.pkg.PackageDigest, manifestDigest: x.pkg.ManifestDigest, descriptorDigest: x.pkg.DescriptorDigest, payloadDir: x.final, entrypoint: p.Entrypoint}
	obs, err := x.e.retention.observeAndRecord(x.cache, id, event, fresh)
	if err != nil {
		return nil, publishFailure(x.release, PublishVisibleNotUsable, "evidence", ReasonUpdaterUnavailable, true, err)
	}
	p.Evidence = obs
	p.Cleanup = CleanupReport{Pending: append([]string(nil), x.pending...), Err: x.cleanupErr}
	x.e.retention.leases[x.pkg.PackageDigest]++
	p.lease = &lease{owner: x.e.retention, digest: x.pkg.PackageDigest}
	return p, nil
}

// quarantine renames an invalid, unpinned final out of the way. Cleanup
// removes it later.
func (x *extraction) quarantine() error {
	suffix, err := randomSuffix()
	if err != nil {
		return publishFailure(x.release, PublishNotVisible, "quarantine", ReasonUpdaterUnavailable, true, err)
	}
	name := quarantinePrefix + suffix
	if err := x.cache.updaters.RenameNoReplace(x.final, x.cache.updaters, name); err != nil {
		return publishFailure(x.release, PublishNotVisible, "quarantine", ReasonUpdaterUnavailable, true, err)
	}
	x.e.log.Warnf("Quarantined the invalid updater payload %s of %s as %s", x.final, x.release, name)
	x.pending = append(x.pending, name)
	return nil
}

func (x *extraction) fresh() (*PublishedPayload, error) {
	if err := checkFreeSpace(x.cache.updaters, x.release, x.limits); err != nil {
		return nil, err
	}
	xctx, cancel := context.WithTimeout(x.ctx, time.Duration(x.limits.MaxExtractionSeconds)*time.Second)
	defer cancel()
	mountPoint, err := x.e.mounter.Mount(xctx, x.pkg.Reference)
	if err != nil {
		if xctx.Err() != nil {
			return nil, contextFailure(xctx, x.release, PublishNotVisible, "stage")
		}
		return nil, publishFailure(x.release, PublishNotVisible, "stage", ReasonUpdaterUnavailable, true, fmt.Errorf("mount %s: %w", x.pkg.Reference, err))
	}
	st, stageErr := x.stage(xctx, mountPoint)
	unmountErr := x.unmount(mountPoint)
	switch {
	case stageErr != nil:
		return nil, x.discardAfterFailure(st, joinCleanup(stageErr, unmountErr))
	case unmountErr != nil:
		return nil, x.discardAfterFailure(st, &FetchError{Reason: ReasonUpdaterUnavailable, Retryable: true, Err: unmountErr})
	}
	payload, err := st.Publish(xctx, x.final)
	switch {
	case err == nil:
		return x.finish(payload, true)
	case errors.Is(err, deviceerrors.ErrDestinationExists):
		return x.contended(xctx, st)
	case isVisibleNotUsable(err):
		return nil, err
	default:
		return nil, x.discardAfterFailure(st, err)
	}
}

// stage opens the mounted image read-only, binds its manifest to the verified
// package and stages the payload. The source is closed before it returns.
func (x *extraction) stage(ctx context.Context, mountPoint string) (*StagedTree, error) {
	src, err := x.e.rw.OpenAnchor(mountPoint, fileio.DirOptions{ReadOnly: true})
	if err != nil {
		return nil, publishFailure(x.release, PublishNotVisible, "stage", ReasonUpdaterUnavailable, false, err)
	}
	defer src.Close()
	data, err := src.ReadFile(ManifestPath, x.limits.MaxExtractedManifestBytes)
	switch {
	case err != nil && ctx.Err() != nil:
		return nil, contextFailure(ctx, x.release, PublishNotVisible, "stage")
	case errors.Is(err, deviceerrors.ErrFileLimitExceeded):
		return nil, budgetFailure(x.release, "stage", "maxExtractedManifestBytes", err)
	case err != nil:
		return nil, publishFailure(x.release, PublishNotVisible, "stage", ReasonUpdaterUnavailable, false, err)
	}
	if policy.Hash(data) != x.pkg.ManifestDigest {
		return nil, publishFailure(x.release, PublishNotVisible, "stage", ReasonUpdaterUnavailable, false, errors.New("the mounted manifest differs from the verified manifest"))
	}
	if parsed, err := ParseManifest(data); err != nil || !reflect.DeepEqual(*parsed, x.pkg.Manifest) {
		return nil, publishFailure(x.release, PublishNotVisible, "stage", ReasonUpdaterUnavailable, false, errors.New("the mounted manifest does not parse to the verified manifest"))
	}
	return StageTree(ctx, src, x.cache.updaters, data, &x.pkg.Manifest, x.limits, x.e.goarch)
}

// unmount releases the source under its own timeout, which still runs when
// the caller's context is canceled or the extraction deadline has expired.
func (x *extraction) unmount(mountPoint string) error {
	uctx, cancel := context.WithTimeout(context.WithoutCancel(x.ctx), x.e.unmountTimeout)
	defer cancel()
	if err := x.e.mounter.Unmount(uctx, x.pkg.Reference); err != nil {
		x.e.log.Warnf("Unmounting updater image %s from %s failed; the mount persists until it is unmounted manually or the host reboots: %v", x.pkg.Reference, mountPoint, err)
		return fmt.Errorf("updater image %w mounted at %s: %w", deviceerrors.WithElement(x.pkg.Reference), mountPoint, &PublishError{State: PublishNotVisible, Op: "unmount", Err: errors.Join(ErrUnmountFailed, err)})
	}
	return nil
}

// joinCleanup joins cleanup failures after a primary failure while keeping
// the primary reason, retryability and exceeded budget.
func joinCleanup(primary error, cleanup ...error) error {
	var errs []error
	for _, err := range cleanup {
		if err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return primary
	}
	if fe, ok := primary.(*FetchError); ok {
		joined := *fe
		joined.Err = errors.Join(append([]error{fe.Err}, errs...)...)
		return &joined
	}
	return errors.Join(append([]error{primary}, errs...)...)
}

func (x *extraction) discard(st *StagedTree) error {
	if st == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(x.ctx), x.e.cleanupTimeout)
	defer cancel()
	if err := st.Discard(ctx); err != nil {
		x.e.log.Warnf("Discarding updater staging %s of %s failed; cleanup removes it later: %v", st.Name(), x.release, err)
		return err
	}
	return nil
}

func (x *extraction) discardAfterFailure(st *StagedTree, primary error) error {
	return joinCleanup(primary, x.discard(st))
}

func isVisibleNotUsable(err error) bool {
	var pe *PublishError
	return errors.As(err, &pe) && pe.State == PublishVisibleNotUsable
}

type winnerClass int

const (
	winnerUnknown winnerClass = iota
	winnerValid
	winnerInvalidPinned
	winnerInvalidUnpinned
)

// classify verifies the existing final against this package, re-checking the
// pin.
func (x *extraction) classify(ctx context.Context) (winnerClass, *Manifest, error) {
	pin, pinned, err := pinFor(x.cache.firmware, x.pkg.PackageDigest)
	if err != nil {
		return winnerUnknown, nil, err
	}
	digest, limits := x.pkg.ManifestDigest, x.limits
	if pinned {
		digest, limits = pin.ManifestDigest, capLimits(pin.Limits, x.e.ceilings)
	}
	m, err := verifyPublishedTree(ctx, x.cache.updaters, x.final, digest, limits)
	switch {
	case err == nil:
		return winnerValid, m, nil
	case ctx.Err() != nil || errors.Is(err, ErrRetainedPayloadMissing) || errors.Is(err, ErrRetainedVerificationIncomplete):
		return winnerUnknown, nil, err
	case pinned:
		return winnerInvalidPinned, nil, err
	default:
		return winnerInvalidUnpinned, nil, err
	}
}

// contended handles a publication onto an existing final with at most two
// classifications, one quarantine and one retry of the same staging.
func (x *extraction) contended(ctx context.Context, st *StagedTree) (*PublishedPayload, error) {
	for attempt := 1; attempt <= 2; attempt++ {
		class, m, err := x.classify(ctx)
		switch {
		case class == winnerValid:
			return x.adopt(st, m)
		case class == winnerInvalidPinned:
			return nil, x.discardAfterFailure(st, unavailable(x.release, false, err))
		case attempt == 2:
			return nil, x.discardAfterFailure(st, publishFailure(x.release, PublishNotVisible, "rename-retry", ReasonUpdaterUnavailable, true, errors.Join(ErrPublishContended, err)))
		case class == winnerUnknown:
			return nil, x.discardAfterFailure(st, publishFailure(x.release, PublishNotVisible, "classify", ReasonUpdaterUnavailable, true, err))
		}
		if err := x.quarantine(); err != nil {
			return nil, x.discardAfterFailure(st, err)
		}
		payload, err := st.Publish(ctx, x.final)
		switch {
		case err == nil:
			return x.finish(payload, true)
		case errors.Is(err, deviceerrors.ErrDestinationExists):
			continue
		case isVisibleNotUsable(err):
			return nil, err
		default:
			return nil, x.discardAfterFailure(st, publishFailure(x.release, PublishNotVisible, "rename-retry", ReasonUpdaterUnavailable, true, err))
		}
	}
	return nil, x.discardAfterFailure(st, publishFailure(x.release, PublishNotVisible, "rename-retry", ReasonUpdaterUnavailable, true, ErrPublishContended))
}

// adopt uses a valid existing final instead of the staging, which is then
// discarded; a failed discard is reported in the result's cleanup report.
func (x *extraction) adopt(st *StagedTree, m *Manifest) (*PublishedPayload, error) {
	payload, err := x.reuse(m)
	if err != nil {
		return nil, x.discardAfterFailure(st, err)
	}
	if err := x.discard(st); err != nil {
		payload.Cleanup.Pending = append(payload.Cleanup.Pending, st.Name())
		payload.Cleanup.Err = boundedText(err.Error(), maxEvidenceErrorBytes)
	}
	return payload, nil
}
