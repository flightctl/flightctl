package util

import (
	"context"
	"os"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel/codes"
)

// TestIDKey is the context key used to store the test ID
type TestIDKeyType struct{}

var TestIDKey = TestIDKeyType{}

// Context keys for storing test data
type DeviceIDKeyType struct{}
type DeviceKeyType struct{}
type TestContextKeyType struct{}

var (
	DeviceIDKey    = DeviceIDKeyType{}
	DeviceKey      = DeviceKeyType{}
	TestContextKey = TestContextKeyType{}
)

// generateUniqueTestID generates a unique test identifier using UUID
func generateUniqueTestID() string {
	return "test-" + uuid.New().String()
}

func InitTracerForTests() func(context.Context) error {
	var opts []config.ConfigOption
	if value := os.Getenv("TRACE_TESTS"); value == "true" {
		opts = append(opts, config.WithTracingEnabled())
	}

	return instrumentation.InitTracer(
		flightlog.InitLogs(),
		config.NewDefault(opts...),
		"flightctl-tests",
	)
}

func InitSuiteTracerForGinkgo(description string) context.Context {
	var opts []config.ConfigOption
	if value := os.Getenv("TRACE_TESTS"); value == "true" {
		opts = append(opts, config.WithTracingEnabled())
	}

	s := instrumentation.InitTracer(
		flightlog.InitLogs(),
		config.NewDefault(opts...),
		"flightctl-tests",
	)

	DeferCleanup(func() {
		Expect(s(context.Background())).To(Succeed(), "error shutting down tracer provider")
	})

	suiteCtx, suiteSpan := tracing.StartSpan(context.Background(), "flightctl/tests", description)
	DeferCleanup(func() {
		suiteSpan.End()
	})
	return suiteCtx
}

func StartSpecTracerForGinkgo(parent context.Context) context.Context {
	ctx, span := tracing.StartSpan(parent, "flightctl/tests", CurrentSpecReport().FullText())

	// Check if parent context already has a test ID (this should not happen)
	if existingID, ok := parent.Value(TestIDKey).(string); ok && existingID != "" {
		GinkgoWriter.Printf("WARNING: Parent context already has test ID: %s\n", existingID)
	}

	// Generate a unique test ID for this specific test and embed it in the context
	testID := generateUniqueTestID()
	GinkgoWriter.Printf("Generated new test ID: %s for test: %s\n", testID, CurrentSpecReport().FullText())
	ctx = context.WithValue(ctx, TestIDKey, testID)

	// Verify the test ID was actually set in the new context
	if verifyID, ok := ctx.Value(TestIDKey).(string); ok {
		GinkgoWriter.Printf("Verified test ID in new context: %s\n", verifyID)
	} else {
		GinkgoWriter.Printf("ERROR: Failed to set test ID in context!\n")
	}

	DeferCleanup(func() {
		if CurrentSpecReport().Failed() {
			span.SetStatus(codes.Error, CurrentSpecReport().Failure.Message)
		} else {
			span.SetStatus(codes.Ok, "test passed")
		}
		span.End()
	})
	return ctx
}
