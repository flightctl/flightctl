# FlightCtl Graceful Shutdown Framework

## Overview

The FlightCtl graceful shutdown framework provides coordinated, priority-based component shutdown with proper resource cleanup and optional fail-fast behavior. It ensures all services shut down gracefully when receiving termination signals while maintaining data integrity and preventing resource leaks.

## Core Concepts

### Priority-Based Shutdown
Components are shut down in priority order (0-5):
- **Priority 0**: Servers (stop accepting new requests first)
- **Priority 1**: Special services (e.g., periodic tasks)
- **Priority 2-3**: Caches and middleware
- **Priority 4**: Core resources (databases, queues, KV stores)
- **Priority 5**: Cleanup and completion callbacks

### Fail-Fast Behavior
When enabled, component failures trigger immediate coordinated shutdown to maintain original service restart semantics while ensuring proper cleanup.

### Signal Handling
Handles `SIGTERM`, `SIGINT`, and `SIGQUIT` with configurable global timeout and coordinated shutdown execution.

## Usage

### Basic Setup

```go
package main

import (
    "context"
    "time"

    "github.com/flightctl/flightctl/pkg/log"
    "github.com/flightctl/flightctl/pkg/shutdown"
)

func main() {
    log := log.InitLogs()

    // Create shutdown manager
    shutdownManager := shutdown.NewShutdownManager(log)

    // Optional: Enable fail-fast behavior
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    shutdownManager.EnableFailFast(cancel)

    // Setup signal handling with 30-second timeout
    shutdown.HandleSignalsWithManager(log, shutdownManager, 30*time.Second)

    // Register components and run service...
    if err := runService(shutdownManager, ctx, log); err != nil {
        log.Fatalf("Service error: %v", err)
    }
}
```

### Component Registration

Register components with priority, timeout, and cleanup callback:

```go
// High priority: Stop servers first (priority 0)
shutdownManager.Register("servers", 0, 30*time.Second, func(ctx context.Context) error {
    log.Info("Gracefully stopping HTTP/gRPC servers")
    serverCancel() // Cancel server contexts
    return nil
})

// Medium priority: Stop caches (priority 3)
shutdownManager.Register("org-cache", 3, 5*time.Second, func(ctx context.Context) error {
    log.Info("Stopping organization cache")
    orgCache.Stop()
    return nil
})

// Low priority: Close databases (priority 4)
shutdownManager.Register("database", 4, 10*time.Second, func(ctx context.Context) error {
    log.Info("Closing database connections")
    store.Close()
    return nil
})
```

### Fail-Fast Integration

```go
// Start server with fail-fast support
go func() {
    if err := server.Run(ctx); err != nil {
        log.Errorf("Server error: %v", err)
        shutdownManager.TriggerFailFast("server", err)
    }
}()
```

### Service Implementation Examples

#### API Service Pattern
```go
func runAPIService(shutdownManager *shutdown.ShutdownManager, ctx context.Context, log *logrus.Logger) error {
    // Initialize resources...

    // Register shutdown components in priority order
    shutdownManager.Register("servers", 0, 30*time.Second, stopServers)
    shutdownManager.Register("org-cache", 3, 5*time.Second, stopOrgCache)
    shutdownManager.Register("membership-cache", 3, 5*time.Second, stopMembershipCache)
    shutdownManager.Register("database", 4, 10*time.Second, closeDatabase)
    shutdownManager.Register("kvstore", 4, 5*time.Second, closeKVStore)
    shutdownManager.Register("queues", 4, 10*time.Second, stopQueues)
    shutdownManager.Register("tracer", 4, 5*time.Second, shutdownTracer)

    // Start services...

    // Wait for shutdown completion
    <-done
    return nil
}
```

#### Periodic Service Pattern
```go
// Special handling for periodic services that need extended shutdown time
shutdownManager.Register("periodic-server", 1, 45*time.Second, func(ctx context.Context) error {
    log.Info("Initiating periodic server shutdown - waiting for tasks to complete")
    serverCancel()

    // Wait for periodic tasks to complete naturally
    select {
    case <-ctx.Done():
        if ctx.Err() == context.DeadlineExceeded {
            log.Warn("Periodic server shutdown timeout exceeded")
        }
        return ctx.Err()
    default:
        time.Sleep(2 * time.Second) // Brief wait for completion
        return nil
    }
})
```

## Testing

The framework includes comprehensive test coverage:

### Unit Tests
```bash
# Core functionality tests
go test -short -v ./pkg/shutdown

# Specific test patterns
go test -v ./pkg/shutdown -run TestShutdownManager_Priority
```

### Integration Tests
```bash
# API service shutdown coordination
go test -short -v ./cmd/flightctl-api

# Periodic service extended timeouts
go test -short -v ./cmd/flightctl-periodic
```

### Load Tests
```bash
# Performance testing with many components
go test -v ./pkg/shutdown -run LoadTest

# Benchmarks
go test -run=^$ -bench=. -benchmem ./pkg/shutdown
```

