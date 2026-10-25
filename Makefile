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

# go-mk's release command signs darwin binaries with quill and reads the
# hardened-runtime entitlements from RELEASE_ENTITLEMENTS. pcre2 now links
# statically on darwin, so no third-party dylib has to load at runtime.
RELEASE_ENTITLEMENTS  := $(CODESIGN_ENTITLEMENTS)

# Pipeline modules
GO_MK_MODULES := go-build.mk go-release.mk go-service.mk

GO_MK_WORKSPACE_USE := . third_party/gksyntax

# pcre2 is linked through cgo (#cgo pkg-config: libpcre2-8). On a linux target the
# build uses the system libpcre2-8 from apt (apt_packages: libpcre2-dev). On the
# darwin cross target go-makefile provisions a static pcre2 through this hook, so
# the release binary is self-contained. Set before include bootstrap.mk.
GO_MK_CGO_DEPS := pcre2

include bootstrap.mk

.PHONY: gksyntax-submodule
gksyntax-submodule:
	@git submodule update --init third_party/gksyntax

go-mk-workspace: | gksyntax-submodule

.DEFAULT_GOAL := check

# go-makefile's release command runs this per build target with CC/CXX,
# GO_MK_TARGET_GOOS/GOARCH, and GO_MK_CGO_PREFIX set; the script builds a static
# darwin pcre2 into the prefix and is a no-op on linux.
.PHONY: go-mk-cgo-dep-pcre2
go-mk-cgo-dep-pcre2:
	@bash scripts/setup-cgo-pcre2.sh

# ---------------------------------------------------------------------------
# Project-local
# ---------------------------------------------------------------------------

# Daemon control comes from go-service.mk: service-install, service-uninstall,
# service-restart, service-status. Templates live at packaging/{macos,systemd}/.

.PHONY: proto smoke-build deploy deploy-service install-release \
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
