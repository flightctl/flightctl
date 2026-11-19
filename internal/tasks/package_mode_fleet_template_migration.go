package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	checkpointservice "github.com/flightctl/flightctl/internal/service/checkpoint"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const (
	packageModeTemplateMigrationConsumer = "package-mode-fleet-template-migration"
	// packageModeTemplateMigrationVersion is the checkpoint schema version. A
	// checkpoint written under a different version is discarded, which is what
	// stops a run completed by an earlier build from suppressing work that a
	// later build would do.
	packageModeTemplateMigrationVersion = 3
	// packageModeTemplateMigrationKey is the per-organization checkpoint key
	// for live runs.
	packageModeTemplateMigrationKey = "migration"
	// packageModeTemplateMigrationKeyDry is the per-organization checkpoint
	// key for dry-run previews, so a preview never overwrites live progress.
	packageModeTemplateMigrationKeyDry = "migration-dry"
	// maxPackageModeTemplateMigrationRetries bounds how many fleet names are
	// carried in the checkpoint for retry, so one organization's checkpoint
	// cannot grow without limit.
	maxPackageModeTemplateMigrationRetries = 256
	// packageModeTemplateMigrationPersistTimeout bounds how long a checkpoint
	// write may take after the parent context is cancelled. This follows the
	// pattern in internal/delta_worker/tasks/generate/handler.go.
	packageModeTemplateMigrationPersistTimeout = 5 * time.Second
)

// Migration lifecycle phases.
const (
	packageModeMigrationPending  = "pending"
	packageModeMigrationRunning  = "running"
	packageModeMigrationComplete = "complete"
)

// Migration run modes.
const (
	packageModeMigrationModeDry  = "dry"
	packageModeMigrationModeLive = "live"
)

// PackageModeTemplateMigrationConfig bounds the work performed by one poll.
type PackageModeTemplateMigrationConfig struct {
	// PageSize is how many fleets are listed at a time.
	PageSize int32
	// BatchSize is how many fleets may be written in one poll.
	BatchSize int
	// TimeBudget is the wall-clock budget for one poll.
	TimeBudget time.Duration
	// DryRun classifies and logs without writing anything.
	DryRun bool
}

// deferredFleet records a fleet the migration will not rewrite, with the owner
// that must act instead.
type deferredFleet struct {
	Name  string `json:"name"`
	Owner string `json:"owner"`
}

// packageModeMigrationCheckpoint is the durable state of one organization's run.
type packageModeMigrationCheckpoint struct {
	Version int    `json:"version"`
	Phase   string `json:"phase"`
	// Cursor is the continue token of the page to process next. It advances only
	// once a page has been processed in full, so a poll that stops inside a page
	// replays that page rather than losing its remainder.
	Cursor *string `json:"cursor,omitempty"`
	// Retry holds fleets that could not be completed and must be reattempted on
	// the next poll with a freshly read resource version. It is durable because
	// a budget may end the poll long before the listing is exhausted.
	Retry []string `json:"retry,omitempty"`
	// Deferred records fleets left to their owner, with that owner's identity.
	Deferred []deferredFleet `json:"deferred,omitempty"`
	// Transformed counts fleets rewritten across the whole run.
	Transformed int `json:"transformed,omitempty"`
	// Pending holds the fleet names from an incomplete page that were listed but
	// not yet processed. When non-empty the cursor still points at the page
	// start and the pending identities are resumed via GetFleet on the next
	// poll before any new listing starts. Backward-compatible: an older reader
	// ignoring this field replays the whole page from the cursor.
	Pending []string `json:"pending,omitempty"`
	// NextCursor is the server-provided continuation token for the page after
	// the one whose items are in Pending. It is applied once all Pending items
	// are completed.
	NextCursor *string `json:"next_cursor,omitempty"`
	// ScanFirst controls whether scan or retry work runs first on the next
	// poll, ensuring fair alternation so neither phase can permanently starve
	// the other. Backward-compatible: defaults to false (retries first).
	ScanFirst bool `json:"scan_first,omitempty"`
}

