package celruntime_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"github.com/flightctl/flightctl/internal/celruntime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompileAndEvaluate(t *testing.T) {
	t.Run("When a boolean expression is compiled it should preserve its type and evaluate reported values", func(t *testing.T) {
		engine, err := celruntime.New(cel.Variable("serial", cel.StringType))
		require.NoError(t, err)
		program, err := engine.Compile(`serial.startsWith("WEST-")`)
		require.NoError(t, err)
		require.True(t, program.OutputType().IsExactType(cel.BoolType))
		value, err := program.Evaluate(map[string]any{"serial": "WEST-001"})
		require.NoError(t, err)
		require.Equal(t, types.True, value)
	})
}

func TestEvaluationValues(t *testing.T) {
	tests := []struct {
		name          string
		expression    string
		want          ref.Val
		errorContains string
	}{
		{name: "When a boolean is false it should preserve false as a value", expression: `false`, want: types.False},
		{name: "When a result is an integer it should preserve the numeric type", expression: `42`, want: types.Int(42)},
		{name: "When a string contains spaces it should preserve the original value", expression: `"east coast"`, want: types.String("east coast")},
		{name: "When a result is null it should preserve null", expression: `null`, want: types.NullValue},
		{name: "When an optional is empty it should preserve the empty optional", expression: `optional.none()`, want: types.OptionalNone},
		{name: "When an optional has a value it should preserve the optional wrapper", expression: `optional.of("east coast")`, want: types.OptionalOf(types.String("east coast"))},
		{name: "When a key is missing it should return an evaluation error", expression: `reported.absent`, errorContains: "no such key"},
		{name: "When evaluation divides by zero it should return an evaluation error", expression: `1 / 0`, errorContains: "division by zero"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine, err := celruntime.New(cel.Variable("reported", cel.DynType), cel.OptionalTypes())
			require.NoError(t, err)
			program, err := engine.Compile(tt.expression)
			require.NoError(t, err)
			value, err := program.Evaluate(map[string]any{"reported": map[string]any{}})
			if tt.errorContains != "" {
				require.ErrorContains(t, err, "evaluating CEL expression")
				require.ErrorContains(t, err, tt.errorContains)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, value)
		})
	}
}

func TestEnvironmentIsolation(t *testing.T) {
	t.Run("When engines declare different types it should compile independently of other caches", func(t *testing.T) {
		stringsEngine, err := celruntime.New(cel.Variable("reported", cel.StringType))
		require.NoError(t, err)
		boolEngine, err := celruntime.New(cel.Variable("reported", cel.BoolType))
		require.NoError(t, err)
		stringProgram, err := stringsEngine.Compile(`reported`)
		require.NoError(t, err)
		boolProgram, err := boolEngine.Compile(`reported`)
		require.NoError(t, err)
		require.True(t, stringProgram.OutputType().IsExactType(cel.StringType))
		require.True(t, boolProgram.OutputType().IsExactType(cel.BoolType))
		_, err = stringsEngine.Compile(`reported.startsWith("WEST-")`)
		require.NoError(t, err)
		_, err = boolEngine.Compile(`reported.startsWith("WEST-")`)
		require.ErrorContains(t, err, "checking CEL expression")
	})
}

func TestCompilationFailures(t *testing.T) {
	for _, tt := range []struct {
		name          string
		expression    string
		errorContains string
	}{
		{name: "When syntax is invalid it should cache the parse failure", expression: `(`, errorContains: "parsing CEL expression"},
		{name: "When a variable is undeclared it should cache the type-check failure", expression: `undeclared`, errorContains: "checking CEL expression"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			engine, err := celruntime.New()
			require.NoError(t, err)
			program, firstErr := engine.Compile(tt.expression)
			require.Nil(t, program)
			require.ErrorContains(t, firstErr, tt.errorContains)
			_, cachedErr := engine.Compile(tt.expression)
			require.Same(t, firstErr, cachedErr)
			for index := range 256 {
				_, err = engine.Compile(fmt.Sprintf("undeclared_%d", index))
				require.ErrorContains(t, err, "checking CEL expression")
			}
			_, recompiledErr := engine.Compile(tt.expression)
			require.ErrorContains(t, recompiledErr, tt.errorContains)
			require.NotSame(t, firstErr, recompiledErr)
		})
	}
}

