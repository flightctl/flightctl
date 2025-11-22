package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandleCrash(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic to be re-raised")
	}()

	defer HandleCrash()
	panic("test panic")
}

func TestHandleCrashWithHandler(t *testing.T) {
	require := require.New(t)
	called := false
	handler := func(r interface{}) {
		called = true
		require.Equal("test panic with handler", r, "expected panic message 'test panic with handler'")
	}

	defer func() {
		r := recover()
		require.NotNil(r, "expected panic to be re-raised")
		require.True(called, "expected handler to be called")
	}()

	defer HandleCrash(handler)
	panic("test panic with handler")
}

func TestHandleCrashWithContext(t *testing.T) {
	ctx := context.Background()
	require := require.New(t)
	called := false
	handler := func(ctx context.Context, r interface{}) {
		called = true
		require.NotNil(ctx, "expected context to be non-nil")
		require.Equal("test panic with context", r, "expected panic message 'test panic with context'")
	}

	defer func() {
		r := recover()
		require.NotNil(r, "expected panic to be re-raised")
		require.True(called, "expected handler to be called")
	}()

	defer HandleCrashWithContext(ctx, handler)
	panic("test panic with context")
}

func TestRecoverFromPanic(t *testing.T) {
	require := require.New(t)
	testFunc := func() (err error) {
		defer RecoverFromPanic(&err)
		panic("test panic for recovery")
	}

	err := testFunc()
	require.Error(err, "expected error from recovered panic")
	require.Contains(err.Error(), "test panic for recovery", "expected error message to contain panic value")
	require.Contains(err.Error(), "recovered from panic", "expected error message to contain recovery prefix")
}

func TestMust(t *testing.T) {
	require := require.New(t)
	result := Must("success", nil)
	require.Equal("success", result, "expected 'success'")

	// test panic
	defer func() {
		r := recover()
		require.NotNil(r, "expected Must to panic on error")
		_, ok := r.(*testError)
		require.True(ok, "expected panic value to be testError")
	}()
	Must("", &testError{})
}

type testError struct{}

func (e *testError) Error() string {
	return "test error"
}

func TestHandleCrashNoReallyCrash(t *testing.T) {
	require := require.New(t)
	// Temporarily disable ReallyCrash for testing
	originalReallyCrash := ReallyCrash
	ReallyCrash = false
	defer func() {
		ReallyCrash = originalReallyCrash
	}()

	called := false
	handler := func(r interface{}) {
		called = true
	}

	func() {
		defer HandleCrash(handler)
		panic("test panic no crash")
	}()

	require.True(called, "expected handler to be called")
}