// PackageModeFleetTemplateMigrationTask rewrites fleet templates whose OS image
// is an unguarded literal so that the image is emitted only for devices that
// can apply it.
//
// It is one-shot and resumable: a checkpoint records the paging cursor, the
// fleets still owing a retry, and completion, so an interrupted run resumes and
// a completed run costs one checkpoint read per poll. It adds no code to the
// rollout path.
type PackageModeFleetTemplateMigrationTask struct {
	log         logrus.FieldLogger
	fleetSvc    fleetservice.Service
	checkpoints checkpointservice.Service
	config      PackageModeTemplateMigrationConfig
	mode        string           // "dry" or "live"
	now         func() time.Time // defaults to time.Now; test-injectable
}

func NewPackageModeFleetTemplateMigrationTask(
	fleetSvc fleetservice.Service,
	checkpoints checkpointservice.Service,
	config PackageModeTemplateMigrationConfig,
	log logrus.FieldLogger,
) (*PackageModeFleetTemplateMigrationTask, error) {
	if fleetSvc == nil || checkpoints == nil {
		return nil, errors.New("package-mode template migration requires a fleet service and checkpoint service")
	}
	if config.PageSize < 1 {
		return nil, fmt.Errorf("package-mode template migration page size must be at least 1")
	}
	if config.BatchSize < 1 {
		return nil, fmt.Errorf("package-mode template migration batch size must be at least 1")
	}
	if config.TimeBudget <= 0 {
		return nil, errors.New("package-mode template migration time budget must be positive")
	}
	if log == nil {
		log = logrus.New()
	}
	mode := packageModeMigrationModeLive
	if config.DryRun {
		mode = packageModeMigrationModeDry
	}
	return &PackageModeFleetTemplateMigrationTask{
		log:         log,
		fleetSvc:    fleetSvc,
		checkpoints: checkpoints,
		config:      config,
		mode:        mode,
		now:         time.Now,
	}, nil
}

// Poll advances one organization's migration.
func (t *PackageModeFleetTemplateMigrationTask) Poll(ctx context.Context, orgID uuid.UUID) {
	if err := t.poll(ctx, orgID); err != nil {
		t.log.WithError(err).WithField("orgID", orgID).Error("Package-mode fleet template migration stopped")
	}
}

func (t *PackageModeFleetTemplateMigrationTask) poll(ctx context.Context, orgID uuid.UUID) error {
	checkpoint, err := t.loadCheckpoint(ctx, orgID)
	if err != nil {
		return err
	}
	if checkpoint.Phase == packageModeMigrationComplete {
		return nil
	}
	checkpoint.Phase = packageModeMigrationRunning

	startedAt := t.now()
	run := &migrationRun{checkpoint: checkpoint}

	// Fair scheduling: alternate which phase runs first so neither retries
	// nor scanning can permanently starve the other. The preference is
	// flipped before persisting regardless of error so a failing phase
	// cannot hold the preference permanently.
	var firstErr, secondErr error
	if checkpoint.ScanFirst {
		firstErr = t.scanPages(ctx, orgID, run, startedAt)
		if firstErr == nil {
			secondErr = t.drainRetries(ctx, orgID, run, startedAt)
		}
		run.checkpoint.ScanFirst = false
	} else {
		firstErr = t.drainRetries(ctx, orgID, run, startedAt)
		if firstErr == nil {
			secondErr = t.scanPages(ctx, orgID, run, startedAt)
		}
		run.checkpoint.ScanFirst = true
	}
	if firstErr != nil {
		return t.persist(ctx, orgID, run, firstErr)
	}
	return t.persist(ctx, orgID, run, secondErr)
}

// migrationRun is the mutable state of one poll.
type migrationRun struct {
	checkpoint packageModeMigrationCheckpoint
	// listingExhausted is true once the listing reached its end in this poll.
	listingExhausted bool
	// writes counts fleets written in this poll, against the batch size.
	writes int
	// budgetStopped is true when a bound ended the poll before the work ran out.
	budgetStopped bool
	// retryOverflow is true once the retry list was full and a fleet owing a
	// retry could not be recorded by name. The listing has to be replayed from
	// the start instead, so that fleet is seen again.
	retryOverflow bool
	// failed is true when any fleet could not be completed in this poll.
	failed                                 bool
	processed, skipped, deferred, eligible int
	// attemptedRetries tracks names attempted this poll so a re-queued failure
	// is not retried again in the same poll (bounded per-poll work).
	attemptedRetries map[string]bool
}

