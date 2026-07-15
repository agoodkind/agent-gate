#!/usr/bin/env bash
# Thin installer. Routes to go-makefile's hosted installer, which fetches and
# verifies go-mk-install, installs the agent-gate release binary, then runs
# agent-gate install all to set up config, hooks, and the user service.
set -euo pipefail
curl -fsSL https://raw.githubusercontent.com/agoodkind/go-makefile/main/install.sh \
    | bash -s -- --repo agoodkind/agent-gate --binary agent-gate "$@" -- install all
