package shutdown

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	DefaultGracefulShutdownTimeout = 30 * time.Second
)

// Priority constants for component shutdown ordering
// Lower numbers = higher priority (shutdown first)
const (
	// PriorityHighest - Critical infrastructure that must shutdown first
	// Examples: HTTP servers, load balancer endpoints, external APIs
	PriorityHighest = 0

	// PriorityHigh - Important services that handle active requests
	// Examples: gRPC servers, message consumers, active connections
	PriorityHigh = 1

	// PriorityNormal - Standard application services
	// Examples: business logic services, data processors, background tasks
	PriorityNormal = 2

	// PriorityLow - Supporting services and caches
	// Examples: Redis connections, memory caches, non-critical services
	PriorityLow = 3

	// PriorityLowest - Persistence and storage layers
	// Examples: database connections, file handles, cleanup tasks
	PriorityLowest = 4

	// PriorityLast - Final cleanup and completion markers
	// Examples: completion signals, final logging, test markers
	PriorityLast = 5
)

// Timeout constants for component shutdown durations
// These provide semantic meaning for common shutdown timeout values
const (
	// TimeoutCompletion - For completion markers and quick cleanup tasks
	TimeoutCompletion = 1 * time.Second

	// TimeoutQuick - For lightweight components that shutdown quickly
	TimeoutQuick = 2 * time.Second

	// TimeoutCache - For cache components and in-memory data structures
	TimeoutCache = 3 * time.Second

	// TimeoutStandard - Standard timeout for most service components
	// Examples: business logic services, API handlers, background processors
	TimeoutStandard = 5 * time.Second

	// TimeoutDatabase - For database connections and persistent storage
	// Examples: SQL connections, data stores, queue connections
	TimeoutDatabase = 10 * time.Second

	// TimeoutServer - For HTTP servers and critical service endpoints
	// Allows time for graceful connection draining and request completion
	TimeoutServer = 30 * time.Second

	// TimeoutPeriodic - For long-running periodic services and schedulers
	// Examples: background job processors, periodic data sync services
	TimeoutPeriodic = 45 * time.Second

	// TimeoutPeriodicServiceShutdown - Overall timeout for periodic service shutdown
	// Maximum time to wait for all periodic tasks and components to shut down gracefully
	TimeoutPeriodicServiceShutdown = 5 * time.Minute

	// TimeoutForceShutdownWindow - Time window for second signal to trigger force shutdown
	// Users can send the same signal again within this window to bypass graceful shutdown
	TimeoutForceShutdownWindow = 5 * time.Second
)

// Test timeout constants for consistent test behavior
const (
	// TimeoutTestVeryFast - For very fast test operations and quick timeouts
	TimeoutTestVeryFast = 50 * time.Millisecond

	// TimeoutTestFast - For fast test components and quick assertions
	TimeoutTestFast = 100 * time.Millisecond

	// TimeoutTestStandard - Standard test timeout for component verification
	TimeoutTestStandard = 200 * time.Millisecond
)

// ShutdownState represents the various states of shutdown
type ShutdownState int

const (
	StateIdle ShutdownState = iota
	StateInitiated
	StateInProgress
	StateCompleted
	StateFailed
)

func (s ShutdownState) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateInitiated:
		return "initiated"
	case StateInProgress:
		return "in_progress"
	case StateCompleted:
		return "completed"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// ComponentStatus tracks the status of an individual component during shutdown
type ComponentStatus struct {
	Name      string
	Status    string // "pending", "in_progress", "success", "error", "timeout"
	StartTime *time.Time
	EndTime   *time.Time
	Duration  time.Duration
	Error     string
}

// ShutdownStatus represents the current shutdown state of the service for API responses
type ShutdownStatus struct {
	IsShuttingDown      bool                 `json:"isShuttingDown"`
	ShutdownInitiated   *time.Time           `json:"shutdownInitiated,omitempty"`
	ActiveComponents    []string             `json:"activeComponents,omitempty"`
	CompletedComponents []CompletedComponent `json:"completedComponents,omitempty"`
	State               string               `json:"state"` // "idle", "initiated", "in_progress", "completed", "failed"
	Message             string               `json:"message,omitempty"`
}

