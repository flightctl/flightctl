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
	packageModeTemplateMigrationVersion = 1
	// packageModeTemplateMigrationKey is the per-organization checkpoint key.
	packageModeTemplateMigrationKey = "migration"
	// maxPackageModeTemplateMigrationRetries bounds how many fleet names are
	// carried in the checkpoint for retry, so one organization's checkpoint
	// cannot grow without limit.
	maxPackageModeTemplateMigrationRetries = 256
)

// Migration lifecycle phases.
const (
	packageModeMigrationPending  = "pending"
	packageModeMigrationRunning  = "running"
	packageModeMigrationComplete = "complete"
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
	return &PackageModeFleetTemplateMigrationTask{
		log:         log,
		fleetSvc:    fleetSvc,
		checkpoints: checkpoints,
		config:      config,
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

	startedAt := time.Now()
	run := &migrationRun{checkpoint: checkpoint}

	// Fleets owing a retry are reattempted first and with a fresh read, so a
	// conflict is resolved on the next poll regardless of where the previous
	// poll's budget stopped.
	if err := t.drainRetries(ctx, orgID, run, startedAt); err != nil {
		return t.persist(ctx, orgID, run, err)
	}
	if err := t.scanPages(ctx, orgID, run, startedAt); err != nil {
		return t.persist(ctx, orgID, run, err)
	}
	return t.persist(ctx, orgID, run, nil)
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
	failed                       bool
	processed, skipped, deferred int
}

// budgetExceeded reports whether this poll must stop doing more work.
func (t *PackageModeFleetTemplateMigrationTask) budgetExceeded(run *migrationRun, startedAt time.Time) bool {
	if run.writes >= t.config.BatchSize {
		return true
	}
	return time.Since(startedAt) >= t.config.TimeBudget
}

func (t *PackageModeFleetTemplateMigrationTask) drainRetries(ctx context.Context, orgID uuid.UUID, run *migrationRun, startedAt time.Time) error {
	pending := run.checkpoint.Retry
	run.checkpoint.Retry = nil
	for i, name := range pending {
		if err := ctx.Err(); err != nil {
			// Whatever is left still owes a retry; keep it durable.
			run.checkpoint.Retry = append(run.checkpoint.Retry, pending[i:]...)
			return err
		}
		if t.budgetExceeded(run, startedAt) {
			run.budgetStopped = true
			run.checkpoint.Retry = append(run.checkpoint.Retry, pending[i:]...)
			return nil
		}
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

		completedPage := true
		for i := range fleets.Items {
			if err := ctx.Err(); err != nil {
				return err
			}
			if t.budgetExceeded(run, startedAt) {
				// Stop inside the page without advancing the cursor, so the
				// remainder is listed again next poll instead of being skipped.
				run.budgetStopped = true
				completedPage = false
				break
			}
			t.applyToFleet(ctx, orgID, run, &fleets.Items[i])
		}
		if !completedPage {
			return nil
		}

		if run.retryOverflow {
			// A fleet owing a retry could not be recorded by name, so the only
			// way it is seen again is a replay of the listing. End the poll with
			// the cursor rewound; the listing is not exhausted and a fleet has
			// failed, so this poll cannot be reported as complete either.
			run.checkpoint.Cursor = nil
			return nil
		}

		next := fleets.Metadata.Continue
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

// applyToFleet advances one fleet. Until classification and rewriting are in
// place every fleet counts as processed and nothing is written.
func (t *PackageModeFleetTemplateMigrationTask) applyToFleet(_ context.Context, _ uuid.UUID, run *migrationRun, fleet *domain.Fleet) {
	if fleet == nil || fleet.Metadata.Name == nil {
		return
	}
	run.processed++
	run.skipped++
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
		return
	}
	run.checkpoint.Retry = append(run.checkpoint.Retry, name)
}

func (t *PackageModeFleetTemplateMigrationTask) persist(ctx context.Context, orgID uuid.UUID, run *migrationRun, cause error) error {
	// Completion requires that the listing ran out, nothing is waiting for a
	// retry, no fleet failed, and no bound cut the poll short. Any of those
	// means eligible work may remain.
	if run.listingExhausted && len(run.checkpoint.Retry) == 0 && !run.failed && !run.budgetStopped && cause == nil {
		run.checkpoint.Phase = packageModeMigrationComplete
		run.checkpoint.Cursor = nil
	}
	if err := t.persistCheckpoint(ctx, orgID, run.checkpoint); err != nil {
		if cause != nil {
			return cause
		}
		return err
	}
	t.log.WithFields(logrus.Fields{
		"org_id":      orgID.String(),
		"phase":       run.checkpoint.Phase,
		"processed":   run.processed,
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
	raw, status := t.checkpoints.GetCheckpoint(ctx, packageModeTemplateMigrationConsumer, packageModeMigrationCheckpointKey(orgID))
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
	if status := t.checkpoints.SetCheckpoint(ctx, packageModeTemplateMigrationConsumer, packageModeMigrationCheckpointKey(orgID), encoded); status.Code >= http.StatusBadRequest {
		return fmt.Errorf("writing migration checkpoint returned %d: %s", status.Code, status.Message)
	}
	return nil
}

func packageModeMigrationCheckpointKey(orgID uuid.UUID) string {
	return fmt.Sprintf("%s/%s", orgID.String(), packageModeTemplateMigrationKey)
}
