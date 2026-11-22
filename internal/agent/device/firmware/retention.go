package firmware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"weak"

	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	// CleanupTreesPerPass caps the owned trees one cleanup pass removes.
	CleanupTreesPerPass = 4
	// MaxCacheListingEntries caps the entries one cleanup pass lists per
	// cache directory.
	MaxCacheListingEntries = 8192
	// CleanupPassMaxWork caps the listing and single-file work of one
	// cleanup pass.
	CleanupPassMaxWork = 1 << 16

	registryFileName   = "retained.json"
	registryAPIVersion = "updater-retention/v1"
	maxRegistryBytes   = 1 << 20
	maxAttemptIDBytes  = 256
	firmwareDirName    = "firmware"
	updatersDirName    = "updaters"

	// capabilityXattr names file capabilities; capabilityXattrLimit bounds
	// the read of one value.
	capabilityXattr      = "security.capability"
	capabilityXattrLimit = 64
)

// PinRole says why an attempt retains an updater payload.
type PinRole string

const (
	PinRoleActive   PinRole = "active"
	PinRoleRecovery PinRole = "recovery"
)

// Pin retains one published updater payload for an attempt. PayloadDir is
// the hex form of PackageDigest.
type Pin struct {
	AttemptID        string                  `json:"attemptID"`
	Role             PinRole                 `json:"role"`
	Reference        string                  `json:"reference"`
	PackageDigest    string                  `json:"packageDigest"`
	ManifestDigest   string                  `json:"manifestDigest"`
	DescriptorDigest string                  `json:"descriptorDigest"`
	PayloadDir       string                  `json:"payloadDir"`
	WorkspaceDir     string                  `json:"workspaceDir,omitempty"`
	Limits           policy.ExtractionLimits `json:"limits"`

	lease *lease
}

// Pin returns a retention pin of the payload for attemptID in role.
func (p *PublishedPayload) Pin(attemptID string, role PinRole) Pin {
	return Pin{
		AttemptID:        attemptID,
		Role:             role,
		Reference:        p.Reference,
		PackageDigest:    p.PackageDigest,
		ManifestDigest:   p.ManifestDigest,
		DescriptorDigest: p.DescriptorDigest,
		PayloadDir:       p.Dir,
		Limits:           p.Limits,
		lease:            p.lease,
	}
}

// lease protects the payload of one Extract result until the result is pinned
// or released.
type lease struct {
	owner     *retentionCache
	digest    string
	released  bool
	attemptID string
}

// identity returns p without the lease of the result it was made from.
func (p Pin) identity() Pin {
	p.lease = nil
	return p
}

// VerifyResult reports the host observation made after a retained payload
// verified. Evidence persistence never changes the verification verdict; a
// failure to persist is reported here.
type VerifyResult struct {
	Observation       HostObservation
	EvidencePersisted bool
	EvidenceErr       error
}

type registryDocument struct {
	APIVersion string `json:"apiVersion"`
	Pins       []Pin  `json:"pins"`
}

// Retention keeps the durable registry of pinned updater payloads, verifies
// retained payloads before reuse and removes only unreferenced owned cache
// entries. One gate serializes every operation on the updater cache; a
// single agent process is assumed.
type Retention struct {
	dataDir  string
	rw       fileio.ReadWriter
	log      *log.PrefixLogger
	cache    *retentionCache
	ceilings policy.ExtractionLimits
	now      func() time.Time

	treesPerPass int
	listingCap   int
	passWork     int
}

// retentionCache shares in-process serialization and leases across handles
// that address the same configured cache directory.
type retentionCache struct {
	gate         chan struct{}
	phaseGate    chan struct{}
	leases       map[string]int
	phaseLeases  map[string]int
	cleanupAfter string
}

var retentionCaches sync.Map // physical path to weak.Pointer[retentionCache]

func sharedRetentionCache(path string) *retentionCache {
	for {
		cache := &retentionCache{gate: make(chan struct{}, 1), phaseGate: make(chan struct{}, 1), leases: make(map[string]int), phaseLeases: make(map[string]int)}
		pointer := weak.Make(cache)
		existing, loaded := retentionCaches.LoadOrStore(path, pointer)
		if !loaded {
			runtime.AddCleanup(cache, func(path string) {
				retentionCaches.CompareAndDelete(path, pointer)
			}, path)
			return cache
		}
		if cache := existing.(weak.Pointer[retentionCache]).Value(); cache != nil {
			return cache
		}
		retentionCaches.CompareAndDelete(path, existing)
	}
}

