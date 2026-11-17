package firmware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"reflect"
	"slices"
	"strings"

	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	"golang.org/x/sys/unix"
)

const (
	// CleanupMaxWork caps the work of one owned-tree removal.
	CleanupMaxWork = 1 << 20
	// CleanupMaxDetach caps the over-depth directories one removal detaches.
	CleanupMaxDetach = 64
	// CleanupMaxDirRetries caps the rewinds of one directory during removal.
	CleanupMaxDirRetries = 4

	stagingPrefix    = ".staging-"
	stagingAttempts  = 8
	removalBatchSize = 128
	maxEntryName     = 255

	publishedDirMode  fs.FileMode = 0o700
	publishedExecMode fs.FileMode = 0o500
	publishedFileMode fs.FileMode = 0o400
	manifestFileName              = "manifest.json"
	payloadDirName                = "payload"
	updaterDirName                = "updater"
)

// PublishState classifies the outcome of staging and publishing a payload.
type PublishState int

const (
	// PublishNotVisible means no final directory was made visible by the call.
	PublishNotVisible PublishState = iota
	// PublishVisibleNotUsable means a complete final directory is visible but
	// its durability, anchor or evidence could not be confirmed; it must not
	// be used or pinned, and it is not deleted.
	PublishVisibleNotUsable
	// PublishUsable means the payload is published, durable and recorded.
	PublishUsable
)

func (s PublishState) String() string {
	switch s {
	case PublishNotVisible:
		return "NotVisible"
	case PublishVisibleNotUsable:
		return "VisibleNotUsable"
	case PublishUsable:
		return "Usable"
	default:
		return fmt.Sprintf("PublishState(%d)", int(s))
	}
}

// PublishError reports the publication state reached when an operation failed.
type PublishError struct {
	State PublishState
	// Op is one of stage, copy, sync, unmount, rename, classify, quarantine,
	// rename-retry, sync-parent, revalidate or evidence.
	Op  string
	Err error
}

func (e *PublishError) Error() string {
	return fmt.Sprintf("%s failed (%s): %v", e.Op, e.State, e.Err)
}

func (e *PublishError) Unwrap() error { return e.Err }

// PublishedFile describes one published payload file.
type PublishedFile struct {
	Path       string
	SHA256     string
	Size       int64
	Executable bool
}

// CleanupReport lists owned non-final leftovers of a usable result that a
// later cleanup removes.
type CleanupReport struct {
	// Pending holds at most three names: quarantined finals and an
	// incompletely discarded staging directory.
	Pending []string
	// Err is a sanitized description of the cleanup failure, at most 256 bytes.
	Err string
}

// PublishedPayload describes a published updater payload. Dir is the name of
// its version directory in the updater cache.
type PublishedPayload struct {
	Reference        string
	PackageDigest    string
	ManifestDigest   string
	DescriptorDigest string
	Dir              string
	Entrypoint       string
	Files            []PublishedFile
	Limits           policy.ExtractionLimits
	// SourceMountReadOnly records whether the source mount reported itself
	// read-only. It is an observation, not a guarantee.
	SourceMountReadOnly bool
	Reused              bool
	Cleanup             CleanupReport
}

// StagedTree is a fully staged, synced and unpublished payload owned by the
// call that created it. A successful Publish consumes it; until then it stays
// owned, including after Publish reports that the final name exists.
type StagedTree struct {
	cache          fileio.Anchor
	name           string
	release        string
	entrypoint     string
	manifestDigest string
	files          []PublishedFile
	limits         policy.ExtractionLimits
	sourceReadOnly bool
	consumed       bool
	discarded      bool
}

func cleanupBounds() fileio.RemoveBounds {
	nodes, _ := MaxTreeNodes(maxPayloadFiles, MaxPayloadDepth)
	return fileio.RemoveBounds{
		MaxNodes:      nodes,
		MaxWork:       CleanupMaxWork,
		MaxDetach:     CleanupMaxDetach,
		MaxDirRetries: CleanupMaxDirRetries,
		MaxDepth:      MaxPayloadDepth,
		BatchSize:     removalBatchSize,
	}
}

