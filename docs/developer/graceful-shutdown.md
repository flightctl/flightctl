# FlightCtl Graceful Shutdown Principles

This guide outlines the core principles for graceful shutdown in FlightCtl services. Implementation details may evolve, but these principles remain constant.

## Core Principles

### 1. **Zero Data Loss**
- Complete in-flight operations before terminating
- Ensure transaction consistency and proper state persistence

### 2. **Coordinated Shutdown**
- Use context cancellation for service-wide shutdown coordination
- Implement timeout boundaries (30s default) to prevent hanging
- Signal propagation through context chains

### 3. **Resource Cleanup**
- Use `defer` statements for guaranteed cleanup execution
- Clean up in reverse order of initialization
- Never leave dangling connections, files, or goroutines

### 4. **Observable Termination**
- Structured logging for shutdown phases
- Error collection without immediate termination
- Clear distinction between normal and error-driven shutdown

## Implementation Patterns

### Signal Handling
```go
// Standard signal setup - inherits context from parent
ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
defer cancel()
```

**Note:** Exclude `SIGHUP` for services where it should trigger config reload, not shutdown.

### Error Collection
```go
// Collect errors from multiple services without immediate exit
errCh := make(chan error, numberOfServices)

// Services report errors instead of calling log.Fatal
if err := server.Run(ctx); err != nil {
    log.Errorf("Server error: %v", err)
    errCh <- err
}
```

### Context Chain Preservation
```go
// Use parent context for shutdown timeout to respect existing cancellation
shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
defer cancel()
```

**Note:** Use parent `ctx` when you want shutdown to respect existing cancellation, or `context.Background()` when you need a fresh timeout regardless of parent state.

### Clean Error Handling Pattern
```go
func main() {
    ctx, cancel := signal.NotifyContext(ctx, signals...)
    defer cancel()

    if err := runCmd(ctx); err != nil {
        log.Fatalf("service-name: %v", err)
    }
}

func runCmd(ctx context.Context) error {
    // Business logic returns errors instead of calling log.Fatal
    // Single decision point for fatal vs non-fatal errors
    return serverErr
}
```

## Service Categories

### **HTTP Services**
- Use `server.Shutdown(shutdownCtx)` for graceful connection draining
- Examples: API, alertmanager-proxy, userinfo-proxy

### **Multi-Server Services**
- Coordinate multiple servers (HTTP + gRPC + Metrics)
- Use error channels to handle failures from any server
- Example: flightctl-api

### **Background Workers**
- Handle queue processing and periodic tasks
- Ensure proper queue connection cleanup
- Examples: worker, alert-exporter

### **Periodic Services**
- Long-running task schedulers that require extended shutdown timeouts
- Use `TimeoutPeriodicServiceShutdown` (5 minutes) to allow task completion
- Example: flightctl-periodic

## Reference Implementations

Look at these files for current patterns:

- **Periodic service**: `cmd/flightctl-periodic/main.go` (extended timeout handling)
- **HTTP service**: `cmd/flightctl-userinfo-proxy/main.go`
- **Multi-server**: `cmd/flightctl-api/main.go`
- **Worker service**: `cmd/flightctl-worker/main.go`
- **Agent patterns**: `internal/agent/shutdown/`

## Force Shutdown

FlightCtl services support emergency force shutdown for situations where graceful shutdown may be stuck or taking too long:

- **Double-signal pattern**: Send the same shutdown signal (SIGTERM, SIGINT, SIGQUIT) twice within the force shutdown window
- **First signal**: Initiates graceful shutdown with normal timeouts
- **Second signal**: Triggers immediate force shutdown without timeouts (parallel execution using `context.Background()`)
- **Window**: Configurable via `shutdown.TimeoutForceShutdownWindow` (default: 5 seconds)
- **User feedback**: Clear logging instructs users: "Send the same signal again within 5s for immediate force shutdown"

### Force Shutdown Behavior
```go
// Force shutdown executes all components in parallel without timeouts
if err := shutdownManager.ForceShutdown(); err != nil {
    log.WithError(err).Error("Force shutdown failed")
}
```

Use force shutdown sparingly, only in emergency situations where graceful shutdown is insufficient.

## Key Guidelines

- **Logging**: Use `log.Info()` for lifecycle events, `log.Errorf()` for recoverable errors
- **Context**: Always respect context cancellation in long-running operations
- **Timeouts**: 30-second default shutdown timeout aligns with Kubernetes defaults
  - **Exception**: Periodic services use 5-minute timeout (`TimeoutPeriodicServiceShutdown`) to allow task completion
- **Testing**: Verify shutdown behavior with different signals (`SIGTERM`, `SIGINT`, etc.)

These principles ensure consistent, reliable shutdown behavior across all FlightCtl services while allowing implementation flexibility as the codebase evolves.