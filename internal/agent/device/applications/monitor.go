package applications

import (
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
)

const (
	stopMonitorWaitDuration = 5 * time.Second
	maxAppSummaryInfoLength = 256
	restartDelay            = 5 * time.Second
)

// streamingMonitor defines the interface for monitors that stream events from external processes.
// Implementations provide the command factory and event handling logic.
type streamingMonitor interface {
	CreateCommand(ctx context.Context) (*exec.Cmd, error)
	Parser() streamParser
	HandleEvent(ctx context.Context, data []byte)
	OnRestart()
}

type streamParser interface {
	parse(ctx context.Context, r io.Reader, handler func([]byte)) error
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

func (m *monitor) Has(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.apps[id]
	return ok
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

func (m *monitor) updateWithWorkloads(app Application) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	appID := app.ID()
	oldApp, ok := m.apps[appID]
	if !ok {
		return errors.ErrAppNotFound
	}

	app.CopyWorkloadsFrom(oldApp)
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

func (m *monitor) clearAllWorkloads() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, app := range m.apps {
		app.ClearWorkloads()
	}
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

func (m *monitor) startStreaming(ctx context.Context, mon streamingMonitor) error {
	m.mu.Lock()
	if m.running {
		m.log.Debugf("%s monitor is already running", m.name)
		m.mu.Unlock()
		return nil
	}

	m.log.Infof("Starting %s monitor", m.name)
	ctx, cancelFn := context.WithCancel(ctx)

	listenerChannel := make(chan struct{})
	m.cancelFn = cancelFn
	m.running = true
	m.listenerCloseChan = listenerChannel
	m.mu.Unlock()

	go func() {
		defer close(listenerChannel)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			mon.OnRestart()

			cmd, err := mon.CreateCommand(ctx)
			if err != nil {
				m.log.Errorf("Failed to create %s command: %v", m.name, err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(restartDelay):
				}
				continue
			}

			err = m.runStreamingLoop(ctx, cmd, mon.Parser(), mon.HandleEvent)
			if errors.IsContext(err) {
				return
			}

			if err != nil {
				m.log.Errorf("%s monitor error: %v, restarting...", m.name, err)
			} else {
				m.log.Debugf("%s monitor stream closed, restarting...", m.name)
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(restartDelay):
			}
		}
	}()

	return nil
}

func (m *monitor) runStreamingLoop(
	ctx context.Context,
	cmd *exec.Cmd,
	parser streamParser,
	handler func(ctx context.Context, data []byte),
) error {
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	m.mu.Lock()
	m.cmd = cmd
	m.mu.Unlock()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start command: %w", err)
	}

	m.listenForEvents(ctx, stdoutPipe, parser, handler)

	if err := cmd.Wait(); err != nil && !isExpectedShutdownError(err) {
		return fmt.Errorf("command exited with error: %w", err)
	}

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

	m.log.Infof("Stopping %s monitor", m.name)

	if cmd != nil && cmd.Process != nil {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			m.log.Debugf("Failed to send SIGTERM to %s monitor (process may have exited): %v", m.name, err)
		}
	}

	cancelFn()

	if !waitForChannelWithTimeout(listenerChan, stopMonitorWaitDuration) {
		m.log.Warnf("Timeout waiting for %s monitor to shutdown", m.name)
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