func randomSuffix() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func publishFailure(release string, state PublishState, op string, reason Reason, retryable bool, err error) error {
	return &FetchError{Reason: reason, Retryable: retryable, Err: fmt.Errorf("updater %w: %w", deviceerrors.WithElement(release), &PublishError{State: state, Op: op, Err: err})}
}

func budgetFailure(release, op, key string, err error) error {
	return &FetchError{Reason: ReasonResourceLimitExceeded, ExceededBudget: key, Err: fmt.Errorf("updater %w: %w", deviceerrors.WithElement(release), &PublishError{State: PublishNotVisible, Op: op, Err: err})}
}

// contextFailure maps a finished extraction context: an expired deadline is
// the release's extraction time budget, anything else is a cancellation that
// carries no firmware reason.
func contextFailure(ctx context.Context, release string, state PublishState, op string) error {
	err := ctx.Err()
	if errors.Is(err, context.DeadlineExceeded) {
		return budgetFailure(release, op, "maxExtractionSeconds", fmt.Errorf("extraction time budget expired: %w", err))
	}
	return fmt.Errorf("updater %w: %w", deviceerrors.WithElement(release), &PublishError{State: state, Op: op, Err: err})
}

type expectedEntry struct {
	dir  bool
	size int64
}

type planDir struct {
	name  string
	dirs  []*planDir
	files []ManifestFile
	index map[string]*planDir
}

func (d *planDir) child(name string) *planDir {
	if c, ok := d.index[name]; ok {
		return c
	}
	c := &planDir{name: name, index: map[string]*planDir{}}
	d.index[name] = c
	d.dirs = append(d.dirs, c)
	return c
}

type payloadPlan struct {
	expected map[string]expectedEntry
	root     *planDir
}

// planPayload validates the manifest defensively and against the admitted
// budgets, and derives the exact expected source tree.
func planPayload(release string, m *Manifest, manifestBytes []byte, limits policy.ExtractionLimits) (*payloadPlan, error) {
	invalid := func(format string, args ...any) error {
		return publishFailure(release, PublishNotVisible, "stage", ReasonUpdaterUnavailable, false, fmt.Errorf(format, args...))
	}
	if len(m.Files) == 0 {
		return nil, invalid("manifest lists no files")
	}
	if int64(len(manifestBytes)) > limits.MaxExtractedManifestBytes {
		return nil, budgetFailure(release, "stage", "maxExtractedManifestBytes", fmt.Errorf("manifest of %d bytes exceeds the admitted %d bytes", len(manifestBytes), limits.MaxExtractedManifestBytes))
	}
	if int64(len(m.Files)) > limits.MaxExtractedFiles {
		return nil, budgetFailure(release, "stage", "maxExtractedFiles", fmt.Errorf("manifest lists %d files, more than the admitted %d", len(m.Files), limits.MaxExtractedFiles))
	}
	plan := &payloadPlan{
		expected: map[string]expectedEntry{manifestFileName: {size: int64(len(manifestBytes))}, payloadDirName: {dir: true}},
		root:     &planDir{name: payloadDirName, index: map[string]*planDir{}},
	}
	var total int64
	files := make(map[string]bool, len(m.Files))
	for _, f := range m.Files {
		switch {
		case !validPayloadPath(f.Path):
			return nil, invalid("manifest path %q is not a clean path beneath payload/", f.Path)
		case !fileDigestPattern.MatchString(f.SHA256):
			return nil, invalid("manifest digest of %q is malformed", f.Path)
		case f.Size < 0:
			return nil, invalid("manifest size of %q is negative", f.Path)
		case files[f.Path]:
			return nil, invalid("manifest path %q is listed more than once", f.Path)
		}
		files[f.Path] = true
		components := strings.Split(f.Path, "/")
		if len(components) > MaxPayloadDepth {
			return nil, publishFailure(release, PublishNotVisible, "stage", ReasonFirmwareNotSupported, false, fmt.Errorf("manifest path %q is deeper than %d components", f.Path, MaxPayloadDepth))
		}
		if f.Size > limits.MaxExtractedFileBytes {
			return nil, budgetFailure(release, "stage", "maxExtractedFileBytes", fmt.Errorf("file %q of %d bytes exceeds the admitted %d bytes", f.Path, f.Size, limits.MaxExtractedFileBytes))
		}
		if f.Size > limits.MaxExtractedTotalBytes-total {
			return nil, budgetFailure(release, "stage", "maxExtractedTotalBytes", fmt.Errorf("files exceed the admitted total of %d bytes", limits.MaxExtractedTotalBytes))
		}
		total += f.Size
		dir := plan.root
		for i := 1; i < len(components)-1; i++ {
			dir = dir.child(components[i])
			plan.expected[strings.Join(components[:i+1], "/")] = expectedEntry{dir: true}
		}
		dir.files = append(dir.files, f)
	}
	for _, f := range m.Files {
		if plan.expected[f.Path].dir {
			return nil, invalid("manifest file %q is also a directory", f.Path)
		}
		plan.expected[f.Path] = expectedEntry{size: f.Size}
	}
	entry := slices.IndexFunc(m.Files, func(f ManifestFile) bool { return f.Path == m.Entrypoint })
	if entry < 0 || !m.Files[entry].Executable {
		return nil, invalid("manifest entrypoint %q is not a listed executable file", m.Entrypoint)
	}
	parsed, err := ParseManifest(manifestBytes)
	if err != nil || !reflect.DeepEqual(parsed, m) {
		return nil, invalid("manifest bytes do not match the verified manifest")
	}
	return plan, nil
}

