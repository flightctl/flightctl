# Flightctl Agent Contribution Guidelines

## Overview
The agent reconciles `Device.Spec` from control plane, reports resource usage, manages application lifecycles, handles OS-level configuration, and provides lifecycle hooks.

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