# Flightctl Agent Contribution Guidelines

## Overview
The agent reconciles `Device.Spec` from control plane, reports resource usage, manages application lifecycles, handles OS-level configuration, and provides lifecycle hooks.

> [!IMPORTANT]
> Before contributing, read the [Agent Architecture](../../docs/user/references/agent-architecture.md) documentation for design principles and secure device lifecycle context.

## Core Principles

### Minimal Binary Profile
**"A little copying is better than a little dependency"**
- Strongly vet new dependencies - verify existing code first
- Serial operations preferred over parallel (resource conservation > speed)

### PR Requirements
- Concise and minimal changes
- Demonstrate understanding of agent internals

## Agent Architecture

### Key Components
- **Bootstrap**: Initialization phase before reconciliation - only used in agent.go setup
- **Publisher**: Async long-polling for specs (4min timeout, 5s min delay between polls)
- **Engine**: Checks priority queue for new specs every 1s, pushes status on interval
- **Priority Queue**: Version reconciliation with cache/policy dependencies
- **FileIO Module**: ALL disk operations (enables testing/simulation)
- **Managers**: `agent/device/<namespace>` mirrors spec keys (e.g., applications/, config/, os/)
- **Prefetch Manager**: Pulls images serially to conserve resources

### Failure Handling
- Non-retryable errors during spec reconciliation trigger rollback
- Error declarations and helpers in `agent/device/errors` package

#### Structured Error Messages with `errors.WithElement()`

Use `errors.WithElement()` to embed element identifiers (app names, file paths, volume names, service names) into error chains. This allows `FormatError()` to extract and display these identifiers in user-facing status messages without parsing error strings.

**Why use it:**
- No string parsing - `errors.As` walks the chain to find the element
- Idiomatic Go - standard error wrapping with `%w`
- Survives all wrapping - `fmt.Errorf`, `errors.Join`, custom wrappers
- Clean logs/JSON - no invisible characters or special delimiters

**Usage pattern:**
```go
// Embed element identifier in error chain
fmt.Errorf("creating volume %w: %w", errors.WithElement(volumeName), err)
fmt.Errorf("%w %w: %w", errors.ErrParsingComposeSpec, errors.WithElement(appName), err)

// Extract element (walks the error chain)
element := errors.GetElement(err) // returns "" if not found
```

**When to use:**
- App names in application lifecycle errors
- File/directory paths in I/O errors
- Volume names in storage operations
- Service names in systemd errors
- Any identifier useful for troubleshooting in status messages

The `StructuredError.Element` field is automatically populated by `FormatError()` using `GetElement()`, making it available in device status messages sent to the control plane.

### Threading & Lifecycle
- Primarily single-threaded (async requires strong justification)
- **Graceful Termination** (`agent/shutdown`): Clean shutdown with in-flight completion
- **Configuration Reload** (`agent/reload`): Runtime updates without restart

## Testing Standards
- Use `testify/require`: `require := require.New(t)`
- Table-driven tests with inline `setupMocks`
- gomock with `defer ctrl.Finish()`

```go
testCases := []struct {
    name          string
    setupMocks    func(*MockA, *MockB)
    expectedError error
}{
    {
        name: "success case",
        setupMocks: func(mockA *MockA, mockB *MockB) {
            mockA.EXPECT().Method(arg).Return(result, nil)
        },
    },
}
```

## Extending Functionality
- Use functional options pattern: `func New(opts ...Option)`
- **One way to do things** - no duplicate functionality
- Integrate with existing patterns

## Code Review Checklist
- [ ] No unnecessary dependencies
- [ ] Uses fileio for ALL disk operations
- [ ] Spec access via spec manager only
- [ ] No unwarranted async code
- [ ] PR is minimal and focused
- [ ] One way to do things