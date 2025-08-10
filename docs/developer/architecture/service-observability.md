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

All services should call `InitTracer()` during startup to configure the global OpenTelemetry tracer:

```go
shutdown := InitTracer(log, cfg, "flightctl-api")
defer shutdown(ctx)
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

