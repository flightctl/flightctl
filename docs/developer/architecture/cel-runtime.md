# Shared CEL runtime

`internal/celruntime` owns bounded CEL compilation and evaluation. Consumers
declare their own variables and libraries and interpret results in their own
domain. Label-sync mapping is the current production consumer.

## Compilation and evaluation contract

Construct an `Engine` with `celruntime.New`, passing CEL environment options.
The engine owns that environment for its lifetime. It does not expose an
environment that callers can extend or mutate after construction.

`Engine.Compile` parses, type-checks, and builds a program. Compilation is also
the validation path: parse failures and undeclared or incompatible types return
errors before evaluation. `Program.OutputType` exposes the checked CEL type so
the consumer can enforce its allowed result types. Dynamic expressions still
need result validation after evaluation.

`Program.Evaluate` accepts an activation and returns a CEL value without
converting it to a label or a policy decision. Boolean results remain booleans,
strings retain their original contents, and nulls and optional values retain
their CEL types. Missing-key lookups and other evaluation failures return errors.
A nil activation is rejected; an empty activation is valid for constant
expressions.

`celruntime.Activate` normalizes a caller-selected map of roots through JSON.
This preserves the JSON field names and numeric representation used by existing
device activations. Consumers choose which roots and fields to include; the
runtime adds no device or enrollment data.

## Execution bounds and cache ownership

The runtime retains the established label-mapping limits:

- At most 1,000 Unicode code points per expression.
- At most 10,000 CEL cost units per evaluation.
- At most 256 cached compilations per engine, including compilation failures.

Every engine owns its own cache. Expressions compiled under different variable
declarations or libraries cannot share cached programs. Cache hits refresh LRU
recency; a full cache evicts the least recently used compilation.

Successful cache hits return the same `Program` handle. Eviction does not
invalidate handles already held by callers. Compilation and evaluation may run
concurrently, with independent activations for each evaluation. Concurrent cold
misses may compile the same expression more than once, but share the retained
cache entry after insertion.

## Consumer responsibilities

Label-sync mapping retains its dynamic `metadata`, `spec`, and `status` roots,
optional types, comprehensions, and semver library. Its evaluator still validates
scalar or map output, sanitizes label values, validates label keys, and limits
map entries. Missing keys, nulls, empty optionals, and empty strings retain their
existing no-value behavior, including removal of previously mapped labels.

A future enrollment consumer must construct a separate restricted environment
and enforce boolean output and its policy-specific validation. Extracting the
runtime does not install an enrollment evaluator or enable automatic approval.
