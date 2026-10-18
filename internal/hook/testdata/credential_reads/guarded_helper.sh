#!/usr/bin/env bash
set -euo pipefail
readonly TACK_DIR="{{ROOT}}"
printf '%s\n' executed >"${TACK_DIR}/executed"
load_env() {
  local line key value
  while IFS= read -r line; do
    key="${line%%=*}"
    value="${line#*=}"
    case "${key}" in
      OPENSEARCH_CA | OPENSEARCH_USERNAME | OPENSEARCH_PASSWORD | OPENSEARCH_ENDPOINT)
        printf -v "${key}" '%s' "${value}"
        ;;
    esac
  done <"${TACK_DIR}/.env"
}
load_env
printf '%s\n' "${OPENSEARCH_PASSWORD}"