func TestActivation(t *testing.T) {
	t.Run("When reported structs become activation roots it should use their JSON field names", func(t *testing.T) {
		type reportedStatus struct {
			ProductSerial string `json:"productSerial"`
		}
		activation, err := celruntime.Activate(map[string]any{
			"status": reportedStatus{ProductSerial: "WEST-001"},
			"spec":   nil,
		})
		require.NoError(t, err)
		engine, err := celruntime.New(cel.Variable("status", cel.DynType), cel.Variable("spec", cel.DynType))
		require.NoError(t, err)
		program, err := engine.Compile(`status.productSerial == "WEST-001" && spec == null`)
		require.NoError(t, err)
		value, err := program.Evaluate(activation)
		require.NoError(t, err)
		require.Equal(t, types.True, value)
		_, err = engine.Compile(`metadata`)
		require.ErrorContains(t, err, "undeclared reference")
	})
}

func TestEvaluateRejectsNilActivation(t *testing.T) {
	t.Run("When activation is nil it should reject evaluation explicitly", func(t *testing.T) {
		engine, err := celruntime.New()
		require.NoError(t, err)
		program, err := engine.Compile(`true`)
		require.NoError(t, err)
		_, err = program.Evaluate(nil)
		require.ErrorContains(t, err, "activation is nil")
	})
}

func TestConcurrentEvaluation(t *testing.T) {
	t.Run("When callers compile and evaluate concurrently it should isolate activations during eviction", func(t *testing.T) {
		engine, err := celruntime.New(cel.Variable("serial", cel.IntType))
		require.NoError(t, err)
		var wg sync.WaitGroup
		for worker := range 16 {
			wg.Go(func() {
				for index := range 300 {
					program, compileErr := engine.Compile(fmt.Sprintf("serial + %d", index))
					if !assert.NoError(t, compileErr) {
						return
					}
					value, evalErr := program.Evaluate(map[string]any{"serial": worker})
					if assert.NoError(t, evalErr) {
						assert.Equal(t, types.Int(worker+index), value)
					}
				}
			})
		}
		wg.Wait()
	})
}

func TestConcurrentCompilation(t *testing.T) {
	t.Run("When callers compile the same cold expression it should return the retained program to every caller", func(t *testing.T) {
		engine, err := celruntime.New()
		require.NoError(t, err)
		const callers = 16
		programs := make([]*celruntime.Program, callers)
		errors := make([]error, callers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for index := range callers {
			wg.Go(func() {
				<-start
				programs[index], errors[index] = engine.Compile(strings.Repeat("1 + ", 100) + "1")
			})
		}
		close(start)
		wg.Wait()
		for index := range callers {
			require.NoError(t, errors[index])
			require.Same(t, programs[0], programs[index])
			value, err := programs[index].Evaluate(map[string]any{})
			require.NoError(t, err)
			require.Equal(t, types.Int(101), value)
		}
	})
}

func TestProgramCache(t *testing.T) {
	t.Run("When the cache fills it should retain recently used programs and evict the least recently used", func(t *testing.T) {
		engine, err := celruntime.New()
		require.NoError(t, err)
		first, err := engine.Compile(`0`)
		require.NoError(t, err)
		second, err := engine.Compile(`1`)
		require.NoError(t, err)
		for index := 2; index < 256; index++ {
			_, err = engine.Compile(fmt.Sprint(index))
			require.NoError(t, err)
		}
		reused, err := engine.Compile(`0`)
		require.NoError(t, err)
		require.Same(t, first, reused)
		_, err = engine.Compile(`256`)
		require.NoError(t, err)
		reused, err = engine.Compile(`0`)
		require.NoError(t, err)
		require.Same(t, first, reused)
		recompiled, err := engine.Compile(`1`)
		require.NoError(t, err)
		require.NotSame(t, second, recompiled)
		value, err := second.Evaluate(map[string]any{})
		require.NoError(t, err)
		require.Equal(t, types.Int(1), value)
	})
}

func TestExpressionSizeLimit(t *testing.T) {
	t.Run("When an expression exceeds the size bound it should fail compilation", func(t *testing.T) {
		engine, err := celruntime.New()
		require.NoError(t, err)
		_, err = engine.Compile(`"` + strings.Repeat("a", 1_001) + `"`)
		require.ErrorContains(t, err, "parsing CEL expression")
	})
}

func TestEvaluationCostLimit(t *testing.T) {
	t.Run("When evaluation exceeds its cost bound it should return an error", func(t *testing.T) {
		engine, err := celruntime.New(cel.Variable("items", cel.ListType(cel.StringType)))
		require.NoError(t, err)
		program, err := engine.Compile(`items.exists(item, item == "absent")`)
		require.NoError(t, err)
		_, err = program.Evaluate(map[string]any{"items": make([]string, 10_100)})
		require.ErrorContains(t, err, "cost limit exceeded")
	})
}
