#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' executed >"{{ROOT}}/executed"
cat "{{ROOT}}/inspection-marker"