// RetentionOption configures a Retention.
type RetentionOption func(*Retention)

// WithRetentionCeilings sets the ceilings that cap the pinned limits used to
// verify retained payloads.
func WithRetentionCeilings(c policy.ExtractionLimits) RetentionOption {
	return func(r *Retention) { r.ceilings = c }
}

// WithCleanupLimits sets the trees removed, the entries listed per directory
// and the listing work allowed per cleanup pass.
func WithCleanupLimits(trees, listing, passWork int) RetentionOption {
	return func(r *Retention) { r.treesPerPass, r.listingCap, r.passWork = trees, listing, passWork }
}

// NewRetention returns the retention of the updater cache beneath dataDir.
func NewRetention(dataDir string, rw fileio.ReadWriter, log *log.PrefixLogger, opts ...RetentionOption) *Retention {
	r := &Retention{
		dataDir:      dataDir,
		cache:        sharedRetentionCache(filepath.Clean(rw.PathFor(dataDir))),
		rw:           rw,
		log:          log,
		ceilings:     DefaultExtractionCeilings(),
		now:          time.Now,
		treesPerPass: CleanupTreesPerPass,
		listingCap:   MaxCacheListingEntries,
		passWork:     CleanupPassMaxWork,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *Retention) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case r.cache.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-r.cache.gate
			return err
		}
		return nil
	}
}

type cacheDirs struct {
	firmware fileio.Anchor
	updaters fileio.Anchor
	evidence fileio.Anchor
}

func (c *cacheDirs) Close() {
	for _, a := range []fileio.Anchor{c.evidence, c.updaters, c.firmware} {
		if a != nil {
			_ = a.Close()
		}
	}
}

// openCache opens the trusted firmware and updaters directories. Without
// create, an absent directory leaves its handle nil.
func (r *Retention) openCache(create bool) (*cacheDirs, error) {
	data, err := r.rw.OpenAnchor(r.dataDir, fileio.DirOptions{Trusted: true})
	if err != nil {
		return nil, err
	}
	defer data.Close()
	opts := fileio.DirOptions{Create: create, Perm: publishedDirMode, Trusted: true}
	c := &cacheDirs{}
	if c.firmware, err = data.OpenDir(firmwareDirName, opts); err != nil {
		if !create && errors.Is(err, fs.ErrNotExist) {
			return c, nil
		}
		return nil, err
	}
	if c.updaters, err = c.firmware.OpenDir(updatersDirName, opts); err != nil {
		if !create && errors.Is(err, fs.ErrNotExist) {
			return c, nil
		}
		c.Close()
		return nil, err
	}
	return c, nil
}

