package appinspection

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/storeutil"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type OwnerKind string

const (
	OwnerDevice OwnerKind = "device"
	OwnerFleet  OwnerKind = "fleet"
)

type Owner struct {
	Kind OwnerKind
	Name string
}

type OwnerVersion struct {
	Owner
	Generation       int64
	CreatedAt        time.Time
	TemplateIdentity string
}

type UsageBinding struct {
	ApplicationContext string
	InspectionID       uuid.UUID
	User               string
	Mechanism          model.InspectionPullMechanism
	Pending            bool
	ContinuationID     *uuid.UUID
}

func (owner Owner) tableAndColumn() (string, string, error) {
	if owner.Name == "" {
		return "", "", errors.New("inspection usage requires an owner name")
	}
	switch owner.Kind {
	case OwnerDevice:
		return "devices", "device_name", nil
	case OwnerFleet:
		return "fleets", "fleet_name", nil
	default:
		return "", "", errors.New("invalid inspection usage owner kind")
	}
}

// ReplaceUsagesIfCurrent replaces the full usage set for the observed owner
// version, including an empty set. Stale versions leave current usages intact.
func (s *Store) ReplaceUsagesIfCurrent(ctx context.Context, orgID uuid.UUID, owner OwnerVersion, bindings []UsageBinding) (bool, error) {
	table, column, err := owner.tableAndColumn()
	if err != nil {
		return false, err
	}
	rows, resultIDs, err := prepareUsages(orgID, owner, bindings)
	if err != nil {
		return false, err
	}

	applied := false
	err = store.RunInTransaction(ctx, s.db, func(tx *gorm.DB) error {
		var version struct{ Matches bool }
		if err := tx.Table(table).Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("COALESCE(generation, 0) = ? AND created_at = ? AS matches", owner.Generation, owner.CreatedAt).
			Where("org_id = ? AND name = ? AND deleted_at IS NULL", orgID, owner.Name).Take(&version).Error; err != nil {
			return err
		}
		if !version.Matches {
			return nil
		}
		if len(resultIDs) > 0 {
			var locked []uuid.UUID
			err := tx.Model(&model.ApplicationInspection{}).Clauses(clause.Locking{Strength: "KEY SHARE"}).
				Where("org_id = ? AND id IN ?", orgID, resultIDs).Pluck("id", &locked).Error
			if err != nil {
				return err
			}
			if len(locked) != len(resultIDs) {
				return flterrors.ErrResourceNotFound
			}
		}
		if err := tx.Where("org_id = ?", orgID).Where(clause.Eq{Column: clause.Column{Name: column}, Value: owner.Name}).
			Delete(&model.ApplicationInspectionUsage{}).Error; err != nil {
			return err
		}
		if len(rows) > 0 {
			if err := tx.CreateInBatches(&rows, 500).Error; err != nil {
				return err
			}
		}
		applied = true
		return nil
	})
	if err != nil {
		return false, storeutil.ErrorFromGormError(err)
	}
	return applied, nil
}

var usageIdentity = regexp.MustCompile(`^[a-zA-Z0-9_.:/@+-]{1,255}$`)

func prepareUsages(orgID uuid.UUID, owner OwnerVersion, bindings []UsageBinding) ([]model.ApplicationInspectionUsage, []uuid.UUID, error) {
	if owner.CreatedAt.IsZero() || owner.Generation < 0 || (owner.TemplateIdentity != "" && !usageIdentity.MatchString(owner.TemplateIdentity)) {
		return nil, nil, errors.New("invalid inspection usage owner version")
	}
	rows := make([]model.ApplicationInspectionUsage, 0, len(bindings))
	contexts := make(map[string]struct{}, len(bindings))
	seenIDs := make(map[uuid.UUID]struct{}, len(bindings))
	var ids []uuid.UUID
	for _, binding := range bindings {
		if !usageIdentity.MatchString(binding.ApplicationContext) || (binding.User != "" && !usageIdentity.MatchString(binding.User)) ||
			!validMechanism(binding.Mechanism) || binding.InspectionID == uuid.Nil {
			return nil, nil, errors.New("invalid inspection application binding")
		}
		if _, exists := contexts[binding.ApplicationContext]; exists {
			return nil, nil, errors.New("duplicate inspection application context")
		}
		contexts[binding.ApplicationContext] = struct{}{}
		if _, exists := seenIDs[binding.InspectionID]; !exists {
			ids = append(ids, binding.InspectionID)
			seenIDs[binding.InspectionID] = struct{}{}
		}
		row := model.ApplicationInspectionUsage{
			OrgID: orgID, ID: uuid.New(), InspectionID: binding.InspectionID, Generation: owner.Generation,
			OwnerCreatedAt: owner.CreatedAt, TemplateIdentity: owner.TemplateIdentity, ApplicationContext: binding.ApplicationContext,
			User: binding.User, Mechanism: binding.Mechanism, Pending: binding.Pending, ContinuationID: binding.ContinuationID,
		}
		if owner.Kind == OwnerDevice {
			row.DeviceName = &owner.Name
		} else {
			row.FleetName = &owner.Name
		}
		rows = append(rows, row)
	}
	return rows, ids, nil
}

func (s *Store) ListUsages(ctx context.Context, orgID uuid.UUID, owner Owner) ([]model.ApplicationInspectionUsage, error) {
	_, column, err := owner.tableAndColumn()
	if err != nil {
		return nil, err
	}
	var rows []model.ApplicationInspectionUsage
	err = store.DB(ctx, s.db).Where("org_id = ?", orgID).Where(clause.Eq{Column: clause.Column{Name: column}, Value: owner.Name}).
		Order("application_context").Find(&rows).Error
	if err != nil {
		return nil, storeutil.ErrorFromGormError(err)
	}
	return rows, nil
}