// CompletedComponent represents a component that has finished shutting down
type CompletedComponent struct {
	Name     string        `json:"name"`
	Status   string        `json:"status"` // "success", "error", "timeout"
	Duration time.Duration `json:"duration"`
	Error    string        `json:"error,omitempty"`
}

// ShutdownCallback is a function that performs cleanup for a component
type ShutdownCallback func(ctx context.Context) error

// Component represents a component that can be shut down gracefully
type Component struct {
	Name     string
	Priority int           // Lower number = higher priority (shutdown first)
	Timeout  time.Duration // Maximum time allowed for this component's shutdown
	Callback ShutdownCallback
}

// MetricsCollector defines the interface for recording shutdown metrics
type MetricsCollector interface {
	RecordShutdownInitiated(serviceName string)
	RecordShutdownInProgress(serviceName string)
	RecordShutdownCompleted(serviceName string, outcome string, totalDuration time.Duration)
	RecordComponentShutdownStart(component string, priority int)
	RecordComponentShutdownEnd(component string, priority int, status string, duration time.Duration)
	RecordShutdownError(component string, errorType string)
	RecordFailFastEvent(component string, reason string)
	RecordSignalReceived(signal string)
	RecordSignalTimeout()
}

// ShutdownManager manages the graceful shutdown of multiple components
type ShutdownManager struct {
	components       []Component
	log              *logrus.Logger
	mu               sync.RWMutex
	failFastEnabled  bool
	failFastCancel   context.CancelFunc
	metricsCollector MetricsCollector
	serviceName      string

	// Status tracking for API endpoint
	state             ShutdownState
	shutdownInitiated *time.Time
	componentStatuses map[string]*ComponentStatus
	lastStatusMessage string

	// Concurrency protection for shutdown
	shutdownOnce   sync.Once
	shutdownResult error
	shutdownDone   bool
}

// NewShutdownManager creates a new shutdown manager
func NewShutdownManager(log *logrus.Logger) *ShutdownManager {
	return &ShutdownManager{
		components:        make([]Component, 0),
		log:               log,
		failFastEnabled:   false,
		serviceName:       "unknown", // Default service name
		state:             StateIdle,
		componentStatuses: make(map[string]*ComponentStatus),
	}
}

// SetMetricsCollector sets the metrics collector for shutdown tracking
func (sm *ShutdownManager) SetMetricsCollector(collector MetricsCollector) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.metricsCollector = collector
}

// SetServiceName sets the service name for metrics identification
func (sm *ShutdownManager) SetServiceName(name string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.serviceName = name
}

// EnableFailFast enables fail-fast behavior where component failures trigger immediate shutdown
func (sm *ShutdownManager) EnableFailFast(cancel context.CancelFunc) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.failFastEnabled = true
	sm.failFastCancel = cancel
}

// TriggerFailFast triggers an immediate shutdown due to component failure
func (sm *ShutdownManager) TriggerFailFast(componentName string, err error) {
	// Record fail-fast event in metrics
	if sm.metricsCollector != nil {
		reason := "unknown"
		if err != nil {
			reason = err.Error()
		}
		sm.metricsCollector.RecordFailFastEvent(componentName, reason)
	}

	if !sm.failFastEnabled || sm.failFastCancel == nil {
		sm.log.WithError(err).Warnf("Component %s failed but fail-fast not enabled", componentName)
		return
	}

	sm.log.WithError(err).Errorf("Component %s failed, triggering fail-fast shutdown", componentName)
	sm.failFastCancel()
}

// Register adds a component to be shut down with the given priority and timeout
func (sm *ShutdownManager) Register(name string, priority int, timeout time.Duration, callback ShutdownCallback) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.components = append(sm.components, Component{
		Name:     name,
		Priority: priority,
		Timeout:  timeout,
		Callback: callback,
	})
}