// budgetExceeded reports whether this poll must stop doing more work.
func (t *PackageModeFleetTemplateMigrationTask) budgetExceeded(run *migrationRun, startedAt time.Time) bool {
	if run.writes >= t.config.BatchSize {
		return true
	}
	return t.now().Sub(startedAt) >= t.config.TimeBudget
}

func (t *PackageModeFleetTemplateMigrationTask) drainRetries(ctx context.Context, orgID uuid.UUID, run *migrationRun, startedAt time.Time) error {
	pending := run.checkpoint.Retry
	run.checkpoint.Retry = nil
	if run.attemptedRetries == nil {
		run.attemptedRetries = make(map[string]bool)
	}
	for i, name := range pending {
		if err := ctx.Err(); err != nil {
			// Place unattempted tail BEFORE already-requeued failures so
			// healthy entries get a turn on the next poll instead of being
			// permanently stuck behind slow failures.
			run.checkpoint.Retry = append(pending[i:], run.checkpoint.Retry...)
			return err
		}
		if t.budgetExceeded(run, startedAt) {
			run.budgetStopped = true
			run.checkpoint.Retry = append(pending[i:], run.checkpoint.Retry...)
			return nil
		}
		// Skip names already attempted this poll so a re-queued failure
		// cannot loop indefinitely when the test clock is frozen.
		if run.attemptedRetries[name] {
			t.rememberRetry(run, name)
			continue
		}
		run.attemptedRetries[name] = true

		fleet, status := t.fleetSvc.GetFleet(ctx, orgID, name, domain.GetFleetParams{})
		if status.Code == http.StatusNotFound {
			// The fleet is gone, so there is nothing left to migrate for it.
			continue
		}
		if status.Code >= http.StatusBadRequest {
			run.failed = true
			t.rememberRetry(run, name)
			continue
		}
		t.applyToFleet(ctx, orgID, run, fleet)
	}
	return nil
}

func (t *PackageModeFleetTemplateMigrationTask) scanPages(ctx context.Context, orgID uuid.UUID, run *migrationRun, startedAt time.Time) error {
	// Resume a pending page from a previous poll before listing anything new.
	if len(run.checkpoint.Pending) > 0 {
		done, err := t.completePendingPage(ctx, orgID, run, startedAt)
		if err != nil || !done || run.listingExhausted {
			return err
		}
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if t.budgetExceeded(run, startedAt) {
			run.budgetStopped = true
			return nil
		}
		pageStart := cloneString(run.checkpoint.Cursor)
		fleets, status := t.fleetSvc.ListFleets(ctx, orgID, domain.ListFleetsParams{
			Limit:    lo.ToPtr(t.config.PageSize),
			Continue: cloneString(pageStart),
		})
		if status.Code >= http.StatusBadRequest {
			return fmt.Errorf("fleet listing returned %d: %s", status.Code, status.Message)
		}

		// Collect named fleet items, skipping entries without a name.
		type namedFleet struct {
			name  string
			fleet *domain.Fleet
		}
		named := make([]namedFleet, 0, len(fleets.Items))
		for i := range fleets.Items {
			if fleets.Items[i].Metadata.Name != nil {
				named = append(named, namedFleet{
					name:  *fleets.Items[i].Metadata.Name,
					fleet: &fleets.Items[i],
				})
			}
		}
		next := fleets.Metadata.Continue

		// If the listing itself consumed the budget, save the page as
		// pending rather than processing any fleet. The identities will
		// be resumed via GetFleet on a later poll.
		if t.budgetExceeded(run, startedAt) {
			if len(named) > 0 {
				pending := make([]string, len(named))
				for i, nf := range named {
					pending[i] = nf.name
				}
				run.checkpoint.Pending = pending
				run.checkpoint.NextCursor = cloneString(next)
			} else if next == nil {
				run.listingExhausted = true
				run.checkpoint.Cursor = nil
				return nil
			} else {
				// An empty page with an advancing continuation: advance
				// the cursor so the same empty page is not re-listed
				// forever when the listing always consumes the budget.
				if pageStart != nil && *next == *pageStart {
					return errors.New("fleet listing did not advance its cursor")
				}
				run.checkpoint.Cursor = cloneString(next)
			}
			run.budgetStopped = true
			return nil
		}

		// Process items one at a time, checking the budget BEFORE each.
		completedPage := true
		for i, nf := range named {
			if err := ctx.Err(); err != nil {
				return err
			}
			if t.budgetExceeded(run, startedAt) {
				// Save remaining names as pending for the next poll.
				remaining := make([]string, len(named)-i)
				for j, r := range named[i:] {
					remaining[j] = r.name
				}
				run.checkpoint.Pending = remaining
				run.checkpoint.NextCursor = cloneString(next)
				run.budgetStopped = true
				completedPage = false
				break
			}
			t.applyToFleet(ctx, orgID, run, nf.fleet)
		}
		if !completedPage {
			return nil
		}

		if run.retryOverflow {
			run.checkpoint.Cursor = nil
			return nil
		}

		if next == nil {
			run.listingExhausted = true
			run.checkpoint.Cursor = nil
			return nil
		}
		if pageStart != nil && *next == *pageStart {
			return errors.New("fleet listing did not advance its cursor")
		}
		run.checkpoint.Cursor = cloneString(next)
	}
}

