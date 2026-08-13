#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
FIXTURE_DIR="$REPO_ROOT/tests/fixtures/install"
TEST_ROOT="$(mktemp -d)"
CHILD_PIDS=()
readonly SCRIPT_DIR REPO_ROOT FIXTURE_DIR TEST_ROOT

cleanup() {
    local child_pid

    if [[ "${#CHILD_PIDS[@]}" -gt 0 ]]; then
        for child_pid in "${CHILD_PIDS[@]}"; do
            if kill -TERM "$child_pid"; then
                if wait "$child_pid"; then
                    :
                else
                    :
                fi
            fi
        done
    fi
    rm -rf "$TEST_ROOT"
}

handle_int() {
    cleanup
    exit 130
}

handle_term() {
    cleanup
    exit 143
}

trap cleanup EXIT
trap handle_int INT
trap handle_term TERM

fail() {
    printf 'test-install-setup.sh: %s\n' "$*" >&2
    exit 1
}

assert_bytes() {
    local actual_path="$1"
    shift

    if ! cmp -s "$actual_path" <(printf '%s\0' "$@"); then
        fail "unexpected bytes in $actual_path"
    fi
}

assert_download_removed() {
    local capture_dir="$1"
    local download_path
    download_path="$(<"$capture_dir/download-path")"
    if [[ -e "$download_path" || -d "$(dirname "$download_path")" ]]; then
        fail "temporary installer remains at $download_path"
    fi
}

prepare_case() {
    local name="$1"
    CAPTURE_DIR="$TEST_ROOT/$name"
    mkdir -p "$CAPTURE_DIR"
    export CAPTURE_DIR
    export EXPECT_TTY=0
    export FAKE_AGENT_GATE_EXIT_CODE=0
    export BLOCK_DOWNLOAD=0
    export BLOCK_SETUP=0
    export READ_SETUP_INPUT=0
}

export PATH="$FIXTURE_DIR:$PATH"
export FAKE_HOSTED_INSTALLER="$FIXTURE_DIR/hosted-installer"
export FAKE_AGENT_GATE="$FIXTURE_DIR/agent-gate"
export INSTALL_TEST_REPO_ROOT="$REPO_ROOT"

prepare_case noninteractive
/bin/bash "$REPO_ROOT/install.sh" \
    --version v1.2.3 \
    --bin-dir "$CAPTURE_DIR/path with spaces" \
    --require-attestation
assert_bytes "$CAPTURE_DIR/hosted-args" \
    --repo agoodkind/agent-gate \
    --binary agent-gate \
    --version v1.2.3 \
    --bin-dir "$CAPTURE_DIR/path with spaces" \
    --require-attestation \
    -- \
    setup \
    --non-interactive \
    --providers claude,codex,cursor,gemini,copilot \
    --audit-profile balanced \
    --auto-update apply
assert_bytes "$CAPTURE_DIR/setup-args" \
    setup \
    --non-interactive \
    --providers claude,codex,cursor,gemini,copilot \
    --audit-profile balanced \
    --auto-update apply
assert_download_removed "$CAPTURE_DIR"

prepare_case interactive
export EXPECT_TTY=1
export READ_SETUP_INPUT=1
if [[ "$(uname -s)" == Darwin ]]; then
    printf '%s\n' confirmed | script -q /dev/null "$FIXTURE_DIR/tty-driver" &
else
    printf '%s\n' confirmed | script -q -e -c "$FIXTURE_DIR/tty-driver" /dev/null &
fi
interactive_pid=$!
CHILD_PIDS+=("$interactive_pid")
interactive_finished=false
for _ in {1..100}; do
    if ! kill -0 "$interactive_pid" 2>/dev/null; then
        interactive_finished=true
        break
    fi
    sleep 0.05
done
if [[ "$interactive_finished" != true ]]; then
    if kill -TERM "$interactive_pid"; then
        if wait "$interactive_pid"; then
            :
        else
            :
        fi
    fi
    CHILD_PIDS=()
    fail "interactive setup did not complete after reading terminal input"
fi
if wait "$interactive_pid"; then
    :
else
    interactive_status=$?
    CHILD_PIDS=()
    fail "interactive setup returned $interactive_status"
fi
CHILD_PIDS=()
if [[ "$(<"$CAPTURE_DIR/setup-input")" != confirmed ]]; then
    fail "interactive setup did not receive terminal input"
fi
assert_bytes "$CAPTURE_DIR/hosted-args" \
    --repo agoodkind/agent-gate \
    --binary agent-gate \
    --version v1.2.3 \
    --bin-dir "$CAPTURE_DIR/path with spaces" \
    --require-attestation \
    -- \
    setup
assert_bytes "$CAPTURE_DIR/setup-args" setup
assert_download_removed "$CAPTURE_DIR"

prepare_case failure
export FAKE_AGENT_GATE_EXIT_CODE=29
if /bin/bash "$REPO_ROOT/install.sh"; then
    fail "setup failure returned success"
