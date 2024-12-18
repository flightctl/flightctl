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

type schedule struct {
	location           *time.Location
	cron               cron.Schedule
	startGraceDuration time.Duration
}

// Ready returns true if the schedule is ready to run
func (s *schedule) Ready() bool {
	now := time.Now().In(s.location)
	return s.cron.Next(now).Before(now.Add(s.startGraceDuration))
}

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
		schedule, err := parseSchedule(desired.UpdatePolicy.DownloadSchedule)
		if err != nil {
			return fmt.Errorf("failed to parse download schedule: %w", err)
		}
		m.download = schedule
	} else {
		m.download = nil
	}

	if desired.UpdatePolicy.UpdateSchedule != nil {
		schedule, err := parseSchedule(desired.UpdatePolicy.UpdateSchedule)
		if err != nil {
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
		return m.download.Ready()
	}

	if policyType == Update {
		if m.update == nil {
			return true
		}
		return m.update.Ready()
	}

	return false
}

func parseSchedule(updateSchedule *v1alpha1.UpdateSchedule) (*schedule, error) {
	s := &schedule{}
	// parse time zone
	if updateSchedule.TimeZone != nil {
		loc, err := time.LoadLocation(util.FromPtr(updateSchedule.TimeZone))
		if err != nil {
			return nil, fmt.Errorf("invalid time zone: %w", err)
		}
		s.location = loc
	} else {
		s.location = time.Local
	}

	// parse cron expression
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	cronExpr, err := parser.Parse(updateSchedule.At)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression: %w", err)
	}
	s.cron = cronExpr

	// parse grace duration
	if updateSchedule.StartGraceDuration != nil {
		duration, err := time.ParseDuration(*updateSchedule.StartGraceDuration)
		if err != nil {
			return nil, fmt.Errorf("invalid start grace duration: %w", err)
		}
		s.startGraceDuration = duration
	}

	return s, nil
}
