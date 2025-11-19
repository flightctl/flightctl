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
	"github.com/samber/lo"
)

type Manager interface {
	// EnsurePatterns sets the match patterns for systemd units.
	EnsurePatterns([]string) error
	// AddExclusions adds service names to the exclusion list.
	// Excluded services will not be reported in status even if they match patterns.
	AddExclusions(serviceNames ...string)
	// RemoveExclusions removes service names from the exclusion list.
	// Services will be reported again if they match patterns.
	RemoveExclusions(serviceNames ...string)
	// DaemonReload reloads systemd daemon configuration.
	DaemonReload(ctx context.Context) error
	// Start starts one or more systemd units.
	Start(ctx context.Context, units ...string) error
	// Stop stops one or more systemd units.
	Stop(ctx context.Context, units ...string) error
	// ResetFailed resets failed state for one or more systemd units.
	ResetFailed(ctx context.Context, units ...string) error
	// ListUnitsByMatchPattern lists systemd units matching the provided patterns.
	ListUnitsByMatchPattern(ctx context.Context, matchPatterns []string) ([]client.SystemDUnitListEntry, error)
	status.Exporter
}

type manager struct {
	patterns         []string
	client           *client.Systemd
	log              *log.PrefixLogger
	excludedServices map[string]struct{}
}

func NewManager(log *log.PrefixLogger, client *client.Systemd) Manager {
	return &manager{
		log:              log,
		client:           client,
		excludedServices: make(map[string]struct{}),
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

func (m *manager) AddExclusions(serviceNames ...string) {
	for _, name := range serviceNames {
		m.excludedServices[name] = struct{}{}
	}
}

func (m *manager) RemoveExclusions(serviceNames ...string) {
	for _, name := range serviceNames {
		delete(m.excludedServices, name)
	}
}

func (m *manager) DaemonReload(ctx context.Context) error {
	return m.client.DaemonReload(ctx)
}

func (m *manager) Start(ctx context.Context, units ...string) error {
	return m.client.Start(ctx, units...)
}

func (m *manager) Stop(ctx context.Context, units ...string) error {
	return m.client.Stop(ctx, units...)
}

func (m *manager) ResetFailed(ctx context.Context, units ...string) error {
	return m.client.ResetFailed(ctx, units...)
}

func (m *manager) ListUnitsByMatchPattern(ctx context.Context, matchPatterns []string) ([]client.SystemDUnitListEntry, error) {
	return m.client.ListUnitsByMatchPattern(ctx, matchPatterns)
}

func (m *manager) Status(ctx context.Context, device *v1alpha1.DeviceStatus, _ ...status.CollectorOpt) error {
	if len(m.patterns) == 0 {
		return nil
	}

	units, err := m.client.ShowByMatchPattern(ctx, m.patterns)
	if err != nil {
		return err
	}

	systemdUnits := make([]v1alpha1.SystemdUnitStatus, 0, len(units))
	for _, unit := range units {
		unitName := unit["Id"]
		if _, excluded := m.excludedServices[unitName]; excluded {
			m.log.Debugf("Excluding systemd unit from status report: %s", unitName)
			continue
		}
		systemdUnits = append(systemdUnits, v1alpha1.SystemdUnitStatus{
			Unit:        unitName,
			Description: unit["Description"],
			EnableState: v1alpha1.SystemdEnableStateType(unit["UnitFileState"]),
			LoadState:   v1alpha1.SystemdLoadStateType(unit["LoadState"]),
			ActiveState: v1alpha1.SystemdActiveStateType(unit["ActiveState"]),
			SubState:    unit["SubState"],
		})
	}
	device.Systemd = lo.ToPtr(systemdUnits)
	return nil
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