else
    setup_status=$?
fi
if [[ "$setup_status" -ne 29 ]]; then
    fail "setup failure returned $setup_status, want 29"
fi
assert_download_removed "$CAPTURE_DIR"

prepare_case download-interruption
export BLOCK_DOWNLOAD=1
/bin/bash "$REPO_ROOT/install.sh" &
wrapper_pid=$!
CHILD_PIDS+=("$wrapper_pid")
for _ in {1..100}; do
    if [[ -f "$CAPTURE_DIR/curl-ready" ]]; then
        break
    fi
    sleep 0.05
done
if [[ ! -f "$CAPTURE_DIR/curl-ready" ]]; then
    kill -TERM "$wrapper_pid"
    fail "download did not reach interruption point"
fi
kill -TERM "$wrapper_pid"
if wait "$wrapper_pid"; then
    fail "interrupted download returned success"
else
    interrupt_status=$?
fi
CHILD_PIDS=()
if [[ "$interrupt_status" -ne 143 ]]; then
    fail "interrupted download returned $interrupt_status, want 143"
fi
curl_pid="$(<"$CAPTURE_DIR/curl-pid")"
if kill -0 "$curl_pid" 2>/dev/null; then
    kill -TERM "$curl_pid"
    fail "curl child $curl_pid survived interruption"
fi
assert_download_removed "$CAPTURE_DIR"

prepare_case interruption
export BLOCK_SETUP=1
/bin/bash "$REPO_ROOT/install.sh" &
wrapper_pid=$!
CHILD_PIDS+=("$wrapper_pid")
for _ in {1..100}; do
    if [[ -f "$CAPTURE_DIR/setup-ready" ]]; then
        break
    fi
    sleep 0.05
done
if [[ ! -f "$CAPTURE_DIR/setup-ready" ]]; then
    kill -TERM "$wrapper_pid"
    fail "setup did not reach interruption point"
fi
kill -TERM "$wrapper_pid"
if wait "$wrapper_pid"; then
    fail "interrupted setup returned success"
else
    interrupt_status=$?
fi
CHILD_PIDS=()
if [[ "$interrupt_status" -ne 143 ]]; then
    fail "interrupted setup returned $interrupt_status, want 143"
fi
setup_pid="$(<"$CAPTURE_DIR/setup-pid")"
if kill -0 "$setup_pid" 2>/dev/null; then
    kill -TERM "$setup_pid"
    fail "setup descendant $setup_pid survived interruption"
fi
assert_download_removed "$CAPTURE_DIR"
prepare_case interruption-int
export EXPECT_TTY=1
export BLOCK_SETUP=1
export READ_SETUP_INPUT=1
(
    for _ in {1..100}; do
        if [[ -f "$CAPTURE_DIR/setup-ready" ]]; then
            setup_pid="$(<"$CAPTURE_DIR/setup-pid")"
            setup_pgid="$(ps -o pgid= -p "$setup_pid")"
            setup_pgid="${setup_pgid//[[:space:]]/}"
            kill -INT -- "-$setup_pgid"
            exit 0
        fi
        sleep 0.05
    done
    printf 'timed out\n' >"$CAPTURE_DIR/int-timeout"
    if [[ -f "$CAPTURE_DIR/wrapper-pid" ]]; then
        kill -TERM "$(<"$CAPTURE_DIR/wrapper-pid")"
    fi
    exit 1
) &
signal_pid=$!
CHILD_PIDS+=("$signal_pid")
if [[ "$(uname -s)" == Darwin ]]; then
    if printf '%s\n' confirmed | script -q /dev/null "$FIXTURE_DIR/tty-driver"; then
        interrupt_status=0
    else
        interrupt_status=$?
    fi
else
    if printf '%s\n' confirmed | script -q -e -c "$FIXTURE_DIR/tty-driver" /dev/null; then
        interrupt_status=0
    else
        interrupt_status=$?
    fi
fi
if wait "$signal_pid"; then
    :
else
    signal_status=$?
    CHILD_PIDS=()
    fail "INT signal driver returned $signal_status"
fi
CHILD_PIDS=()
if [[ -f "$CAPTURE_DIR/int-timeout" ]]; then
    fail "interactive setup did not reach INT interruption point"
fi
if [[ "$(uname -s)" != Darwin && "$interrupt_status" -ne 130 ]]; then
    fail "INT interrupted setup returned $interrupt_status, want 130"
fi
setup_pid="$(<"$CAPTURE_DIR/setup-pid")"
if kill -0 "$setup_pid" 2>/dev/null; then
    kill -TERM "$setup_pid"
    fail "setup descendant $setup_pid survived INT"
fi
if [[ "$(<"$CAPTURE_DIR/setup-input")" != confirmed ]]; then
    fail "INT setup did not receive terminal input"
fi
assert_download_removed "$CAPTURE_DIR"

printf 'test-install-setup.sh: PASS\n'
