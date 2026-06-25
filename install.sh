#!/usr/bin/env bash
#
# install.sh installs agent-gate from a GitHub release, then delegates hook and
# service setup to the installed agent-gate binary.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/agoodkind/agent-gate/main/install.sh | bash
#
# Local checkout:
#   ./install.sh [flags]
#
# Flags:
#   --bin-only           install the binary only, skip hook config updates
#   --hooks-only         update hook configs only, skip binary download
#   --service-only       install/start only the user daemon service
#   --no-service         skip user daemon service setup
#   --no-claude          skip Claude hook config update
#   --no-codex           skip Codex hook config update
#   --no-cursor          skip Cursor hook config update
#   --no-gemini          skip Gemini hook config update
#   --no-copilot         skip GitHub Copilot Chat hook config update
#   --bin-dir PATH       override binary install dir (default: $XDG_BIN_HOME or
#                        $HOME/.local/bin)
#   --version TAG        pin to a specific release tag (default: latest)
#   --repo OWNER/NAME    override GitHub repo (default: agoodkind/agent-gate)
#   --templates PATH     local hooks template dir to use instead of embedded
#                        templates
#   --service-templates PATH
#                        local service template dir to use instead of embedded
#                        templates
#   -h, --help           show this help
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
HOOK_INSTALL_ARGS=()
SERVICE_INSTALL_ARGS=()
HOOK_TEMPLATES_SET=0
SERVICE_TEMPLATES_SET=0
DEFAULT_HOOK_TEMPLATES=""
DEFAULT_SERVICE_TEMPLATES=""

SCRIPT_DIR=""
if [[ -n "${BASH_SOURCE[0]:-}" && -f "${BASH_SOURCE[0]}" ]]; then
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    if [[ -d "$SCRIPT_DIR/hooks" ]]; then
        DEFAULT_HOOK_TEMPLATES="$SCRIPT_DIR/hooks"
    fi
    if [[ -d "$SCRIPT_DIR/packaging" ]]; then
        DEFAULT_SERVICE_TEMPLATES="$SCRIPT_DIR/packaging"
    fi
fi

usage() {
    printf '%s\n' \
        "install.sh installs agent-gate from a GitHub release." \
        "" \
        "Usage:" \
        "  curl -fsSL https://raw.githubusercontent.com/agoodkind/agent-gate/main/install.sh | bash" \
        "  ./install.sh [flags]" \
        "" \
        "Flags:" \
        "  --bin-only           install the binary only, skip hook config updates" \
        "  --hooks-only         update hook configs only, skip binary download" \
        "  --service-only       install/start only the user daemon service" \
        "  --no-service         skip launchd/systemd user service setup" \
        "  --no-claude          skip Claude hook config update" \
        "  --no-codex           skip Codex hook config update" \
        "  --no-cursor          skip Cursor hook config update" \
        "  --no-gemini          skip Gemini hook config update" \
        "  --no-copilot         skip GitHub Copilot Chat hook config update" \
        "  --bin-dir PATH       override binary install dir" \
        "  --version TAG        pin to a specific release tag" \
        "  --repo OWNER/NAME    override GitHub repo" \
        "  --templates PATH     local hooks template dir" \
        "  --service-templates PATH" \
        "                       local service template dir" \
        "  -h, --help           show this help"
}

die() {
    printf 'install.sh: %s\n' "$*" >&2
    exit 1
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --bin-only)
            DO_HOOKS=0
            DO_SERVICE=0
            ;;
        --hooks-only)
            DO_BIN=0
            DO_SERVICE=0
            ;;
        --service-only)
            DO_BIN=0
            DO_HOOKS=0
            DO_SERVICE=1
            ;;
        --no-service)
            DO_SERVICE=0
            ;;
        --no-*)
            HOOK_INSTALL_ARGS+=("$1")
            ;;
        --bin-dir)
            shift
            BIN_DIR="${1:?--bin-dir requires a value}"
            ;;
        --version)
            shift
            VERSION="${1:?--version requires a value}"
            ;;
        --repo)
            shift
            REPO="${1:?--repo requires a value}"
            ;;
        --templates)
            shift
            HOOK_INSTALL_ARGS+=(--templates "${1:?--templates requires a value}")
            HOOK_TEMPLATES_SET=1
            ;;
        --service-templates)
            shift
            SERVICE_INSTALL_ARGS+=(--service-templates "${1:?--service-templates requires a value}")
            SERVICE_TEMPLATES_SET=1
            ;;
        -h | --help)
            usage
            exit 0
            ;;
        *)
            die "unknown flag: $1 (try --help)"
            ;;
    esac
    shift
