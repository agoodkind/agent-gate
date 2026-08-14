#!/usr/bin/env bash

start_doc_daemon() {
    mkdir -p "$HOME/.config/agent-gate" "$XDG_STATE_HOME" "$XDG_RUNTIME_DIR"
    : > "$HOME/.config/agent-gate/config.toml"
    "$AGENT_GATE_BIN" daemon >"$DOC_TEST_ROOT/daemon.log" 2>&1 &
    DOC_DAEMON_PID=$!
    CHILD_PIDS+=("$DOC_DAEMON_PID")
    local attempt
    for attempt in {1..100}; do
        if "$AGENT_GATE_BIN" daemon status >/dev/null 2>&1; then
            printf '%s' '{"hook_event_name":"SessionStart","session_id":"doc-test","source":"startup"}' |
                "$AGENT_GATE_BIN" managed-hook codex >/dev/null
            return 0
        fi
        sleep 0.05
    done
    printf 'daemon did not become ready\n' >&2
    sed -n '1,120p' "$DOC_TEST_ROOT/daemon.log" >&2
    return 1
}

stop_doc_daemon() {
    if [[ -z "${DOC_DAEMON_PID:-}" ]]; then
        return
    fi
    kill -TERM "$DOC_DAEMON_PID" 2>/dev/null || true
    wait "$DOC_DAEMON_PID" 2>/dev/null || true
    DOC_DAEMON_PID=
}

prepare_doc_fixture() {
    stop_doc_daemon
    rm -rf "$DOC_TEST_ROOT/home" "$DOC_TEST_ROOT/state" "$DOC_TEST_ROOT/runtime"
    export HOME="$DOC_TEST_ROOT/home"
    export XDG_CONFIG_HOME="$HOME/.config"
    export XDG_STATE_HOME="$DOC_TEST_ROOT/state"
    export XDG_RUNTIME_DIR="$DOC_TEST_ROOT/runtime"
    start_doc_daemon
}
