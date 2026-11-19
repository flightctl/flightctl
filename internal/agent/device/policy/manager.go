package policy

import (
	"context"
	"fmt"
	"strings"
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
	var syncErr error
	if desired.UpdatePolicy.DownloadSchedule != nil {
		schedule := newSchedule(Download)
		if err := schedule.Parse(m.log, desired.UpdatePolicy.DownloadSchedule); err != nil {
			syncErr = fmt.Errorf("failed to parse download schedule: %w", err)
		}
		m.download = schedule
	} else {
		m.download = nil
	}

	if desired.UpdatePolicy.UpdateSchedule != nil {
		schedule := newSchedule(Update)
		if err := schedule.Parse(m.log, desired.UpdatePolicy.UpdateSchedule); err != nil {
			if syncErr == nil {
				syncErr = fmt.Errorf("failed to parse update schedule: %w", err)
			}
		}
		m.update = schedule
	} else {
		m.update = nil
	}

	return syncErr
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
	oneTimeWindows     []parsedWindow
	valid              bool

	// this is used for testing to override time.Now
	nowFn func() time.Time
}

type parsedWindow struct {
	start time.Time
	end   time.Time
}

func (w parsedWindow) IsReady(now time.Time) bool {
	return !now.Before(w.start) && !now.After(w.end)
}

func newSchedule(policyType Type) *schedule {
	return &schedule{
		policyType: policyType,
		nowFn:      time.Now,
	}
}

func (s *schedule) Parse(log *log.PrefixLogger, updateSchedule *v1beta1.UpdateSchedule) error {
	s.location = nil
	s.cron = nil
	s.startGraceDuration = 0
	s.oneTimeWindows = nil
	s.valid = false

	if updateSchedule == nil {
		return fmt.Errorf("schedule is nil")
	}

	// Parse the time zone once and use it for both recurring and one-time windows.
	location := time.Local
	if updateSchedule.TimeZone != nil {
		timeZone := lo.FromPtr(updateSchedule.TimeZone)
		if strings.EqualFold(timeZone, "local") {
			location = time.Local
		} else {
			loc, err := time.LoadLocation(timeZone)
			if err != nil {
				return fmt.Errorf("invalid time zone: %w", err)
			}
			location = loc
		}
	}

	hasCron := updateSchedule.At != nil || updateSchedule.StartGraceDuration != nil
	if hasCron && (updateSchedule.At == nil || updateSchedule.StartGraceDuration == nil) {
		return fmt.Errorf("recurring schedule must define both at and start grace duration")
	}

	var cronExpr cron.Schedule
	var duration time.Duration
	if hasCron {
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		var err error
		cronExpr, err = parser.Parse(lo.FromPtr(updateSchedule.At))
		if err != nil {
			return fmt.Errorf("invalid cron expression: %w", err)
		}

		duration, err = time.ParseDuration(lo.FromPtr(updateSchedule.StartGraceDuration))
		if err != nil {
			return fmt.Errorf("invalid start grace duration: %w", err)
		}
		if duration <= 0 {
			return fmt.Errorf("invalid start grace duration: must be positive")
		}
		log.Debugf("Parsed cron expression: %s", cronExpr)
	}

	var oneTimeWindows []parsedWindow
	if updateSchedule.OneTimeWindows != nil {
		if len(*updateSchedule.OneTimeWindows) == 0 {
			return fmt.Errorf("one-time windows must contain at least one window")
		}
		oneTimeWindows = make([]parsedWindow, 0, len(*updateSchedule.OneTimeWindows))
		for i, window := range *updateSchedule.OneTimeWindows {
			parsed, err := parseOneTimeWindow(window, location)
			if err != nil {
				return fmt.Errorf("invalid one-time window %d: %w", i, err)
			}
			oneTimeWindows = append(oneTimeWindows, parsed)
		}
	}

	if !hasCron && len(oneTimeWindows) == 0 {
		return fmt.Errorf("schedule must contain a recurring definition or at least one one-time window")
	}

	s.location = location
	s.cron = cronExpr
	s.startGraceDuration = duration
	s.oneTimeWindows = oneTimeWindows
	s.valid = true

	return nil
}

func parseOneTimeWindow(window v1beta1.OneTimeWindow, location *time.Location) (parsedWindow, error) {
	start, err := time.ParseInLocation("2006-01-02 15:04", window.Date+" "+window.At, location)
	if err != nil {
		return parsedWindow{}, fmt.Errorf("invalid date or time: %w", err)
	}

	duration, err := time.ParseDuration(window.StartGraceDuration)
	if err != nil {
		return parsedWindow{}, fmt.Errorf("invalid start grace duration: %w", err)
	}
	if duration <= 0 {
		return parsedWindow{}, fmt.Errorf("start grace duration must be positive")
	}

	return parsedWindow{start: start, end: start.Add(duration)}, nil
}

// IsReady returns true if an activation falls within the current grace window.
func (s *schedule) IsReady(log *log.PrefixLogger) bool {
	if !s.valid {
		log.Debugf("Policy %s schedule is invalid", s.policyType)
		return false
	}

	now := s.nowFn().In(s.location)
	if s.cron != nil {
		windowStart := now.Add(-s.startGraceDuration)
		// Next is strictly after its input; include an activation exactly at windowStart.
		activation := s.cron.Next(windowStart.Add(-time.Nanosecond))
		log.Debugf("Policy %s current time: %s, window start: %s, candidate activation: %s", s.policyType, now, windowStart, activation)

		if !activation.IsZero() && !activation.After(now) {
			log.Infof("Policy %s schedule is ready", s.policyType)
			return true
		}
	}

	for _, window := range s.oneTimeWindows {
		if window.IsReady(now) {
			log.Infof("Policy %s one-time schedule is ready", s.policyType)
			return true
		}
	}

	log.Debugf("Policy %s schedule is not ready", s.policyType)
	return false
}
