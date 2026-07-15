#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
FIXTURE_DIR="$SCRIPT_DIR/fixtures/install"
CAPTURE_DIR="$(mktemp -d)"
readonly SCRIPT_DIR REPO_ROOT FIXTURE_DIR CAPTURE_DIR

cleanup() {
    rm -rf "$CAPTURE_DIR"
}
trap cleanup EXIT

fail() {
    printf 'install_test.sh: %s\n' "$*" >&2
    exit 1
}

assert_bytes() {
    local actual_path="$1"
    shift

    if ! cmp -s "$actual_path" <(printf '%s\0' "$@"); then
        fail "unexpected bytes in $actual_path"
    fi
}

export CAPTURE_DIR
export FAKE_BASH_EXIT_CODE=23
export PATH="$FIXTURE_DIR:$PATH"

set +e
/bin/bash "$REPO_ROOT/install.sh" \
    --version v1.2.3 \
    --bin-dir "$CAPTURE_DIR/path with spaces" \
    --require-attestation
install_status=$?
set -e

if [[ "$install_status" -ne "$FAKE_BASH_EXIT_CODE" ]]; then
    fail "expected exit $FAKE_BASH_EXIT_CODE, got $install_status"
fi

assert_bytes "$CAPTURE_DIR/curl-args" \
    -fsSL \
    https://raw.githubusercontent.com/agoodkind/go-makefile/main/install.sh

assert_bytes "$CAPTURE_DIR/bash-args" \
    -s \
    -- \
    --repo \
    agoodkind/agent-gate \
    --binary \
    agent-gate \
    --version \
    v1.2.3 \
    --bin-dir \
    "$CAPTURE_DIR/path with spaces" \
    --require-attestation \
    -- \
    install \
    all

if ! cmp -s "$CAPTURE_DIR/bash-stdin" <(printf '%s\n' '# hosted installer sentinel'); then
    fail "hosted installer body was not piped to Bash"
fi

printf 'install_test.sh: PASS\n'
