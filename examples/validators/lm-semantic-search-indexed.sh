#!/usr/bin/env bash
# lm-semantic-search-indexed.sh: example agent-gate exec validator.
#
# agent-gate runs this synchronously when a rule's cheap conditions match a
# grep/rg-over-code command. It asks the lm-semantic-search daemon whether the
# target codebase is already indexed, and if so blocks the command so the agent
# is redirected to the lm-semantic-search MCP (search_code) instead of grep.
#
# Exit contract (the rule uses the default block_on = "nonzero"):
#   exit 1  -> the codebase is indexed: BLOCK; the first stdout line is the reason
#   exit 0  -> not indexed, or status undeterminable: ALLOW (fail open)
#
# AGENT_GATE_CACHE_KEY is the canonical effective_cwd that agent-gate resolved.

set -euo pipefail

LMS_BIN="${AGENT_GATE_LMS_BIN:-$HOME/.local/bin/lm-semantic-search}"

main() {
    local target="${AGENT_GATE_CACHE_KEY:-}"
    if [[ -z "$target" ]]; then
        exit 0
    fi
    if [[ ! -x "$LMS_BIN" ]]; then
        exit 0
    fi

    local status_json
    if ! status_json="$("$LMS_BIN" --json codebase status "$target" 2>/dev/null)"; then
        exit 0
    fi

    # KIND_IN_SCOPE_INDEXED is the daemon's verdict for a path that is covered by
    # a tracked codebase and has indexed chunks (the searchable case).
    if printf '%s' "$status_json" | grep -q '"kind":[[:space:]]*"KIND_IN_SCOPE_INDEXED"'; then
        echo "This codebase is indexed by lm-semantic-search ($target). You MUST use the lm-semantic-search MCP (search_code) for code discovery here instead of grep/rg."
        exit 1
    fi

    exit 0
}

main "$@"
