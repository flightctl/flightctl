package infra

import (
	"context"
	"errors"
	"fmt"

	"github.com/sirupsen/logrus"
)

// CleanupStep is one teardown action a suite owes its environment.
//
// Steps are ordered but independent: each one removes a different piece of
// suite-owned state, so a step that fails must not prevent the ones after it
// from running. Anything that genuinely depends on an earlier step belongs in
// that step's Run, not in a later entry.
type CleanupStep struct {
	// Name identifies the step in progress output and in the error returned
	// by RunCleanup. It reads as the action, e.g. "uninstall the chart".
	Name string
	// Skip reports that this step has nothing to do, typically because the
	// resource it removes was never created. A nil Skip means "always run".
	Skip func() bool
	// Run performs the cleanup. A non-nil error means suite-owned state may
	// have been left behind and must surface as a suite failure.
	Run func(ctx context.Context) error
}

// RunCleanup runs every step in order and returns the accumulated failures.
//
// Unlike asserting after each action, a failing step does not abort the
// sequence: every remaining step still runs, so one broken teardown action
// cannot strand the rest of the suite's resources. All failures are joined and
// returned together, so the caller can fail the suite once with the full list
// rather than only the first problem.
//
// ctx is passed to every step unchanged. An expired context does not stop the
// loop; each step decides how to handle it and reports its own error, which
// keeps the accumulated report complete instead of truncating at the first
// timeout.
func RunCleanup(ctx context.Context, steps []CleanupStep) error {
	var errs []error
	for _, step := range steps {
		if step.Skip != nil && step.Skip() {
			continue
		}
		if step.Run == nil {
			errs = append(errs, fmt.Errorf("cleanup step %q has no action", step.Name))
			continue
		}
		logrus.Infof("Cleanup: %s", step.Name)
		if err := step.Run(ctx); err != nil {
			// Logged as well as collected: the log line marks the point in the
			// run where the failure happened, which the joined error at the
			// end no longer shows.
			logrus.Errorf("Cleanup failed: %s: %v", step.Name, err)
			errs = append(errs, fmt.Errorf("%s: %w", step.Name, err))
		}
	}
	return errors.Join(errs...)
}
