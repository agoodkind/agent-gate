# Lint is centralized in go-makefile. Do NOT define project-local lint,
# deadcode, audit, fmt, vet, or staticcheck targets here. They duplicate
# the central pipeline and let agents bypass strict rules. Run `make help`
# for the canonical entry points (build/check/lint/fmt) and per-linter
# sub-targets (lint-golangci, lint-format, lint-gocyclo, lint-deadcode,
# staticcheck-extra). Refresh baselines via the matching *-baseline target.
#
# agent-gate Makefile.
# Build/lint/release pipeline lives in go-makefile and is fetched at runtime.
# Local daemon deployment uses the shared build/sign/install path, then restarts
# the service through go-service.mk.

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
BUNDLE_ID             ?= io.goodkind.agent-gate
CODESIGN_ENTITLEMENTS := packaging/macos/agent-gate.entitlements

# Pipeline modules
GO_MK_MODULES := go-build.mk go-release.mk go-service.mk

include bootstrap.mk

.DEFAULT_GOAL := check

# ---------------------------------------------------------------------------
# Project-local
# ---------------------------------------------------------------------------

# Daemon control comes from go-service.mk: service-install, service-uninstall,
# service-restart, service-status. Templates live at packaging/{macos,systemd}/.

.PHONY: proto smoke-build deploy daemon-wait daemon-status spawn-smoke

proto:
	buf generate

# smoke-build produces a signed binary at $(OUT) (default .make/smoke/agent-gate)
# through the shared go-build.mk build/sign path.
smoke-build:
	@out="$${OUT:-.make/smoke/agent-gate}"; \
	dist_dir="$$(dirname "$$out")"; \
	$(MAKE) BUILD_CHECKS=false DIST_DIR="$$dist_dir" build; \
	if [ "$$out" != "$$dist_dir/$(BINARY)" ]; then \
		cp -f "$$dist_dir/$(BINARY)" "$$out"; \
	fi; \
	echo "smoke-built: $$out"

deploy:
	$(MAKE) BUILD_CHECKS=false install
	$(MAKE) service-restart
	$(MAKE) daemon-wait
	$(MAKE) daemon-status

# daemon-status calls the agent-gate CLI's own status subcommand, which is
# richer than launchctl/systemctl status. service-status from go-service.mk
# is also available for the platform-level view.
daemon-status:
	$(INSTALL_BIN) daemon status

daemon-wait:
	@for attempt in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do \
		if "$(INSTALL_BIN)" daemon status >/dev/null 2>&1; then \
			exit 0; \
		fi; \
		sleep 0.25; \
	done; \
	"$(INSTALL_BIN)" daemon status

spawn-smoke:
	go run ./cmd/spawn-smoke -input-file "$(INPUT)" $(ARGS)
