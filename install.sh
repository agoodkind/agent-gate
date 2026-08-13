#!/usr/bin/env bash
# Thin installer. Routes to go-makefile's hosted installer, which fetches and
# verifies go-mk-install, installs the agent-gate release binary, then runs setup.
set -euo pipefail

HOSTED_INSTALLER_URL="https://raw.githubusercontent.com/agoodkind/go-makefile/main/install.sh"
INSTALLER_TEMP_DIR="$(mktemp -d)"
INSTALLER_PATH="$INSTALLER_TEMP_DIR/install.sh"
TTY_ERROR_PATH="$INSTALLER_TEMP_DIR/tty-error"
TTY_OPENED=false
CHILD_PIDS=()
readonly HOSTED_INSTALLER_URL INSTALLER_TEMP_DIR INSTALLER_PATH TTY_ERROR_PATH
PROCESS_TREE_PIDS=()

cleanup() {
    if [[ "$TTY_OPENED" == true ]]; then
        exec 3>&-
        TTY_OPENED=false
    fi
    if [[ -d "$INSTALLER_TEMP_DIR" ]]; then
        rm -rf "$INSTALLER_TEMP_DIR"
    fi
}

freeze_process_tree() {
    local process_pid="$1"
    local child_pid
    local child_pids

    if ! kill -STOP "$process_pid"; then
        printf 'agent-gate installer: child %s already exited before termination\n' "$process_pid" >&2
        return
    fi
    if child_pids="$(pgrep -P "$process_pid")"; then
        :
    else
        child_pids=""
    fi
    while IFS= read -r child_pid; do
        if [[ -z "$child_pid" ]]; then
            continue
        fi
        freeze_process_tree "$child_pid"
    done <<<"$child_pids"
    PROCESS_TREE_PIDS+=("$process_pid")
}

terminate_children() {
    local root_pid
    local process_pid
    local child_status

    if [[ "${#CHILD_PIDS[@]}" -eq 0 ]]; then
        return
    fi
    PROCESS_TREE_PIDS=()
    for root_pid in "${CHILD_PIDS[@]}"; do
        freeze_process_tree "$root_pid"
    done
    for process_pid in "${PROCESS_TREE_PIDS[@]}"; do
        if kill -TERM "$process_pid"; then
            if kill -CONT "$process_pid"; then
                :
            else
                printf 'agent-gate installer: child %s exited before resume\n' "$process_pid" >&2
            fi
        else
            printf 'agent-gate installer: child %s exited before termination\n' "$process_pid" >&2
        fi
    done
    for root_pid in "${CHILD_PIDS[@]}"; do
        if wait "$root_pid"; then
            child_status=0
        else
            child_status=$?
        fi
        if [[ "$child_status" -ne 0 && "$child_status" -ne 130 && "$child_status" -ne 143 ]]; then
            printf 'agent-gate installer: child %s exited with status %s during termination\n' "$root_pid" "$child_status" >&2
        fi
    done
    CHILD_PIDS=()
    PROCESS_TREE_PIDS=()
}

handle_int() {
    terminate_children
    cleanup
    exit 130
}

handle_term() {
    terminate_children
    cleanup
    exit 143
}

wait_for_installer() {
    local status

    if wait "${CHILD_PIDS[0]}"; then
        status=0
    else
        status=$?
    fi
    CHILD_PIDS=()
    return "$status"
}

trap cleanup EXIT
trap handle_int INT
trap handle_term TERM

curl -fsSL -o "$INSTALLER_PATH" "$HOSTED_INSTALLER_URL" &
CHILD_PIDS+=("$!")
wait_for_installer

if [[ -r /dev/tty && -w /dev/tty ]]; then
    if { exec 3<>/dev/tty; } 2>"$TTY_ERROR_PATH"; then
        TTY_OPENED=true
    else
        tty_error=""
        if IFS= read -r tty_error <"$TTY_ERROR_PATH"; then
            :
        else
            tty_error="terminal open failed"
        fi
        printf 'agent-gate installer: no controlling terminal; using non-interactive setup: %s\n' "$tty_error" >&2
    fi
else
    printf 'agent-gate installer: no controlling terminal; using non-interactive setup\n' >&2
fi

if [[ "$TTY_OPENED" == true ]]; then
    /bin/bash "$INSTALLER_PATH" \
        --repo agoodkind/agent-gate --binary agent-gate "$@" -- setup <&3 &
else
    /bin/bash "$INSTALLER_PATH" \
        --repo agoodkind/agent-gate --binary agent-gate "$@" -- \
        setup \
        --non-interactive \
        --providers claude,codex,cursor,gemini,copilot \
        --audit-profile balanced \
        --auto-update apply </dev/null &
fi
CHILD_PIDS+=("$!")
wait_for_installer
