# Shutdown Package

The `pkg/shutdown` package provides a unified shutdown coordination pattern for all FlightCtl services. It replaces the manual error-prone shutdown logic with a builder-pattern manager that handles multi-server coordination, cleanup ordering, and deadlock prevention.

## Features

- **Builder Pattern**: Fluent API for configuring servers and cleanup functions
- **Multi-Server Coordination**: Uses `golang.org/x/sync/errgroup` for proper error handling
- **Cleanup Ordering**: LIFO (last-in-first-out) cleanup execution
- **Cleanup Logging**: Detailed logging for each cleanup operation
- **Database Cleanup**: Specialized adapter with proper "Closing database connections" logging
- **Shutdown Timeout**: Optional timeout to prevent hanging shutdowns
- **Deadlock Prevention**: Optional force-stop function for unblocking servers
- **Error Wrapping**: Server identification in error messages
- **Signal Handling**: Built-in OS signal management
- **Resource Safety**: Context cancellation and proper resource cleanup

## Usage

### Basic Example

```go
import "github.com/flightctl/flightctl/pkg/shutdown"

func main() {
    log := logrus.New()

    // Create shutdown manager with timeout and custom signals
    shutdownMgr := shutdown.NewManager(log).
        WithSignals(syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT).
        WithTimeout(30*time.Second)  // Optional: prevent hanging shutdowns

    // Add servers and cleanup functions
    shutdownMgr.
        AddServer("api", apiServer).
        AddServer("metrics", metricsServer).
        AddCleanup("database", shutdown.DatabaseCloseFunc(log, db.Close)).  // Includes logging
        AddCleanup("provider", shutdown.StopWaitFunc("provider", provider.Stop, provider.Wait)).
        WithForceStop(provider.Stop)  // Prevent deadlocks

    // Run with coordinated shutdown
    if err := shutdownMgr.Run(context.Background()); err != nil {
        log.Fatalf("Service error: %v", err)
    }
}
```

### Migration from Manual Pattern

**Before (Manual Pattern):**
```go
func runCmd(log *logrus.Logger) error {
    ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)

    var cleanupFuncs []func() error
    defer func() {
        cancel()
        for i := len(cleanupFuncs) - 1; i >= 0; i-- {
            if err := cleanupFuncs[i](); err != nil {
                log.WithError(err).Error("Cleanup error")
            }
        }
    }()

    // Setup resources...
    cleanupFuncs = append(cleanupFuncs, dbCleanup)

    // Start servers manually...
    errCh := make(chan error, 2)
    go func() {
        if err := server1.Run(ctx); err != nil {
            errCh <- fmt.Errorf("server1: %w", err)
        }
    }()

    // Wait for errors...
    for i := 0; i < serversStarted; i++ {
        if err := <-errCh; err != nil {
            // Complex error handling...
        }
    }
}
```

**After (Shutdown Package):**
```go
import "github.com/flightctl/flightctl/pkg/shutdown"

func runCmd(log *logrus.Logger) error {
    shutdownMgr := shutdown.NewManager(log)

    // Setup resources and add to manager...
    shutdownMgr.AddCleanup("database", shutdown.DatabaseCloseFunc(log, db.Close))

    // Add servers...
    shutdownMgr.AddServer("server1", server1)

    // Run with coordinated shutdown
    return shutdownMgr.Run(context.Background())
}
```

## Server Interface

Any type implementing the `Server` interface can be managed:

```go
type Server interface {
    Run(context.Context) error
}
```

### Adapters

The package provides adapters for common patterns:

```go
// Function adapter
serverFunc := shutdown.NewServerFunc(func(ctx context.Context) error {
    return httpServer.ListenAndServe()
})

// Metrics server adapter
metricsServer := shutdown.MetricsServerFunc(func(ctx context.Context) error {
    return tracing.RunMetricsServer(ctx, log, cfg.Metrics.Address, collectors...)
})
```

## Cleanup Functions

Cleanup functions are executed in reverse order (LIFO):

```go
// Database cleanup (with proper logging)
shutdownMgr.AddCleanup("database", shutdown.DatabaseCloseFunc(log, store.Close))

// Generic database cleanup (without logging)
shutdownMgr.AddCleanup("database", shutdown.CloseErrFunc(store.Close))

// KV store cleanup (void method)
shutdownMgr.AddCleanup("kv-store", shutdown.CloseFunc(kvStore.Close))

// Provider cleanup (stop + wait pattern)
shutdownMgr.AddCleanup("provider", shutdown.StopWaitFunc("provider", provider.Stop, provider.Wait))

// Tracer cleanup (with timeout)
shutdownMgr.AddCleanup("tracer", func() error {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    return tracerShutdown(ctx)
})
```

## Configuration Options

```go
shutdownMgr := shutdown.NewManager(log).
    WithSignals(syscall.SIGTERM, syscall.SIGINT).           // Custom signals
    WithTimeout(30*time.Second).                            // Shutdown timeout
    WithForceStop(provider.Stop).                           // Deadlock prevention
    AddServer("api", apiServer).
    AddServer("metrics", metricsServer).
    AddCleanup("database", shutdown.DatabaseCloseFunc(log, db.Close))
```

## Error Handling

- **Server Errors**: Wrapped with server name for identification
- **Context Cancellation**: Treated as normal shutdown (returns nil)
- **Force Stop**: Called on first error to prevent deadlock scenarios
- **Cleanup Errors**: Logged but don't stop other cleanup functions
- **Timeout Handling**: If `WithTimeout()` is set, the entire shutdown process times out

## Timeout Configuration

The shutdown manager supports optional timeouts to prevent hanging shutdowns:

```go
// Set 30-second timeout for entire shutdown process
shutdownMgr := shutdown.NewManager(log).
    WithTimeout(30*time.Second)
```

**Timeout Behavior:**
- Timer starts when shutdown begins (signal received or server error)
- Applies to entire shutdown: server stops + cleanup functions
- If timeout exceeded, context cancellation will force termination
- Timeout logging: `"Shutdown timeout set to 30s"` message on startup

## Migration Benefits

1. **Consistency**: All services use the same shutdown pattern
2. **Drift Prevention**: Builder pattern prevents manual error-prone coordination
3. **Error Safety**: Proper error wrapping and deadlock prevention
4. **Maintainability**: Centralized shutdown logic with comprehensive tests
5. **Resource Safety**: Guaranteed cleanup execution in correct order

## Testing

The package includes comprehensive tests covering:
- Builder pattern functionality
- Multi-server coordination
- Error scenarios and wrapping
- Cleanup ordering (LIFO)
- Context cancellation behavior
- Adapter functions
- Force stop functionality

Run tests with:
```bash
go test -v ./pkg/shutdown
```