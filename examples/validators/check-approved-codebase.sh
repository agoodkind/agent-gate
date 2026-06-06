#!/usr/bin/env bash
# check-approved-codebase.sh: example agent-gate exec validator.
#
# agent-gate runs this synchronously inside the daemon when a rule's cheap
# conditions match. It receives the decision context as JSON on stdin and as
# AGENT_GATE_* environment variables, and decides whether the target codebase is
# on a dynamic approved list that a static regex cannot express.
#
# Exit code contract (with the rule's default block_on = "nonzero"):
#   exit 0      -> approved, allow the action
#   exit 1      -> not approved, block (the first stdout line becomes the reason)
#   exit >= 2   -> error; with on_error = "open" the gate fails open and logs
#
# The approved list is one canonical codebase root per line; blank lines and
# lines starting with # are ignored.

set -euo pipefail

APPROVED_LIST="${AGENT_GATE_APPROVED_CODEBASES:-$HOME/.config/agent-gate/approved-codebases.txt}"

main() {
    local target="${AGENT_GATE_CACHE_KEY:-}"
    if [[ -z "$target" ]]; then
        echo "no codebase path provided"
        exit 1
    fi
    if [[ ! -f "$APPROVED_LIST" ]]; then
        echo "approved-codebase list missing: $APPROVED_LIST"
        exit 1
    fi

    local approved
    while IFS= read -r approved || [[ -n "$approved" ]]; do
        if [[ -z "$approved" || "$approved" == \#* ]]; then
            continue
        fi
        if [[ "$target" == "$approved" || "$target" == "$approved"/* ]]; then
            exit 0
        fi
    done < "$APPROVED_LIST"

    echo "codebase not on approved list: $target"
    exit 1
}

main "$@"
