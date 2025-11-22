package runtime

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
)

var (
	// ReallyCrash controls whether to panic after logging a crash.
	// Useful for testing to prevent actual crashes.
	ReallyCrash = true

	// PanicHandlers is a list of functions to call when a panic occurs.
	// This can be used to perform cleanup or logging before crashing.
	PanicHandlers = []func(context.Context, interface{}){logPanic}

	// PanicHandlersMutex protects PanicHandlers from concurrent modification.
	PanicHandlersMutex = &sync.RWMutex{}
)

// HandleCrash recovers from panics and executes additional handlers.
// It should be used with defer to catch panics in goroutines.
func HandleCrash(additionalHandlers ...func(interface{})) {
	if r := recover(); r != nil {
		// convert old-style handlers to context-aware handlers
		additionalHandlersWithContext := make([]func(context.Context, interface{}), len(additionalHandlers))
		for i, handler := range additionalHandlers {
			additionalHandlersWithContext[i] = func(_ context.Context, r interface{}) {
				handler(r)
			}
		}

		handleCrash(context.Background(), r, additionalHandlersWithContext...)
	}
}

// HandleCrashWithContext recovers from panics and executes additional handlers with context.
// It should be used with defer to catch panics in goroutines.
func HandleCrashWithContext(ctx context.Context, additionalHandlers ...func(context.Context, interface{})) {
	if r := recover(); r != nil {
		handleCrash(ctx, r, additionalHandlers...)
	}
}

// handleCrash is the actual implementation that handles the panic.
func handleCrash(ctx context.Context, r interface{}, additionalHandlers ...func(context.Context, interface{})) {
	// copy global panic handlers while holding read lock
	PanicHandlersMutex.RLock()
	handlers := make([]func(context.Context, interface{}), len(PanicHandlers))
	copy(handlers, PanicHandlers)
	PanicHandlersMutex.RUnlock()

	// execute handlers without holding the lock to avoid deadlock
	for _, handler := range handlers {
		handler(ctx, r)
	}

	for _, handler := range additionalHandlers {
		handler(ctx, r)
	}

	if ReallyCrash {
		// re-panic to crash the program
		panic(r)
	}
}

// logPanic is the default panic handler that logs the panic and stack trace.
func logPanic(ctx context.Context, r interface{}) {
	callers := getCallers()

	fmt.Fprintf(os.Stderr, "Observed a panic: %v\n%s\n", r, callers)
}

// getCallers returns a formatted stack trace.
func getCallers() string {
	const size = 64 << 10 // 64 KB
	buf := make([]byte, size)
	n := runtime.Stack(buf, false)
	return string(buf[:n])
}

// RecoverFromPanic replaces the specified error with a panic message if a panic occurs.
// It should be used with defer to catch panics and convert them to errors.
func RecoverFromPanic(err *error) {
	if r := recover(); r != nil {
		callers := getCallers()
		*err = fmt.Errorf("recovered from panic: %v\n%s", r, callers)
	}
}

// Must panics if the error is not nil.
// It's useful for initialization code where errors should cause immediate failure.
func Must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
