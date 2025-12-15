package systemd

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"

	"github.com/flightctl/flightctl/api/v1beta1"
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
	// ListDependencies returns the list of units that the specified unit depends on.
	ListDependencies(ctx context.Context, unit string) ([]string, error)
	// Logs returns the logs based on the specified options
	Logs(ctx context.Context, options ...client.LogOptions) ([]string, error)
	// Show gets information about the specified unit
	Show(ctx context.Context, unit string, options ...client.SystemdShowOptions) ([]string, error)
	status.Exporter
}

type manager struct {
	patterns         []string
	client           *client.Systemd
	journalctl       *client.Journalctl
	log              *log.PrefixLogger
	excludedServices map[string]struct{}
}

func NewManager(log *log.PrefixLogger, client *client.Systemd, journalctl *client.Journalctl) Manager {
	return &manager{
		log:              log,
		client:           client,
		journalctl:       journalctl,
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

func (m *manager) ListDependencies(ctx context.Context, unit string) ([]string, error) {
	return m.client.ListDependencies(ctx, unit)
}

func (m *manager) Logs(ctx context.Context, options ...client.LogOptions) ([]string, error) {
	return m.journalctl.Logs(ctx, options...)
}

func (m *manager) Show(ctx context.Context, unit string, options ...client.SystemdShowOptions) ([]string, error) {
	return m.client.Show(ctx, unit, options...)
}

func (m *manager) normalizeEnabledStateValue(val v1beta1.SystemdEnableStateType) v1beta1.SystemdEnableStateType {
	if err := val.Validate(); err != nil {
		m.log.Warnf("invalid systemd enable state %s, replacing with %s : %v", val, v1beta1.SystemdEnableStateUnknown, err)
		return v1beta1.SystemdEnableStateUnknown
	}
	return val
}

func (m *manager) normalizeLoadStateValue(val v1beta1.SystemdLoadStateType) v1beta1.SystemdLoadStateType {
	if err := val.Validate(); err != nil {
		m.log.Warnf("invalid systemd load state %s, replacing with %s : %v", val, v1beta1.SystemdLoadStateUnknown, err)
		return v1beta1.SystemdLoadStateUnknown
	}
	return val
}

func (m *manager) normalizeActiveStateValue(val v1beta1.SystemdActiveStateType) v1beta1.SystemdActiveStateType {
	if err := val.Validate(); err != nil {
		m.log.Warnf("invalid systemd active state %s, replacing with %s : %v", val, v1beta1.SystemdActiveStateUnknown, err)
		return v1beta1.SystemdActiveStateUnknown
	}
	return val
}

func (m *manager) Status(ctx context.Context, device *v1beta1.DeviceStatus, _ ...status.CollectorOpt) error {
	if len(m.patterns) == 0 {
		device.Systemd = nil
		return nil
	}

	units, err := m.client.ShowByMatchPattern(ctx, m.patterns)
	if err != nil {
		return err
	}

	systemdUnits := make([]v1beta1.SystemdUnitStatus, 0, len(units))
	for _, unit := range units {
		unitName := unit["Id"]
		if _, excluded := m.excludedServices[unitName]; excluded {
			m.log.Debugf("Excluding systemd unit from status report: %s", unitName)
			continue
		}
		systemdUnits = append(systemdUnits, v1beta1.SystemdUnitStatus{
			Unit:        unitName,
			Description: unit["Description"],
			EnableState: m.normalizeEnabledStateValue(v1beta1.SystemdEnableStateType(unit["UnitFileState"])),
			LoadState:   m.normalizeLoadStateValue(v1beta1.SystemdLoadStateType(unit["LoadState"])),
			ActiveState: m.normalizeActiveStateValue(v1beta1.SystemdActiveStateType(unit["ActiveState"])),
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