// Shutdown performs graceful shutdown of all registered components in priority order
func (sm *ShutdownManager) Shutdown(ctx context.Context) error {
	// Use sync.Once to ensure shutdown only happens once, even with concurrent calls
	sm.shutdownOnce.Do(func() {
		sm.shutdownResult = sm.performShutdown(ctx)
		sm.mu.Lock()
		sm.shutdownDone = true
		sm.mu.Unlock()
	})

	// Return the result of the shutdown
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.shutdownResult
}

// performShutdown is the actual shutdown implementation, protected by sync.Once
func (sm *ShutdownManager) performShutdown(ctx context.Context) error {
	shutdownStart := time.Now()

	sm.mu.Lock()
	// Initialize component statuses and set initial state
	sm.state = StateInitiated
	sm.shutdownInitiated = &shutdownStart
	sm.lastStatusMessage = "Shutdown initiated"

	// Initialize all components as pending
	for _, comp := range sm.components {
		sm.componentStatuses[comp.Name] = &ComponentStatus{
			Name:   comp.Name,
			Status: "pending",
		}
	}

	components := make([]Component, len(sm.components))
	copy(components, sm.components)
	serviceName := sm.serviceName
	metricsCollector := sm.metricsCollector
	sm.mu.Unlock()

	// Record shutdown initiation immediately for metrics scraping
	if metricsCollector != nil {
		metricsCollector.RecordShutdownInitiated(serviceName)
		metricsCollector.RecordShutdownInProgress(serviceName)
	}

	// Update state to in progress
	sm.mu.Lock()
	sm.state = StateInProgress
	sm.lastStatusMessage = fmt.Sprintf("Shutting down %d components", len(components))
	sm.mu.Unlock()

	// Sort by priority (lower numbers first)
	sort.Slice(components, func(i, j int) bool {
		return components[i].Priority < components[j].Priority
	})

	var allErrors []error

	for _, comp := range components {
		compLog := sm.log.WithFields(logrus.Fields{
			"component": comp.Name,
			"priority":  comp.Priority,
			"timeout":   comp.Timeout,
		})

		compLog.Info("Starting component shutdown")
		start := time.Now()

		// Update component status to in_progress
		sm.mu.Lock()
		if compStatus, exists := sm.componentStatuses[comp.Name]; exists {
			compStatus.Status = "in_progress"
			compStatus.StartTime = &start
		}
		sm.mu.Unlock()

		// Record component shutdown start for metrics
		if metricsCollector != nil {
			metricsCollector.RecordComponentShutdownStart(comp.Name, comp.Priority)
		}

		// Create timeout context for this specific component
		compCtx, cancel := context.WithTimeout(ctx, comp.Timeout)

		// Execute component callback with panic recovery
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					compLog.WithField("panic", r).Error("Component shutdown panicked")
					err = fmt.Errorf("component panicked: %v", r)
				}
			}()
			err = comp.Callback(compCtx)
		}()
		cancel()

		duration := time.Since(start)
		endTime := time.Now()

		// Determine component shutdown status
		status := "success"
		errorMsg := ""
		if err != nil {
			if compCtx.Err() == context.DeadlineExceeded {
				status = "timeout"
				errorMsg = "component shutdown timed out"
			} else {
				status = "error"
				errorMsg = err.Error()
			}
			compLog.WithError(err).WithField("duration", duration).Error("Component shutdown failed")
			allErrors = append(allErrors, fmt.Errorf("component %s shutdown failed: %w", comp.Name, err))

			// Record shutdown error for metrics
			if metricsCollector != nil {
				errorType := "unknown"
				if compCtx.Err() == context.DeadlineExceeded {
					errorType = "timeout"
				} else if err != nil {
					errorType = "callback_error"
				}
				metricsCollector.RecordShutdownError(comp.Name, errorType)
			}
		} else {
			compLog.WithField("duration", duration).Info("Component shutdown completed successfully")
		}

		// Update component status with completion details
		sm.mu.Lock()
		if compStatus, exists := sm.componentStatuses[comp.Name]; exists {
			compStatus.Status = status
			compStatus.EndTime = &endTime
			compStatus.Duration = duration
			compStatus.Error = errorMsg
		}
		sm.mu.Unlock()

		// Record component shutdown end for metrics
		if metricsCollector != nil {
			metricsCollector.RecordComponentShutdownEnd(comp.Name, comp.Priority, status, duration)
		}
	}

	// Calculate total shutdown duration and determine outcome
	totalDuration := time.Since(shutdownStart)
	outcome := "success"
	var finalErr error

	if len(allErrors) > 0 {
		outcome = "error"
		finalErr = fmt.Errorf("shutdown completed with %d errors: %v", len(allErrors), allErrors)
	}

	// Update final state and status message
	sm.mu.Lock()
	if finalErr != nil {
		sm.state = StateFailed
		sm.lastStatusMessage = fmt.Sprintf("Shutdown failed: %s", finalErr.Error())
	} else {
		sm.state = StateCompleted
		sm.lastStatusMessage = fmt.Sprintf("Shutdown completed successfully in %v", totalDuration)
	}
	sm.mu.Unlock()

	// Record shutdown completion for metrics - do this before final log/return
	// to ensure metrics are available for scraping during the metrics server's
	// own graceful shutdown window
	if metricsCollector != nil {
		metricsCollector.RecordShutdownCompleted(serviceName, outcome, totalDuration)
	}

	if finalErr != nil {
		return finalErr
	}

	sm.log.Info("All components shut down successfully")
	return nil
}