// completePendingPage resumes and finishes a durably saved pending page. It
// returns (true, nil) when the page is fully drained and the cursor has been
// advanced, (false, nil) when pending work remains, or (false, err) on error.
func (t *PackageModeFleetTemplateMigrationTask) completePendingPage(ctx context.Context, orgID uuid.UUID, run *migrationRun, startedAt time.Time) (bool, error) {
	if err := t.resumePending(ctx, orgID, run, startedAt); err != nil {
		return false, err
	}
	if len(run.checkpoint.Pending) > 0 {
		return false, nil
	}
	next := run.checkpoint.NextCursor
	run.checkpoint.NextCursor = nil
	// Guard: reject a continuation token that did not advance.
	if next != nil && run.checkpoint.Cursor != nil && *next == *run.checkpoint.Cursor {
		return false, errors.New("fleet listing did not advance its cursor")
	}
	run.checkpoint.Cursor = cloneString(next)
	if run.checkpoint.Cursor == nil && !run.failed && !run.retryOverflow {
		run.listingExhausted = true
	}
	return true, nil
}

// resumePending processes durably saved fleet identities from an incomplete
// page, using GetFleet to fetch each one freshly. A 404 means the fleet was
// deleted and can be forgotten; other errors move the name to the retry list.
func (t *PackageModeFleetTemplateMigrationTask) resumePending(ctx context.Context, orgID uuid.UUID, run *migrationRun, startedAt time.Time) error {
	pending := run.checkpoint.Pending
	var remaining []string
	for i, name := range pending {
		if err := ctx.Err(); err != nil {
			remaining = append(remaining, pending[i:]...)
			run.checkpoint.Pending = remaining
			return err
		}
		if t.budgetExceeded(run, startedAt) {
			remaining = append(remaining, pending[i:]...)
			run.budgetStopped = true
			break
		}
		fleet, status := t.fleetSvc.GetFleet(ctx, orgID, name, domain.GetFleetParams{})
		if status.Code == http.StatusNotFound {
			continue
		}
		if status.Code >= http.StatusBadRequest {
			run.failed = true
			if !t.queueRetryOrKeepPending(run, name) {
				remaining = append(remaining, name)
			}
			continue
		}
		t.applyToFleet(ctx, orgID, run, fleet)
	}
	run.checkpoint.Pending = remaining
	return nil
}

// queueRetryOrKeepPending tries to move a failed name to the retry list.
// If the retry list is full, it returns false so the caller can keep the
// name in the pending list instead, preserving it without advancing past it.
func (t *PackageModeFleetTemplateMigrationTask) queueRetryOrKeepPending(run *migrationRun, name string) bool {
	for _, existing := range run.checkpoint.Retry {
		if existing == name {
			return true // already in retry
		}
	}
	if len(run.checkpoint.Retry) >= maxPackageModeTemplateMigrationRetries {
		return false
	}
	run.checkpoint.Retry = append(run.checkpoint.Retry, name)
	return true
}

