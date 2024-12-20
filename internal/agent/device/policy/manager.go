package policy

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/robfig/cron/v3"
)

type manager struct {
	download *schedule
	update   *schedule

	log *log.PrefixLogger
}

func NewManager(log *log.PrefixLogger) Manager {
	return &manager{
		log: log,
	}
}

func (m *manager) Sync(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	m.log.Debug("Starting Policy Sync")
	defer m.log.Debug("Policy Sync Complete")

	if desired.UpdatePolicy == nil {
		m.log.Debugf("no update policy defined")
		m.update = nil
		m.download = nil
		return nil
	}
	if desired.UpdatePolicy.DownloadSchedule != nil {
		schedule := newSchedule(Download)
		if err := schedule.Parse(desired.UpdatePolicy.DownloadSchedule); err != nil {
			return fmt.Errorf("failed to parse download schedule: %w", err)
		}
		m.download = schedule
	} else {
		m.download = nil
	}

	if desired.UpdatePolicy.UpdateSchedule != nil {
		schedule := newSchedule(Update)
		if err := schedule.Parse(desired.UpdatePolicy.UpdateSchedule); err != nil {
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
	interval           time.Duration

	// this is used for testing to override time.Now
	nowFn func() time.Time
}

func newSchedule(policyType Type) *schedule {
	return &schedule{
		policyType: policyType,
		nowFn:      time.Now,
	}
}

func (s *schedule) Parse(updateSchedule *v1alpha1.UpdateSchedule) error {
	// parse time zone
	if updateSchedule.TimeZone != nil {
		loc, err := time.LoadLocation(util.FromPtr(updateSchedule.TimeZone))
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

	// calculate cron interval
	nextRun := s.cron.Next(s.nowFn().In(s.location))
	secondRun := s.cron.Next(nextRun)
	s.interval = secondRun.Sub(nextRun)

	// parse grace duration
	if updateSchedule.StartGraceDuration != nil {
		duration, err := time.ParseDuration(*updateSchedule.StartGraceDuration)
		if err != nil {
			return fmt.Errorf("invalid start grace duration: %w", err)
		}
		s.startGraceDuration = duration
	}

	return nil
}

// Ready returns true if the schedule is ready to run
func (s *schedule) IsReady(log *log.PrefixLogger) bool {
	now := s.nowFn().In(s.location)
	lastRun := s.cron.Next(now.Add(-s.interval))
	graceEnd := lastRun.Add(s.startGraceDuration)

	if now.Equal(lastRun) || (now.After(lastRun) && !now.After(graceEnd)) {
		log.Infof("Policy %s schedule is ready to run. Current time: %s, last run: %s, grace ends: %s", s.policyType, now, lastRun, graceEnd)
		return true
	}

	log.Debugf("Policy %s schedule is not ready. Current time: %s, last run: %s, grace ends: %s", s.policyType, now, lastRun, graceEnd)
	return false
}