// ForceShutdown performs immediate shutdown of all registered components without timeouts
// This is for emergency situations where graceful shutdown is not possible/desired
func (sm *ShutdownManager) ForceShutdown() error {
	shutdownStart := time.Now()

	sm.mu.Lock()
	// Initialize component statuses and set initial state
	sm.state = StateInitiated
	sm.shutdownInitiated = &shutdownStart
	sm.lastStatusMessage = "Force shutdown initiated"

	// Initialize all components as pending
	for _, comp := range sm.components {
		sm.componentStatuses[comp.Name] = &ComponentStatus{
			Name:   comp.Name,
			Status: "pending",
		}
	}

	components := make([]Component, len(sm.components))
	copy(components, sm.components)
	serviceName := sm.serviceName
	metricsCollector := sm.metricsCollector
	sm.mu.Unlock()

	// Record shutdown initiation immediately for metrics scraping
	if metricsCollector != nil {
		metricsCollector.RecordShutdownInitiated(serviceName)
		metricsCollector.RecordShutdownInProgress(serviceName)
	}

	sm.log.WithField("force", true).Warn("Force shutdown initiated - no timeouts applied")

	// Sort components by priority (same as graceful shutdown)
	sort.SliceStable(components, func(i, j int) bool {
		return components[i].Priority < components[j].Priority
	})

	var errs []error
	var wg sync.WaitGroup

	// Execute all components in parallel with no timeouts
	for _, comp := range components {
		wg.Add(1)
		go func(component Component) {
			defer wg.Done()

			compLog := sm.log.WithField("component", component.Name)
			compLog.Warn("Force shutdown component - no timeout")

			start := time.Now()
			sm.mu.Lock()
			if sm.componentStatuses[component.Name] != nil {
				sm.componentStatuses[component.Name].Status = "force_stopping"
				sm.componentStatuses[component.Name].StartTime = &start
			}
			sm.mu.Unlock()

			// Record component shutdown start for metrics
			if metricsCollector != nil {
				metricsCollector.RecordComponentShutdownStart(component.Name, component.Priority)
			}

			// Execute component callback with panic recovery but NO timeout
			var err error
			func() {
				defer func() {
					if r := recover(); r != nil {
						compLog.WithField("panic", r).Error("Component force shutdown panicked")
						err = fmt.Errorf("component panicked: %v", r)
					}
				}()

				// Use background context - no cancellation/timeout
				err = component.Callback(context.Background())
			}()

			endTime := time.Now()
			duration := endTime.Sub(*sm.componentStatuses[component.Name].StartTime)

			sm.mu.Lock()
			if sm.componentStatuses[component.Name] != nil {
				if err != nil {
					sm.componentStatuses[component.Name].Status = "failed"
					sm.componentStatuses[component.Name].Error = err.Error()
					compLog.WithError(err).Error("Component force shutdown failed")
					errs = append(errs, fmt.Errorf("component %s force shutdown failed: %w", component.Name, err))
				} else {
					sm.componentStatuses[component.Name].Status = "completed"
					compLog.Info("Component force shutdown completed")
				}
				sm.componentStatuses[component.Name].Duration = duration
			}
			sm.mu.Unlock()

			// Record component shutdown completion for metrics
			if metricsCollector != nil {
				outcome := "success"
				if err != nil {
					outcome = "error"
				}
				metricsCollector.RecordComponentShutdownEnd(component.Name, component.Priority, outcome, duration)
			}
		}(comp)
	}

	// Wait for all components to complete (or fail)
	wg.Wait()

	totalDuration := time.Since(shutdownStart)

	sm.mu.Lock()
	var finalErr error
	if len(errs) > 0 {
		sm.state = StateFailed
		errMsg := fmt.Sprintf("Force shutdown completed with %d errors in %v", len(errs), totalDuration)
		sm.lastStatusMessage = errMsg
		finalErr = fmt.Errorf("force shutdown completed with %d errors: %v", len(errs), errs)
	} else {
		sm.state = StateCompleted
		sm.lastStatusMessage = fmt.Sprintf("Force shutdown completed successfully in %v", totalDuration)
	}
	sm.mu.Unlock()

	// Record shutdown completion for metrics
	if metricsCollector != nil {
		outcome := "success"
		if finalErr != nil {
			outcome = "error"
		}
		metricsCollector.RecordShutdownCompleted(serviceName, outcome, totalDuration)
	}

	if finalErr != nil {
		sm.log.WithField("duration", totalDuration).WithField("errors", len(errs)).Error("Force shutdown completed with errors")
		return finalErr
	}

	sm.log.WithField("duration", totalDuration).Info("Force shutdown completed successfully")
	return nil
}

