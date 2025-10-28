package systemd

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/pkg/log"
)

type Manager interface {
	// EnsurePatterns sets the match patterns for systemd units.
	EnsurePatterns([]string) error
	status.Exporter
}

type manager struct {
	patterns []string
	client   *client.Systemd
	log      *log.PrefixLogger
}

func NewManager(log *log.PrefixLogger, client *client.Systemd) Manager {
	return &manager{
		log:    log,
		client: client,
	}
}

func (m *manager) EnsurePatterns(patterns []string) error {
	if !reflect.DeepEqual(m.patterns, patterns) {
		if err := validatePatterns(patterns); err != nil {
			return fmt.Errorf("invalid patterns: %w", err)
		}
		m.patterns = patterns
	}
	return nil
}

func (m *manager) Status(ctx context.Context, device *v1alpha1.DeviceStatus, _ ...status.CollectorOpt) error {
	if len(m.patterns) == 0 {
		return nil
	}

	units, err := m.client.ListUnitsByMatchPattern(ctx, m.patterns)
	if err != nil {
		return err
	}

	appStatus := make([]v1alpha1.DeviceApplicationStatus, 0, len(units))
	for _, u := range units {
		status, ready := parseApplicationStatusType(u)
		appStatus = append(appStatus, v1alpha1.DeviceApplicationStatus{
			Name:   u.Unit,
			Status: status,
			Ready:  ready,
		})
	}

	device.Applications = append(device.Applications, appStatus...)

	return nil
}

func parseApplicationStatusType(unit client.SystemDUnitListEntry) (v1alpha1.ApplicationStatusType, string) {
	switch {
	case unit.ActiveState == client.SystemDActiveStateActivating &&
		(unit.Sub == client.SystemDSubStateStartPre || unit.Sub == client.SystemDSubStateStartPost):
		return v1alpha1.ApplicationStatusStarting, "0/1"
	case unit.ActiveState == client.SystemDActiveStateActive && unit.Sub == client.SystemDSubStateRunning:
		return v1alpha1.ApplicationStatusRunning, "1/1"
	case unit.ActiveState == client.SystemDActiveStateActive && unit.Sub == client.SystemDSubStateExited,
		unit.ActiveState == client.SystemDActiveStateInactive && unit.Sub == client.SystemDSubStateDead:
		return v1alpha1.ApplicationStatusCompleted, "0/1"
	case unit.ActiveState == client.SystemDActiveStateFailed:
		return v1alpha1.ApplicationStatusError, "0/1"
	default:
		return v1alpha1.ApplicationStatusUnknown, "0/1"
	}
}

func validatePatterns(patterns []string) error {
	var errs []error
	for _, pattern := range patterns {
		if _, err := regexp.Compile(pattern); err != nil {
			errs = append(errs, fmt.Errorf("invalid regex: %s, error: %w", pattern, err))
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}
