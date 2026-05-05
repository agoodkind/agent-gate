# Lint is centralized in go-makefile. Do NOT define project-local lint,
# deadcode, audit, fmt, vet, or staticcheck targets here. They duplicate
# the central pipeline and let agents bypass strict rules. Run `make help`
# for the canonical entry points (build/check/lint/fmt) and per-linter
# sub-targets (lint-golangci, lint-format, lint-gocyclo, lint-deadcode,
# staticcheck-extra). Refresh baselines via the matching *-baseline target.
#
# agent-gate Makefile.
# Build/lint/release pipeline lives in go-makefile and is fetched at runtime.
# Daemon install is currently still driven by install.sh; go-service.mk wiring
# is a planned follow-up (the existing service templates use a __VAR__ marker
# scheme + STATE_DIR pattern that the canonical module does not yet support).

# Optional local overrides (signing identity, never committed). Copy config.mk.example.
-include config.mk

# Identity
BINARY     := agent-gate
CMD        := ./cmd/$(BINARY)
VPKG       := goodkind.io/agent-gate/internal/version
GKLOG_VPKG := goodkind.io/gklog/version

# CGO=1 for the daemon's runtime requirements.
export CGO_ENABLED := 1

# Daemon identity. go-service.mk reads these at parse time, so they must be
# set BEFORE -include $(GO_MK).
LAUNCHD_LABEL := io.goodkind.agent-gate
SYSTEMD_UNIT  := agent-gate.service
LOG_PATH      := $(or $(XDG_STATE_HOME),$(HOME)/.local/state)/agent-gate/agent-gate.log

# Pipeline modules
GO_MK_MODULES := go-build.mk go-release.mk go-service.mk

include bootstrap.mk

.DEFAULT_GOAL := check

# ---------------------------------------------------------------------------
# Project-local
# ---------------------------------------------------------------------------

# Daemon control comes from go-service.mk: service-install, service-uninstall,
# service-restart, service-status. Templates live at packaging/{macos,systemd}/.

BUNDLE_ID    ?= io.goodkind.agent-gate
ENTITLEMENTS := packaging/macos/agent-gate.entitlements
CODESIGN_IDENTITY := $(or $(CERT_ID),$(shell if [ "$$(uname -s)" = "Darwin" ]; then security find-identity -v -p codesigning 2>/dev/null | awk '/Developer ID Application/ { print $$2; exit }'; fi))

.PHONY: proto smoke-build install-release install-release-bin install-release-hooks install-release-service \
        daemon-status spawn-smoke

proto:
	buf generate

# smoke-build produces a stamped + signed binary at $(OUT) (default
# .make/smoke/agent-gate) for smoke tests. Distinct from `make build` which
# is the canonical dev build.
smoke-build:
	@out="$${OUT:-.make/smoke/agent-gate}"; \
	version="$${VERSION:-smoke}"; \
	commit="$${COMMIT:-smoke}"; \
	build_time="$$(date -u +%Y-%m-%dT%H:%M:%SZ)"; \
	mkdir -p "$$(dirname "$$out")"; \
	go build -ldflags "-X $(VPKG).Commit=$$commit -X $(VPKG).Version=$$version -X $(VPKG).Dirty=true -X $(GKLOG_VPKG).Commit=$$commit -X $(GKLOG_VPKG).Dirty=true -X $(GKLOG_VPKG).BuildTime=$$build_time -X $(GKLOG_VPKG).BinHash=" -o "$$out" $(CMD); \
	if [ "$$(uname -s)" = "Darwin" ]; then \
		if [ -z "$(CODESIGN_IDENTITY)" ]; then \
			echo "No Developer ID Application signing identity found." >&2; \
			exit 1; \
		fi; \
		codesign --force --sign "$(CODESIGN_IDENTITY)" --identifier "$(BUNDLE_ID)" --options runtime --timestamp=none --entitlements "$(ENTITLEMENTS)" "$$out"; \
		codesign --verify --verbose=2 "$$out"; \
	fi; \
	echo "smoke-built: $$out"

# install-release fetches the latest release via install.sh. Distinct from
# canonical `make install` which atomically copies the locally-built binary
# into $XDG_BIN_HOME.
install-release:
	./install.sh $(ARGS)

install-release-bin:
	./install.sh --bin-only $(ARGS)

install-release-hooks:
	./install.sh --hooks-only $(ARGS)

install-release-service:
	./install.sh --service-only --bin-dir $(INSTALL_DIR) $(ARGS)

# daemon-status calls the agent-gate CLI's own status subcommand, which is
# richer than launchctl/systemctl status. service-status from go-service.mk
# is also available for the platform-level view.
daemon-status:
	$(INSTALL_BIN) daemon status

spawn-smoke:
	go run ./cmd/spawn-smoke -input-file "$(INPUT)" $(ARGS)
