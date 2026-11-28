package appinspection

import (
	"context"
	"errors"
	"fmt"

	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/storeutil"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct {
	db *gorm.DB
}

func NewStore(db *gorm.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("application inspection database is required")
	}
	return &Store{db: db}, nil
}

const (
	completeAttemptPredicate  = "state = 'complete'"
	canonicalAttemptPredicate = "input_key <> '' AND state IN ('pending','complete')"
)

func (s *Store) InitialMigration(ctx context.Context) error {
	db := store.DB(ctx, s.db)
	if err := db.AutoMigrate(&model.ApplicationInspection{}, &model.ApplicationInspectionRef{}, &model.ApplicationInspectionUsage{}, &model.ApplicationInspectionContinuation{}); err != nil {
		return err
	}
	if db.Migrator().HasConstraint(&model.ApplicationInspection{}, "application_inspection_state") {
		if err := db.Migrator().DropConstraint(&model.ApplicationInspection{}, "application_inspection_state"); err != nil {
			return err
		}
	}
	for _, statement := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS application_inspection_reusable_key ON application_inspections (org_id, input_key) WHERE ` + canonicalAttemptPredicate,
		`DROP INDEX IF EXISTS application_inspection_complete_key`,
		`DROP INDEX IF EXISTS application_inspection_canonical_key`,
		`CREATE INDEX IF NOT EXISTS application_inspection_recoverable ON application_inspections (org_id, id) WHERE state IN ('resolving','pending')`,
		`CREATE INDEX IF NOT EXISTS inspection_continuation_recoverable ON application_inspection_continuations (org_id, id) WHERE current AND consumed_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS inspection_continuation_notify ON application_inspection_continuations (org_id, id) WHERE pending_version IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS inspection_continuation_cleanup ON application_inspection_continuations (org_id, superseded_at, id) WHERE NOT current`,
		`CREATE UNIQUE INDEX IF NOT EXISTS inspection_continuation_current_device ON application_inspection_continuations (org_id, device_name) WHERE current AND device_name IS NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS inspection_continuation_current_fleet ON application_inspection_continuations (org_id, fleet_name) WHERE current AND fleet_name IS NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS inspection_continuation_device_event ON application_inspection_continuations (org_id, device_name, event_id) WHERE device_name IS NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS inspection_continuation_fleet_event ON application_inspection_continuations (org_id, fleet_name, event_id) WHERE fleet_name IS NOT NULL`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	for _, fk := range []struct {
		table, name, ddl string
	}{
		{"application_inspections", "application_inspection_org_fk", `ALTER TABLE application_inspections ADD CONSTRAINT application_inspection_org_fk
			FOREIGN KEY (org_id) REFERENCES organizations (id) ON DELETE CASCADE`},
		{"application_inspection_refs", "application_inspection_ref_result_fk", `ALTER TABLE application_inspection_refs ADD CONSTRAINT application_inspection_ref_result_fk
			FOREIGN KEY (org_id, inspection_id) REFERENCES application_inspections (org_id, id) ON DELETE CASCADE`},
		{"application_inspection_usages", "application_inspection_usage_result_fk", `ALTER TABLE application_inspection_usages ADD CONSTRAINT application_inspection_usage_result_fk
			FOREIGN KEY (org_id, inspection_id) REFERENCES application_inspections (org_id, id) ON DELETE NO ACTION`},
		{"application_inspection_usages", "application_inspection_usage_device_fk", `ALTER TABLE application_inspection_usages ADD CONSTRAINT application_inspection_usage_device_fk
			FOREIGN KEY (org_id, device_name) REFERENCES devices (org_id, name) ON DELETE CASCADE`},
		{"application_inspection_usages", "application_inspection_usage_fleet_fk", `ALTER TABLE application_inspection_usages ADD CONSTRAINT application_inspection_usage_fleet_fk
			FOREIGN KEY (org_id, fleet_name) REFERENCES fleets (org_id, name) ON DELETE CASCADE`},
		{"application_inspection_continuations", "inspection_continuation_device_fk", `ALTER TABLE application_inspection_continuations ADD CONSTRAINT inspection_continuation_device_fk
			FOREIGN KEY (org_id, device_name) REFERENCES devices (org_id, name) ON DELETE CASCADE`},
		{"application_inspection_continuations", "inspection_continuation_fleet_fk", `ALTER TABLE application_inspection_continuations ADD CONSTRAINT inspection_continuation_fleet_fk
			FOREIGN KEY (org_id, fleet_name) REFERENCES fleets (org_id, name) ON DELETE CASCADE`},
	} {
		if !db.Migrator().HasConstraint(fk.table, fk.name) {
			if err := db.Exec(fk.ddl).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

type Attempt struct {
	Input           model.ApplicationInspectionInput
	State           model.InspectionState
	Revision        model.InspectionFingerprint
	DiagnosticCodes []model.InspectionDiagnostic
	References      []ReferenceInput
}

type Result struct {
	Inspection model.ApplicationInspection
	References []model.ApplicationInspectionRef
}

type ReferenceScope struct {
	Registry string
	Port     *uint16
	Path     []string
}

const MaxPageSize = 1000

func validatePageSize(limit int) error {
	if limit < 1 || limit > MaxPageSize {
		return fmt.Errorf("inspection list page size must be between 1 and %d", MaxPageSize)
	}
	return nil
}

type ReferenceCursor struct {
	InspectionID uuid.UUID
	Reference    string
	Role         model.InspectionArtifactRole
	Mechanism    model.InspectionPullMechanism
}

type ReferencePage struct {
	Items []model.ApplicationInspectionRef
	Next  *ReferenceCursor
}

// RecordAttempt commits attempt and references together. Duplicate complete
// writes return the existing ID and preserve its references.
func (s *Store) RecordAttempt(ctx context.Context, orgID uuid.UUID, attempt Attempt) (uuid.UUID, error) {
	key, err := KeyForInput(orgID, attempt.Input)
	if err != nil {
		return uuid.Nil, err
	}
	if err := validateAttempt(attempt); err != nil {
		return uuid.Nil, err
	}
	if attempt.Input.Helm != nil && attempt.Input.Helm.FreshFor != 0 {
		return uuid.Nil, errors.New("Helm refresh results require queued work")
	}
	id := uuid.New()
	refs, err := normalizeReferences(orgID, id, attempt.References)
	if err != nil {
		return uuid.Nil, err
	}
	row := model.ApplicationInspection{
		OrgID: orgID, ID: id, InputKey: string(key), Input: attempt.Input, State: attempt.State,
		Revision: attempt.Revision, DiagnosticCodes: attempt.DiagnosticCodes,
	}
	err = store.RunInTransaction(ctx, s.db, func(tx *gorm.DB) error {
		if attempt.State == model.InspectionComplete {
			if err := insertCanonicalAttempt(tx, &row); err != nil {
				return err
			}
			if row.ID != id {
				if row.State == model.InspectionComplete {
					return nil
				}
				for i := range refs {
					refs[i].InspectionID = row.ID
				}
				return finishAttempt(tx, orgID, row.ID, attempt, refs)
			}
		} else if err := tx.Create(&row).Error; err != nil {
			return err
		}
		return insertReferences(tx, refs)
	})
	if err != nil {
		return uuid.Nil, storeutil.ErrorFromGormError(err)
	}
	return row.ID, nil
}

func insertCanonicalAttempt(tx *gorm.DB, row *model.ApplicationInspection) error {
	// Return and lock the canonical row without changing its current attempt state.
	return tx.Clauses(clause.OnConflict{
		Columns:     []clause.Column{{Name: "org_id"}, {Name: "input_key"}},
		TargetWhere: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: canonicalAttemptPredicate}}},
		DoUpdates:   clause.AssignmentColumns([]string{"input_key"}),
	}, clause.Returning{Columns: []clause.Column{{Name: "id"}, {Name: "state"}, {Name: "fresh_until"}}}).Create(row).Error
}

func normalizeReferences(orgID, id uuid.UUID, inputs []ReferenceInput) ([]model.ApplicationInspectionRef, error) {
	refs := make([]model.ApplicationInspectionRef, 0, len(inputs))
	for i, input := range inputs {
		ref, err := NormalizeReference(input)
		if err != nil {
			return nil, fmt.Errorf("inspection reference %d: %w", i+1, err)
		}
		ref.OrgID, ref.InspectionID = orgID, id
		refs = append(refs, ref)
	}
	return refs, nil
}

func insertReferences(tx *gorm.DB, refs []model.ApplicationInspectionRef) error {
	if len(refs) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&refs, 500).Error
}

func validateAttempt(attempt Attempt) error {
	switch attempt.State {
	case model.InspectionComplete:
		if len(attempt.DiagnosticCodes) != 0 {
			return errors.New("complete inspection cannot carry incomplete diagnostics")
		}
	case model.InspectionFinishedIncomplete:
		if len(attempt.DiagnosticCodes) == 0 {
			return errors.New("incomplete inspection requires a diagnostic code")
		}
		for _, code := range attempt.DiagnosticCodes {
			switch code {
			case model.InspectionContentUnavailable, model.InspectionParseFailed, model.InspectionBudgetExceeded:
			default:
				return errors.New("invalid inspection diagnostic code")
			}
		}
	default:
		return errors.New("invalid inspection attempt state")
	}
	return nil
}

func (s *Store) FindComplete(ctx context.Context, orgID uuid.UUID, key InputKey) (Result, error) {
	return s.getResult(ctx, orgID, "input_key <> '' AND input_key = ? AND (fresh_until IS NULL OR fresh_until > clock_timestamp()) AND "+completeAttemptPredicate, key)
}

func (s *Store) GetAttempt(ctx context.Context, orgID, resultID uuid.UUID) (Result, error) {
	return s.getResult(ctx, orgID, "id = ?", resultID)
}

func (s *Store) getResult(ctx context.Context, orgID uuid.UUID, predicate string, arg any) (Result, error) {
	var row struct {
		model.ApplicationInspection
		References []model.ApplicationInspectionRef `gorm:"column:reference_facts;serializer:json"`
	}
	// Read the attempt and its refs in the same snapshot while cleanup can proceed independently.
	err := store.DB(ctx, s.db).Model(&model.ApplicationInspection{}).
		Select(`application_inspections.*, (
			SELECT COALESCE(jsonb_agg(ref ORDER BY ref.reference, ref.role, ref.mechanism), '[]'::jsonb)
			FROM application_inspection_refs AS ref
			WHERE ref.org_id = application_inspections.org_id AND ref.inspection_id = application_inspections.id
		) AS reference_facts`).
		Where("org_id = ?", orgID).Where(predicate, arg).Take(&row).Error
	if err != nil {
		return Result{}, storeutil.ErrorFromGormError(err)
	}
	return Result{Inspection: row.ApplicationInspection, References: row.References}, nil
}

// ListReferences returns a page in primary-key order within the requested scope.
// Use Next as after with the same organization and scope; nil Next marks the last page.
func (s *Store) ListReferences(ctx context.Context, orgID uuid.UUID, scope ReferenceScope, limit int, after *ReferenceCursor) (ReferencePage, error) {
	if err := validatePageSize(limit); err != nil {
		return ReferencePage{}, err
	}
	if scope.Registry == "" {
		return ReferencePage{}, errors.New("inspection reference scope requires a registry")
	}
	query := store.DB(ctx, s.db).Where("org_id = ? AND registry = ?", orgID, scope.Registry)
	if scope.Port != nil {
		query = query.Where("port = ?", *scope.Port)
	}
	if len(scope.Path) > 0 {
		query = query.Where("path = ?", model.StringArray(scope.Path))
	}
	if after != nil {
		query = query.Where("(inspection_id, reference, role, mechanism) > (?, ?, ?, ?)",
			after.InspectionID, after.Reference, after.Role, after.Mechanism)
	}
	var page ReferencePage
	if err := query.Order("inspection_id, reference, role, mechanism").Limit(limit + 1).Find(&page.Items).Error; err != nil {
		return ReferencePage{}, storeutil.ErrorFromGormError(err)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.Next = &ReferenceCursor{InspectionID: last.InspectionID, Reference: last.Reference, Role: last.Role, Mechanism: last.Mechanism}
	}
	return page, nil
}
