# Task 5b inference contract synchronization report

## Status

The agent-gate inference wire client matches the pinned lm-review contract and
keeps local verification separate from metadata reported by the inference
service.

Compact inference traces contain only locally derived provenance. Rich
evaluation metadata retains a bounded copy of the exact upstream protobuf JSON
under an explicit untrusted label.

## Wire contract

The request retains fields `prompt = 1`, `input = 2`, `output_schema = 3`,
`context = 4`, and `model = 5`, and adds `generation_options = 6`.

Generation options contain the reasoning enum at field 1, optional completion
tokens at field 2, and optional temperature at field 3. The reasoning enum
preserves values 0 through 6 for unspecified, none, minimal, low, medium, high,
and xhigh.

The reply retains `output_json = 1` and `status = 2`, and adds invocation
metadata at field 3. Token fields remain optional, so absent values and explicit
zero values have different protobuf presence.

## Provenance boundary

`VerifiedProvenance` is derived from the local condition, request input, and
cache key. It records the configured requested model, endpoint identity hash,
cache identity hash, input identity hash, local prompt and schema SHA-256, and
the match status of any reported prompt and schema hashes.

Payload-free `InferenceTrace` values contain `VerifiedProvenance` and no raw
invocation metadata. They do not contain prompt, input, context, schema, output,
or endpoint excerpts.

Rich inference layers retain upstream invocation metadata as protobuf JSON with
`source = "inference_reply"` and `trust = "untrusted"`. The raw JSON is limited
to 4096 encoded bytes. Absent, malformed, and oversized metadata use explicit
statuses and omit `raw`.

Evaluation `MetadataJSON` uses schema version 2 and stores
`verified_provenance` separately from `upstream_metadata`. Exact bounded raw
metadata preserves optional token presence, including explicit zero values.

Evaluation columns use only local values. `ModelName` is the configured model,
`PromptHash` and `SchemaHash` are local hashes, and upstream service version,
actual model, backend version, backend fingerprint, request ID, finish reason,
usage, and latency never populate verified columns.

## Cache behavior

Cache schema version 2 stores the schema-valid inference output and bounded raw
upstream metadata. It does not store separate reported-hash siblings. Cache
reads reject legacy, malformed, oversized, or incorrectly labeled provenance
records. Raw cache metadata must also decode as `InvocationMetadata` protobuf
JSON with no unknown fields. Scalar, array, unknown-field, and malformed JSON
values are rejected, while an explicit-zero optional token remains valid.

Each cache hit recomputes `VerifiedProvenance` from the current local condition,
input, endpoint, and cache key. Reported prompt and schema hashes come only from
the parsed bounded raw metadata. Absent raw metadata reports `absent`, and
omitted raw metadata reports `unavailable`. The cache does not replay a compact
trace from the upstream response.

## Output contract

Schema-valid `OutputJSON` and its `OutputHash` remain unchanged for successful
inference replies. Valid non-complete replies retain their prior rich output.
An `invalid_response` or any non-JSON error reply stores a bounded structured
object with the stable error code, original byte length, and original SHA-256.
Raw invalid bytes never enter `OutputJSON`, so the hot ledger can commit the
configured open or closed error result. The provenance split does not replace
schema-valid training output with an empty object.

## Verification evidence

`GOFLAGS=-tags=sqlite_fts5 go test ./internal/rules ./internal/daemon ./internal/hook -count=1`

Passed all affected packages after the provenance migration.

`GOFLAGS=-tags=sqlite_fts5 go test -race -v ./internal/rules ./internal/daemon -run '<focused provenance tests>' -count=1`

Passed compact cold, cache, error, bounded metadata, strict cache protojson, and
evaluation-record tests under the race detector.

`GOFLAGS=-tags=sqlite_fts5 go test ./internal/... -count=1`

Passed every internal package without the test cache.

`make GO_MK_GENERATE= GO_MK_GENERATE_INPUTS= GO_MK_GENERATE_OUTPUTS= GO_MK_WORKSPACE_USE= test`

Passed every repository package while using the isolated worktree's existing
ignored `go.work` entry for the pinned generated grammar checkout.

The same isolated-worktree `make check` path could not provide a complete green
signal. Its golangci-lint configuration reports an empty unsupported version,
and `staticcheck-extra` reports seven baseline findings in unrelated audit,
config, gitbranch, hotkv, shellread, and exec-gate files. After the provenance
GoDoc fix, the check output contains no finding in a file changed by this task.
The full check must run again after this commit reaches the current shared
branch and its later tool setup and baseline.

The focused tests cover cold and cache-hit compact traces, error and
non-complete replies, local hash match status, bounded and malformed raw
metadata, optional-zero token presence, verified evaluation columns, retained
successful output JSON, structured invalid-output digests, closed-error ledger
blocking, payload-free compact provenance, and contradictory cache-poisoning
inputs.