// applyToFleet classifies one fleet and acts on the classification.
func (t *PackageModeFleetTemplateMigrationTask) applyToFleet(ctx context.Context, orgID uuid.UUID, run *migrationRun, fleet *domain.Fleet) {
	if fleet == nil || fleet.Metadata.Name == nil {
		return
	}
	run.processed++

	disposition, owner := classifyFleetForMigration(fleet)
	switch disposition {
	case dispositionDeferResourceSync:
		run.deferred++
		t.rememberDeferred(run, *fleet.Metadata.Name, owner)
	case dispositionTransform:
		run.eligible++
		t.guardFleetOsImage(ctx, orgID, run, fleet)
	default:
		run.skipped++
	}
}

// guardFleetOsImage rewrites one fleet's OS image so that it is emitted only
// for devices that can apply it.
//
// The fleet is written back exactly as it was read with one string changed, so
// every other field, including a catalog item reference, round-trips untouched.
// The resource version that was read goes back with it, which is what makes the
// write conditional on nothing else having changed the fleet meanwhile.
func (t *PackageModeFleetTemplateMigrationTask) guardFleetOsImage(ctx context.Context, orgID uuid.UUID, run *migrationRun, fleet *domain.Fleet) {
	name := *fleet.Metadata.Name
	current := fleet.Spec.Template.Spec.Os.Image
	guarded := guardedOsImage(current)
	if guarded == current {
		// Nothing would change, so do not write: a write would produce a new
		// template version and a rollout for no reason.
		run.skipped++
		return
	}
	if t.config.DryRun {
		t.log.WithFields(logrus.Fields{"org_id": orgID.String(), "fleet": name}).
			Info("Would guard fleet template OS image")
		run.skipped++
		return
	}

	updated := *fleet
	osSpec := *fleet.Spec.Template.Spec.Os
	osSpec.Image = guarded
	updated.Spec.Template.Spec.Os = &osSpec

	// Count the attempt, not the success, so a fleet that keeps conflicting
	// cannot consume the whole poll.
	run.writes++
	_, status := t.fleetSvc.ReplaceFleetRequireExisting(ctx, orgID, name, updated)
	if status.Code == http.StatusNotFound {
		// The fleet was deleted between our read and this write. Do not
		// re-create it and do not retry: the fleet is gone.
		t.log.WithFields(logrus.Fields{
			"org_id": orgID.String(), "fleet": name,
		}).Debug("Fleet deleted between read and write, skipping guard")
		return
	}
	if status.Code >= http.StatusBadRequest {
		run.failed = true
		t.rememberRetry(run, name)
		t.log.WithFields(logrus.Fields{
			"org_id": orgID.String(), "fleet": name, "code": status.Code,
		}).Info("Deferring fleet template guard to the next poll")
		return
	}
	run.checkpoint.Transformed++
}

// rememberDeferred records a fleet the migration will not rewrite together with
// the owner that has to make the change instead.
func (t *PackageModeFleetTemplateMigrationTask) rememberDeferred(run *migrationRun, name, owner string) {
	for _, existing := range run.checkpoint.Deferred {
		if existing.Name == name {
			return
		}
	}
	run.checkpoint.Deferred = append(run.checkpoint.Deferred, deferredFleet{Name: name, Owner: owner})
}

// rememberRetry records that a fleet must be reattempted on the next poll. When
// the retry list is full the cursor is rewound to the start of the listing so
// that nothing is dropped, at the cost of re-listing.
func (t *PackageModeFleetTemplateMigrationTask) rememberRetry(run *migrationRun, name string) {
	for _, existing := range run.checkpoint.Retry {
		if existing == name {
			return
		}
	}
	if len(run.checkpoint.Retry) >= maxPackageModeTemplateMigrationRetries {
		run.retryOverflow = true
		run.checkpoint.Cursor = nil
		// Invalidate any cached page so the full replay from cursor=nil
		// cannot be bypassed by a stale Pending/NextCursor.
		run.checkpoint.Pending = nil
		run.checkpoint.NextCursor = nil
		return
	}
	run.checkpoint.Retry = append(run.checkpoint.Retry, name)
}

