# Service Observability

This guide explains how to instrument Flightctl services with OpenTelemetry tracing. It covers span creation, context propagation, and best practices for integrating tracing into service logic.

---

## What Is Tracing?

Tracing provides visibility into how requests flow through a distributed system. In OpenTelemetry, a trace represents a single request's path and is made up of multiple spans.

### What is a Span?

A span is a single unit of work or operation within a trace. It includes:

- A name (e.g., `create-device`)
- Duration
- Status (OK/Error)
- Attributes (metadata like `request.id`, SQL table, etc.)
- Parent/child relationships to form the full request flow

### What is a Tracer?

A tracer is the component responsible for creating spans. Each service or component can use a tracer (usually named) to start spans consistently. All spans are created using the global tracer provider.

---

## Tracer Initialization

All services should call `tracing.InitTracer()` during startup to configure the global OpenTelemetry tracer:

```go
import "github.com/flightctl/flightctl/internal/instrumentation/tracing"

tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-api")
defer func() {
    if err := tracerShutdown(ctx); err != nil {
        log.WithError(err).Error("Failed to shutdown tracer")
    }
}()
```

This function:

- Initializes an OTLP HTTP exporter using values from `config.yaml`.
- Configures the global tracer provider (`otel.SetTracerProvider`).
- Sets `TraceContext` propagation.
- Falls back to a no-op provider if tracing is disabled.

> [!NOTE]
> The `serviceName` argument can distinguish spans across components (e.g., `flightctl-api`, `flightctl-worker`).

Be sure to call the shutdown function on app exit:

```go
shutdown(ctx)
```

This flushes any remaining spans before exit.

---

## Creating Spans

Use the `StartSpan` helper to begin a new span:

```go
ctx, span := StartSpan(ctx, "flightctl/service", "CreateDevice")
defer span.End()
```

This:

- Uses the global tracer
- Normalizes the span name to kebab-case (e.g., `create-device`)
- Maintains parent-child relationships via the context

You can pass optional `trace.SpanStartOption` values such as `WithAttributes` or `WithLinks` to enrich the span with metadata or associate it with another context (e.g., for async task correlation):

```go
receivedCtx, handlerSpan := tracing.StartSpan(
  receivedCtx, "flightctl/queues", r.name, trace.WithLinks(
    trace.LinkFromContext(ctx, attribute.String("request.id", requestID))))
```

---

## Instrumenting Services (Example)

The following is an example of how Flightctl uses a `TracedService` wrapper to consistently trace service logic. Each method is wrapped with standardized span handling:

```go
func startSpan(ctx context.Context, method string) (context.Context, trace.Span) {
	ctx, span := tracing.StartSpan(ctx, "flightctl/service", method)
	return ctx, span
}

func endSpan(span trace.Span, st api.Status) {
	span.SetAttributes(attribute.Int("status.code", int(st.Code)))

	if st != api.StatusOK() {
		span.RecordError(errors.New(st.Message))
		span.SetStatus(codes.Error, st.Message)
	}

	span.End()
}

// --- Example usage ---
func (t *TracedService) ListCertificateSigningRequests(ctx context.Context, p api.ListCertificateSigningRequestsParams) (*api.CertificateSigningRequestList, api.Status) {
	ctx, span := startSpan(ctx, "ListCertificateSigningRequests")
	resp, st := t.inner.ListCertificateSigningRequests(ctx, p)
	endSpan(span, st)
	return resp, st
}
```

### Why this pattern is helpful

- Reduces boilerplate in service logic
- Standardizes status and error reporting
- Keeps all span logic consistent and easy to audit

---

## Graceful Shutdown Integration

All FlightCtl services implement standardized graceful shutdown that works seamlessly with OpenTelemetry tracing. The shutdown process ensures spans are properly flushed before service termination.

### Standard Shutdown Pattern

All services follow this pattern for coordinated shutdown:

```go
func main() {
    startTime := time.Now()
    ctx := context.Background()

    log := log.InitLogs()
    log.Info("Starting service")
    defer func() {
        log.WithField("uptime", time.Since(startTime)).Info("Service stopped")
    }()

    // Initialize tracer with shutdown function
    tracerShutdown := tracing.InitTracer(log, cfg, "service-name")
    defer func() {
        if err := tracerShutdown(ctx); err != nil {
            log.WithError(err).Error("Failed to shutdown tracer")
        }
    }()

    // Set up signal handling for graceful shutdown
    ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
    defer cancel()

    // Start services in goroutines...

    // Wait for shutdown signal or error
    select {
    case <-ctx.Done():
        log.Info("Shutdown signal received, initiating graceful shutdown")
    case err := <-serverErrors:
        log.Errorf("Server error received: %v", err)
        cancel()
    }

    // Coordinated shutdown with timeout
    const shutdownTimeout = 30 * time.Second
    log.Infof("Starting coordinated shutdown (timeout: %v)", shutdownTimeout)

    shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
    defer shutdownCancel()

    // Shutdown servers and resources in order
    if err := server.Shutdown(shutdownCtx); err != nil {
        log.Errorf("Server shutdown error: %v", err)
    }

    // Stop queue providers
    provider.Stop()
    provider.Wait()

    // Close database connections
    store.Close()

    // Close other resources
    kvStore.Close()

    // Tracer shutdown is handled by defer earlier, ensuring it completes
    // within timeout. The defer ensures spans are flushed even if shutdown
    // logic encounters errors.

    log.Info("Graceful shutdown completed")
}
```

### Shutdown Signal Handling

**Supported Signals:**
- `SIGINT` (Ctrl+C) - Interactive shutdown
- `SIGTERM` - Graceful termination (default for `kubectl delete pod`)
- `SIGQUIT` - Quit with core dump

**Note:** `SIGHUP` is reserved for configuration reloading and is not handled by the graceful shutdown system.

**Signal Processing:**
- Uses `signal.NotifyContext()` for cross-platform compatibility
- 30-second timeout for graceful shutdown operations
- Tracer shutdown happens automatically via defer before timeout

### Shutdown Observability

**Structured Logging:**

```go
// Service startup
log.WithField("version", version).Info("Starting service")

// Shutdown initiated
log.Info("Shutdown signal received, initiating graceful shutdown")

// Shutdown completed
log.WithField("uptime", time.Since(startTime)).Info("Service stopped")
```

**Tracing Integration:**
- Active spans are completed before shutdown
- Tracer flush occurs during shutdown sequence
- No spans are lost during graceful termination

### Error Handling During Shutdown

Services handle shutdown errors gracefully:

```go
// Example: Coordinated server shutdown
go func() {
    if err := server.Run(ctx); err != nil {
        log.Errorf("Server error: %v", err)
        serverErrors <- err  // Signal main thread, don't exit immediately
    }
}()

// Main thread coordinates shutdown
select {
case <-ctx.Done():
    // Signal-based shutdown
case err := <-serverErrors:
    // Error-based shutdown - still allows tracer cleanup
    log.Errorf("Initiating shutdown due to error: %v", err)
}
```

This ensures that even during error conditions, OpenTelemetry spans are properly flushed and observability data is preserved.

