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

`Program.Evaluate` accepts an activation and returns a CEL value without
converting it to a label or a policy decision. Boolean results remain booleans,
strings retain their original contents, and nulls and optional values retain
their CEL types. Missing-key lookups and other evaluation failures return errors.
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
declarations or libraries cannot share cached programs. Cache hits refresh LRU
recency; a full cache evicts the least recently used compilation.
The engine rejects oversized expressions before consulting the cache. It caches
successful compilations and compilation failures for expressions within the size
limit.

Successful cache hits return the same `Program` handle. Eviction does not
invalidate handles already held by callers. Concurrent cold misses may
compile the same expression more than once, but share the retained cache entry
after insertion.

Use independent or concurrency-safe activations for concurrent evaluations.
CEL can write resolved lazy globals into a shared activation. Resolve shared lazy
globals before concurrent evaluation or provide a concurrency-safe global
activation.

## Consumer responsibilities

Label-sync mapping retains its dynamic `metadata`, `spec`, and `status` roots,
optional types, comprehensions, and semver library. Its evaluator still validates
scalar or map output, sanitizes label values, validates label keys, and limits
map entries. Missing keys, nulls, empty optionals, and empty strings retain their
existing no-value behavior, including removal of previously mapped labels.

## Enrollment approval policy expressions

`internal/enrollmentpolicy` uses a dedicated CEL environment.
`Matcher.Compile` validates each `spec.matchExpression` independently and
returns a reusable expression. The matcher accepts only these
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

Certificate identity and TPM verification are separate policy checks. The
matcher exposes only the reported system identifiers. An attempted lookup of
an unreported identifier or custom key can return an evaluation error. An
expression that returns an error does not match.
