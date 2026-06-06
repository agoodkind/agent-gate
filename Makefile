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

# Build tags. sqlite_fts5 makes mattn/go-sqlite3 compile the FTS5 extension,
# which the durable intake store's command-search index depends on. Driving the
# tag through GOFLAGS (rather than GO_BUILD_TAGS) makes every `go` subcommand
# pick it up uniformly: the build path, the go-mk test helper, and vet all link
# FTS5. Exported so recipe subprocesses inherit it.
export GOFLAGS := -tags=sqlite_fts5

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

.PHONY: proto smoke-build deploy deploy-service install-release install-release-bin install-release-hooks install-release-service \
        daemon-wait daemon-status spawn-smoke

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
	$(MAKE) deploy-service
	$(MAKE) daemon-wait
	$(MAKE) daemon-status

# deploy-service restarts the supervised daemon. On macOS it fully unloads the
# service (bootout), waits for the process to exit, then loads it again
# (bootstrap), rather than `launchctl kickstart -k`. kickstart can start the new
# instance before the old one releases the SQLite WAL lock, which makes the
# daemon crash-loop on "database is locked" during the startup intake replay.
# bootout + wait + bootstrap guarantees no instance overlap. Linux keeps
# service-restart because systemctl restart is already overlap-free.
deploy-service:
	@if [ "$$(uname)" = "Darwin" ]; then \
		echo "restarting $(LAUNCHD_LABEL): bootout + bootstrap"; \
		launchctl bootout "$(LAUNCHD_DOMAIN)/$(LAUNCHD_LABEL)" 2>/dev/null || true; \
		for _ in $$(seq 1 50); do \
			launchctl print "$(LAUNCHD_DOMAIN)/$(LAUNCHD_LABEL)" >/dev/null 2>&1 || break; \
			sleep 0.2; \
		done; \
		if ! launchctl bootstrap "$(LAUNCHD_DOMAIN)" "$(LAUNCHD_PLIST)" 2>/dev/null; then \
			echo "bootstrap failed; (re)installing user service"; \
			$(MAKE) service-install; \
			launchctl enable "$(LAUNCHD_DOMAIN)/$(LAUNCHD_LABEL)" 2>/dev/null || true; \
			launchctl bootstrap "$(LAUNCHD_DOMAIN)" "$(LAUNCHD_PLIST)" 2>/dev/null || true; \
		fi; \
	else \
		$(MAKE) service-restart || { \
			echo "service restart failed; installing user service"; \
			$(MAKE) service-install; \
			$(MAKE) service-restart; \
		}; \
	fi

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