// openEvidence opens the trusted evidence directory beside the updater
// cache. Without create, an absent directory leaves the handle nil.
func (c *cacheDirs) openEvidence(create bool) error {
	if c.evidence != nil || c.firmware == nil {
		return nil
	}
	dir, err := c.firmware.OpenDir(evidenceDirName, fileio.DirOptions{Create: create, Perm: publishedDirMode, Trusted: true})
	if err != nil {
		if !create && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	c.evidence = dir
	return nil
}

func validatePin(p Pin) error {
	switch {
	case p.AttemptID == "" || len(p.AttemptID) > maxAttemptIDBytes || strings.IndexFunc(p.AttemptID, unicode.IsControl) >= 0:
		return errors.New("pin needs an attempt identifier")
	case p.Role != PinRoleActive && p.Role != PinRoleRecovery:
		return fmt.Errorf("unknown pin role %q", p.Role)
	case !schemaDigestPattern.MatchString(p.PackageDigest) || !schemaDigestPattern.MatchString(p.ManifestDigest) || !schemaDigestPattern.MatchString(p.DescriptorDigest):
		return errors.New("pin digests must be sha256 digests")
	case p.PayloadDir != strings.TrimPrefix(p.PackageDigest, "sha256:"):
		return errors.New("pin payload directory must be the package digest")
	case len(p.Reference) <= len(p.PackageDigest)+1 || !strings.HasSuffix(p.Reference, "@"+p.PackageDigest):
		return errors.New("pin reference must be pinned to the package digest")
	}
	if err := p.Limits.Validate(); err != nil {
		return fmt.Errorf("pin limits: %w", err)
	}
	return nil
}

// readRegistry returns the registered pins. An absent registry holds none;
// any unreadable, oversized, malformed or inconsistent registry is an error.
func readRegistry(fw fileio.Anchor) ([]Pin, error) {
	if fw == nil {
		return nil, nil
	}
	data, err := fw.ReadFile(registryFileName, maxRegistryBytes)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read retention registry: %w", err)
	}
	var doc struct {
		APIVersion string `json:"apiVersion"`
		Pins       *[]Pin `json:"pins"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode retention registry: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("retention registry has trailing content")
	}
	if doc.APIVersion != registryAPIVersion {
		return nil, fmt.Errorf("unsupported retention registry version %q", doc.APIVersion)
	}
	if doc.Pins == nil {
		return nil, errors.New("retention registry has no pin list")
	}
	byDigest := map[string]Pin{}
	byAttempt := map[string]bool{}
	for _, p := range *doc.Pins {
		if err := validatePin(p); err != nil {
			return nil, fmt.Errorf("retention registry: %w", err)
		}
		key := p.AttemptID + "\x00" + string(p.Role)
		if byAttempt[key] {
			return nil, fmt.Errorf("retention registry pins attempt %q as %s twice", p.AttemptID, p.Role)
		}
		byAttempt[key] = true
		if q, ok := byDigest[p.PackageDigest]; ok && !sameRetainedPayload(p, q) {
			return nil, fmt.Errorf("retention registry: %w", ErrPinConflict)
		}
		byDigest[p.PackageDigest] = p
	}
	return *doc.Pins, nil
}

func sameRetainedPayload(a, b Pin) bool {
	return a.ManifestDigest == b.ManifestDigest && a.PayloadDir == b.PayloadDir
}

func writeRegistry(fw fileio.Anchor, pins []Pin) error {
	sort.Slice(pins, func(i, j int) bool {
		if pins[i].AttemptID != pins[j].AttemptID {
			return pins[i].AttemptID < pins[j].AttemptID
		}
		return pins[i].Role < pins[j].Role
	})
	if pins == nil {
		pins = []Pin{}
	}
	data, err := json.Marshal(registryDocument{APIVersion: registryAPIVersion, Pins: pins})
	if err != nil {
		return err
	}
	if len(data) > maxRegistryBytes {
		return fmt.Errorf("retention registry of %d bytes would exceed the %d byte read limit", len(data), maxRegistryBytes)
	}
	return fw.ReplaceFileDurable(registryFileName, data, 0o600)
}

// Pin durably retains a published payload for an attempt and ends the lease
// of the Extract result the pin was made from. The version directory must exist; pinning the same attempt and role
// again only makes the registry durable before succeeding.
func (r *Retention) Pin(p Pin) error {
	r.cache.gate <- struct{}{}
	defer func() { <-r.cache.gate }()
	if err := validatePin(p); err != nil {
		return err
	}
	c, err := r.openCache(true)
	if err != nil {
		return err
	}
	defer c.Close()
	pins, err := readRegistry(c.firmware)
	if err != nil {
		return err
	}
	retired, err := (phaseRecordStore{dir: c.firmware, attemptID: p.AttemptID}).isRetired()
	if err != nil {
		return err
	}
	if retired {
		return fmt.Errorf("attempt %q has already been retired: %w", p.AttemptID, ErrPinConflict)
	}
	e, err := c.updaters.Lstat(p.PayloadDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("pin %s: %w", p.PackageDigest, ErrRetainedPayloadMissing)
	case err != nil:
		return fmt.Errorf("pin %s: %w", p.PackageDigest, err)
	case !e.IsDir():
		return fmt.Errorf("pin %s: %w", p.PackageDigest, retainedChanged("version directory has type %s", e.Type))
	}
	for _, q := range pins {
		if q.AttemptID == p.AttemptID && q.Role == p.Role {
			if q != p.identity() {
				return fmt.Errorf("attempt %q already pins a different %s payload: %w", p.AttemptID, p.Role, ErrPinConflict)
			}
			if err := c.firmware.Sync(); err != nil {
				return fmt.Errorf("persist retention registry: %w", err)
			}
			r.endLease(p.lease)
			return nil
		}
		if q.PackageDigest == p.PackageDigest && !sameRetainedPayload(p, q) {
			return fmt.Errorf("pin %s: %w", p.PackageDigest, ErrPinConflict)
		}
	}
	if err := writeRegistry(c.firmware, append(pins, p)); err != nil {
		return fmt.Errorf("persist retention registry: %w", err)
	}
	r.endLease(p.lease)
	return nil
}

// Release removes every pin and completed runner state of an attempt.
func (r *Retention) Release(attemptID string) error {
	r.cache.gate <- struct{}{}
	defer func() { <-r.cache.gate }()
	if r.cache.phaseLeases[attemptID] != 0 {
		return fmt.Errorf("attempt %q has an active execution lease: %w", attemptID, ErrPinConflict)
	}
	c, err := r.openCache(false)
	if err != nil {
		return err
	}
	defer c.Close()
	pins, err := readRegistry(c.firmware)
	if err != nil {
		return err
	}
	held, err := phaseHeldPin(c.firmware, attemptID)
	if err != nil {
		return err
	}
	if held {
		return fmt.Errorf("attempt %q has unresolved runner state: %w", attemptID, ErrPinConflict)
	}
	store := phaseRecordStore{dir: c.firmware, attemptID: attemptID}
	hasPin := false
	for _, pin := range pins {
		if pin.AttemptID == attemptID {
			hasPin = true
			break
		}
	}
	hasRunnerState := false
	if c.firmware != nil {
		record, err := store.load()
		if err != nil {
			return err
		}
		hasRunnerState = record.AttemptID != ""
	}
	if hasPin || hasRunnerState {
		if err := store.retire(); err != nil {
			return fmt.Errorf("retire firmware runner state: %w", err)
		}
	}
	kept := make([]Pin, 0, len(pins))
	for _, p := range pins {
		if p.AttemptID != attemptID {
			kept = append(kept, p)
		}
	}
	if len(kept) != len(pins) {
		if err := writeRegistry(c.firmware, kept); err != nil {
			return fmt.Errorf("persist retention registry: %w", err)
		}
	} else {
		if c.firmware != nil {
			if err := c.firmware.Sync(); err != nil {
				return fmt.Errorf("persist retention registry: %w", err)
			}
		}
	}
	return nil
}

// ReleaseLease ends the lease of payload, a result of Extract. Releasing a
// result again does nothing, and the payload stays protected while another
// result still holds its lease.
func (r *Retention) ReleaseLease(payload *PublishedPayload) {
	if payload == nil {
		return
	}
	r.cache.gate <- struct{}{}
	defer func() { <-r.cache.gate }()
	r.endLease(payload.lease)
}

func (r *Retention) endLease(l *lease) {
	if l == nil || l.owner != r.cache || l.released {
		return
	}
	l.released = true
	if l.attemptID != "" {
		if r.cache.phaseLeases[l.attemptID]--; r.cache.phaseLeases[l.attemptID] <= 0 {
			delete(r.cache.phaseLeases, l.attemptID)
		}
	}
	if r.cache.leases[l.digest]--; r.cache.leases[l.digest] <= 0 {
		delete(r.cache.leases, l.digest)
	}
}

// Pins returns the registered pins.
func (r *Retention) Pins() ([]Pin, error) {
	r.cache.gate <- struct{}{}
	defer func() { <-r.cache.gate }()
	c, err := r.openCache(false)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	pins, err := readRegistry(c.firmware)
	if err != nil {
		return nil, err
	}
	return append([]Pin(nil), pins...), nil
}

// PinFor returns a pin of the package digest, if any.
func (r *Retention) PinFor(packageDigest string) (*Pin, bool, error) {
	r.cache.gate <- struct{}{}
	defer func() { <-r.cache.gate }()
	c, err := r.openCache(false)
	if err != nil {
		return nil, false, err
	}
	defer c.Close()
	return pinFor(c.firmware, packageDigest)
}

func pinFor(fw fileio.Anchor, packageDigest string) (*Pin, bool, error) {
	pins, err := readRegistry(fw)
	if err != nil {
		return nil, false, err
	}
	for _, p := range pins {
		if p.PackageDigest == packageDigest {
			found := p
			return &found, true, nil
		}
	}
	return nil, false, nil
}

func capLimits(limits, ceilings policy.ExtractionLimits) policy.ExtractionLimits {
	capped := limits
	for _, f := range []struct{ v, c *int64 }{
		{&capped.MaxExtractedManifestBytes, &ceilings.MaxExtractedManifestBytes},
		{&capped.MaxExtractedFiles, &ceilings.MaxExtractedFiles},
		{&capped.MaxExtractedFileBytes, &ceilings.MaxExtractedFileBytes},
		{&capped.MaxExtractedTotalBytes, &ceilings.MaxExtractedTotalBytes},
		{&capped.ExtractionScratchBytes, &ceilings.ExtractionScratchBytes},
		{&capped.MaxExtractionSeconds, &ceilings.MaxExtractionSeconds},
	} {
		if *f.v > *f.c {
			*f.v = *f.c
		}
	}
	return capped
}

// VerifyPublished verifies the retained payload of pin within the pinned
// limits capped by the ceilings. A missing payload reports
// ErrRetainedPayloadMissing, any difference, including file capabilities on a
// retained file, ErrRetainedPayloadChanged, and an expired deadline or
// unreadable capability metadata ErrRetainedVerificationIncomplete; nothing is
// repaired or extracted again.
func (r *Retention) VerifyPublished(ctx context.Context, pin Pin) (VerifyResult, error) {
	if err := r.acquire(ctx); err != nil {
		return VerifyResult{}, retainedFailure(ctx, pin.Reference, err)
	}
	defer func() { <-r.cache.gate }()
	if err := validatePin(pin); err != nil {
		return VerifyResult{}, unavailable(pin.Reference, false, err)
	}
	c, err := r.openCache(false)
	if err != nil {
		return VerifyResult{}, unavailable(pin.Reference, false, err)
	}
	defer c.Close()
	if c.updaters == nil {
		return VerifyResult{}, unavailable(pin.Reference, false, ErrRetainedPayloadMissing)
	}
	limits := capLimits(pin.Limits, r.ceilings)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(limits.MaxExtractionSeconds)*time.Second)
	defer cancel()
	m, err := verifyPublishedTree(ctx, c.updaters, pin.PayloadDir, pin.ManifestDigest, limits)
	if err != nil {
		return VerifyResult{}, retainedFailure(ctx, pin.Reference, err)
	}
	id := evidenceIdentity{packageDigest: pin.PackageDigest, manifestDigest: pin.ManifestDigest, descriptorDigest: pin.DescriptorDigest, payloadDir: pin.PayloadDir, entrypoint: m.Entrypoint}
	obs, err := r.observeAndRecord(c, id, evidenceEventVerify, false)
	return VerifyResult{Observation: obs, EvidencePersisted: err == nil, EvidenceErr: err}, nil
}

// observeAndRecord observes the host for the published version of id and
// persists the observation in its evidence record.
func (r *Retention) observeAndRecord(c *cacheDirs, id evidenceIdentity, event string, fresh bool) (HostObservation, error) {
	final, err := c.updaters.OpenDir(id.payloadDir, fileio.DirOptions{ReadOnly: true})
	if err != nil {
		return HostObservation{}, fmt.Errorf("%w: %w", ErrEvidenceNotPersisted, err)
	}
	defer final.Close()
	obs := observeHost(r.rw, final, id.entrypoint, event, r.now())
	if err := c.openEvidence(true); err != nil {
		return obs, fmt.Errorf("%w: %w", ErrEvidenceNotPersisted, err)
	}
	return recordEvidence(c.evidence, id, obs, fresh)
}

// Evidence returns a copy of the host evidence record of a package.
func (r *Retention) Evidence(packageDigest string) (*HostEvidence, error) {
	r.cache.gate <- struct{}{}
	defer func() { <-r.cache.gate }()
	c, err := r.openCache(false)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if err := c.openEvidence(false); err != nil {
		return nil, err
	}
	if c.evidence == nil {
		return nil, fmt.Errorf("evidence of %s: %w", packageDigest, fs.ErrNotExist)
	}
	data, err := c.evidence.ReadFile(evidenceFileName(strings.TrimPrefix(packageDigest, "sha256:")), MaxEvidenceBytes)
	if err != nil {
		return nil, fmt.Errorf("evidence of %s: %w", packageDigest, err)
	}
	e, reason := decodeEvidence(data)
	if e == nil {
		return nil, fmt.Errorf("evidence of %s: %s", packageDigest, reason)
	}
	if e.PackageDigest != packageDigest {
		return nil, fmt.Errorf("evidence of %s: record identity differs", packageDigest)
	}
	return e, nil
}

// retainedFailure maps a retained-payload verification failure.
func retainedFailure(ctx context.Context, release string, err error) error {
	switch {
	case ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		return unavailable(release, true, fmt.Errorf("%w: %w", ErrRetainedVerificationIncomplete, ctx.Err()))
	case ctx.Err() != nil:
		return fmt.Errorf("verify updater %s: %w", release, ctx.Err())
	default:
		return unavailable(release, false, err)
	}
}

var errRetainedManifestDigest = fmt.Errorf("%w: manifest digest differs", ErrRetainedPayloadChanged)

// verifyPublishedTree checks that dir in updaters is exactly the published
// payload whose manifest hashes to manifestDigest: published modes, owner and
// single links, no extra or missing entries, and listed sizes and digests,
// all within limits.
func verifyPublishedTree(ctx context.Context, updaters fileio.Anchor, dir, manifestDigest string, limits policy.ExtractionLimits) (*Manifest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	owner := os.Geteuid()
	e, err := updaters.Lstat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrRetainedPayloadMissing
		}
		return nil, retainedReadFailure("stat version directory", err)
	}
	if !e.IsDir() || e.Perm != publishedDirMode || int64(e.UID) != int64(owner) {
		return nil, retainedChanged("version directory has type %s, mode %s, owner %d", e.Type, e.Perm, e.UID)
	}
	final, err := updaters.OpenDir(dir, fileio.DirOptions{ReadOnly: true})
	if err != nil {
		return nil, retainedReadFailure("open version directory", err)
	}
	defer final.Close()
	data, err := final.ReadFile(manifestFileName, limits.MaxExtractedManifestBytes)
	if err != nil {
		return nil, retainedReadFailure("read manifest", err)
	}
	if policy.Hash(data) != manifestDigest {
		return nil, errRetainedManifestDigest
	}
	m, err := ParseManifest(data)
	if err != nil {
		return nil, retainedChanged("manifest: %v", err)
	}
	plan, err := planPayload(m.Name+"/"+m.Version, m, data, limits)
	if err != nil {
		return nil, retainedChanged("manifest exceeds the retained limits: %v", err)
	}
	if err := verifyPublishedEntries(ctx, final, plan, owner); err != nil {
		return nil, err
	}
	if err := verifyPublishedCapabilities(ctx, final, plan); err != nil {
		return nil, err
	}
	if err := verifyPublishedDigests(ctx, final, m); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return m, nil
}

func retainedChanged(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRetainedPayloadChanged, fmt.Sprintf(format, args...))
}

// retainedReadFailure distinguishes confirmed structural violations from
// inconclusive reads. Both keep the underlying cause.
func retainedReadFailure(op string, err error) error {
	for _, violation := range []error{fs.ErrNotExist, deviceerrors.ErrUnsafeFileType, deviceerrors.ErrFileLimitExceeded,
		deviceerrors.ErrWalkLimitExceeded, deviceerrors.ErrCrossDevice, deviceerrors.ErrFileCapability} {
		if errors.Is(err, violation) {
			return fmt.Errorf("%w: %s: %w", ErrRetainedPayloadChanged, op, err)
		}
	}
	return fmt.Errorf("%w: %s: %w", ErrRetainedVerificationIncomplete, op, err)
}

// verifyPublishedEntries walks the version directory and requires exactly the
// planned entries with published types, modes, owner, links and sizes.
func verifyPublishedEntries(ctx context.Context, final fileio.Anchor, plan *payloadPlan, owner int) error {
	seen := make(map[string]bool, len(plan.expected))
	walkErr := final.Walk(ctx, ".", fileio.WalkBounds{MaxEntries: len(plan.expected), MaxDepth: MaxPayloadDepth, MaxNameBytes: maxEntryName}, func(rel string, e fileio.EntryInfo) error {
		want, ok := plan.expected[rel]
		if !ok {
			return retainedChanged("unexpected entry %q", rel)
		}
		if err := checkPublishedEntry(rel, e, want, owner); err != nil {
			return err
		}
		seen[rel] = true
		return nil
	})
	if walkErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(walkErr, ErrRetainedPayloadChanged) {
			return walkErr
		}
		return retainedReadFailure("walk retained payload", walkErr)
	}
	for rel := range plan.expected {
		if !seen[rel] {
			return retainedChanged("entry %q is missing", rel)
		}
	}
	return nil
}

func checkPublishedEntry(rel string, e fileio.EntryInfo, want expectedEntry, owner int) error {
	if int64(e.UID) != int64(owner) {
		return retainedChanged("entry %q is owned by uid %d", rel, e.UID)
	}
	if want.dir {
		if !e.IsDir() || e.Perm != publishedDirMode {
			return retainedChanged("entry %q has type %s and mode %s", rel, e.Type, e.Perm)
		}
		return nil
	}
	mode := publishedFileMode
	if want.exec {
		mode = publishedExecMode
	}
	if !e.IsRegular() || e.Perm != mode || e.Nlink != 1 || e.Size != want.size {
		return retainedChanged("file %q has type %s, mode %s, %d links and %d bytes", rel, e.Type, e.Perm, e.Nlink, e.Size)
	}
	return nil
}

// verifyPublishedCapabilities requires every retained regular file, including
// the manifest, to carry no file capabilities. Metadata that cannot be read
// leaves the payload unverified rather than proven changed.
func verifyPublishedCapabilities(ctx context.Context, final fileio.Anchor, plan *payloadPlan) error {
	files := make([]string, 0, len(plan.expected))
	for rel, want := range plan.expected {
		if !want.dir {
			files = append(files, rel)
		}
	}
	sort.Strings(files)
	for _, rel := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, state, err := final.GetXattr(rel, capabilityXattr, capabilityXattrLimit)
		switch state {
		case fileio.XattrPresent:
			return retainedChanged("file %q carries file capabilities", rel)
		case fileio.XattrUnknown:
			return fmt.Errorf("%w: capabilities of %q: %w", ErrRetainedVerificationIncomplete, rel, err)
		}
	}
	return nil
}

// verifyPublishedDigests hashes every listed file within its listed size. A
// file whose capabilities cannot be read while hashing leaves the payload
// unverified rather than proven changed.
func verifyPublishedDigests(ctx context.Context, final fileio.Anchor, m *Manifest) error {
	for _, f := range m.Files {
		d, err := final.Hash(ctx, f.Path, fileio.CopyBounds{MaxBytes: f.Size})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return retainedReadFailure(fmt.Sprintf("hash file %q", f.Path), err)
		}
		if d.Size != f.Size || d.SHA256 != f.SHA256 {
			return retainedChanged("file %q content differs", f.Path)
		}
	}
	return nil
}

// RetainedImageReferences returns the digest-pinned image references of every
// pinned payload.
func (r *Retention) RetainedImageReferences(ctx context.Context) ([]string, error) {
	if err := r.acquire(ctx); err != nil {
		return nil, err
	}
	defer func() { <-r.cache.gate }()
	c, err := r.openCache(false)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	pins, err := readRegistry(c.firmware)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	refs := []string{}
	for _, p := range pins {
		if !seen[p.Reference] {
			seen[p.Reference] = true
			refs = append(refs, p.Reference)
		}
	}
	sort.Strings(refs)
	return refs, nil
}

var errPassWork = errors.New("cleanup pass work limit reached")

// CleanupUnreferenced removes stale staging and quarantine directories and
// unpinned, unleased version directories from the updater cache, at most
// CleanupTreesPerPass trees per pass, and returns the removed names. It
// never removes foreign names, and a malformed registry removes nothing. A
// pass that stops early reports an error; the next pass continues.
func (r *Retention) CleanupUnreferenced(ctx context.Context) ([]string, error) {
	if err := r.acquire(ctx); err != nil {
		return nil, err
	}
	defer func() { <-r.cache.gate }()
	c, err := r.openCache(false)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	pins, err := readRegistry(c.firmware)
	if err != nil {
		return nil, err
	}
	if c.updaters == nil {
		return nil, nil
	}
	// A failed registry replacement can leave a visible rename that is not yet durable.
	if err := c.firmware.Sync(); err != nil {
		return nil, fmt.Errorf("sync retention registry before cleanup: %w", err)
	}
	retained := map[string]bool{}
	for _, p := range pins {
		retained[p.PayloadDir] = true
	}
	for digest := range r.cache.leases {
		retained[strings.TrimPrefix(digest, "sha256:")] = true
	}
	work := 0
	var candidates []string
	listErr := c.updaters.Walk(ctx, ".", fileio.WalkBounds{MaxEntries: r.listingCap, MaxDepth: 1, MaxNameBytes: maxEntryName}, func(name string, e fileio.EntryInfo) error {
		work += 2
		if work > r.passWork {
			return errPassWork
		}
		owned := strings.HasPrefix(name, stagingPrefix) || strings.HasPrefix(name, quarantinePrefix) ||
			(e.IsDir() && fileDigestPattern.MatchString(name) && !retained[name])
		if owned {
			candidates = append(candidates, name)
		}
		if e.IsDir() {
			return fs.SkipDir
		}
		return nil
	})
	if listErr != nil && ctx.Err() != nil {
		return nil, listErr
	}
	var errs []error
	if listErr != nil {
		errs = append(errs, fmt.Errorf("list updater cache: %w", listErr))
	}
	var removed []string
	sort.Strings(candidates)
	start := sort.Search(len(candidates), func(i int) bool { return candidates[i] > r.cache.cleanupAfter })
	for i := range min(r.treesPerPass, len(candidates)) {
		name := candidates[(start+i)%len(candidates)]
		r.cache.cleanupAfter = name
		res, err := c.updaters.RemoveTree(ctx, name, cleanupBounds())
		if err != nil {
			errs = append(errs, fmt.Errorf("remove %s (%s): %w", name, res.Stop, err))
			if ctx.Err() != nil {
				break
			}
			continue
		}
		r.log.Infof("Removed unreferenced updater cache entry %s", name)
		removed = append(removed, name)
	}
	if ctx.Err() == nil && work < r.passWork {
		orphans, err := r.removeEvidenceOrphans(ctx, c, retained, work)
		removed = append(removed, orphans...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return removed, errors.Join(errs...)
}

// removeEvidenceOrphans removes evidence records of versions that are absent,
// unpinned and unleased, within the remaining pass work.
func (r *Retention) removeEvidenceOrphans(ctx context.Context, c *cacheDirs, retained map[string]bool, work int) ([]string, error) {
	if err := c.openEvidence(false); err != nil || c.evidence == nil {
		return nil, err
	}
	var orphans []string
	listErr := c.evidence.Walk(ctx, ".", fileio.WalkBounds{MaxEntries: r.listingCap, MaxDepth: 1, MaxNameBytes: maxEntryName}, func(name string, e fileio.EntryInfo) error {
		work += 2
		if work > r.passWork {
			return errPassWork
		}
		hex, ok := strings.CutSuffix(name, ".json")
		if ok && e.IsRegular() && fileDigestPattern.MatchString(hex) && !retained[hex] {
			if _, err := c.updaters.Lstat(hex); errors.Is(err, fs.ErrNotExist) {
				orphans = append(orphans, name)
			}
		}
		if e.IsDir() {
			return fs.SkipDir
		}
		return nil
	})
	var errs []error
	if listErr != nil {
		errs = append(errs, fmt.Errorf("list evidence: %w", listErr))
	}
	var removed []string
	for _, name := range orphans {
		if work++; work > r.passWork {
			errs = append(errs, errPassWork)
			break
		}
		if _, err := c.evidence.RemoveTree(ctx, name, cleanupBounds()); err != nil {
			errs = append(errs, fmt.Errorf("remove evidence %s: %w", name, err))
			continue
		}
		removed = append(removed, name)
	}
	return removed, errors.Join(errs...)
}