// HandleSignals sets up signal handling for graceful shutdown with timeout enforcement
func HandleSignals(log *logrus.Logger, cancel context.CancelFunc, timeout time.Duration) {
	go func() {
		signals := make(chan os.Signal, 2)
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
		defer func() {
			signal.Stop(signals)
			close(signals)
		}()

		s := <-signals
		log.Infof("Shutdown signal received, initiating graceful shutdown: %s", s.String())

		// Start the graceful shutdown timeout
		timer := time.NewTimer(timeout)
		defer timer.Stop()

		go func() {
			<-timer.C
			log.Errorf("Graceful shutdown timeout (%v) exceeded, forcing shutdown", timeout)
			cancel()
		}()

		// Trigger the cancellation immediately for components to start shutting down
		cancel()
	}()
}

// HandleSignalsWithManager sets up signal handling using a shutdown manager
// HandleSignalsWithManager sets up signal handling using a shutdown manager
// Supports both graceful and forced shutdown:
// - First signal: graceful shutdown with timeout
// - Second signal within TimeoutForceShutdownWindow: immediate force shutdown
func HandleSignalsWithManager(log *logrus.Logger, manager *ShutdownManager, globalTimeout time.Duration) {
	go func() {
		signals := make(chan os.Signal, 5)
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
		defer func() {
			signal.Stop(signals)
			close(signals)
		}()

		// Wait for first signal
		s := <-signals
		log.Infof("Shutdown signal received, initiating coordinated graceful shutdown: %s", s.String())
		log.Infof("Send the same signal again within %v for immediate force shutdown", TimeoutForceShutdownWindow)

		// Record signal received for metrics
		manager.mu.RLock()
		metricsCollector := manager.metricsCollector
		manager.mu.RUnlock()

		if metricsCollector != nil {
			metricsCollector.RecordSignalReceived(s.String())
		}

		// Start graceful shutdown in background
		shutdownDone := make(chan error, 1)
		go func() {
			// Create shutdown context with global timeout
			ctx, cancel := context.WithTimeout(context.Background(), globalTimeout)
			defer cancel()

			// Set up timeout monitoring for metrics
			go func() {
				<-ctx.Done()
				if ctx.Err() == context.DeadlineExceeded && metricsCollector != nil {
					metricsCollector.RecordSignalTimeout()
				}
			}()

			// Perform graceful shutdown
			err := manager.Shutdown(ctx)
			if err != nil {
				log.WithError(err).Error("Coordinated shutdown completed with errors")
			} else {
				log.Info("Coordinated shutdown completed successfully")
			}
			shutdownDone <- err
		}()

		// Listen for second signal or shutdown completion
		forceTimer := time.NewTimer(TimeoutForceShutdownWindow)
		defer forceTimer.Stop()

		select {
		case <-shutdownDone:
			// Graceful shutdown completed
			return

		case s2 := <-signals:
			// Second signal received - force shutdown
			log.WithField("signal", s2.String()).Warnf("Second shutdown signal received within %v, initiating FORCE shutdown (no timeouts)", TimeoutForceShutdownWindow)

			if metricsCollector != nil {
				metricsCollector.RecordSignalReceived(s2.String() + "_force")
			}

			// Cancel graceful shutdown and do force shutdown
			if err := manager.ForceShutdown(); err != nil {
				log.WithError(err).Error("Force shutdown failed")
			}
			return

		case <-forceTimer.C:
			// Force window expired, just wait for graceful shutdown
			log.Debugf("Force shutdown window (%v) expired, continuing graceful shutdown", TimeoutForceShutdownWindow)
			<-shutdownDone
			return
		}
	}()
}

