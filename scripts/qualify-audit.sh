#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
OUTPUT_ROOT="${1:?provide an output directory}"
mkdir -p "$OUTPUT_ROOT"
OUTPUT_ROOT="$(cd "$OUTPUT_ROOT" && pwd -P)"
BASELINE_ROOT="$OUTPUT_ROOT/baseline-source"
BASELINE_SOURCE=2c9070b8866afae235ed24bbe5a29a0f689824a2
IDENTITY_SOURCE=ccb898c46743282a890bb1aafa40ab3042aa5442
CHILD_PIDS=()
INTERRUPTED=false

handle_interrupt() {
    INTERRUPTED=true
    local child_pid
    for child_pid in "${CHILD_PIDS[@]}"; do
        kill -TERM "$child_pid"
    done
    exit 130
}
trap handle_interrupt INT TERM

cd "$REPO_ROOT"
git fetch origin
mkdir "$BASELINE_ROOT"
git archive "$BASELINE_SOURCE" | tar -x -C "$BASELINE_ROOT"
git show "$IDENTITY_SOURCE:internal/version/version.go" >"$BASELINE_ROOT/internal/version/version.go"
# Identity regression tests need the newer API; they are tested on the candidate.
rm "$BASELINE_ROOT/internal/version/version_test.go"
cp scripts/testdata/audit-qualification-identity.go "$BASELINE_ROOT/internal/daemon/process_identity.go"
cp scripts/testdata/audit-qualification-baseline.go "$BASELINE_ROOT/internal/daemon/audit_qualification_adapter_darwin_test.go"
cp internal/daemon/audit_qualification_darwin_test.go "$BASELINE_ROOT/internal/daemon/"
cp internal/daemon/audit_commit_counter_test.go "$BASELINE_ROOT/internal/daemon/"
mkdir -p "$BASELINE_ROOT/internal/testutil/processusage"
cp internal/testutil/processusage/* "$BASELINE_ROOT/internal/testutil/processusage/"
(cd "$BASELINE_ROOT" && GOWORK=off go work init . "$REPO_ROOT/third_party/gksyntax")
go version >"$OUTPUT_ROOT/go-version.txt"
git rev-parse HEAD >"$OUTPUT_ROOT/candidate-source.txt"
git diff --binary >"$OUTPUT_ROOT/candidate.patch"
shasum -a 256 internal/daemon/audit_qualification* internal/testutil/processusage/* scripts/testdata/audit-qualification* >"$OUTPUT_ROOT/instrumentation.sha256"

build_test_binary() {
    local root="$1"
    local binary="$2"
    local started
    started=$(date +%s)
    rm -f "$binary"
    (cd "$root" && go test -tags sqlite_fts5,auditqualification -c -o "$binary" ./internal/daemon)
    if [[ "$(stat -f %m "$binary")" -lt "$started" ]]; then
        printf 'test executable has stale modification time\n' >&2
        return 1
    fi
    (cd "$root/internal/daemon" && "$binary" -test.run '^TestAuditPerformanceTraceIsFixed$' -test.count=1)
    stat -f '%N %m %z' "$binary"
}

build_test_binary "$BASELINE_ROOT" "$OUTPUT_ROOT/baseline.test" >"$OUTPUT_ROOT/baseline-build.log" 2>&1
build_test_binary "$REPO_ROOT" "$OUTPUT_ROOT/candidate.test" >"$OUTPUT_ROOT/candidate-build.log" 2>&1

for history in empty seven; do
    for scenario in burst steady status cancellation backlog; do
        for repetition in 1 2 3 4 5; do
            for variant in baseline candidate; do
                if [[ "$INTERRUPTED" == true ]]; then
                    exit 130
                fi
                root="$REPO_ROOT"
                source="$(<"$OUTPUT_ROOT/candidate-source.txt")+instrumentation"
                if [[ "$variant" == baseline ]]; then
                    root="$BASELINE_ROOT"
                    source="$BASELINE_SOURCE+identity=$IDENTITY_SOURCE+instrumentation"
                fi
                artifact="$OUTPUT_ROOT/$variant-$history-$scenario-$repetition"
                (
                    cd "$root/internal/daemon"
                    export QUALIFICATION_SOURCE="$source"
                    export QUALIFICATION_OUTPUT="$artifact.json"
                    export QUALIFICATION_SCENARIO="$scenario"
                    export QUALIFICATION_HISTORY="$history"
                    export QUALIFICATION_REPETITION="$repetition"
                    exec "$OUTPUT_ROOT/$variant.test" -test.run '^$' -test.bench '^BenchmarkAuditQualification$' -test.benchtime=1x -test.count=1 -test.timeout=3m
                ) >"$artifact.log" 2>&1 &
                CHILD_PIDS=("$!")
                if wait "${CHILD_PIDS[0]}"; then
                    printf '%s PASS\n' "$artifact"
                else
                    result=$?
                    printf '%s FAILED exit=%s\n' "$artifact" "$result"
                    printf '%s\n' "$result" >"$artifact.exit"
                fi
                CHILD_PIDS=()
            done
        done
    done
done

go run ./scripts/testdata/audit-qualification-summary.go "$OUTPUT_ROOT" \
    >"$OUTPUT_ROOT/summary.md"
