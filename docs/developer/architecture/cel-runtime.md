# Shared CEL runtime

`internal/celruntime` owns bounded CEL compilation and evaluation. Consumers
declare their own variables and libraries and interpret results in their own
domain. Label-sync mapping is the current production consumer.

## Compilation and evaluation contract

Construct an `Engine` with `celruntime.New`. Pass declarations and libraries
through `celruntime.WithEnvOptions`. The engine owns its environment for its
lifetime.

`Engine.Compile` parses, type-checks, and builds a program. Compilation is also
the validation path: parse failures and undeclared or incompatible types return
errors before evaluation. `Program.OutputType` exposes the checked CEL type so
the consumer can enforce its allowed result types. Dynamic expressions still
need result validation after evaluation.

`Program.CheckedExpression` returns a copy of the checked syntax tree for static
analysis. Consumers can inspect it without mutating a cached program.

`Program.Evaluate` accepts an activation and returns a CEL value without
converting it to a label or a policy decision. Boolean and string values retain
their original contents. Null and optional values retain their CEL types.
Evaluation returns an error when an expression accesses a missing key or fails
for another reason.
A nil activation or a nil activation map is rejected. An empty map is valid for
constant expressions.

`celruntime.Activate` normalizes a caller-selected map of roots through JSON.
This preserves the JSON field names and numeric representation used by existing
device activations. Consumers choose which roots and fields to include; the
runtime adds no device or enrollment data.

## Execution bounds and cache ownership

The runtime uses the established label-mapping limits by default.

- At most 1,000 Unicode code points per expression.
- At most 10,000 CEL cost units per evaluation.
- At most 256 cached compilations per engine, with no expiration.

To override these values for one engine, pass `celruntime.WithConfig` with a
`celruntime.Config`. Zero fields use the defaults. A positive `CacheTTL` sets
how long an entry can remain idle before it expires.

Every engine owns its own cache. Expressions compiled under different variable
declarations or libraries have separate caches. A cache hit makes the entry
the most recently used. A full cache evicts the least recently used compilation.
The engine rejects oversized expressions before consulting the cache. It caches
successful compilations and compilation failures for expressions within the size
limit.

Successful cache hits return the same `Program` handle. Eviction does not
invalidate handles already held by callers. Concurrent cold misses may
compile the same expression more than once, but share the retained cache entry
after insertion.

Use independent or concurrency-safe activations for concurrent evaluations.
CEL can update a shared activation when resolving global variables on demand.
Resolve those variables before concurrent evaluation or provide a
concurrency-safe global activation.

## Consumer responsibilities

Label-sync mapping retains its dynamic `metadata`, `spec`, and `status` roots,
optional types, comprehensions, and semver library. Its evaluator still validates
scalar or map output, sanitizes label values, validates label keys, and limits
map entries. Missing keys, null values, empty optional values, and empty strings
retain their existing no-value behavior, including removal of previously mapped
labels.

## Enrollment expressions

`internal/enrollmentpolicy` uses a dedicated CEL environment.
`Matcher.Compile` checks the syntax, result type, and supported operations
before returning a reusable expression. The matcher reads the following
operands from an EnrollmentRequest's `spec.deviceStatus.systemInfo`.

| Operand | CEL type |
| --- | --- |
| `systemInfo.productSerial` | String |
| `systemInfo.productUuid` | String |
| `systemInfo.customInfo["key"]` | String from a string-to-string map, selected by a literal key |

Expressions can use `==` with a string literal on the right, `in` with a
literal list of strings, `&&` to combine predicates, parentheses for grouping,
and the boolean literals `true` and `false`. An empty membership list matches
no values.

```cel
systemInfo.productUuid == "c42b5585-53dc-434a-9f25-24d078e1f9e1" &&
  systemInfo.customInfo["site"] in ["east", "west"]
```

The matcher builds the activation from identifiers referenced by the expression.
Each referenced value can contain up to 4,096 bytes. A longer referenced value
returns `ErrIdentifierTooLong` during activation. A missing identifier or custom
key returns `ErrEvaluation` if CEL needs that value to determine the result.
An empty reported string is available for comparison like any other string.
The compiler returns source diagnostics for expression authors. Runtime errors
use fixed messages without reported values, expression literals, or custom keys.

### Evaluate a group of expressions

Compile each candidate independently with the same `Matcher`. A group can
contain overlapping expressions. `Evaluate` runs up to 32 candidates against
the same enrollment request and returns an outcome based on that request.

| Outcome | Meaning |
| --- | --- |
| `matched` | One expression returned `true`. |
| `no_match` | Every candidate returned `false`, or the group was empty. |
| `multiple_matches` | At least two candidates returned `true`. |
| `evaluation_failed` | At least one candidate failed and none returned `true`. |

`MatchIndexes` lists the expressions that returned `true`. `Failures` records
the candidate index and sanitized error for each failure, including when another
candidate matches. Evaluation reads reported identifiers and leaves the request
untouched.

### Exercise fleet-scale evaluation

Run the evaluator test against 10,000 synthetic enrollment requests. It compiles
two overlapping site groups once and checks the outcome for each request,
including a site that matches both groups, a site outside both groups, and
requests with no site.

```bash
go test ./internal/enrollmentpolicy -run '^TestEvaluateReportedIdentifiersAtScale$' -count=1 -v
```