// FormatPriority formats a priority value as a string for metrics labels
func FormatPriority(priority int) string {
	return fmt.Sprintf("%d", priority)
}

// GetShutdownStatus returns the current shutdown status for API responses
func (sm *ShutdownManager) GetShutdownStatus() ShutdownStatus {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	status := ShutdownStatus{
		IsShuttingDown:      sm.state == StateInitiated || sm.state == StateInProgress,
		ShutdownInitiated:   sm.shutdownInitiated,
		State:               sm.state.String(),
		Message:             sm.lastStatusMessage,
		ActiveComponents:    make([]string, 0),
		CompletedComponents: make([]CompletedComponent, 0),
	}

	// Build lists of active and completed components
	for name, compStatus := range sm.componentStatuses {
		switch compStatus.Status {
		case "in_progress":
			status.ActiveComponents = append(status.ActiveComponents, name)
		case "success", "error", "timeout":
			completed := CompletedComponent{
				Name:     compStatus.Name,
				Status:   compStatus.Status,
				Duration: compStatus.Duration,
				Error:    compStatus.Error,
			}
			status.CompletedComponents = append(status.CompletedComponents, completed)
		}
	}

	// Sort for consistent API responses
	sort.Strings(status.ActiveComponents)
	sort.Slice(status.CompletedComponents, func(i, j int) bool {
		return status.CompletedComponents[i].Name < status.CompletedComponents[j].Name
	})

	return status
}

// ========================== Task Management Methods ==========================

// IsShutdownInProgress returns true if shutdown has been initiated
func (sm *ShutdownManager) IsShutdownInProgress() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state == StateInitiated || sm.state == StateInProgress
}

// ShouldAcceptNewTasks returns false if shutdown is in progress and new tasks should not be started
func (sm *ShutdownManager) ShouldAcceptNewTasks() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state == StateIdle
}

// IsShutdownCompleted returns true if shutdown has completed (successfully or with errors)
func (sm *ShutdownManager) IsShutdownCompleted() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state == StateCompleted || sm.state == StateFailed
}

// GetShutdownState returns the current shutdown state
func (sm *ShutdownManager) GetShutdownState() ShutdownState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state
}

