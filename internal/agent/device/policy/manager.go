package policy

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/robfig/cron/v3"
	"github.com/samber/lo"
)

// policySchedule combines a schedule with its ready state
type policySchedule struct {
	schedule *schedule
	isReady  bool // defaults to false
}

// versionSchedules holds the schedules and cached results for a specific version
type versionSchedules struct {
	version  int64 // Store version as int64 for extractor
	download *policySchedule
	update   *policySchedule
}

type manager struct {
	// versions holds per-version schedules with automatic compaction
	versions *queues.IndexedPriorityQueue[*versionSchedules, int64]

	log *log.PrefixLogger
}

// NewManager returns a new device policy manager.
// Note: This manager is designed for sequential operations only and is not
// thread-safe.
func NewManager(log *log.PrefixLogger) Manager {
	extractor := func(vs *versionSchedules) int64 {
		return vs.version
	}

	comparator := queues.Min[int64] // Min-heap for version numbers

	pq := queues.NewIndexedPriorityQueue[*versionSchedules, int64](
		comparator,
		extractor,
	)

	return &manager{
		versions: pq,
		log:      log,
	}
}

// validateDevice performs common device validation checks and returns the parsed version
func (m *manager) validateDevice(device *v1alpha1.Device, operation string) (int64, error) {
	if device == nil {
		return 0, fmt.Errorf("device is required for %s", operation)
	}

	versionStr := device.Version()
	if versionStr == "" {
		return 0, fmt.Errorf("device version is required for %s", operation)
	}

	// Validate that version can be parsed as int64
	version, err := strconv.ParseInt(versionStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("device version must be a valid integer for %s: %w", operation, err)
	}

	return version, nil
}

// validateDeviceWithSpec performs device validation including spec check and returns the parsed version
func (m *manager) validateDeviceWithSpec(device *v1alpha1.Device, operation string) (int64, error) {
	version, err := m.validateDevice(device, operation)
	if err != nil {
		return 0, err
	}

	if device.Spec == nil {
		return 0, fmt.Errorf("device spec is required for %s", operation)
	}

	return version, nil
}

func (m *manager) Sync(ctx context.Context, device *v1alpha1.Device) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	m.log.Debug("Syncing policy")
	defer m.log.Debug("Finished syncing policy")

	version, err := m.validateDeviceWithSpec(device, "policy sync")
	if err != nil {
		return err
	}

	_, exists := m.versions.PeekAt(version)
	if exists {
		return nil
	}

	vs := &versionSchedules{
		version: version,
	}

	if device.Spec.UpdatePolicy == nil {
		m.log.Debugf("No update policy defined for version %s", device.Version())
		vs.download = nil
		vs.update = nil
		m.versions.Add(vs)
		return nil
	}

	if device.Spec.UpdatePolicy.DownloadSchedule != nil {
		schedule := newSchedule(Download)
		if err := schedule.Parse(m.log, device.Spec.UpdatePolicy.DownloadSchedule); err != nil {
			return fmt.Errorf("failed to parse download schedule: %w", err)
		}
		vs.download = &policySchedule{
			schedule: schedule,
			isReady:  false,
		}
	}

	if device.Spec.UpdatePolicy.UpdateSchedule != nil {
		schedule := newSchedule(Update)
		if err := schedule.Parse(m.log, device.Spec.UpdatePolicy.UpdateSchedule); err != nil {
			return fmt.Errorf("failed to parse update schedule: %w", err)
		}
		vs.update = &policySchedule{
			schedule: schedule,
			isReady:  false,
		}
	}

	m.versions.Add(vs)

	return nil
}

func (m *manager) IsReady(ctx context.Context, policyType Type, device *v1alpha1.Device) bool {
	if ctx.Err() != nil {
		return false
	}

	version, err := m.validateDevice(device, "policy check")
	if err != nil {
		m.log.Errorf("Device validation failed: %v", err)
		return false
	}

	vs, exists := m.versions.PeekAt(version)
	if !exists {
		return false
	}

	var policySchedule *policySchedule
	switch policyType {
	case Download:
		policySchedule = vs.download
	case Update:
		policySchedule = vs.update
	default:
		return false
	}

	return m.checkPolicyReady(policySchedule)
}

