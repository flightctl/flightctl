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
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

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
	}
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
// entries. One mutex serializes every operation on the updater cache; a
// single agent process is assumed.
type Retention struct {
	dataDir  string
	rw       fileio.ReadWriter
	log      *log.PrefixLogger
	mu       sync.Mutex
	ceilings policy.ExtractionLimits
	leases   map[string]struct{}
	now      func() time.Time

	treesPerPass int
	listingCap   int
	passWork     int
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
		rw:           rw,
		log:          log,
		ceilings:     DefaultExtractionCeilings(),
		leases:       map[string]struct{}{},
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
	var doc registryDocument
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
	byDigest := map[string]Pin{}
	byAttempt := map[string]bool{}
	for _, p := range doc.Pins {
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
	return doc.Pins, nil
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
	return fw.ReplaceFileDurable(registryFileName, data, 0o600)
}

// Pin durably retains a published payload for an attempt and clears its
// lease. The version directory must exist; pinning the same attempt and role
// again is a no-op.
func (r *Retention) Pin(p Pin) error {
	r.mu.Lock()
	defer r.mu.Unlock()
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
	e, err := c.updaters.Lstat(p.PayloadDir)
	if err != nil || !e.IsDir() {
		return fmt.Errorf("pin %s: %w", p.PackageDigest, ErrRetainedPayloadMissing)
	}
	for _, q := range pins {
		if q.AttemptID == p.AttemptID && q.Role == p.Role {
			if q != p {
				return fmt.Errorf("attempt %q already pins a different %s payload: %w", p.AttemptID, p.Role, ErrPinConflict)
			}
			delete(r.leases, p.PackageDigest)
			return nil
		}
		if q.PackageDigest == p.PackageDigest && !sameRetainedPayload(p, q) {
			return fmt.Errorf("pin %s: %w", p.PackageDigest, ErrPinConflict)
		}
	}
	if err := writeRegistry(c.firmware, append(pins, p)); err != nil {
		return fmt.Errorf("persist retention registry: %w", err)
	}
	delete(r.leases, p.PackageDigest)
	return nil
}

// Release removes every pin of an attempt. Releasing an unknown attempt is a
// no-op.
func (r *Retention) Release(attemptID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, err := r.openCache(false)
	if err != nil {
		return err
	}
	defer c.Close()
	pins, err := readRegistry(c.firmware)
	if err != nil {
		return err
	}
	kept := make([]Pin, 0, len(pins))
	for _, p := range pins {
		if p.AttemptID != attemptID {
			kept = append(kept, p)
		}
	}
	if len(kept) == len(pins) {
		return nil
	}
	if err := writeRegistry(c.firmware, kept); err != nil {
		return fmt.Errorf("persist retention registry: %w", err)
	}
	return nil
}

// ReleaseLease ends the lease on a payload that Extract returned as usable
// and that will not be pinned.
func (r *Retention) ReleaseLease(packageDigest string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.leases, packageDigest)
}

// Pins returns the registered pins.
func (r *Retention) Pins() ([]Pin, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
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
	r.mu.Lock()
	defer r.mu.Unlock()
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
// ErrRetainedPayloadMissing, any difference ErrRetainedPayloadChanged and an
// expired deadline ErrRetainedVerificationIncomplete; nothing is repaired or
// extracted again.
func (r *Retention) VerifyPublished(ctx context.Context, pin Pin) (VerifyResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
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
	m, err := verifyPublishedTree(ctx, c.updaters, pin.PayloadDir, pin.ManifestDigest, capLimits(pin.Limits, r.ceilings))
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
	r.mu.Lock()
	defer r.mu.Unlock()
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

// verifyPublishedTree checks that dir in updaters is exactly the published
// payload whose manifest hashes to manifestDigest: published modes, owner and
// single links, no extra or missing entries, and listed sizes and digests,
// all within limits.
func verifyPublishedTree(ctx context.Context, updaters fileio.Anchor, dir, manifestDigest string, limits policy.ExtractionLimits) (*Manifest, error) {
	changed := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrRetainedPayloadChanged, fmt.Sprintf(format, args...))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	owner := os.Geteuid()
	e, err := updaters.Lstat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrRetainedPayloadMissing
		}
		return nil, changed("version directory: %v", err)
	}
	if !e.IsDir() || e.Perm != publishedDirMode || int64(e.UID) != int64(owner) {
		return nil, changed("version directory has type %s, mode %s, owner %d", e.Type, e.Perm, e.UID)
	}
	final, err := updaters.OpenDir(dir, fileio.DirOptions{ReadOnly: true})
	if err != nil {
		return nil, changed("open version directory: %v", err)
	}
	defer final.Close()
	data, err := final.ReadFile(manifestFileName, limits.MaxExtractedManifestBytes)
	if err != nil {
		return nil, changed("read manifest: %v", err)
	}
	if policy.Hash(data) != manifestDigest {
		return nil, changed("manifest digest differs")
	}
	m, err := ParseManifest(data)
	if err != nil {
		return nil, changed("manifest: %v", err)
	}
	plan, err := planPayload(m.Name+"/"+m.Version, m, data, limits)
	if err != nil {
		return nil, changed("manifest exceeds the retained limits: %v", err)
	}
	seen := make(map[string]bool, len(plan.expected))
	walkErr := final.Walk(ctx, ".", fileio.WalkBounds{MaxEntries: len(plan.expected), MaxDepth: MaxPayloadDepth, MaxNameBytes: maxEntryName}, func(rel string, e fileio.EntryInfo) error {
		want, ok := plan.expected[rel]
		if !ok {
			return changed("unexpected entry %q", rel)
		}
		if int64(e.UID) != int64(owner) {
			return changed("entry %q is owned by uid %d", rel, e.UID)
		}
		if want.dir {
			if !e.IsDir() || e.Perm != publishedDirMode {
				return changed("entry %q has type %s and mode %s", rel, e.Type, e.Perm)
			}
		} else {
			mode := publishedFileMode
			if want.exec {
				mode = publishedExecMode
			}
			if !e.IsRegular() || e.Perm != mode || e.Nlink != 1 || e.Size != want.size {
				return changed("file %q has type %s, mode %s, %d links and %d bytes", rel, e.Type, e.Perm, e.Nlink, e.Size)
			}
		}
		seen[rel] = true
		return nil
	})
	if walkErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(walkErr, ErrRetainedPayloadChanged) {
			return nil, walkErr
		}
		return nil, changed("%v", walkErr)
	}
	for rel := range plan.expected {
		if !seen[rel] {
			return nil, changed("entry %q is missing", rel)
		}
	}
	for _, f := range m.Files {
		d, err := final.Hash(ctx, f.Path, fileio.CopyBounds{MaxBytes: f.Size})
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, changed("file %q: %v", f.Path, err)
		}
		if d.Size != f.Size || d.SHA256 != f.SHA256 {
			return nil, changed("file %q content differs", f.Path)
		}
	}
	return m, nil
}

// RetainedImageReferences returns the digest-pinned image references of every
// pinned payload.
func (r *Retention) RetainedImageReferences(ctx context.Context) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
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
	r.mu.Lock()
	defer r.mu.Unlock()
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
	retained := map[string]bool{}
	for _, p := range pins {
		retained[p.PayloadDir] = true
	}
	for digest := range r.leases {
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
	for i, name := range candidates {
		if i >= r.treesPerPass {
			break
		}
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
