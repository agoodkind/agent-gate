# Task 5b inference contract synchronization report

## Status

Commit `7d01c32` synchronizes token usage presence with lm-review commit
`ea438346`. Invocation metadata fields 9, 10, and 11 retain their field numbers
and use `optional int64`, so omitted and JSON `null` differ from explicit zero.

The inference runtime treats reply metadata as untrusted. It derives requested
model, prompt SHA-256, and schema SHA-256 from the exact local request and
sanitizes every backend-derived value before cache or trace cloning.

## Sanitization contract

Server provenance mismatches produce `hash_mismatch`. Stored error metadata
uses local provenance and sanitized backend values.

Backend strings use conservative byte and rune limits. Empty values, invalid
UTF-8, control characters, oversized values, and values sharing an eight-rune
fragment with prompt, input, Clyde context, schema, or output payloads are
omitted. Rejected raw values are never logged.

Optional token counts must be nonnegative. A present total cannot be smaller
than either component, and a complete usage group must add exactly without
overflow. Invalid groups are omitted. Server latency must be nonnegative and no
greater than the configured inference timeout.

Cache reads repeat sanitization and restore local provenance. Intentional,
schema-valid output storage from `9b2d4f5` remains unchanged. Protobuf cloning
preserves optional usage presence through cold and cache-hit traces.

## Evidence

The protocol tests failed before `7d01c32` because explicit zero disappeared
during wire and protojson round trips. The metadata canary test failed before
sanitization because arbitrary reply metadata entered cache and traces. Review
then found Clyde context and long-payload excerpt gaps, which final tests cover.

The following commands passed for the final patch:

`go test ./api/inferencepb -run 'TestInferenceWireContract|TestInvocationTokenUsagePresenceOnWireAndJSON' -count=1`

`go test ./internal/rules -run '^TestInfer' -count=1`

`go test -race ./internal/rules -run '^TestInfer' -count=1`

`make test`

`make check`

Coverage includes explicit-zero usage after a cache hit, prompt, input, context,
schema, and output excerpt canaries, malformed backend strings, invalid usage,
bounded latency, sanitized error replies, and local provenance.