func checkFreeSpace(cache fileio.Anchor, release string, limits policy.ExtractionLimits) error {
	available, err := cache.AvailableBytes()
	if err != nil {
		return publishFailure(release, PublishNotVisible, "stage", ReasonUpdaterUnavailable, false, err)
	}
	if limits.ExtractionScratchBytes > 0 && available < uint64(limits.ExtractionScratchBytes) {
		return publishFailure(release, PublishNotVisible, "stage", ReasonInsufficientSpace, false, fmt.Errorf("updater cache has %d bytes free; the release needs %d", available, limits.ExtractionScratchBytes))
	}
	return nil
}

type stager struct {
	ctx     context.Context
	src     fileio.Anchor
	release string
	goarch  string
	digests map[string]fileio.FileDigest
}

// fail classifies a staging failure: context expiry or cancellation first,
// then running out of space, then unsupported executables; everything else
// leaves the payload unavailable.
func (s *stager) fail(op string, err error) error {
	switch {
	case s.ctx.Err() != nil:
		return contextFailure(s.ctx, s.release, PublishNotVisible, op)
	case errors.Is(err, unix.ENOSPC):
		return publishFailure(s.release, PublishNotVisible, op, ReasonInsufficientSpace, false, err)
	case errors.Is(err, errNotNativeExecutable):
		return publishFailure(s.release, PublishNotVisible, "stage", ReasonFirmwareNotSupported, false, err)
	default:
		return publishFailure(s.release, PublishNotVisible, op, ReasonUpdaterUnavailable, false, err)
	}
}

