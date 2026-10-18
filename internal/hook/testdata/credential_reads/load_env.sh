#!/usr/bin/env bash
set -euo pipefail
readonly SERVICE_DIR="{{ROOT}}"
load_env() {
  local line key value
  while IFS= read -r line; do
    key="${line%%=*}"
    value="${line#*=}"
    case "${key}" in
      SERVICE_CA | SERVICE_USERNAME | SERVICE_PASSWORD | SERVICE_ENDPOINT)
        printf -v "${key}" '%s' "${value}"
        ;;
    esac
  done <"${SERVICE_DIR}/.env"
}
load_env
printf '%s\n' "${SERVICE_PASSWORD}"
