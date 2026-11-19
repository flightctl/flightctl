// Package packagemodemigration holds the exclusivity precondition for the
// mixed OS-mode fleet template migration suite.
//
// The suite has to discard the fleet template migration's checkpoint and stop
// the periodic worker. Neither is scoped to one test or to one tenant: the
// migration rewrites every eligible fleet in an organization, the periodic
// worker is a single deployment-wide service, and its publisher schedules work
// for every organization it finds. The suite may therefore only run on a
// deployment it owns outright.
//
// Ownership is not taken from a flag and not inferred from what the current
// user can see through the API, which is filtered to that user's organization.
// It is decided here, from a read-only inventory of the whole database, before
// anything is mutated.
package packagemodemigration

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// ExclusivityEnvVar is the opt-in that marks a deployment as disposable and
// exclusively available to this suite. It is necessary but not sufficient:
// the inventory below still has to agree.
const ExclusivityEnvVar = "E2E_PACKAGE_MODE_MIGRATION_EXCLUSIVE"

// inventoryFields is how many columns InventoryQuery selects.
const inventoryFields = 4

// OptedIn reports whether the deployment was declared disposable.
//
// Anything other than an explicit affirmative is a no, so an unset, empty or
// misspelled value leaves the suite inert rather than letting it reach a
// deployment nobody offered it. Being opted in is not permission to mutate:
// the inventory decides that.
func OptedIn(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// GlobalInventory is a point-in-time census of the whole deployment, taken
// across every organization rather than through the caller's own.
type GlobalInventory struct {
	// Fleets is every live fleet row in the database. The migration rewrites
	// eligible fleets, so this is the population it could reach.
	Fleets int
	// Devices is every live device row. The periodic worker serves all of
	// them, so this is the population stopping it could disturb.
	Devices int
	// Organizations is every organization row.
	Organizations int
	// TargetOrgRows is how many organization rows carry the id this suite
	// authenticated as, so the organization it will act on is confirmed
	// against the same global source rather than taken on trust.
	TargetOrgRows int
}

// InventoryQuery returns the read-only census statement for one organization
// id, or an error if that id is not a UUID.
//
// Physical names come from the running schema, not from guesses: "fleets" is
// used directly by the fleet store (internal/store/fleet/store.go), "devices"
// and its org_id and deleted_at columns are used directly by the vulnerability
// finding store (internal/store/vulnerabilityfinding/cve_lifecycle.go), and
// "organizations" with its id column is the organization model
// (internal/org/model/organization.go).
//
// Live rows are the ones that matter and the ones counted: both fleets and
// devices carry GORM's soft delete (model.Resource.DeletedAt), the stores read
// them with deleted_at IS NULL, and a soft-deleted row is invisible to the
// migration and to the worker. Counting deleted rows as well would also make a
// disposable deployment unusable for a second run, because this suite's own
// cleanup leaves them behind. Organizations are not soft-deleted and are
// counted whole.
func InventoryQuery(orgID string) (string, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(orgID))
	if err != nil {
		return "", fmt.Errorf("organization %q is not a UUID: %w", orgID, err)
	}
	return fmt.Sprintf(
		"SELECT (SELECT COUNT(*) FROM fleets WHERE deleted_at IS NULL), "+
			"(SELECT COUNT(*) FROM devices WHERE deleted_at IS NULL), "+
			"(SELECT COUNT(*) FROM organizations), "+
			"(SELECT COUNT(*) FROM organizations WHERE id = '%s')", parsed.String()), nil
}

// ParseInventory reads the census back.
//
// psql is run with -t -A, so a well-formed answer is exactly one line of
// pipe-separated integers. Anything else — no rows, several rows, a missing
// column, a non-numeric or negative value — is reported as an error rather
// than guessed at, because an inventory that cannot be read is not evidence
// that a deployment is empty.
func ParseInventory(raw string) (GlobalInventory, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return GlobalInventory{}, fmt.Errorf("inventory query returned no output")
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) != 1 {
		return GlobalInventory{}, fmt.Errorf("inventory query returned %d rows, want 1: %q", len(lines), trimmed)
	}

	fields := strings.Split(lines[0], "|")
	if len(fields) != inventoryFields {
		return GlobalInventory{}, fmt.Errorf(
			"inventory query returned %d columns, want %d: %q", len(fields), inventoryFields, trimmed)
	}

	counts := make([]int, inventoryFields)
	for i, field := range fields {
		value, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil {
			return GlobalInventory{}, fmt.Errorf("inventory column %d is not a number: %q", i+1, field)
		}
		if value < 0 {
			return GlobalInventory{}, fmt.Errorf("inventory column %d is negative: %d", i+1, value)
		}
		counts[i] = value
	}

	return GlobalInventory{
		Fleets:        counts[0],
		Devices:       counts[1],
		Organizations: counts[2],
		TargetOrgRows: counts[3],
	}, nil
}

// EvaluateInventory reports whether the deployment may be mutated.
//
// Every condition is a refusal, never a warning. A deployment holding a single
// fleet or device belongs to somebody else at least in part, and a deployment
// holding more than one organization is not the single disposable tenant this
// suite is allowed to take over.
func EvaluateInventory(inv GlobalInventory, orgID string) error {
	var refusals []string
	if inv.Fleets != 0 {
		refusals = append(refusals, fmt.Sprintf("%d fleet(s) exist across all organizations", inv.Fleets))
	}
	if inv.Devices != 0 {
		refusals = append(refusals, fmt.Sprintf("%d device(s) exist across all organizations", inv.Devices))
	}
	if inv.Organizations != 1 {
		refusals = append(refusals, fmt.Sprintf(
			"the deployment holds %d organizations, want exactly 1", inv.Organizations))
	}
	if inv.TargetOrgRows != 1 {
		refusals = append(refusals, fmt.Sprintf(
			"organization %s matched %d rows in the database, want exactly 1", orgID, inv.TargetOrgRows))
	}
	if len(refusals) == 0 {
		return nil
	}
	return fmt.Errorf(
		"deployment is not exclusively available to this suite: %s. "+
			"This suite discards the fleet template migration checkpoint and stops the periodic worker, "+
			"which reaches every organization in the deployment. "+
			"Run it against a disposable deployment with no fleets and no devices",
		strings.Join(refusals, "; "))
}
