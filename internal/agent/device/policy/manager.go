package policy

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/robfig/cron/v3"
	"github.com/samber/lo"
)

type manager struct {
	download *schedule
	update   *schedule

	log *log.PrefixLogger
}

// NewManager returns a new device policy manager.
// Note: This manager is designed for sequential operations only and is not
// thread-safe.
func NewManager(log *log.PrefixLogger) Manager {
	return &manager{
		log: log,
	}
}

func (m *manager) Sync(ctx context.Context, desired *v1beta1.DeviceSpec) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	m.log.Debug("Syncing policy")
	defer m.log.Debug("Finished syncing policy")

	if desired.UpdatePolicy == nil {
		m.log.Debugf("No update policy defined")
		m.update = nil
		m.download = nil
		return nil
	}
	if desired.UpdatePolicy.DownloadSchedule != nil {
		schedule := newSchedule(Download)
		if err := schedule.Parse(m.log, desired.UpdatePolicy.DownloadSchedule); err != nil {
			return fmt.Errorf("failed to parse download schedule: %w", err)
		}
		m.download = schedule
	} else {
		m.download = nil
	}

	if desired.UpdatePolicy.UpdateSchedule != nil {
		schedule := newSchedule(Update)
		if err := schedule.Parse(m.log, desired.UpdatePolicy.UpdateSchedule); err != nil {
			return fmt.Errorf("failed to parse update schedule: %w", err)
		}
		m.update = schedule
	} else {
		m.update = nil
	}

	return nil
}

func (m *manager) IsReady(ctx context.Context, policyType Type) bool {
	if ctx.Err() != nil {
		return false
	}

	if policyType == Download {
		if m.download == nil {
			return true
		}
		return m.download.IsReady(m.log)
	}

	if policyType == Update {
		if m.update == nil {
			return true
		}
		return m.update.IsReady(m.log)
	}

	return false
}

type schedule struct {
	policyType         Type
	location           *time.Location
	cron               cron.Schedule
	startGraceDuration time.Duration

	// this is used for testing to override time.Now
	nowFn func() time.Time
}

func newSchedule(policyType Type) *schedule {
	return &schedule{
		policyType: policyType,
		nowFn:      time.Now,
	}
}

func (s *schedule) Parse(log *log.PrefixLogger, updateSchedule *v1beta1.UpdateSchedule) error {
	// parse time zone
	if updateSchedule.TimeZone != nil {
		loc, err := time.LoadLocation(lo.FromPtr(updateSchedule.TimeZone))
		if err != nil {
			return fmt.Errorf("invalid time zone: %w", err)
		}
		s.location = loc
	} else {
		s.location = time.Local
	}

	// parse cron expression
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	cronExpr, err := parser.Parse(updateSchedule.At)
	if err != nil {
		return fmt.Errorf("invalid cron expression: %w", err)
	}
	s.cron = cronExpr
	log.Debugf("Parsed cron expression: %s", s.cron)

	// parse grace duration
	duration, err := time.ParseDuration(updateSchedule.StartGraceDuration)
	if err != nil {
		return fmt.Errorf("invalid start grace duration: %w", err)
	}
	s.startGraceDuration = duration

	return nil
}

// IsReady returns true if an activation falls within the current grace window.
func (s *schedule) IsReady(log *log.PrefixLogger) bool {
	now := s.nowFn().In(s.location)
	windowStart := now.Add(-s.startGraceDuration)
	// Next is strictly after its input; include an activation exactly at windowStart.
	activation := s.cron.Next(windowStart.Add(-time.Nanosecond))
	log.Debugf("Policy %s current time: %s, window start: %s, candidate activation: %s", s.policyType, now, windowStart, activation)

	if !activation.IsZero() && !activation.After(now) {
		log.Infof("Policy %s schedule is ready", s.policyType)
		return true
	}

	log.Debugf("Policy %s schedule is not ready", s.policyType)
	return false
}