### Coverage Analysis
```bash
# Generate coverage report
go test -coverprofile=coverage.out ./pkg/shutdown
go tool cover -html=coverage.out
```

## Test Structure

### Unit Tests (`pkg/shutdown/*_test.go`)

#### Core Functionality (`shutdown_test.go`)
- Priority-based shutdown ordering
- Component timeout handling
- Error collection and propagation
- Fail-fast behavior validation
- Concurrent operation safety
- Edge case handling

#### Signal Handling (`signal_test.go`)
- OS signal reception (SIGTERM, SIGINT, SIGQUIT)
- Global shutdown timeout enforcement
- Signal handler integration
- Multiple signal handling

#### Load Testing (`load_test.go`)
- Performance with many components (50+)
- Concurrent shutdown managers
- Memory leak detection
- High frequency shutdowns
- Stress testing with failures
- Timeout behavior under load

### Integration Tests (`cmd/*/main_test.go`)

#### API Service Tests
- End-to-end shutdown coordination
- Multi-component priority ordering
- Fail-fast behavior integration
- Component timeout validation

#### Periodic Service Tests
- Extended timeout handling (45 seconds)
- Task completion waiting
- Priority coordination
- Graceful vs. forced shutdown

## Implementation Details

### ShutdownManager Structure
```go
type ShutdownManager struct {
    components     []Component
    log            *logrus.Logger
    mu             sync.RWMutex
    failFastEnabled bool
    failFastCancel  context.CancelFunc
}

type Component struct {
    Name     string
    Priority int             // Lower number = higher priority
    Timeout  time.Duration   // Component shutdown timeout
    Callback ShutdownCallback
}
```

### Key Methods
- `Register(name, priority, timeout, callback)` - Add component for shutdown
- `EnableFailFast(cancelFunc)` - Enable fail-fast behavior
- `TriggerFailFast(component, error)` - Trigger immediate shutdown
- `Shutdown(ctx)` - Execute coordinated shutdown

### Signal Handling
- `HandleSignalsWithManager(logger, manager, timeout)` - Setup signal handling with shutdown manager
- `HandleSignals(logger, cancelFunc, timeout)` - Basic signal handling with context cancellation

## Best Practices

### Component Registration
1. **Register in reverse shutdown order** - Last initialized, first shutdown
2. **Use appropriate priorities** - Servers first, resources last
3. **Set realistic timeouts** - Allow enough time for graceful cleanup
4. **Handle context cancellation** - Respect timeout signals

### Error Handling
1. **Continue on component failures** - Don't let one failure stop others
2. **Log component errors** - Provide visibility into shutdown issues
3. **Use fail-fast judiciously** - Only for critical component failures
4. **Return meaningful errors** - Help with debugging

### Testing
1. **Test all shutdown paths** - Success, failure, and timeout scenarios
2. **Validate priority ordering** - Ensure components shut down in correct order
3. **Test under load** - Verify performance with many components
4. **Check for resource leaks** - Memory and connection cleanup

### Performance Considerations
1. **Concurrent shutdown within priority levels** - Components with same priority shut down in parallel
2. **Reasonable timeouts** - Balance thoroughness with shutdown speed
3. **Efficient cleanup** - Avoid unnecessary work in shutdown callbacks
4. **Resource cleanup** - Close connections, stop goroutines, release memory

## Migration from Legacy Shutdown

### Before (Manual Signal Handling)
```go
ctx, cancel := context.WithCancel(context.Background())
shutdown.HandleSignals(log, cancel, timeout)

// Manual resource cleanup in defer statements
defer store.Close()
defer provider.Stop()
```

### After (Coordinated Shutdown)
```go
shutdownManager := shutdown.NewShutdownManager(log)
shutdown.HandleSignalsWithManager(log, shutdownManager, timeout)

shutdownManager.Register("database", 4, 10*time.Second, func(ctx context.Context) error {
    store.Close()
    return nil
})

shutdownManager.Register("queues", 4, 10*time.Second, func(ctx context.Context) error {
    provider.Stop()
    provider.Wait()
    return nil
})
```

## Common Patterns

### Server Shutdown
```go
serverCtx, serverCancel := context.WithCancel(ctx)

shutdownManager.Register("servers", 0, 30*time.Second, func(ctx context.Context) error {
    log.Info("Gracefully stopping servers")
    serverCancel() // This stops all servers
    return nil
})
```

### Resource Cleanup
```go
shutdownManager.Register("connections", 4, 10*time.Second, func(ctx context.Context) error {
    log.Info("Closing connections")
    if err := db.Close(); err != nil {
        return fmt.Errorf("failed to close database: %w", err)
    }
    kvStore.Close()
    return nil
})
```

### Completion Callback
```go
done := make(chan struct{})

shutdownManager.Register("completion", 5, 1*time.Second, func(ctx context.Context) error {
    close(done)
    return nil
})

// Wait for shutdown to complete
<-done
log.Info("All components shut down successfully")
```