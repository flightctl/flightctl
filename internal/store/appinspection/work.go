package appinspection

import (
	"context"
	"errors"
	"time"

	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/storeutil"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AdmissionApplication struct {
	Context    string
	User       string
	Mechanism  model.InspectionPullMechanism
	References []ReferenceInput
}

type Admission struct {
	Owner        OwnerVersion
	EventID      uuid.UUID
	PreviousID   uuid.UUID
	Snapshot     model.InspectionFingerprint
	ForceRender  bool
	Applications []AdmissionApplication
}

type AdmissionResult struct {
	ContinuationID uuid.UUID
	WorkIDs        []uuid.UUID
}

// Admit records a continuation and its current usages together. The observed
// continuation ID fences replacements within the same owner generation.
func (s *Store) Admit(ctx context.Context, orgID uuid.UUID, admission Admission) (AdmissionResult, error) {
	if admission.EventID == uuid.Nil || admission.Snapshot == (model.InspectionFingerprint{}) {
		return AdmissionResult{}, errors.New("inspection admission requires an event and snapshot")
	}
	owner := admission.Owner
	table, column, err := owner.tableAndColumn()
	if err != nil {
		return AdmissionResult{}, err
	}
	continuation := model.ApplicationInspectionContinuation{
		OrgID: orgID, ID: uuid.New(), Generation: owner.Generation, OwnerCreatedAt: owner.CreatedAt,
		TemplateIdentity: owner.TemplateIdentity, Snapshot: admission.Snapshot, EventID: admission.EventID, Current: true, ForceRender: admission.ForceRender,
	}
	if owner.Kind == OwnerDevice {
		continuation.DeviceName = &owner.Name
	} else {
		continuation.FleetName = &owner.Name
	}
	bindings := make([]UsageBinding, 0, len(admission.Applications))
	var refs []model.ApplicationInspectionRef
	for _, app := range admission.Applications {
		id := uuid.New()
		known, err := normalizeReferences(orgID, id, app.References)
		if err != nil {
			return AdmissionResult{}, err
		}
		refs = append(refs, known...)
		bindings = append(bindings, UsageBinding{
			ApplicationContext: app.Context, InspectionID: id, User: app.User, Mechanism: app.Mechanism,
			Pending: true, ContinuationID: &continuation.ID,
		})
	}
	usages, err := prepareUsages(orgID, owner, bindings)
	if err != nil {
		return AdmissionResult{}, err
	}

	var result AdmissionResult
	err = store.DB(ctx, s.db).Transaction(func(tx *gorm.DB) error {
		current, err := lockOwnerVersion(tx, table, orgID, owner)
		if err != nil || !current {
			return err
		}
		owned := func() *gorm.DB {
			return forOwner(tx.Model(&model.ApplicationInspectionContinuation{}), column, orgID, owner.Name)
		}
		var replay model.ApplicationInspectionContinuation
		err = owned().Where("event_id = ?", admission.EventID).Take(&replay).Error
		if err == nil {
			if replay.Current && replay.Generation == owner.Generation && replay.Snapshot == admission.Snapshot {
				result.ContinuationID = replay.ID
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var previous model.ApplicationInspectionContinuation
		err = owned().Where("current").Take(&previous).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if previous.ID != admission.PreviousID {
			return nil
		}
		if previous.ID != uuid.Nil {
			if err := owned().Where("id = ?", previous.ID).Updates(map[string]any{
				"current": false, "superseded_at": gorm.Expr("clock_timestamp()"),
			}).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(&continuation).Error; err != nil {
			return err
		}
		work := make([]model.ApplicationInspection, 0, len(usages))
		for _, usage := range usages {
			work = append(work, model.ApplicationInspection{OrgID: orgID, ID: usage.InspectionID, State: model.InspectionResolving})
			result.WorkIDs = append(result.WorkIDs, usage.InspectionID)
		}
		if len(work) != 0 {
			if err := tx.CreateInBatches(&work, 500).Error; err != nil {
				return err
			}
		}
		if err := insertReferences(tx, refs); err != nil {
			return err
		}
		if err := replaceUsages(tx, column, orgID, owner.Name, usages); err != nil {
			return err
		}
		result.ContinuationID = continuation.ID
		return nil
	})
	if err != nil {
		return AdmissionResult{}, storeutil.ErrorFromGormError(err)
	}
	return result, nil
}

type WorkClaim struct {
	Token      uuid.UUID
	ClaimUntil time.Time
	State      model.InspectionState
	Input      model.ApplicationInspectionInput `gorm:"serializer:json"`
	InputKey   string
}

const liveWork = `state IN ('resolving','pending') AND EXISTS (
	SELECT 1 FROM application_inspection_usages AS u
	WHERE u.org_id = application_inspections.org_id AND u.inspection_id = application_inspections.id AND u.pending
)`

const unclaimedWork = liveWork + ` AND (claim_until IS NULL OR claim_until <= clock_timestamp())`

// Claim grants a lease only for work that still has a waiter and has no live claim.
func (s *Store) Claim(ctx context.Context, orgID, id uuid.UUID, duration time.Duration) (WorkClaim, error) {
	if duration < time.Microsecond {
		return WorkClaim{}, errors.New("inspection claim requires a positive lease")
	}
	var claim WorkClaim
	err := store.DB(ctx, s.db).Raw(`UPDATE application_inspections
		SET claim_token = ?, claimed_at = clock_timestamp(), claim_until = clock_timestamp() + (? * interval '1 microsecond')
		WHERE org_id = ? AND id = ? AND `+unclaimedWork+`
		RETURNING claim_token AS token, claim_until, state, input, input_key`, uuid.New(), duration.Microseconds(), orgID, id).Scan(&claim).Error
	return claim, storeutil.ErrorFromGormError(err)
}

func lockClaim(tx *gorm.DB, orgID, id, token uuid.UUID) (model.ApplicationInspection, error) {
	var work model.ApplicationInspection
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND id = ? AND claim_token = ? AND claim_until > clock_timestamp()", orgID, id, token).
		Where(liveWork).Take(&work).Error
	return work, err
}

// PromoteOrAttach joins all waiters for a resolved input to one reusable attempt.
func (s *Store) PromoteOrAttach(ctx context.Context, orgID, id, token uuid.UUID, input model.ApplicationInspectionInput) (uuid.UUID, error) {
	key, err := KeyForInput(orgID, input)
	if err != nil {
		return uuid.Nil, err
	}
	var canonical model.ApplicationInspection
	err = store.DB(ctx, s.db).Transaction(func(tx *gorm.DB) error {
		work, err := lockClaim(tx, orgID, id, token)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil || work.State != model.InspectionResolving {
			return err
		}
		candidate := model.ApplicationInspection{OrgID: orgID, ID: uuid.New(), InputKey: string(key), Input: input, State: model.InspectionPending}
		canonical = candidate
		if err := insertCanonicalAttempt(tx, &canonical); err != nil {
			return err
		}
		if canonical.FreshUntil != nil {
			// Retire only the lookup key; existing usages still reference this snapshot.
			retired := tx.Model(&model.ApplicationInspection{}).Where("org_id = ? AND id = ? AND fresh_until <= clock_timestamp()", orgID, canonical.ID).Update("input_key", "")
			if retired.Error != nil {
				return retired.Error
			}
			if retired.RowsAffected != 0 {
				canonical = candidate
				if err := insertCanonicalAttempt(tx, &canonical); err != nil {
					return err
				}
			}
		}
		pending := canonical.State == model.InspectionPending
		if pending {
			if err := tx.Exec(`INSERT INTO application_inspection_refs
				(org_id, inspection_id, reference, role, mechanism, registry, port, path)
				SELECT org_id, ?, reference, role, mechanism, registry, port, path
				FROM application_inspection_refs WHERE org_id = ? AND inspection_id = ? ON CONFLICT DO NOTHING`, canonical.ID, orgID, id).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&model.ApplicationInspectionUsage{}).Where("org_id = ? AND inspection_id = ?", orgID, id).
			Updates(map[string]any{"inspection_id": canonical.ID, "pending": pending}).Error; err != nil {
			return err
		}
		return tx.Delete(&work).Error
	})
	if err != nil {
		return uuid.Nil, storeutil.ErrorFromGormError(err)
	}
	return canonical.ID, nil
}

// Finish commits the result and settles linked usages while the claim is current.
func (s *Store) Finish(ctx context.Context, orgID, id, token uuid.UUID, attempt Attempt) (bool, error) {
	if err := validateAttempt(attempt); err != nil {
		return false, err
	}
	refs, err := normalizeReferences(orgID, id, attempt.References)
	if err != nil {
		return false, err
	}
	var finished bool
	err = store.DB(ctx, s.db).Transaction(func(tx *gorm.DB) error {
		work, err := lockClaim(tx, orgID, id, token)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if work.State == model.InspectionPending {
			key, err := KeyForInput(orgID, attempt.Input)
			if err != nil || string(key) != work.InputKey {
				return errors.New("inspection result does not match the claimed input")
			}
		} else if attempt.State != model.InspectionFinishedIncomplete {
			return errors.New("unresolved inspection cannot finish complete")
		}
		if err := finishAttempt(tx, orgID, id, attempt, refs); err != nil {
			return err
		}
		finished = true
		return nil
	})
	return finished, storeutil.ErrorFromGormError(err)
}

func finishAttempt(tx *gorm.DB, orgID, id uuid.UUID, attempt Attempt, refs []model.ApplicationInspectionRef) error {
	row := model.ApplicationInspection{State: attempt.State, Revision: attempt.Revision, DiagnosticCodes: attempt.DiagnosticCodes}
	if err := tx.Model(&model.ApplicationInspection{}).Where("org_id = ? AND id = ?", orgID, id).
		Select("State", "Revision", "DiagnosticCodes", "ClaimToken", "ClaimUntil").Updates(&row).Error; err != nil {
		return err
	}
	if attempt.State == model.InspectionComplete && attempt.Input.Helm != nil && attempt.Input.Helm.FreshFor > 0 {
		if err := tx.Model(&model.ApplicationInspection{}).Where("org_id = ? AND id = ?", orgID, id).
			Update("fresh_until", gorm.Expr("clock_timestamp() + (? * interval '1 microsecond')", attempt.Input.Helm.FreshFor.Microseconds())).Error; err != nil {
			return err
		}
	}
	if err := insertReferences(tx, refs); err != nil {
		return err
	}
	return tx.Model(&model.ApplicationInspectionUsage{}).Where("org_id = ? AND inspection_id = ? AND pending", orgID, id).Update("pending", false).Error
}

func (s *Store) ListRecoverableWork(ctx context.Context, orgID, after uuid.UUID, limit int) ([]uuid.UUID, error) {
	return s.listWorkIDs(ctx, &model.ApplicationInspection{}, orgID, after, limit, unclaimedWork)
}

// Readiness is derived from usages so the last of two concurrent completions
// cannot miss a wakeup when each transaction finishes a different application.
const readyContinuation = `current AND consumed_at IS NULL AND NOT EXISTS (
	SELECT 1 FROM application_inspection_usages AS u
	WHERE u.org_id = application_inspection_continuations.org_id AND u.continuation_id = application_inspection_continuations.id AND u.pending
)`

const runnableContinuation = `((` + readyContinuation + `) OR pending_version IS NOT NULL)`

func (s *Store) ListReadyContinuations(ctx context.Context, orgID, after uuid.UUID, limit int) ([]uuid.UUID, error) {
	return s.listWorkIDs(ctx, &model.ApplicationInspectionContinuation{}, orgID, after, limit, runnableContinuation)
}

func (s *Store) listWorkIDs(ctx context.Context, table any, orgID, after uuid.UUID, limit int, predicate string, args ...any) ([]uuid.UUID, error) {
	if err := validatePageSize(limit); err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	err := store.DB(ctx, s.db).Model(table).Where("org_id = ? AND id > ?", orgID, after).Where(predicate, args...).Order("id").Limit(limit).Pluck("id", &ids).Error
	return ids, storeutil.ErrorFromGormError(err)
}

// ConsumeIfCurrent must share the transaction with publication so a rollback
// leaves the continuation ready for another delivery.
func (s *Store) ConsumeIfCurrent(ctx context.Context, orgID uuid.UUID, owner Owner, id uuid.UUID, snapshot model.InspectionFingerprint) (bool, error) {
	return s.consumeIfCurrent(ctx, orgID, owner, id, snapshot, true)
}

// ConsumeForRenderFailure settles a current render independently of inspection result freshness.
func (s *Store) ConsumeForRenderFailure(ctx context.Context, orgID uuid.UUID, owner Owner, id uuid.UUID, snapshot model.InspectionFingerprint) (bool, error) {
	return s.consumeIfCurrent(ctx, orgID, owner, id, snapshot, false)
}

func (s *Store) consumeIfCurrent(ctx context.Context, orgID uuid.UUID, owner Owner, id uuid.UUID, snapshot model.InspectionFingerprint, requireFresh bool) (bool, error) {
	table, column, err := owner.tableAndColumn()
	if err != nil {
		return false, err
	}
	var applied bool
	err = store.RunInTransaction(ctx, s.db, func(tx *gorm.DB) error {
		var continuation model.ApplicationInspectionContinuation
		err := forOwner(tx, column, orgID, owner.Name).Where("id = ? AND NOT invalidated", id).Where(readyContinuation).Take(&continuation).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil || continuation.Snapshot != snapshot {
			return err
		}
		version := OwnerVersion{Owner: owner, Generation: continuation.Generation, CreatedAt: continuation.OwnerCreatedAt}
		matches, err := lockOwnerVersion(tx, table, orgID, version)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil || !matches {
			return err
		}
		current := tx.Model(&model.ApplicationInspectionContinuation{}).Where("org_id = ? AND id = ?", orgID, id).
			Where(readyContinuation).Where("NOT invalidated")
		if requireFresh {
			current = current.Where(freshContinuation)
		}
		result := current.Update("consumed_at", gorm.Expr("clock_timestamp()"))
		applied = result.RowsAffected != 0
		return result.Error
	})
	return applied, storeutil.ErrorFromGormError(err)
}