// TaskManager provides a convenient interface for task lifecycle management
type TaskManager struct {
	shutdownManager *ShutdownManager
	taskName        string
	logger          *logrus.Entry
}

// NewTaskManager creates a new task manager for the given task name
func (sm *ShutdownManager) NewTaskManager(taskName string) *TaskManager {
	return &TaskManager{
		shutdownManager: sm,
		taskName:        taskName,
		logger:          sm.log.WithField("task", taskName),
	}
}

// CanStartTask returns true if the task can be started, false if shutdown is in progress
func (tm *TaskManager) CanStartTask() bool {
	if !tm.shutdownManager.ShouldAcceptNewTasks() {
		tm.logger.Debug("Rejecting new task - shutdown in progress")
		return false
	}
	return true
}

// StartPeriodicTask starts a periodic task that respects shutdown signals
// Returns a context that will be cancelled when shutdown begins, and a done channel
func (tm *TaskManager) StartPeriodicTask(ctx context.Context, interval time.Duration, taskFunc func(context.Context) error) (<-chan struct{}, error) {
	if !tm.CanStartTask() {
		return nil, fmt.Errorf("cannot start periodic task '%s' - shutdown in progress", tm.taskName)
	}

	done := make(chan struct{})

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		// Create a fast shutdown check ticker
		shutdownCheckTicker := time.NewTicker(10 * time.Millisecond)
		defer shutdownCheckTicker.Stop()

		tm.logger.Info("Starting periodic task")

		var taskWg sync.WaitGroup

		defer func() {
			// Wait for any running task to complete before exiting
			tm.logger.Debug("Waiting for running task to complete...")
			taskWg.Wait()
			tm.logger.Info("Periodic task scheduler stopped")
		}()

		for {
			select {
			case <-ctx.Done():
				tm.logger.Info("Periodic task stopped - context cancelled")
				return

			case <-shutdownCheckTicker.C:
				// Fast check for shutdown signal
				if tm.shutdownManager.IsShutdownInProgress() {
					tm.logger.Info("Stopping periodic task scheduler - shutdown detected, waiting for current task")
					// Stop accepting new tasks but wait for current task to complete
					ticker.Stop()
					shutdownCheckTicker.Stop()
					return
				}

			case <-ticker.C:
				// Check if shutdown is in progress before starting new task iteration
				if tm.shutdownManager.IsShutdownInProgress() {
					tm.logger.Info("Stopping periodic task scheduler - shutdown in progress")
					// Stop the ticker to prevent new iterations
					ticker.Stop()
					return
				}

				// Wait for previous task to complete before starting new one
				// This ensures we don't have overlapping executions
				taskWg.Wait()

				// Start a new task iteration
				taskWg.Add(1)
				go func() {
					defer taskWg.Done()
					if err := taskFunc(ctx); err != nil {
						tm.logger.WithError(err).Error("Periodic task execution failed")
					}
				}()
			}
		}
	}()

	return done, nil
}

// ExecuteTask runs a single task if shutdown is not in progress
func (tm *TaskManager) ExecuteTask(ctx context.Context, taskFunc func(context.Context) error) error {
	if !tm.CanStartTask() {
		return fmt.Errorf("cannot execute task '%s' - shutdown in progress", tm.taskName)
	}

	tm.logger.Debug("Executing task")
	return taskFunc(ctx)
}

// WaitForShutdownSignal blocks until shutdown is initiated
func (tm *TaskManager) WaitForShutdownSignal() <-chan struct{} {
	shutdownSignal := make(chan struct{})

	go func() {
		defer close(shutdownSignal)
		for {
			if tm.shutdownManager.IsShutdownInProgress() {
				return
			}
			time.Sleep(100 * time.Millisecond) // Check every 100ms
		}
	}()

	return shutdownSignal
}

// ===================== Graceful Task Shutdown Helpers =====================

// GracefulTaskShutdown helps implement graceful shutdown for long-running tasks
type GracefulTaskShutdown struct {
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc
	taskManager    *TaskManager
	activeTasks    sync.WaitGroup
	logger         *logrus.Entry
}

