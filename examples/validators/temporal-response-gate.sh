#!/usr/bin/env bash
# temporal-response-gate.sh sends the configured response only when the latest
# user request differs from the previous response. Missing latest user-message
# or current-response context suppresses, while no prior response sends first.

set -euo pipefail

POLICY_SUPPRESS_STATUS=9

if ! command -v jq >/dev/null; then
    printf '%s\n' "temporal-response-gate.sh requires jq" >&2
    # A signal makes Agent Gate classify this setup failure as an execution error.
    kill -TERM "$$"
fi

main() {
    local jq_status
    if jq --exit-status --join-output '
        def selected($field):
            [.matched[]? |
                select(
                    .field == $field and
                    .available == true and
                    (.value | type) == "string"
                )
            ];
        def suppress:
            null | halt_error(9);

        selected("last_user_message") as $last_user_message |
        selected("last_response_output") as $last_response_output |
        selected("response_output") as $response_output |
        selected("loop_count") as $loop_count |
        if
            ($last_user_message | length) != 1 or
            ($response_output | length) != 1 or
            ($loop_count | length) != 1 or
            ($last_response_output | length) > 1
        then
            suppress
        elif
            ($loop_count[0].value != "0") and
            (($last_response_output | length) == 0)
        then
            suppress
        elif
            (($last_response_output | length) == 1) and
            ($last_user_message[0].value == $last_response_output[0].value)
        then
            suppress
        else
            $response_output[0].value
        end
    '; then
        return 0
    else
        jq_status=$?
    fi

    if [[ "$jq_status" -eq "$POLICY_SUPPRESS_STATUS" ]]; then
        exit 1
    fi

    printf '%s\n' "temporal-response-gate.sh jq failed with status $jq_status" >&2
    kill -TERM "$$"
}

main "$@"