done

need() {
    command -v "$1" >/dev/null 2>&1 || die "missing dependency: $1"
}

detect_platform() {
    local os_name
    local arch_name

    case "$(uname -s)" in
        Darwin)
            os_name="darwin"
            ;;
        Linux)
            os_name="linux"
            ;;
        *)
            die "unsupported OS: $(uname -s)"
            ;;
    esac

    case "$(uname -m)" in
        x86_64 | amd64)
            arch_name="amd64"
            ;;
        arm64 | aarch64)
            arch_name="arm64"
            ;;
        *)
            die "unsupported arch: $(uname -m)"
            ;;
    esac

    printf '%s_%s' "$os_name" "$arch_name"
}

release_url() {
    local platform="$1"

    if [[ -n "$VERSION" ]]; then
        printf 'https://github.com/%s/releases/download/%s/agent-gate_%s.tar.gz' "$REPO" "$VERSION" "$platform"
        return
    fi

    printf 'https://github.com/%s/releases/latest/download/agent-gate_%s.tar.gz' "$REPO" "$platform"
}

install_bin() {
    local platform
    local url
    local tmpdir
    local tarball
    local extracted

    need curl
    need tar
    need install

    platform="$(detect_platform)"
    url="$(release_url "$platform")"
    tmpdir="$(mktemp -d)"
    trap 'rm -rf "$tmpdir"' RETURN

    tarball="$tmpdir/agent-gate.tar.gz"
    printf 'install.sh: downloading %s\n' "$url"
    curl -fsSL "$url" -o "$tarball" || die "download failed: $url"

    tar -xzf "$tarball" -C "$tmpdir" || die "extract failed: $tarball"

    extracted="$tmpdir/agent-gate"
    if [[ ! -x "$extracted" ]]; then
        die "binary not found in tarball at $extracted"
    fi

    mkdir -p "$BIN_DIR"
    install -m 0755 "$extracted" "$BIN_DIR/agent-gate" || die "install failed: $BIN_DIR/agent-gate"
    printf 'install.sh: installed %s\n' "$BIN_DIR/agent-gate"

    rm -rf "$tmpdir"
    trap - RETURN
}

installer_args() {
    local mode="$1"
    shift

    "$BIN_DIR/agent-gate" install "$mode" --bin-path "$BIN_DIR/agent-gate" "$@"
}

run_hooks() {
    if [[ "$HOOK_TEMPLATES_SET" -eq 0 && -n "$DEFAULT_HOOK_TEMPLATES" ]]; then
        HOOK_INSTALL_ARGS+=(--templates "$DEFAULT_HOOK_TEMPLATES")
    fi
    installer_args hooks "${HOOK_INSTALL_ARGS[@]}"
}

run_service() {
    if [[ "$SERVICE_TEMPLATES_SET" -eq 0 && -n "$DEFAULT_SERVICE_TEMPLATES" ]]; then
        SERVICE_INSTALL_ARGS+=(--service-templates "$DEFAULT_SERVICE_TEMPLATES")
    fi
    installer_args service "${SERVICE_INSTALL_ARGS[@]}"
}

ensure_installed_binary() {
    if [[ ! -x "$BIN_DIR/agent-gate" ]]; then
        die "agent-gate binary not found at $BIN_DIR/agent-gate; run without --hooks-only/--service-only first"
    fi
}

if [[ "$DO_BIN" -eq 1 ]]; then
    install_bin
else
    ensure_installed_binary
fi

if [[ "$DO_HOOKS" -eq 1 ]]; then
    run_hooks
fi

if [[ "$DO_SERVICE" -eq 1 ]]; then
    run_service
fi

printf 'install.sh: done\n'