func (t *PackageModeFleetTemplateMigrationTask) persist(ctx context.Context, orgID uuid.UUID, run *migrationRun, cause error) error {
	// When retry overflow triggered, normalize the checkpoint so that
	// caller-local bookkeeping (remaining/next assignments after
	// rememberRetry returned) cannot restore stale Pending/NextCursor and
	// bypass the intended full replay from cursor=nil.
	if run.retryOverflow {
		run.checkpoint.Cursor = nil
		run.checkpoint.Pending = nil
		run.checkpoint.NextCursor = nil
	}

	// Completion requires that the listing ran out, nothing is waiting for a
	// retry or pending processing, no fleet failed, no retry overflow, and
	// no bound cut the poll short. Any of those means eligible work may remain.
	if run.listingExhausted &&
		len(run.checkpoint.Retry) == 0 &&
		len(run.checkpoint.Pending) == 0 &&
		!run.failed && !run.budgetStopped && !run.retryOverflow && cause == nil {
		run.checkpoint.Phase = packageModeMigrationComplete
		run.checkpoint.Cursor = nil
	}
	if err := t.persistCheckpoint(ctx, orgID, run.checkpoint); err != nil {
		if cause != nil {
			return errors.Join(cause, fmt.Errorf("checkpoint write also failed: %w", err))
		}
		return err
	}
	t.log.WithFields(logrus.Fields{
		"org_id":      orgID.String(),
		"phase":       run.checkpoint.Phase,
		"processed":   run.processed,
		"eligible":    run.eligible,
		"skipped":     run.skipped,
		"deferred":    run.deferred,
		"transformed": run.checkpoint.Transformed,
		"retrying":    len(run.checkpoint.Retry),
		"writes":      run.writes,
	}).Debug("Package-mode fleet template migration poll completed")
	return cause
}

func (t *PackageModeFleetTemplateMigrationTask) loadCheckpoint(ctx context.Context, orgID uuid.UUID) (packageModeMigrationCheckpoint, error) {
	fresh := packageModeMigrationCheckpoint{
		Version: packageModeTemplateMigrationVersion,
		Phase:   packageModeMigrationPending,
	}
	raw, status := t.checkpoints.GetCheckpoint(ctx, packageModeTemplateMigrationConsumer, packageModeMigrationCheckpointKey(orgID, t.mode))
	if status.Code == http.StatusNotFound {
		return fresh, nil
	}
	if status.Code >= http.StatusBadRequest {
		return fresh, fmt.Errorf("reading migration checkpoint returned %d: %s", status.Code, status.Message)
	}
	var stored packageModeMigrationCheckpoint
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.log.WithError(err).WithField("orgID", orgID).Warn("Discarding unreadable migration checkpoint")
		return fresh, nil
	}
	if stored.Version != packageModeTemplateMigrationVersion {
		// A checkpoint from another schema version describes work decided under
		// different rules, so it cannot stand in for this version's run.
		t.log.WithFields(logrus.Fields{
			"orgID": orgID, "found": stored.Version, "want": packageModeTemplateMigrationVersion,
		}).Info("Restarting package-mode fleet template migration after a checkpoint version change")
		return fresh, nil
	}
	if stored.Phase == "" {
		stored.Phase = packageModeMigrationPending
	}
	return stored, nil
}

func (t *PackageModeFleetTemplateMigrationTask) persistCheckpoint(ctx context.Context, orgID uuid.UUID, checkpoint packageModeMigrationCheckpoint) error {
	checkpoint.Version = packageModeTemplateMigrationVersion
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	// Use a finalization context that is independent of parent cancellation
	// but bounded by a timeout, so a cancelled poll context does not prevent
	// persisting retry rotation and other durable progress. This follows the
	// pattern in internal/delta_worker/tasks/generate/handler.go.
	writeCtx, writeCancel := context.WithTimeout(context.WithoutCancel(ctx), packageModeTemplateMigrationPersistTimeout)
	defer writeCancel()
	if status := t.checkpoints.SetCheckpoint(writeCtx, packageModeTemplateMigrationConsumer, packageModeMigrationCheckpointKey(orgID, t.mode), encoded); status.Code >= http.StatusBadRequest {
		return fmt.Errorf("writing migration checkpoint returned %d: %s", status.Code, status.Message)
	}
	return nil
}

func packageModeMigrationCheckpointKey(orgID uuid.UUID, mode string) string {
	key := packageModeTemplateMigrationKey
	if mode == packageModeMigrationModeDry {
		key = packageModeTemplateMigrationKeyDry
	}
	return fmt.Sprintf("%s/%s", orgID.String(), key)
}
