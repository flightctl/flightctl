package applications

import (
	"bufio"
	"context"
	"encoding/json"
	stderrs "errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
)

const (
	stopMonitorWaitDuration = 5 * time.Second
	maxAppSummaryInfoLength = 256
)

type streamParser interface {
	parse(ctx context.Context, r io.Reader, handler func([]byte)) error
}

type lineParser struct{}

func (lineParser) parse(ctx context.Context, r io.Reader, handler func([]byte)) error {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			handler(scanner.Bytes())
		}
	}
	return scanner.Err()
}

type jsonStreamParser struct{}

func (jsonStreamParser) parse(ctx context.Context, r io.Reader, handler func([]byte)) error {
	decoder := json.NewDecoder(r)
	for {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			if err == io.EOF {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				return err
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			handler(raw)
		}
	}
}

type handlerRegistration struct {
	appType v1beta1.AppType
	handler lifecycle.ActionHandler
}

// AppStatusResult holds the status result for a single application.
type AppStatusResult struct {
	Status  v1beta1.DeviceApplicationStatus
	Summary v1beta1.DeviceApplicationsSummaryStatus
}

// monitor provides shared functionality for application monitors.
type monitor struct {
	mu                sync.Mutex
	cmd               *exec.Cmd
	cancelFn          context.CancelFunc
	running           bool
	listenerCloseChan chan struct{}

	apps     map[string]Application
	actions  []lifecycle.Action
	handlers map[v1beta1.AppType]lifecycle.ActionHandler

	log  *log.PrefixLogger
	name string
}

func newMonitor(log *log.PrefixLogger, name string, registrations ...handlerRegistration) *monitor {
	handlers := make(map[v1beta1.AppType]lifecycle.ActionHandler, len(registrations))
	for _, reg := range registrations {
		handlers[reg.appType] = reg.handler
	}
	return &monitor{
		apps:     make(map[string]Application),
		handlers: handlers,
		log:      log,
		name:     name,
	}
}

func (m *monitor) hasApps() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.apps) > 0
}

func (m *monitor) isRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

func (m *monitor) Has(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.apps[id]
	return ok
}

func (m *monitor) getByID(appID string) (Application, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	app, ok := m.apps[appID]
	return app, ok
}

func (m *monitor) getApps() []Application {
	m.mu.Lock()
	defer m.mu.Unlock()

	apps := make([]Application, 0, len(m.apps))
	for _, app := range m.apps {
		apps = append(apps, app)
	}
	return apps
}

func (m *monitor) drainActions() []lifecycle.Action {
	m.mu.Lock()
	defer m.mu.Unlock()
	actions := make([]lifecycle.Action, len(m.actions))
	copy(actions, m.actions)
	m.actions = nil
	return actions
}

func (m *monitor) Ensure(app Application) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	appID := app.ID()
	if _, ok := m.apps[appID]; ok {
		return nil
	}
	m.apps[appID] = app

	action := lifecycle.Action{
		AppType:  app.AppType(),
		Type:     lifecycle.ActionAdd,
		Name:     app.Name(),
		ID:       appID,
		Path:     app.Path(),
		Embedded: app.IsEmbedded(),
		Volumes:  provider.ToLifecycleVolumes(app.Volume().List()),
		Spec:     app.ActionSpec(),
	}

	m.actions = append(m.actions, action)
	return nil
}

func (m *monitor) canRemoveApp(app Application) bool {
	_, ok := m.apps[app.ID()]
	return ok || app.IsEmbedded()
}

func (m *monitor) Remove(app Application) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	appID := app.ID()
	if !m.canRemoveApp(app) {
		m.log.Errorf("%s application not found: %s", m.name, app.Name())
		return nil
	}

	delete(m.apps, appID)

	action := lifecycle.Action{
		AppType: app.AppType(),
		Type:    lifecycle.ActionRemove,
		Name:    app.Name(),
		ID:      appID,
		Volumes: provider.ToLifecycleVolumes(app.Volume().List()),
		Spec:    app.ActionSpec(),
	}

	m.actions = append(m.actions, action)
	return nil
}

func (m *monitor) Update(app Application) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	appID := app.ID()
	if _, ok := m.apps[appID]; !ok {
		return errors.ErrAppNotFound
	}

	m.apps[appID] = app

	action := lifecycle.Action{
		AppType: app.AppType(),
		Type:    lifecycle.ActionUpdate,
		Name:    app.Name(),
		ID:      appID,
		Path:    app.Path(),
		Volumes: provider.ToLifecycleVolumes(app.Volume().List()),
		Spec:    app.ActionSpec(),
	}

	m.actions = append(m.actions, action)
	return nil
}

func (m *monitor) drain(ctx context.Context) error {
	var errs []error

	apps := m.getApps()
	m.log.Infof("Draining %d applications", len(apps))
	for _, app := range apps {
		if err := m.Remove(app); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

func (m *monitor) startStreaming(ctx context.Context, cmd *exec.Cmd, parser streamParser, handler func(ctx context.Context, data []byte)) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		m.log.Debugf("%s monitor is already running", m.name)
		return nil
	}

	m.log.Infof("Starting %s monitor", m.name)
	ctx, cancelFn := context.WithCancel(ctx)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		cancelFn()
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancelFn()
		return fmt.Errorf("failed to start %s monitor: %w", m.name, err)
	}

	listenerChannel := make(chan struct{})
	m.cancelFn = cancelFn
	m.cmd = cmd
	m.running = true
	m.listenerCloseChan = listenerChannel

	go func() {
		defer close(listenerChannel)
		m.listenForEvents(ctx, stdoutPipe, parser, handler)
	}()

	return nil
}