// NewGracefulTaskShutdown creates a new graceful task shutdown manager
func (tm *TaskManager) NewGracefulTaskShutdown(parentCtx context.Context) *GracefulTaskShutdown {
	shutdownCtx, shutdownCancel := context.WithCancel(parentCtx)

	gts := &GracefulTaskShutdown{
		shutdownCtx:    shutdownCtx,
		shutdownCancel: shutdownCancel,
		taskManager:    tm,
		logger:         tm.logger.WithField("component", "graceful-shutdown"),
	}

	// Start monitoring for shutdown signals
	go func() {
		<-tm.WaitForShutdownSignal()
		gts.logger.Info("Shutdown signal received - stopping new tasks")
		shutdownCancel()
	}()

	return gts
}

// StartTask starts a task with automatic shutdown handling
func (gts *GracefulTaskShutdown) StartTask(taskFunc func(context.Context) error) error {
	select {
	case <-gts.shutdownCtx.Done():
		return fmt.Errorf("cannot start task - shutdown in progress")
	default:
	}

	gts.activeTasks.Add(1)
	go func() {
		defer gts.activeTasks.Done()
		if err := taskFunc(gts.shutdownCtx); err != nil && err != context.Canceled {
			gts.logger.WithError(err).Error("Task execution failed")
		}
	}()

	return nil
}

// Shutdown gracefully shuts down all tasks within the given timeout
func (gts *GracefulTaskShutdown) Shutdown(timeout time.Duration) error {
	gts.logger.Info("Starting graceful task shutdown")

	// Cancel all tasks
	gts.shutdownCancel()

	// Wait for tasks to complete with timeout
	done := make(chan struct{})
	go func() {
		defer close(done)
		gts.activeTasks.Wait()
	}()

	select {
	case <-done:
		gts.logger.Info("All tasks completed gracefully")
		return nil
	case <-time.After(timeout):
		gts.logger.Warn("Task shutdown timed out - some tasks may still be running")
		return fmt.Errorf("graceful task shutdown timed out after %v", timeout)
	}
}

// ======================= HTTP Server Helpers =======================

// HTTPServer interface represents the minimal interface needed for graceful HTTP server shutdown
type HTTPServer interface {
	Shutdown(ctx context.Context) error
}

// RegisterHTTPServerShutdown registers an HTTP server for graceful shutdown
// This helper consolidates the common pattern of HTTP server shutdown across FlightCtl services
func (sm *ShutdownManager) RegisterHTTPServerShutdown(name string, server HTTPServer, priority int, timeout time.Duration) {
	sm.Register(name, priority, timeout, func(ctx context.Context) error {
		sm.log.WithField("server", name).Info("Initiating HTTP server graceful shutdown")
		return server.Shutdown(ctx)
	})
}

// ======================= Signal Setup Helpers =======================

// SetupGracefulShutdownContext creates a context that cancels on standard shutdown signals
// This consolidates the common signal handling pattern across FlightCtl services
func SetupGracefulShutdownContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
}

// ======================= CLI Client Utilities =======================

// ServiceEndpoints maps service names to their default HTTP endpoints
var ServiceEndpoints = map[string]string{
	"api":      "http://localhost:7443",
	"worker":   "http://localhost:7444",
	"periodic": "http://localhost:7445",
}

// GetServiceEndpoint returns the default HTTP endpoint for a service
func GetServiceEndpoint(serviceName string) string {
	if endpoint, ok := ServiceEndpoints[serviceName]; ok {
		return endpoint
	}
	return ServiceEndpoints["api"] // fallback to API service
}

// ValidSignals contains the list of valid signal names for shutdown
var ValidSignals = []string{"TERM", "INT", "QUIT", "USR1", "USR2"}

// IsValidSignal checks if the given signal name is valid for shutdown operations
func IsValidSignal(signal string) bool {
	signalUpper := strings.ToUpper(signal)
	for _, valid := range ValidSignals {
		if signalUpper == valid {
			return true
		}
	}
	return false
}