// IsVersionReady checks if all policies for a version are ready.
// Returns true if all policies are ready, false otherwise.
// When not ready, returns the minimum next trigger time among all not-ready policies,
// allowing the caller to retry as soon as the first policy becomes ready.
func (m *manager) IsVersionReady(ctx context.Context, device *v1alpha1.Device) (bool, *time.Time, error) {
	if ctx.Err() != nil {
		return false, nil, ctx.Err()
	}

	version, err := m.validateDevice(device, "version ready")
	if err != nil {
		return false, nil, err
	}

	vs, exists := m.versions.PeekAt(version)
	if !exists {
		return false, nil, fmt.Errorf("version ready: version %s not found", device.Version())
	}

	allReady := true
	var minNextTime *time.Time

	if vs.download != nil && !m.checkPolicyReady(vs.download) {
		allReady = false
		minNextTime = lo.ToPtr(vs.download.schedule.NextTriggerTime(m.log))
	}

	if vs.update != nil && !m.checkPolicyReady(vs.update) {
		allReady = false
		nextTime := vs.update.schedule.NextTriggerTime(m.log)
		if minNextTime == nil || nextTime.Before(*minNextTime) {
			minNextTime = &nextTime
		}
	}

	if allReady {
		return true, nil, nil
	}
	return false, minNextTime, nil
}

// SetCurrentDevice compacts the cache up to the current device's version
func (m *manager) SetCurrentDevice(ctx context.Context, device *v1alpha1.Device) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	version, err := m.validateDevice(device, "setting current device")
	if err != nil {
		return err
	}

	// Compact up to the current version (removes all versions before it)
	m.versions.RemoveUpTo(version)
	m.log.Debugf("Compacted policy cache up to version %s", device.Version())

	return nil
}

// checkPolicyReady evaluates if a policy schedule is ready and caches the result
func (m *manager) checkPolicyReady(policySchedule *policySchedule) bool {
	if policySchedule == nil {
		return true
	}

	if policySchedule.isReady {
		return true
	}

	ready := policySchedule.schedule.IsReady(m.log)

	if ready {
		policySchedule.isReady = true
	}

	return ready
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

func (s *schedule) Parse(log *log.PrefixLogger, updateSchedule *v1alpha1.UpdateSchedule) error {
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

	now := s.normalizeTime(log, s.nowFn().In(s.location))

	// parse cron expression
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	cronExpr, err := parser.Parse(updateSchedule.At)
	if err != nil {
		return fmt.Errorf("invalid cron expression: %w", err)
	}
	s.cron = cronExpr
	log.Debugf("Parsed cron expression: %s", updateSchedule.At)

	// calculate cron interval
	nextRun := s.cron.Next(now)
	secondRun := s.cron.Next(nextRun)
	s.interval = secondRun.Sub(nextRun)

	// parse grace duration
	if updateSchedule.StartGraceDuration != nil {
		duration, err := time.ParseDuration(*updateSchedule.StartGraceDuration)
		if err != nil {
			return fmt.Errorf("invalid start grace duration: %w", err)
		}
		s.startGraceDuration = duration
	} else {
		s.startGraceDuration = 0
	}

	return nil
}

func (s *schedule) isReady(now, lastRun, graceEnd time.Time) bool {
	return now.Equal(lastRun) || (now.After(lastRun) && !now.After(graceEnd))
}

func (s *schedule) getInfo() (time.Time, time.Time, time.Time) {
	now := s.nowFn().In(s.location)
	lastRun := s.cron.Next(now.Add(-s.interval))
	graceEnd := lastRun.Add(s.startGraceDuration)
	return now, lastRun, graceEnd
}

func (s *schedule) IsReady(log *log.PrefixLogger) bool {
	now, lastRun, graceEnd := s.getInfo()
	nextRun := s.cron.Next(now)
	log.Debugf("Policy %s current time: %s, last run: %s, next run: %s, grace ends: %s", s.policyType, now, lastRun, nextRun, graceEnd)
	if s.isReady(now, lastRun, graceEnd) {
		log.Infof("Policy %s schedule is ready", s.policyType)
		return true
	}
	log.Debugf("Policy %s schedule is not ready", s.policyType)
	return false
}

func (s *schedule) NextTriggerTime(log *log.PrefixLogger) time.Time {
	now, lastRun, graceEnd := s.getInfo()
	if s.isReady(now, lastRun, graceEnd) {
		return now
	}
	return s.cron.Next(now)
}

// normalizeTime ensures that DST is properly handled.
func (s *schedule) normalizeTime(log *log.PrefixLogger, t time.Time) time.Time {
	if s.location == nil {
		return t
	}

	localTime := t.In(s.location)
	if localTime.IsDST() {
		// dst adjusted ok to return
		return localTime
	}

	// ensure fall DST is handled
	previousHour := localTime.Add(-time.Hour)
	if previousHour.IsDST() {
		log.Warnf("Time %s falls within a DST transition, adjusting to %s", t, localTime)
		return localTime.Add(time.Hour)
	}

	return localTime
}
