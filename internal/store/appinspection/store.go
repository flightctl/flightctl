package appinspection

import (
	"context"
	"errors"

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

const completeAttemptPredicate = "state = 'complete'"

func (s *Store) InitialMigration(ctx context.Context) error {
	db := store.DB(ctx, s.db)
	if err := db.AutoMigrate(&model.ApplicationInspection{}, &model.ApplicationInspectionRef{}, &model.ApplicationInspectionUsage{}); err != nil {
		return err
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS application_inspection_complete_key
		ON application_inspections (org_id, input_key) WHERE ` + completeAttemptPredicate).Error; err != nil {
		return err
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
	id := uuid.New()
	refs := make([]model.ApplicationInspectionRef, 0, len(attempt.References))
	for _, input := range attempt.References {
		ref, err := NormalizeReference(input)
		if err != nil {
			return uuid.Nil, err
		}
		ref.OrgID, ref.InspectionID = orgID, id
		refs = append(refs, ref)
	}
	row := model.ApplicationInspection{
		OrgID: orgID, ID: id, InputKey: string(key), Input: attempt.Input, State: attempt.State,
		Revision: attempt.Revision, DiagnosticCodes: attempt.DiagnosticCodes,
	}
	err = store.RunInTransaction(ctx, s.db, func(tx *gorm.DB) error {
		insert := tx
		if attempt.State == model.InspectionComplete {
			// A no-op update returns the winner while keeping it locked through the transaction.
			insert = tx.Clauses(clause.OnConflict{
				Columns:     []clause.Column{{Name: "org_id"}, {Name: "input_key"}},
				TargetWhere: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: completeAttemptPredicate}}},
				DoUpdates:   clause.AssignmentColumns([]string{"input_key"}),
			}, clause.Returning{Columns: []clause.Column{{Name: "id"}}})
		}
		if err := insert.Create(&row).Error; err != nil {
			return err
		}
		if row.ID != id || len(refs) == 0 {
			return nil
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&refs, 500).Error
	})
	if err != nil {
		return uuid.Nil, storeutil.ErrorFromGormError(err)
	}
	return row.ID, nil
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
	return s.getResult(ctx, orgID, "input_key = ? AND "+completeAttemptPredicate, key)
}

func (s *Store) GetAttempt(ctx context.Context, orgID, resultID uuid.UUID) (Result, error) {
	return s.getResult(ctx, orgID, "id = ?", resultID)
}

func (s *Store) getResult(ctx context.Context, orgID uuid.UUID, predicate string, arg any) (Result, error) {
	var result Result
	err := store.RunInTransaction(ctx, s.db, func(tx *gorm.DB) error {
		// Hold the parent through both reads so reclamation cannot erase its refs between them.
		if err := tx.Clauses(clause.Locking{Strength: "KEY SHARE"}).Where("org_id = ?", orgID).
			Where(predicate, arg).Take(&result.Inspection).Error; err != nil {
			return err
		}
		return tx.Where("org_id = ? AND inspection_id = ?", orgID, result.Inspection.ID).
			Order("reference, role, mechanism").Find(&result.References).Error
	})
	if err != nil {
		return Result{}, storeutil.ErrorFromGormError(err)
	}
	return result, nil
}

func (s *Store) ListReferences(ctx context.Context, orgID uuid.UUID, scope ReferenceScope) ([]model.ApplicationInspectionRef, error) {
	if scope.Registry == "" {
		return nil, errors.New("inspection reference scope requires a registry")
	}
	query := store.DB(ctx, s.db).Where("org_id = ? AND registry = ?", orgID, scope.Registry)
	if scope.Port != nil {
		query = query.Where("port = ?", *scope.Port)
	}
	if len(scope.Path) > 0 {
		query = query.Where("path = ?", model.StringArray(scope.Path))
	}
	var refs []model.ApplicationInspectionRef
	if err := query.Find(&refs).Error; err != nil {
		return nil, storeutil.ErrorFromGormError(err)
	}
	return refs, nil
}
