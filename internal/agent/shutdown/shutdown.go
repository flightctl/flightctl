package shutdown

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	shutdownScheduledPath = "/run/systemd/shutdown/scheduled"
	runlevelPath          = "/run/utmp"

	// utmp record values
	runlevelRecordType = 1 // utmp record type for runlevel changes
	runLevelHalt       = '0'
	runlevelReboot     = '6'
)

// UtmpRecord represents the structure of a utmp record
type utmpRecord struct {
	Type int32     // ut_type
	Pid  int32     // ut_pid - for runlevel: encoded runlevel info
	Line [32]byte  // ut_line
	Id   [4]byte   // ut_id
	User [32]byte  // ut_user
	Host [256]byte // ut_host
	Exit struct {  // ut_exit
		Termination int16
		Exit        int16
	}
	Session int32    // ut_session
	Time    struct { // ut_tv
		Sec  int32
		Usec int32
	}
	AddrV6 [4]int32 // ut_addr_v6
	Unused [20]byte // padding
}

// State indicates the context behind a shutdown
type State struct {
	// SystemShutdown indicates whether the shutdown was triggered by a system shutdown or some other mechanism.
	SystemShutdown bool
}

// Callback is the callback that must be supplied for registering to receive shutdown notifications
type Callback func(ctx context.Context, state State) error

type Manager interface {
	Run(context.Context)
	Shutdown(context.Context)
	Register(string, Callback)
}

type manager struct {
	once          sync.Once
	registered    map[string]Callback
	cancelFn      context.CancelFunc
	timeout       time.Duration
	log           *log.PrefixLogger
	systemdClient *client.Systemd
	reader        fileio.Reader
}

// NewManager creates a new shutdown manager.
func NewManager(
	log *log.PrefixLogger,
	systemdClient *client.Systemd,
	reader fileio.Reader,
	timeout time.Duration,
	cancelFn context.CancelFunc) Manager {
	return &manager{
		registered:    make(map[string]Callback),
		timeout:       timeout,
		cancelFn:      cancelFn,
		log:           log,
		systemdClient: systemdClient,
		reader:        reader,
	}
}

func (m *manager) Run(ctx context.Context) {
	defer m.log.Infof("Agent shutdown complete")
	// handle teardown
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer func() {
		signal.Stop(signals)
		close(signals)
	}()

	done := make(chan struct{})
	go func(ctx context.Context) {
		defer close(done)
		select {
		case s := <-signals:
			m.log.Infof("Agent received shutdown signal: %s", s)
			m.Shutdown(ctx)
			m.cancelFn()
		case <-ctx.Done():
			m.log.Infof("Context has been cancelled, shutting down.")
			m.Shutdown(ctx)
		}
	}(ctx)

	<-done
}

func (m *manager) Shutdown(ctx context.Context) {
	// ensure multiple calls to Shutdown are idempotent
	m.once.Do(func() {
		state := State{
			SystemShutdown: m.isSystemShutdown(ctx),
		}
		now := time.Now()
		// give the agent time to shutdown gracefully
		ctx, cancel := context.WithTimeout(ctx, m.timeout)
		defer cancel()
		for name, fn := range m.registered {
			m.log.Infof("Shutting down: %s", name)
			if err := fn(ctx, state); err != nil {
				m.log.Errorf("Error shutting down: %s", err)
			}
		}
		m.log.Infof("Shutdown complete in %s", time.Since(now))
	})
}

func (m *manager) Register(name string, fn Callback) {
	if _, ok := m.registered[name]; ok {
		m.log.Warnf("Shutdown function %s already registered", name)
		return
	}
	m.registered[name] = fn
}

// IsSystemShutdown checks if the system is shutting down or rebooting
// by checking systemd targets and runlevels
func (m *manager) isSystemShutdown(ctx context.Context) bool {
	return m.isShuttingDownViaSystemd(ctx) || m.isShuttingDownViaRunlevel()
}

func (m *manager) isShuttingDownViaSystemd(ctx context.Context) bool {
	exists, err := m.reader.PathExists(shutdownScheduledPath)
	if err != nil {
		m.log.Errorf("Error checking if %s exists: %v", shutdownScheduledPath, err)
	} else if exists {
		m.log.Debug("System shutdown detected via scheduled file")
		return true
	}

	shutdownJobs, err := m.systemdClient.ListJobs(ctx)
	if err != nil {
		m.log.Errorf("Failed to list systemd jobs: %v", err)
		return false
	}

	// check if any shutdown-related jobs are starting
	shutdownTargets := map[string]struct{}{
		"shutdown.target": {},
		"reboot.target":   {},
		"poweroff.target": {},
		"halt.target":     {},
	}

	for _, job := range shutdownJobs {
		if _, isShutdownTarget := shutdownTargets[job.Unit]; isShutdownTarget && job.JobType == "start" {
			m.log.Debugf("System shutdown detected: %s job is %s", job.Unit, job.State)
			return true
		}
	}

	return false
}

// parseUtmpRecords parses binary utmp data into structured records
func parseUtmpRecords(data []byte, logger *log.PrefixLogger) []utmpRecord {
	var records []utmpRecord
	recordSize := unsafe.Sizeof(utmpRecord{})
	logger.Debugf("Parsing runlevel records. Record size: %d", recordSize)
	for offset := 0; offset < len(data); offset += int(recordSize) {
		if offset+int(recordSize) > len(data) {
			break
		}

		var record utmpRecord
		reader := bytes.NewReader(data[offset : offset+int(recordSize)])
		if err := binary.Read(reader, binary.NativeEndian, &record); err != nil {
			logger.Debugf("Error parsing utmp record at offset %d: %v", offset, err)
			continue // skip malformed records
		}
		records = append(records, record)
	}
	return records
}

func (m *manager) isShuttingDownViaRunlevel() bool {
	utmpBytes, err := m.reader.ReadFile(runlevelPath)
	if err != nil {
		if os.IsNotExist(err) {
			m.log.Debugf("Run level not found at %s; skipping run level detection", runlevelPath)
		} else {
			m.log.Errorf("Failed to read %s file: %v", runlevelPath, err)
		}
		return false
	}

	records := parseUtmpRecords(utmpBytes, m.log)

	// utmp is an append only log. Parse the records in reverse to find the most recent run level entry
	for i := len(records) - 1; i >= 0; i-- {
		record := records[i]
		if record.Type == runlevelRecordType {
			// run_level records use the pid to indicate the level. The pid represents the character, not the numerical
			// value ('0', not 0)
			runLevel := byte(record.Pid)
			if runLevel == runlevelReboot || runLevel == runLevelHalt {
				m.log.Debugf("System shutdown detected: level %c", runLevel)
				return true
			}
			break
		}
	}

	return false
}
