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
	oneTimeWindows     []oneTimeWindow
	valid              bool

	// this is used for testing to override time.Now
	nowFn func() time.Time
}

type oneTimeWindow struct {
	start         time.Time
	graceDuration time.Duration
}

func newSchedule(policyType Type) *schedule {
	return &schedule{
		policyType: policyType,
		nowFn:      time.Now,
	}
}

func (s *schedule) Parse(log *log.PrefixLogger, updateSchedule *v1beta1.UpdateSchedule) error {
	if updateSchedule == nil {
		return fmt.Errorf("schedule is nil")
	}

	s.location = nil
	s.cron = nil
	s.startGraceDuration = 0
	s.oneTimeWindows = nil
	s.valid = false

	hasAt := updateSchedule.At != ""
	hasGraceDuration := updateSchedule.StartGraceDuration != ""
	if hasAt != hasGraceDuration {
		return fmt.Errorf("at and start grace duration must be provided together")
	}
	if !hasAt && updateSchedule.OneTimeWindows == nil {
		return fmt.Errorf("at and start grace duration or one-time windows are required")
	}

	// parse time zone
	if updateSchedule.TimeZone != nil {
		timeZone := lo.FromPtr(updateSchedule.TimeZone)
		if strings.EqualFold(timeZone, "local") {
			s.location = time.Local
		} else {
			loc, err := time.LoadLocation(timeZone)
			if err != nil {
				return fmt.Errorf("invalid time zone: %w", err)
			}
			s.location = loc
		}
	} else {
		s.location = time.Local
	}

	if hasAt {
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
		if duration <= 0 {
			return fmt.Errorf("invalid start grace duration: must be positive")
		}
		s.startGraceDuration = duration
	}

	if updateSchedule.OneTimeWindows != nil {
		if len(*updateSchedule.OneTimeWindows) == 0 {
			return fmt.Errorf("one-time windows must not be empty")
		}
		for i, window := range *updateSchedule.OneTimeWindows {
			parsedWindow, err := parseOneTimeWindow(window, s.location)
			if err != nil {
				return fmt.Errorf("invalid one-time window at index %d: %w", i, err)
			}
			s.oneTimeWindows = append(s.oneTimeWindows, parsedWindow)
		}
	}

	s.valid = true
	return nil
}

func parseOneTimeWindow(window v1beta1.OneTimeWindow, location *time.Location) (oneTimeWindow, error) {
	if window.StartTime != "" && window.At != "" {
		return oneTimeWindow{}, fmt.Errorf("startTime and at must not both be set")
	}
	startTime := window.StartTime
	if startTime == "" {
		startTime = window.At
	}

	start, err := time.ParseInLocation("2006-01-02 15:04", window.Date+" "+startTime, location)
	if err != nil {
		return oneTimeWindow{}, fmt.Errorf("invalid date/time: %w", err)
	}

	graceDuration, err := time.ParseDuration(window.StartGraceDuration)
	if err != nil {
		return oneTimeWindow{}, fmt.Errorf("invalid start grace duration: %w", err)
	}
	if graceDuration <= 0 {
		return oneTimeWindow{}, fmt.Errorf("start grace duration must be positive")
	}

	return oneTimeWindow{start: start, graceDuration: graceDuration}, nil
}

// IsReady returns true if an activation falls within a current grace window.
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
			log.Infof("Policy %s recurring schedule is ready", s.policyType)
			return true
		}
	}

	for _, window := range s.oneTimeWindows {
		graceEnd := window.start.Add(window.graceDuration)
		if (now.Equal(window.start) || now.After(window.start)) && !now.After(graceEnd) {
			log.Infof("Policy %s one-time schedule is ready", s.policyType)
			return true
		}
	}

	log.Debugf("Policy %s schedule is not ready", s.policyType)
	return false
}
