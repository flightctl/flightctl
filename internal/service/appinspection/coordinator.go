package appinspection

import (
	"context"
	"errors"
	"time"

	inspection "github.com/flightctl/flightctl/internal/appinspection"
	"github.com/flightctl/flightctl/internal/flterrors"
	inspectionstore "github.com/flightctl/flightctl/internal/store/appinspection"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
)

const claimGrace = 30 * time.Second

// ReloadApplication returns the current effective input for a work target.
// A false match sends the continuation back for reconciliation.
type ReloadApplication func(context.Context, uuid.UUID, inspectionstore.WorkTarget) (EffectiveApplication, bool, error)

type Coordinator struct {
	store    *inspectionstore.Store
	resolver *InputResolver
	timeout  time.Duration
}

func NewCoordinator(store *inspectionstore.Store, resolver *InputResolver, timeout time.Duration) *Coordinator {
	return &Coordinator{store: store, resolver: resolver, timeout: timeout}
}

type WorkProgress struct {
	InspectionID uuid.UUID
	StaleWaiter  uuid.UUID
	RetryWork    uuid.UUID
}

// Process does all acquisition under a deadline shorter than the durable
// claim. A shutdown leaves the lease recoverable, whereas an acquisition
// failure settles the attempt with the references known so far.
func (s *Coordinator) Process(ctx context.Context, orgID, id uuid.UUID, reload ReloadApplication) (WorkProgress, error) {
	lease := s.timeout + claimGrace
	ctx, cancelProcess := context.WithTimeout(ctx, lease)
	defer cancelProcess()
	claim, err := s.store.Claim(ctx, orgID, id, lease)
	if err != nil || claim.Token == uuid.Nil {
		return WorkProgress{}, err
	}
	workCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	target, err := s.store.WorkTarget(workCtx, orgID, id)
	if errors.Is(err, flterrors.ErrResourceNotFound) {
		return WorkProgress{}, nil
	}
	if err != nil {
		return WorkProgress{}, err
	}
	app, matches, err := reload(workCtx, orgID, target)
	if err != nil {
		return WorkProgress{}, err
	}
	if !matches {
		return s.invalidate(ctx, orgID, id, claim.Token, target.Continuation.ID)
	}
	prepared, code := s.resolver.prepare(workCtx, orgID, app, claim.Input.ParentDigest)
	var key inspectionstore.InputKey
	if code == "" {
		key, err = inspectionstore.KeyForInput(orgID, prepared.identity)
		if err != nil {
			code = model.InspectionParseFailed
		}
	}
	if code != "" {
		return s.finish(ctx, orgID, id, claim.Token, inspectionstore.Attempt{Input: claim.Input, State: model.InspectionFinishedIncomplete, DiagnosticCodes: []model.InspectionDiagnostic{code}})
	}
	if claim.State == model.InspectionResolving {
		id, err = s.store.PromoteOrAttach(ctx, orgID, id, claim.Token, prepared.identity)
		if err != nil || id == uuid.Nil {
			return WorkProgress{}, err
		}
		claim, err = s.store.Claim(ctx, orgID, id, lease)
		if err != nil || claim.Token == uuid.Nil {
			// A complete cache hit may have settled the last waiting application.
			return WorkProgress{InspectionID: id}, err
		}
		if claim.Input.ParentDigest != "" {
			// Resolve service access under the extraction claim, so a credential
			// update between promotion and extraction applies to the new claim.
			prepared, code = s.resolver.prepare(workCtx, orgID, app, claim.Input.ParentDigest)
			if code != "" {
				return s.finish(ctx, orgID, id, claim.Token, inspectionstore.Attempt{Input: claim.Input, State: model.InspectionFinishedIncomplete, DiagnosticCodes: []model.InspectionDiagnostic{code}})
			}
		}
	}
	if string(key) != claim.InputKey {
		return s.invalidate(ctx, orgID, id, claim.Token, target.Continuation.ID)
	}
	attempt := prepared.inspect(workCtx, &app.Spec)
	return s.finish(ctx, orgID, id, claim.Token, attempt)
}

func (s *Coordinator) finish(ctx context.Context, orgID, id, token uuid.UUID, attempt inspectionstore.Attempt) (WorkProgress, error) {
	finished, err := s.store.Finish(ctx, orgID, id, token, attempt)
	if err != nil || !finished {
		return WorkProgress{}, err
	}
	return WorkProgress{InspectionID: id}, nil
}

func (s *Coordinator) invalidate(ctx context.Context, orgID, id, token, waiter uuid.UUID) (WorkProgress, error) {
	invalidated, err := s.store.InvalidateWaiter(ctx, orgID, id, token, waiter)
	if err != nil || !invalidated {
		return WorkProgress{}, err
	}
	return WorkProgress{StaleWaiter: waiter, RetryWork: id}, nil
}

// MatchesRevision checks mutable OCI tags through the normal digest TTL path.
// The caller has already fenced application and platform changes by snapshot.
func (s *Coordinator) MatchesRevision(ctx context.Context, orgID uuid.UUID, input model.ApplicationInspectionInput, app EffectiveApplication) (bool, error) {
	if input.ParentDigest == "" {
		return true, nil
	}
	revision, _, err := s.resolver.packageDigest(ctx, orgID, inspection.DeclaredReferences(&app.Spec))
	return revision == input.ParentDigest, err
}
