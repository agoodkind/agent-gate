#!/usr/bin/env bash
#
# install.sh installs agent-gate from the latest GitHub release and wires
# its hooks into Claude, Codex, and Gemini config files.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/agoodkind/agent-gate/main/install.sh | bash
#
# Local checkout:
#   ./install.sh [flags]
#
# Flags:
#   --bin-only          install the binary only, skip hook config updates
#   --hooks-only        update hook configs only, skip binary download
#   --no-claude         skip Claude hook config update
#   --no-codex          skip Codex hook config update
#   --no-gemini         skip Gemini hook config update
#   --bin-dir PATH      override binary install dir (default: $XDG_BIN_HOME or
#                       $HOME/.local/bin)
#   --version TAG       pin to a specific release tag (default: latest)
#   --repo OWNER/NAME   override GitHub repo (default: agoodkind/agent-gate)
#   --templates PATH    local hooks template dir to use instead of GitHub raw
#                       (auto-detected when run from a checkout)
#   -h, --help          show this help
#
# Exit codes:
#   0 success
#   1 usage / unsupported platform
#   2 download / extract / install failure

set -euo pipefail

REPO="agoodkind/agent-gate"
BIN_DIR="${XDG_BIN_HOME:-$HOME/.local/bin}"
VERSION=""
DO_BIN=1
DO_HOOKS=1
DO_CLAUDE=1
DO_CODEX=1
DO_GEMINI=1
TEMPLATES=""

# Resolve to a local templates dir when run from a checkout.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ -d "$SCRIPT_DIR/hooks" ]]; then
  TEMPLATES="$SCRIPT_DIR/hooks"
fi

usage() {
  sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'
}

die() {
  printf 'install.sh: %s\n' "$*" >&2
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --bin-only)    DO_HOOKS=0 ;;
    --hooks-only)  DO_BIN=0 ;;
    --no-claude)   DO_CLAUDE=0 ;;
    --no-codex)    DO_CODEX=0 ;;
    --no-gemini)   DO_GEMINI=0 ;;
    --bin-dir)     shift; BIN_DIR="${1:?--bin-dir requires a value}" ;;
    --version)     shift; VERSION="${1:?--version requires a value}" ;;
    --repo)        shift; REPO="${1:?--repo requires a value}" ;;
    --templates)   shift; TEMPLATES="${1:?--templates requires a value}" ;;
    -h|--help)     usage; exit 0 ;;
    *) die "unknown flag: $1 (try --help)" ;;
  esac
  shift
done

need() {
  command -v "$1" >/dev/null 2>&1 || die "missing dependency: $1"
}

need curl
need jq
need tar

detect_platform() {
  local os arch
  case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux)  os=linux ;;
    *) die "unsupported OS: $(uname -s)" ;;
  esac
  case "$(uname -m)" in
    x86_64|amd64)   arch=amd64 ;;
    arm64|aarch64)  arch=arm64 ;;
    *) die "unsupported arch: $(uname -m)" ;;
  esac
  printf '%s_%s' "$os" "$arch"
}

resolve_version() {
  if [[ -n "$VERSION" ]]; then
    printf '%s' "$VERSION"
    return
  fi
  curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | jq -r '.tag_name // empty' \
    || die "failed to query latest release from $REPO"
}

install_bin() {
  local platform tag url tmpdir tarball
  platform="$(detect_platform)"
  tag="$(resolve_version)"
  [[ -n "$tag" ]] || die "could not resolve release tag (use --version)"

  url="https://github.com/$REPO/releases/download/$tag/agent-gate_${platform}.tar.gz"
  tmpdir="$(mktemp -d)"
  trap 'rm -rf "$tmpdir"' RETURN

  tarball="$tmpdir/agent-gate.tar.gz"
  printf 'install.sh: downloading %s\n' "$url"
  curl -fsSL "$url" -o "$tarball" || die "download failed: $url"

  tar -xzf "$tarball" -C "$tmpdir" || die "extract failed: $tarball"

  local extracted
  extracted="$tmpdir/agent-gate"
  [[ -x "$extracted" ]] || die "binary not found in tarball at $extracted"

  mkdir -p "$BIN_DIR"
  install -m 0755 "$extracted" "$BIN_DIR/agent-gate"
  printf 'install.sh: installed %s (%s)\n' "$BIN_DIR/agent-gate" "$tag"
}

# fetch_template reads a hook template into stdout. Local checkout takes
# priority. Otherwise pulls from raw.githubusercontent.com on the same tag
# as the binary so binary and hook templates stay in sync.
fetch_template() {
  local tool="$1"
  if [[ -n "$TEMPLATES" && -f "$TEMPLATES/$tool.json" ]]; then
    cat "$TEMPLATES/$tool.json"
    return
  fi
  local ref="${VERSION:-main}"
  local url="https://raw.githubusercontent.com/$REPO/$ref/hooks/$tool.json"
  curl -fsSL "$url" || die "fetch template failed: $url"
}

# update_hooks merges a hook template into a target config file. The
# template uses the placeholder __AGENT_GATE_BIN__ which is substituted
# with the actual binary path. Existing top-level keys are preserved.
update_hooks() {
  local tool="$1" target="$2"
  local cmd_path="$BIN_DIR/agent-gate"

  local template
  template="$(fetch_template "$tool")" || return 1

  local rendered
  rendered="$(printf '%s' "$template" | jq --arg bin "$cmd_path" '
    walk(
      if (type=="object") and (.command? | type=="string")
      then .command = (.command | gsub("__AGENT_GATE_BIN__"; $bin))
      else .
      end
    )
  ')"

  mkdir -p "$(dirname "$target")"

  local merged
  if [[ -f "$target" ]]; then
    merged="$(jq --argjson hooks "$rendered" '.hooks = $hooks' "$target")"
  else
    merged="$(jq -n --argjson hooks "$rendered" '{hooks: $hooks}')"
  fi

  printf '%s\n' "$merged" > "$target.tmp"
  mv "$target.tmp" "$target"
  printf 'install.sh: updated %s (%s hooks)\n' "$target" "$tool"
}

install_hooks() {
  if [[ "$DO_CLAUDE" -eq 1 ]]; then
    update_hooks claude "$HOME/.claude/settings.json"
  fi
  if [[ "$DO_CODEX" -eq 1 ]]; then
    update_hooks codex "$HOME/.codex/hooks.json"
  fi
  if [[ "$DO_GEMINI" -eq 1 ]]; then
    update_hooks gemini "$HOME/.gemini/settings.json"
  fi
}

if [[ "$DO_BIN" -eq 1 ]]; then
  install_bin
fi

if [[ "$DO_HOOKS" -eq 1 ]]; then
  install_hooks
fi

printf 'install.sh: done\n'