func (m *monitor) listenForEvents(ctx context.Context, stdoutPipe io.ReadCloser, parser streamParser, handler func(ctx context.Context, data []byte)) {
	defer m.log.Debugf("Done listening for %s events", m.name)

	wrappedHandler := func(data []byte) {
		m.log.Debugf("Received %s event: %s", m.name, string(data))
		handler(ctx, data)
	}

	if err := parser.parse(ctx, stdoutPipe, wrappedHandler); err != nil && !errors.IsContext(err) {
		m.log.Errorf("Error reading %s events: %v", m.name, err)
	}
}

func (m *monitor) stopStreaming() error {
	m.mu.Lock()
	if !m.running {
		m.log.Debugf("%s monitor is already stopped", m.name)
		m.mu.Unlock()
		return nil
	}
	cancelFn := m.cancelFn
	cmd := m.cmd
	listenerChan := m.listenerCloseChan
	m.cmd = nil
	m.cancelFn = nil
	m.running = false
	m.listenerCloseChan = nil
	m.mu.Unlock()

	if cmd == nil {
		return nil
	}
	defer cancelFn()

	m.log.Infof("Stopping %s monitor", m.name)

	killed := false
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		m.log.Warnf("Failed to send SIGTERM to %s monitor: %v", m.name, err)
		cancelFn()
		killed = true
	}

	if !waitForChannelWithTimeout(listenerChan, stopMonitorWaitDuration) {
		timeoutMessage := fmt.Sprintf("Timeout waiting for %s monitor reader to shutdown", m.name)
		forceKilledMessage := fmt.Sprintf("%s after force killing the process", timeoutMessage)
		message := timeoutMessage
		logLevel := logrus.WarnLevel
		if killed {
			logLevel = logrus.ErrorLevel
			message = forceKilledMessage
		}
		m.log.Log(logLevel, message)
		if !killed {
			cancelFn()
			if !waitForChannelWithTimeout(listenerChan, stopMonitorWaitDuration) {
				m.log.Error(forceKilledMessage)
			}
		}
	}

	if err := cmd.Wait(); err != nil && !isExpectedShutdownError(err) {
		return fmt.Errorf("unexpected error during %s monitor shutdown: %w", m.name, err)
	}
	m.log.Infof("%s monitor stopped", m.name)
	return nil
}

// executeActions executes queued actions grouped by app type.
// The normalizeAppType function allows mapping app types (e.g., Container -> Quadlet).
// Returns the drained actions for any post-processing by the caller.
func (m *monitor) executeActions(ctx context.Context, normalizeAppType func(v1beta1.AppType) v1beta1.AppType) ([]lifecycle.Action, error) {
	actions := m.drainActions()

	groupedActions := make(map[v1beta1.AppType][]lifecycle.Action)
	for i := range actions {
		action := actions[i]
		appType := action.AppType
		if normalizeAppType != nil {
			appType = normalizeAppType(appType)
		}
		if _, ok := m.handlers[appType]; !ok {
			return actions, fmt.Errorf("%w: no action handler registered: %s", errors.ErrUnsupportedAppType, action.AppType)
		}
		groupedActions[appType] = append(groupedActions[appType], action)
	}

	for appType, typeActions := range groupedActions {
		if err := m.handlers[appType].Execute(ctx, typeActions); err != nil {
			return actions, err
		}
	}

	return actions, nil
}

// Status returns the status of all monitored applications.
func (m *monitor) Status() ([]AppStatusResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	results := make([]AppStatusResult, 0, len(m.apps))

	for _, app := range m.apps {
		appStatus, appSummary, err := app.Status()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		results = append(results, AppStatusResult{
			Status:  *appStatus,
			Summary: appSummary,
		})
	}

	if len(errs) > 0 {
		return results, errors.Join(errs...)
	}

	return results, nil
}

func waitForChannelWithTimeout(c <-chan struct{}, dur time.Duration) bool {
	timer := time.NewTimer(dur)
	defer timer.Stop()
	select {
	case <-c:
		return true
	case <-timer.C:
		return false
	}
}

func isExpectedShutdownError(err error) bool {
	if errors.IsContext(err) {
		return true
	}
	var exitErr *exec.ExitError
	if stderrs.As(err, &exitErr) {
		if status, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus); ok {
			if status.ExitStatus() == 1 {
				return true
			}
			if status.Signaled() {
				signal := status.Signal()
				return signal == syscall.SIGKILL || signal == syscall.SIGTERM
			}
		}
	}
	return false
}

func buildAppSummaryInfo(erroredApps, degradedApps []string, maxLen int) *string {
	if len(erroredApps) == 0 && len(degradedApps) == 0 {
		return nil
	}

	info := strings.Join(append(erroredApps, degradedApps...), ", ")
	if len(info) > maxLen {
		info = fmt.Sprintf("%s...", info[:maxLen-3])
	}
	return &info
}
