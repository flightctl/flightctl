package systemd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/pkg/log"
)

type Manager interface {
	// EnsurePatterns sets the match patterns for systemd units.
	EnsurePatterns([]string) error
	// Status returns the status of systemd units.
	Status(context.Context) ([]v1alpha1.DeviceApplicationStatus, error)
}

type SystemDUnitListEntry struct {
	Unit        string `json:"unit"`
	LoadState   string `json:"load"`
	ActiveState string `json:"active"`
	Sub         string `json:"sub"`
	Description string `json:"description"`
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

func (m *manager) Status(ctx context.Context) ([]v1alpha1.DeviceApplicationStatus, error) {
	if len(m.patterns) == 0 {
		return []v1alpha1.DeviceApplicationStatus{}, nil
	}

	status, err := m.client.ListUnitsByMatchPattern(ctx, m.patterns)
	if err != nil {
		return nil, err
	}

	m.log.Debugf("systemd list-units output: %s", status)

	var units []SystemDUnitListEntry
	if err := json.Unmarshal([]byte(status), &units); err != nil {
		return nil, fmt.Errorf("failed unmarshalling systemctl list-units output: %w", err)
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

	return appStatus, nil
}

func parseApplicationStatusType(unit SystemDUnitListEntry) (v1alpha1.ApplicationStatusType, string) {
	switch {
	case unit.ActiveState == "activating" && (unit.Sub == "start-pre" || unit.Sub == "start-post"):
		return v1alpha1.ApplicationStatusStarting, "0/1"
	case unit.ActiveState == "active" && unit.Sub == "running":
		return v1alpha1.ApplicationStatusRunning, "1/1"
	case unit.ActiveState == "active" && unit.Sub == "exited":
		return v1alpha1.ApplicationStatusCompleted, "0/1"
	case unit.ActiveState == "failed":
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
