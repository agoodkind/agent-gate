#!/usr/bin/env bash
# lm-semantic-search-indexed.sh: example agent-gate exec validator.
#
# agent-gate runs this synchronously when a rule's cheap conditions match a
# grep/rg-over-code command. agent-gate resolves the command's effective target
# paths (the files or directories the search actually reads, or the working tree
# for a recursive search) and passes them on AGENT_GATE_READ_TARGETS, one
# canonical path per line. This script asks the lm-semantic-search daemon whether
# any target is part of an indexed codebase, and if so blocks the command so the
# agent is redirected to the lm-semantic-search MCP (search_code) instead of grep.
#
# Exit contract (the rule uses block_on = "nonzero"):
#   exit 1  -> a target is indexed: BLOCK; the first stdout line is the reason
#   exit 0  -> no target indexed, undeterminable, or no target: ALLOW (fail open)
#
# A command that reads stdin or names no file (a non-recursive grep in a pipe)
# yields no targets, so the loop is skipped and the command is allowed.

set -euo pipefail

LMS_BIN="${AGENT_GATE_LMS_BIN:-$HOME/.local/bin/lm-semantic-search}"
# `lms codebase status` is per-file accurate: a path is KIND_IN_SCOPE_INDEXED
# only when the file itself (or, for a directory, a file beneath it) is in the
# index and therefore searchable through the MCP. A file that is excluded or not
# yet indexed reports KIND_IN_SCOPE_EXCLUDED / KIND_IN_SCOPE_UNINDEXED, and a
# path outside any tracked codebase reports KIND_OUT_OF_SCOPE. Block only the
# indexed case so grep stays allowed for anything the MCP cannot search.
INDEXED_KIND='"kind":[[:space:]]*"KIND_IN_SCOPE_INDEXED"'

target_is_indexed() {
    local target="$1"
    local status_json
    status_json="$("$LMS_BIN" --json codebase status "$target" 2>/dev/null)" || return 1
    printf '%s' "$status_json" | grep -q "$INDEXED_KIND"
}

main() {
    if [[ ! -x "$LMS_BIN" ]]; then
        exit 0
    fi

    local targets=()
    if [[ -n "${AGENT_GATE_READ_TARGETS:-}" ]]; then
        mapfile -t targets <<<"$AGENT_GATE_READ_TARGETS"
    fi

    local target
    for target in "${targets[@]}"; do
        if [[ -z "$target" ]]; then
            continue
        fi
        if target_is_indexed "$target"; then
            echo "This codebase is indexed by lm-semantic-search ($target). You MUST use the lm-semantic-search MCP (search_code) for code discovery here instead of grep/rg."
            exit 1
        fi
    done

    exit 0
}

main "$@"
