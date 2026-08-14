#!/usr/bin/env bash

set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
DOC_TEST_ROOT=$(mktemp -d /tmp/agent-gate-docs.XXXXXX)
AGENT_GATE_BIN="$DOC_TEST_ROOT/agent-gate"
DOC_DAEMON_PID=
CHILD_PIDS=("")

cleanup() {
    local pid
    for pid in "${CHILD_PIDS[@]}"; do
        if [[ -z "$pid" ]]; then
            continue
        fi
        kill -TERM "$pid" 2>/dev/null || true
        wait "$pid" 2>/dev/null || true
    done
    rm -rf "$DOC_TEST_ROOT"
}

handle_interrupt() {
    cleanup
    exit 130
}

trap cleanup EXIT
trap handle_interrupt INT TERM

source "$REPO_ROOT/scripts/testdata/doc-command-environment.sh"

cd "$REPO_ROOT"
go build -tags sqlite_fts5 -o "$AGENT_GATE_BIN" ./cmd/agent-gate
export PATH="$DOC_TEST_ROOT:$PATH"

run_block() {
    local page=$1
    local line_number=$2
    local tag=$3
    local command_text=$4
    if [[ "$tag" == *"skip reason="* ]]; then
        return
    fi
    prepare_doc_fixture
    if [[ "$command_text" == *"audit compact --full"* ]]; then
        stop_doc_daemon
    fi
    if bash -euo pipefail -c "$command_text"; then
        return
    else
        local status=$?
        printf '%s:%s command failed with status %s\n%s\n' "$page" "$line_number" "$status" "$command_text" >&2
        return "$status"
    fi
}

check_page() {
    local page=$1
    local line=
    local line_number=0
    local block_line=0
    local pending_tag=
    local command_text=
    local in_shell_block=false
    while IFS= read -r line || [[ -n "$line" ]]; do
        line_number=$((line_number + 1))
        if [[ "$in_shell_block" == true ]]; then
            if [[ "$line" == '```' ]]; then
                run_block "$page" "$block_line" "$pending_tag" "$command_text"
                in_shell_block=false
                pending_tag=
                command_text=
            else
                command_text+="$line"$'\n'
            fi
            continue
        fi
        if [[ "$line" == '<!-- doc-test: '* ]]; then
            pending_tag=$line
            continue
        fi
        if [[ "$line" == '```sh' ]]; then
            if [[ -z "$pending_tag" ]]; then
                printf '%s:%s shell block needs a doc-test tag\n' "$page" "$line_number" >&2
                return 1
            fi
            block_line=$line_number
            in_shell_block=true
        fi
    done < "$page"
    if [[ "$in_shell_block" == true ]]; then
        printf '%s:%s shell block is not closed\n' "$page" "$block_line" >&2
        return 1
    fi
}

PAGES=(README.md HOOKS.md CONTRIBUTING.md docs/*.md)
for page in "${PAGES[@]}"; do
    check_page "$page"
done

printf 'check-doc-commands.sh: PASS\n'
