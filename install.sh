#!/usr/bin/env bash
#
# install.sh installs agent-gate from the latest GitHub release and wires
# its hooks into Claude, Codex, Gemini, and GitHub Copilot Chat config
# files.
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
#   --service-only      install/start only the user daemon service
#   --no-service        skip user daemon service setup
#   --no-claude         skip Claude hook config update
#   --no-codex          skip Codex hook config update
#   --no-gemini         skip Gemini hook config update
#   --no-copilot        skip GitHub Copilot Chat hook config update
#   --no-config         skip agent-gate config creation / merge
#   --no-auto-update    disable auto-update in the merged config
#   --auto-update MODE  set auto-update mode in config: check or apply
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
DO_SERVICE=1
DO_CLAUDE=1
DO_CODEX=1
DO_GEMINI=1
DO_COPILOT=1
DO_CONFIG=1
TEMPLATES=""
SERVICE_TEMPLATES=""
AUTO_UPDATE_MODE="apply"

# Resolve to a local templates dir when run from a checkout.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ -d "$SCRIPT_DIR/hooks" ]]; then
  TEMPLATES="$SCRIPT_DIR/hooks"
fi
if [[ -d "$SCRIPT_DIR/services" ]]; then
  SERVICE_TEMPLATES="$SCRIPT_DIR/services"
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
    --bin-only)    DO_HOOKS=0; DO_SERVICE=0 ;;
    --hooks-only)  DO_BIN=0; DO_SERVICE=0 ;;
    --service-only) DO_BIN=0; DO_HOOKS=0; DO_SERVICE=1 ;;
    --no-service)  DO_SERVICE=0 ;;
    --no-claude)   DO_CLAUDE=0 ;;
    --no-codex)    DO_CODEX=0 ;;
    --no-gemini)   DO_GEMINI=0 ;;
    --no-copilot)  DO_COPILOT=0 ;;
    --no-config)   DO_CONFIG=0 ;;
    --no-auto-update) AUTO_UPDATE_MODE="off" ;;
    --auto-update) shift; AUTO_UPDATE_MODE="${1:?--auto-update requires a value}" ;;
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
  VERSION="$tag"
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
  if [[ "$DO_COPILOT" -eq 1 ]]; then
    update_hooks copilot "$HOME/.copilot/hooks/agent-gate.json"
  fi
}

install_config() {
  local bin_path="$BIN_DIR/agent-gate"
  [[ -x "$bin_path" ]] || die "missing installed binary for config merge: $bin_path"
  "$bin_path" config ensure-defaults --auto-update "$AUTO_UPDATE_MODE" \
    || die "config ensure-defaults failed"
  "$bin_path" config check || die "config check failed after merge"
}

state_dir() {
  printf '%s/agent-gate' "${XDG_STATE_HOME:-$HOME/.local/state}"
}

stop_unmanaged_daemons() {
  local pattern="^$BIN_DIR/agent-gate daemon$"
  local pids
  pids="$(pgrep -f "$pattern" || true)"
  if [[ -n "$pids" ]]; then
    printf '%s\n' "$pids" | xargs kill
  fi
}

fetch_service_template() {
  local platform="$1" name="$2"
  # Map go-service.mk platform name to packaging directory: launchd -> macos.
  local pkg_dir
  case "$platform" in
    launchd) pkg_dir="macos" ;;
    systemd) pkg_dir="systemd" ;;
    *)       pkg_dir="$platform" ;;
  esac
  if [[ -n "$SERVICE_TEMPLATES" && -f "$SERVICE_TEMPLATES/$pkg_dir/$name" ]]; then
    cat "$SERVICE_TEMPLATES/$pkg_dir/$name"
    return
  fi
  local ref="${VERSION:-main}"
  local url="https://raw.githubusercontent.com/$REPO/$ref/packaging/$pkg_dir/$name"
  curl -fsSL "$url" || die "fetch service template failed: $url"
}

install_service() {
  local os_name
  os_name="$(uname -s)"
  case "$os_name" in
    Darwin)
      local label target domain rendered state log_path
      label="io.goodkind.agent-gate"
      target="$HOME/Library/LaunchAgents/$label.plist"
      domain="gui/$(id -u)"
      state="$(state_dir)"
      log_path="$state/agent-gate.log"
      mkdir -p "$(dirname "$target")" "$state"
      rendered="$(fetch_service_template launchd "$label.plist.in" \
        | sed "s#@@BIN_PATH@@#$BIN_DIR/agent-gate#g; s#@@HOME@@#$HOME#g; s#@@LOG_PATH@@#$log_path#g")"
      printf '%s\n' "$rendered" > "$target"
      launchctl bootout "$domain" "$target" >/dev/null 2>&1 || true
      stop_unmanaged_daemons
      launchctl bootstrap "$domain" "$target" || die "launchctl bootstrap failed: $target"
      launchctl enable "$domain/$label" || true
      launchctl kickstart -k "$domain/$label" || die "launchctl kickstart failed: $label"
      printf 'install.sh: installed launchd service %s\n' "$target"
      ;;
    Linux)
      local target rendered
      need systemctl
      target="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/agent-gate.service"
      mkdir -p "$(dirname "$target")"
      rendered="$(fetch_service_template systemd agent-gate.service.in \
        | sed "s#@@BIN_PATH@@#$BIN_DIR/agent-gate#g")"
      printf '%s\n' "$rendered" > "$target"
      systemctl --user daemon-reload
      systemctl --user stop agent-gate.service >/dev/null 2>&1 || true
      stop_unmanaged_daemons
      systemctl --user enable --now agent-gate.service || die "systemctl --user enable --now failed"
      systemctl --user restart agent-gate.service || die "systemctl --user restart failed"
      printf 'install.sh: installed systemd user service %s\n' "$target"
      ;;
    *)
      die "unsupported OS for service install: $os_name"
      ;;
  esac
}

if [[ "$DO_BIN" -eq 1 ]]; then
  install_bin
fi

if [[ "$DO_CONFIG" -eq 1 ]]; then
  install_config
fi

if [[ "$DO_HOOKS" -eq 1 ]]; then
  install_hooks
fi

if [[ "$DO_SERVICE" -eq 1 ]]; then
  install_service
fi

printf 'install.sh: done\n'