// integrityError reports whether a copy failed because the source violated
// the payload rules rather than because of an I/O failure.
func integrityError(err error) bool {
	for _, target := range []error{deviceerrors.ErrUnsafeFileType, deviceerrors.ErrHardLinkedFile, deviceerrors.ErrUnsafeFileMode, deviceerrors.ErrFileCapability, deviceerrors.ErrFileLimitExceeded, deviceerrors.ErrInvalidPath} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

func (s *stager) walkSource(plan *payloadPlan) error {
	seen := make(map[string]bool, len(plan.expected))
	err := s.src.Walk(s.ctx, updaterDirName, fileio.WalkBounds{MaxEntries: len(plan.expected), MaxDepth: MaxPayloadDepth, MaxNameBytes: maxEntryName}, func(rel string, e fileio.EntryInfo) error {
		want, ok := plan.expected[rel]
		switch {
		case !ok:
			return fmt.Errorf("unexpected entry %q in the updater image", rel)
		case want.dir && !e.IsDir():
			return fmt.Errorf("updater entry %q is not a directory", rel)
		case !want.dir && !e.IsRegular():
			return fmt.Errorf("updater entry %q is not a regular file", rel)
		case !want.dir && e.Size != want.size:
			return fmt.Errorf("updater file %q has %d bytes, the manifest lists %d", rel, e.Size, want.size)
		case !want.dir && e.Nlink != 1:
			return fmt.Errorf("updater file %q has %d links", rel, e.Nlink)
		case e.Perm&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0:
			return fmt.Errorf("updater entry %q has special mode bits %s", rel, e.Perm)
		}
		seen[rel] = true
		return nil
	})
	if err != nil {
		return s.fail("stage", err)
	}
	for rel := range plan.expected {
		if !seen[rel] {
			return s.fail("stage", fmt.Errorf("updater entry %q listed by the manifest is missing", rel))
		}
	}
	return nil
}

func createStaging(cache fileio.Anchor) (fileio.Anchor, string, error) {
	for i := 0; i < stagingAttempts; i++ {
		suffix, err := randomSuffix()
		if err != nil {
			return nil, "", err
		}
		name := stagingPrefix + suffix
		dir, err := cache.OpenDir(name, fileio.DirOptions{Create: true, Exclusive: true, Perm: publishedDirMode, Trusted: true})
		if err == nil {
			return dir, name, nil
		}
		if !errors.Is(err, deviceerrors.ErrDestinationExists) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("no unused staging name")
}

// stageDir creates d beneath parent, stages its subdirectories and files, and
// syncs it once its contents are durable.
func (s *stager) stageDir(parent fileio.Anchor, d *planDir) error {
	if s.ctx.Err() != nil {
		return s.fail("stage", s.ctx.Err())
	}
	dir, err := parent.OpenDir(d.name, fileio.DirOptions{Create: true, Exclusive: true, Perm: publishedDirMode, Trusted: true})
	if err != nil {
		return s.fail("stage", err)
	}
	defer dir.Close()
	for _, sub := range d.dirs {
		if err := s.stageDir(dir, sub); err != nil {
			return err
		}
	}
	for _, f := range d.files {
		if err := s.stageFile(dir, f); err != nil {
			return err
		}
	}
	if err := dir.Sync(); err != nil {
		return s.fail("sync", err)
	}
	return nil
}

func (s *stager) stageFile(dir fileio.Anchor, f ManifestFile) error {
	base := path.Base(f.Path)
	perm := publishedFileMode
	if f.Executable {
		perm = publishedExecMode
	}
	digest, err := dir.CopyFrom(s.ctx, s.src, updaterDirName+"/"+f.Path, base, perm, fileio.CopyBounds{MaxBytes: f.Size})
	if err != nil {
		if integrityError(err) {
			return s.fail("stage", err)
		}
		return s.fail("copy", err)
	}
	if digest.Size != f.Size || digest.SHA256 != f.SHA256 {
		return s.fail("stage", fmt.Errorf("updater file %q does not match its manifest size and digest", f.Path))
	}
	if f.Executable {
		file, err := dir.OpenFile(base)
		if err != nil {
			return s.fail("stage", err)
		}
		err = CheckNativeExecutable(file, s.goarch)
		_ = file.Close()
		if err != nil {
			return s.fail("stage", fmt.Errorf("updater file %q: %w", f.Path, err))
		}
	}
	s.digests[f.Path] = digest
	return nil
}

// StageTree streams the manifest-listed payload of src (an image root holding
// updater/) into a new exclusive staging directory under cache. It checks the
// manifest against limits and the cache's free space against the scratch
// budget before creating anything, accepts only the exact expected source
// tree, verifies every copied file and checks that executables run natively
// on goarch. When it fails after creating the staging directory, it returns
// the partial tree with the error so that the caller can discard it.
func StageTree(ctx context.Context, src, cache fileio.Anchor, manifestBytes []byte, m *Manifest, limits policy.ExtractionLimits, goarch string) (*StagedTree, error) {
	if m == nil {
		return nil, publishFailure("", PublishNotVisible, "stage", ReasonUpdaterUnavailable, false, errors.New("no manifest"))
	}
	release := m.Name + "/" + m.Version
	plan, err := planPayload(release, m, manifestBytes, limits)
	if err != nil {
		return nil, err
	}
	if err := checkFreeSpace(cache, release, limits); err != nil {
		return nil, err
	}
	s := &stager{ctx: ctx, src: src, release: release, goarch: goarch, digests: make(map[string]fileio.FileDigest, len(m.Files))}
	if err := s.walkSource(plan); err != nil {
		return nil, err
	}
	readOnly, err := src.ReadOnlyMount()
	if err != nil {
		readOnly = false
	}
	staging, name, err := createStaging(cache)
	if err != nil {
		return nil, s.fail("stage", err)
	}
	defer staging.Close()
	st := &StagedTree{
		cache:          cache,
		name:           name,
		release:        release,
		entrypoint:     m.Entrypoint,
		manifestDigest: policy.Hash(manifestBytes),
		limits:         limits,
		sourceReadOnly: readOnly,
	}
	if err := staging.WriteFileExclusive(manifestFileName, manifestBytes, publishedFileMode); err != nil {
		return st, s.fail("stage", err)
	}
	if err := s.stageDir(staging, plan.root); err != nil {
		return st, err
	}
	if err := staging.Sync(); err != nil {
		return st, s.fail("sync", err)
	}
	for _, f := range m.Files {
		st.files = append(st.files, PublishedFile{Path: f.Path, SHA256: s.digests[f.Path].SHA256, Size: s.digests[f.Path].Size, Executable: f.Executable})
	}
	return st, nil
}

// Name returns the staging directory name in the updater cache.
func (s *StagedTree) Name() string { return s.name }

// Publish makes one no-replace rename of the staged tree to finalName and
// confirms durability. On EEXIST it returns ErrDestinationExists and keeps the
// staging owned and in place.
func (s *StagedTree) Publish(ctx context.Context, finalName string) (*PublishedPayload, error) {
	if s.consumed || s.discarded {
		return nil, publishFailure(s.release, PublishNotVisible, "rename", ReasonUpdaterUnavailable, false, errors.New("staged tree is no longer available"))
	}
	if ctx.Err() != nil {
		return nil, contextFailure(ctx, s.release, PublishNotVisible, "rename")
	}
	if err := s.cache.RenameNoReplace(s.name, s.cache, finalName); err != nil {
		return nil, publishFailure(s.release, PublishNotVisible, "rename", ReasonUpdaterUnavailable, true, err)
	}
	s.consumed = true
	if err := s.cache.Sync(); err != nil {
		return nil, publishFailure(s.release, PublishVisibleNotUsable, "sync-parent", ReasonUpdaterUnavailable, true, err)
	}
	if err := s.cache.Revalidate(); err != nil {
		return nil, publishFailure(s.release, PublishVisibleNotUsable, "revalidate", ReasonUpdaterUnavailable, true, err)
	}
	return &PublishedPayload{
		ManifestDigest:      s.manifestDigest,
		Dir:                 finalName,
		Entrypoint:          s.entrypoint,
		Files:               slices.Clone(s.files),
		Limits:              s.limits,
		SourceMountReadOnly: s.sourceReadOnly,
	}, nil
}

// Discard is idempotent: a no-op after a successful Publish, otherwise it
// removes the call's own staging directory within the cleanup bounds.
func (s *StagedTree) Discard(ctx context.Context) error {
	if s == nil || s.consumed || s.discarded {
		return nil
	}
	res, err := s.cache.RemoveTree(ctx, s.name, cleanupBounds())
	if err != nil {
		return fmt.Errorf("discard staging %w: stopped (%s) after removing %d entries, detached %v: %w", deviceerrors.WithElement(s.name), res.Stop, res.Removed, res.Detached, err)
	}
	s.discarded = true
	return nil
}
