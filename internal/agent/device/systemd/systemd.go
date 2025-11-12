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

	units, err := m.client.ShowByMatchPattern(ctx, m.patterns)
	if err != nil {
		return err
	}

	systemdUnits := make([]v1alpha1.SystemdUnitStatus, len(units))
	for i, unit := range units {
		systemdUnits[i] = v1alpha1.SystemdUnitStatus{
			Unit:        unit["Id"],
			Description: unit["Description"],
			EnableState: v1alpha1.SystemdEnableStateType(unit["UnitFileState"]),
			LoadState:   v1alpha1.SystemdLoadStateType(unit["LoadState"]),
			ActiveState: v1alpha1.SystemdActiveStateType(unit["ActiveState"]),
			SubState:    unit["SubState"],
		}
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
