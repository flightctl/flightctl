package util

import (
	"context"
	"os"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel/codes"
)

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
